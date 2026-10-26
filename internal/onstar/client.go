// Package onstar implements the source-derived protocol contract translated from
// OnStarJS2. See parity.json and the retained upstream MIT notices.
package onstar

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	SourceCommit          = "1ae0cbb6fd7601e871b67cd0cda02c80c4354f9d"
	ClientID              = "3ff30506-d242-4bed-835b-422bf992622e"
	RedirectURI           = "msauth.com.gm.myChevrolet://auth"
	Authority             = "https://custlogin.gm.com/gmb2cprod.onmicrosoft.com/b2c_1a_seamless_mobile_signuporsignin/v2.0/"
	AuthorizationEndpoint = "https://custlogin.gm.com/gmb2cprod.onmicrosoft.com/b2c_1a_seamless_mobile_signuporsignin/oauth2/v2.0/authorize"
	TokenEndpoint         = "https://custlogin.gm.com/gmb2cprod.onmicrosoft.com/b2c_1a_seamless_mobile_signuporsignin/oauth2/v2.0/token"
	JWKSURL               = "https://custlogin.gm.com/gmb2cprod.onmicrosoft.com/b2c_1a_seamless_mobile_signuporsignin/discovery/v2.0/keys"
	APIBase               = "https://na-mobile-api.gm.com"
	Scope                 = "https://gmb2cprod.onmicrosoft.com/" + ClientID + "/Test.Read openid profile offline_access"
)

// Error contains a bounded classification only, never provider bodies or tokens.
type Error struct {
	Kind              string
	Status            int
	RetryAfterSeconds int
	AuthEndpoint      bool
}

func (e *Error) Error() string  { return "onstar: " + e.Kind }
func failure(kind string) error { return &Error{Kind: kind} }

type Client struct {
	http *http.Client
	now  func() time.Time
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: time.Now}
}

// Requests have one attempt. Runtime persistence, cooldown and retry ownership
// belong to the worker; the transport never silently retries or reauthenticates.
func (c *Client) request(ctx context.Context, method, endpoint, contentType, token string, body io.Reader, dst any) error {
	authEndpoint := endpoint == Authority+".well-known/openid-configuration" || endpoint == TokenEndpoint || endpoint == JWKSURL || endpoint == APIBase+"/sec/authz/v3/oauth/token"
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return failure("invalid-request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept-Encoding", "gzip")
	trace := readTrace(ctx, method, endpoint)
	traceStart := time.Now()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept-Language", "en-US")
		req.Header.Set("appversion", "myOwner-chevrolet-android-8.5.0-0")
		req.Header.Set("locale", "en-US")
		req.Header.Set("User-Agent", "myOwner App")
		req.Header.Set("push-request", "allow")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return failure("deadline")
		}
		return failure("network-unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if trace != nil {
			trace.record(req, resp.StatusCode, resp.Header, nil, traceStart)
		}
		kind := "provider-error"
		switch resp.StatusCode {
		case 401:
			kind = "reauth-required"
		case 403:
			kind = "access-denied"
		case 429:
			kind = "rate-limited"
		}
		seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		if seconds < 0 {
			seconds = 0
		}
		if seconds > 3600 {
			seconds = 3600
		}
		if resp.StatusCode == 400 {
			var payload struct {
				Code string `json:"error"`
			}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload)
			if payload.Code == "invalid_grant" {
				kind = "reauth-required"
			}
		}
		return &Error{Kind: kind, Status: resp.StatusCode, RetryAfterSeconds: seconds, AuthEndpoint: authEndpoint}
	}
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzipReader, e := gzip.NewReader(resp.Body)
		if e != nil {
			return failure("invalid-response")
		}
		defer gzipReader.Close()
		reader = gzipReader
	}
	data, err := io.ReadAll(io.LimitReader(reader, 2*1024*1024+1))
	if trace != nil {
		trace.record(req, resp.StatusCode, resp.Header, data, traceStart)
	}
	if authEndpoint && accessDeniedTitle.Match(data) {
		return &Error{Kind: "access-denied", Status: resp.StatusCode, AuthEndpoint: true}
	}
	if err != nil || len(data) > 2*1024*1024 {
		return failure("invalid-response")
	}
	if json.Unmarshal(data, dst) != nil {
		return failure("invalid-response")
	}
	return nil
}

var accessDeniedTitle = regexp.MustCompile(`(?is)<title>\s*access denied\s*</title>`)

type metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

func (c *Client) discover(ctx context.Context) (metadata, error) {
	var m metadata
	err := c.request(ctx, http.MethodGet, Authority+".well-known/openid-configuration", "application/json", "", nil, &m)
	if err != nil {
		return m, err
	}
	issuer, e := url.Parse(m.Issuer)
	if e != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || m.AuthorizationEndpoint != AuthorizationEndpoint || m.TokenEndpoint != TokenEndpoint || m.JWKSURI != JWKSURL {
		return m, failure("untrusted-provider-metadata")
	}
	return m, nil
}

type Authorization struct {
	mu              sync.Mutex
	state, verifier string
	expires         time.Time
	used            bool
}

func NewAuthorization() (string, *Authorization, error) { return newAuthorization(time.Now()) }
func newAuthorization(now time.Time) (string, *Authorization, error) {
	random := func() (string, error) {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b[:]), nil
	}
	state, err := random()
	if err != nil {
		return "", nil, failure("random-source-failed")
	}
	verifier, err := random()
	if err != nil {
		return "", nil, failure("random-source-failed")
	}
	hash := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {ClientID}, "redirect_uri": {RedirectURI}, "response_type": {"code"}, "scope": {Scope}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}, "bundleID": {"com.gm.myChevrolet"}, "mode": {"dark"}, "evar25": {"mobile_mychevrolet_chevrolet_us_app_launcher_sign_in_or_create_account"}, "channel": {"lightreg"}, "ui_locales": {"en-US"}, "brand": {"chevrolet"}}
	return AuthorizationEndpoint + "?" + q.Encode(), &Authorization{state: state, verifier: verifier, expires: now.Add(10 * time.Minute)}, nil
}
func (a *Authorization) consume(redirect string, now time.Time) (string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used || !now.Before(a.expires) {
		return "", "", failure("authorization-expired-or-used")
	}
	u, err := url.Parse(redirect)
	if err != nil || !strings.EqualFold(u.Scheme, "msauth.com.gm.myChevrolet") || u.Host != "auth" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.User != nil || u.Fragment != "" {
		return "", "", failure("invalid-redirect")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["state"]) != 1 || len(q["code"]) != 1 || q.Get("error") != "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.state)) != 1 || q.Get("code") == "" {
		return "", "", failure("invalid-redirect-state")
	}
	a.used = true
	verifier := a.verifier
	a.verifier = ""
	a.state = ""
	return q.Get("code"), verifier, nil
}

// Grant is sensitive and must only be stored through the encrypted credential
// store. Subject is established by a signed ID token at attended enrollment.
type Grant struct {
	AccessToken       string
	RefreshToken      string
	Subject           string
	ExpiresAt         int64
	RefreshObtainedAt int64
}
type tokenResponse struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	IDToken      string      `json:"id_token"`
	ExpiresIn    json.Number `json:"expires_in"`
}

func (c *Client) Enroll(ctx context.Context, a *Authorization, redirect string) (Grant, error) {
	code, verifier, err := a.consume(redirect, c.now())
	if err != nil {
		return Grant{}, err
	}
	m, err := c.discover(ctx)
	if err != nil {
		return Grant{}, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {ClientID}, "redirect_uri": {RedirectURI}, "code": {code}, "code_verifier": {verifier}}
	var response tokenResponse
	if err := c.request(ctx, http.MethodPost, m.TokenEndpoint, "application/x-www-form-urlencoded", "", strings.NewReader(form.Encode()), &response); err != nil {
		return Grant{}, err
	}
	if response.IDToken == "" || response.RefreshToken == "" {
		return Grant{}, failure("incomplete-enrollment")
	}
	claims, err := c.verifyIdentity(ctx, m, response.IDToken)
	if err != nil {
		return Grant{}, err
	}
	return c.checkedGrant(ctx, m, response, claims.Subject, Grant{})
}
func (c *Client) Refresh(ctx context.Context, old Grant) (Grant, error) {
	if old.RefreshToken == "" || old.Subject == "" {
		return Grant{}, failure("reauth-required")
	}
	m, err := c.discover(ctx)
	if err != nil {
		return Grant{}, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {ClientID}, "refresh_token": {old.RefreshToken}}
	var response tokenResponse
	if err := c.request(ctx, http.MethodPost, m.TokenEndpoint, "application/x-www-form-urlencoded", "", strings.NewReader(form.Encode()), &response); err != nil {
		return Grant{}, err
	}
	return c.checkedGrant(ctx, m, response, old.Subject, old)
}
func (c *Client) checkedGrant(ctx context.Context, m metadata, response tokenResponse, subject string, old Grant) (Grant, error) {
	if response.AccessToken == "" {
		return Grant{}, failure("incomplete-token-response")
	}
	claims, err := c.verifyIdentity(ctx, m, response.AccessToken)
	if err != nil {
		return Grant{}, err
	}
	if claims.Subject != subject {
		return Grant{}, failure("account-mismatch")
	}
	if response.IDToken != "" {
		id, err := c.verifyIdentity(ctx, m, response.IDToken)
		if err != nil {
			return Grant{}, err
		}
		if id.Subject != subject {
			return Grant{}, failure("account-mismatch")
		}
	}
	expires, err := response.ExpiresIn.Int64()
	if err != nil || expires <= 0 || expires > 86400*7 {
		return Grant{}, failure("invalid-token-expiry")
	}
	now := c.now().Unix()
	expiresAt := now + expires
	if claims.Expires < expiresAt {
		expiresAt = claims.Expires
	}
	grant := Grant{AccessToken: response.AccessToken, RefreshToken: old.RefreshToken, Subject: subject, ExpiresAt: expiresAt, RefreshObtainedAt: old.RefreshObtainedAt}
	if response.RefreshToken != "" {
		grant.RefreshToken = response.RefreshToken
		grant.RefreshObtainedAt = now
	}
	return grant, nil
}

type GMToken struct {
	AccessToken   string
	ExpiresAt     int64
	VehicleLinked bool
	VIN           string
}

func (c *Client) Exchange(ctx context.Context, grant Grant, deviceID, vin string) (GMToken, error) {
	if grant.AccessToken == "" || grant.ExpiresAt <= c.now().Unix() || !validVIN(vin) || deviceID == "" {
		return GMToken{}, failure("invalid-exchange-input")
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"}, "subject_token": {grant.AccessToken}, "subject_token_type": {"urn:ietf:params:oauth:token-type:access_token"}, "scope": {"onstar gmoc user_trailer user msso priv"}, "device_id": {deviceID}}
	var response tokenResponse
	if err := c.request(ctx, http.MethodPost, APIBase+"/sec/authz/v3/oauth/token", "application/x-www-form-urlencoded", "", strings.NewReader(form.Encode()), &response); err != nil {
		return GMToken{}, err
	}
	expires, err := response.ExpiresIn.Int64()
	if err != nil || expires <= 0 || expires > 86400*7 {
		return GMToken{}, failure("invalid-token-expiry")
	}
	token := GMToken{AccessToken: response.AccessToken, ExpiresAt: c.now().Unix() + expires, VIN: vin}
	// This token came directly from the fixed HTTPS exchange endpoint. As upstream,
	// decode vehicle membership only; do not treat unverified claims as user identity.
	parts := strings.Split(response.AccessToken, ".")
	if len(parts) != 3 {
		return token, failure("invalid-gm-token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return token, failure("invalid-gm-token")
	}
	var claims struct {
		Vehicles []struct {
			VIN string `json:"vin"`
		} `json:"vehs"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return token, failure("invalid-gm-token")
	}
	for _, vehicle := range claims.Vehicles {
		if vehicle.VIN == vin {
			token.VehicleLinked = true
			return token, nil
		}
	}
	return token, failure("vehicle-not-linked")
}

var vinPattern = regexp.MustCompile(`^[A-HJ-NPR-Z0-9]{17}$`)

func validVIN(vin string) bool { return vinPattern.MatchString(vin) }

type Bootstrap struct {
	Schema            int    `json:"schema"`
	EnrollmentVersion int64  `json:"enrollment_version"`
	VIN               string `json:"vin"`
	RefreshToken      string `json:"refresh_token"`
	RefreshObtainedAt int64  `json:"refresh_obtained_at"`
	B2CSubject        string `json:"b2c_subject"`
	DeviceIDSHA256    string `json:"device_id_sha256"`
	ProviderState     string `json:"provider_state"`
}

func NewBootstrap(grant Grant, deviceID, vin string, version int64, state string) (Bootstrap, error) {
	if !validVIN(vin) || deviceID == "" || grant.RefreshToken == "" || grant.Subject == "" || grant.RefreshObtainedAt <= 0 || version <= 0 || (state != "ready" && state != "vehicle-not-linked" && state != "pending-verification") {
		return Bootstrap{}, failure("invalid-bootstrap")
	}
	digest := sha256.Sum256([]byte(deviceID))
	return Bootstrap{1, version, vin, grant.RefreshToken, grant.RefreshObtainedAt, grant.Subject, fmt.Sprintf("%x", digest), state}, nil
}
func (b Bootstrap) ValidateDevice(deviceID string) error {
	digest := sha256.Sum256([]byte(deviceID))
	if b.Schema != 1 || b.DeviceIDSHA256 != fmt.Sprintf("%x", digest) {
		return failure("bootstrap-device-mismatch")
	}
	return nil
}
