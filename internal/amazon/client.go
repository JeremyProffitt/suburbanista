// Package amazon validates Login with Amazon authority. It owns no cache, retry
// loop, session, or credential store. Callers own refresh leases and persistence.
package amazon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

type Outcome string

const (
	Revoked              Outcome = "revoked"
	ConfigError          Outcome = "config-error"
	AuthorityUnavailable Outcome = "authority-unavailable"
	// InvalidAccessToken permits the caller to refresh once. It is not proof that
	// the retained owner grant was revoked. Alexa rejects the request without refresh.
	InvalidAccessToken Outcome = "access-token-invalid"
	maxResponseBytes           = 64 * 1024
	maxTokenBytes              = 32 * 1024
	maxLifetimeSeconds         = 24 * 60 * 60
)

// Error contains only application-defined labels, never a provider message or URL.
type Error struct {
	Outcome    Outcome
	Reason     string
	HTTPStatus int
}

func (e *Error) Error() string                      { return "amazon: " + string(e.Outcome) + " (" + e.Reason + ")" }
func failure(outcome Outcome, reason string) *Error { return &Error{Outcome: outcome, Reason: reason} }

type Client struct {
	http                            *http.Client
	now                             func() time.Time
	clientID, clientSecret, ownerID string
	timeout                         time.Duration
}

// NewClient takes the shared web/mobile/Alexa LWA client ID. Alexa passes an
// empty secret and cannot refresh. The plan allows 3s per worker HTTP call or 2s
// for Alexa; a shorter caller context always wins.
func NewClient(clientID, clientSecret, ownerID string, timeout time.Duration) (*Client, error) {
	if !strings.HasPrefix(clientID, "amzn1.application-oa2-client.") || len(clientID) > 512 || !validSubject(ownerID) || timeout <= 0 || timeout > 3*time.Second {
		return nil, failure(ConfigError, "invalid-client-settings")
	}
	return &Client{http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: time.Now, clientID: clientID, clientSecret: clientSecret, ownerID: ownerID, timeout: timeout}, nil
}

type Identity struct {
	Subject                string
	ValidatedAt, ExpiresAt time.Time
}

// Validate always performs tokeninfo followed by profile. It never reuses a
// previous result. High-impact and scheduled dispatch callers must call it anew.
func (c *Client) Validate(ctx context.Context, accessToken string) (Identity, error) {
	if !validToken(accessToken) {
		return Identity{}, failure(InvalidAccessToken, "missing-or-invalid-access-token")
	}
	started := c.now()
	var info struct {
		Audience  string      `json:"aud"`
		Issuer    string      `json:"iss"`
		Subject   string      `json:"user_id"`
		Remaining json.Number `json:"exp"`
	}
	// Amazon documents tokeninfo's access_token query parameter. It stays entirely
	// server-side; request errors are replaced before they can expose this URL.
	endpoint := "https://api.amazon.com/auth/o2/tokeninfo?" + url.Values{"access_token": {accessToken}}.Encode()
	if err := c.request(ctx, http.MethodGet, endpoint, "", nil, &info); err != nil {
		return Identity{}, err
	}
	if info.Audience == "" || info.Issuer == "" || info.Subject == "" {
		return Identity{}, failure(AuthorityUnavailable, "malformed-token-info")
	}
	if info.Audience != c.clientID {
		return Identity{}, failure(ConfigError, "audience-mismatch")
	}
	if info.Issuer != "https://www.amazon.com" {
		return Identity{}, failure(AuthorityUnavailable, "unexpected-token-issuer")
	}
	remaining, err := info.Remaining.Int64()
	// Amazon's tokeninfo exp is remaining seconds, not a JWT epoch timestamp:
	// https://images-na.ssl-images-amazon.com/images/G/01/lwa/docs/website-developer-guide.pdf
	if err != nil || remaining > maxLifetimeSeconds {
		return Identity{}, failure(AuthorityUnavailable, "invalid-token-lifetime")
	}
	if remaining <= 0 {
		return Identity{}, failure(InvalidAccessToken, "access-token-expired")
	}
	expires := started.Add(time.Duration(remaining) * time.Second)
	if !c.now().Before(expires) {
		return Identity{}, failure(InvalidAccessToken, "access-token-expired")
	}
	var profile struct {
		Subject string `json:"user_id"`
	}
	if err := c.request(ctx, http.MethodGet, "https://api.amazon.com/user/profile", accessToken, nil, &profile); err != nil {
		return Identity{}, err
	}
	if !validSubject(profile.Subject) {
		return Identity{}, failure(AuthorityUnavailable, "malformed-profile")
	}
	if profile.Subject != info.Subject || profile.Subject != c.ownerID {
		return Identity{}, failure(Revoked, "not-owner")
	}
	validated := c.now()
	if !validated.Before(expires) {
		return Identity{}, failure(InvalidAccessToken, "access-token-expired")
	}
	return Identity{Subject: profile.Subject, ValidatedAt: validated, ExpiresAt: expires}, nil
}

// ValidateRefreshed is used only after Refresh succeeded and the caller persisted
// its result. An invalid token at that point is a revoked authority, not an outage.
func (c *Client) ValidateRefreshed(ctx context.Context, accessToken string) (Identity, error) {
	identity, err := c.Validate(ctx, accessToken)
	var invalid *Error
	if errors.As(err, &invalid) && invalid.Outcome == InvalidAccessToken {
		return Identity{}, &Error{Outcome: Revoked, Reason: "invalid-token-after-refresh", HTTPStatus: invalid.HTTPStatus}
	}
	return identity, err
}

// Tokens are sensitive. Persist a returned refresh replacement under the existing
// grant lease before ValidateRefreshed or further provider work. Refresh success
// alone does not establish identity or permit dispatch.
type Tokens struct {
	AccessToken, RefreshToken string
	ExpiresAt                 time.Time
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	if c.clientSecret == "" {
		return Tokens{}, failure(ConfigError, "refresh-client-secret-missing")
	}
	if !validToken(refreshToken) {
		return Tokens{}, failure(Revoked, "refresh-token-missing")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {c.clientID}, "client_secret": {c.clientSecret}, "refresh_token": {refreshToken}}
	started := c.now()
	var response struct {
		Access   string      `json:"access_token"`
		Refresh  string      `json:"refresh_token"`
		Type     string      `json:"token_type"`
		Lifetime json.Number `json:"expires_in"`
	}
	if err := c.request(ctx, http.MethodPost, "https://api.amazon.com/auth/o2/token", "", strings.NewReader(form.Encode()), &response); err != nil {
		return Tokens{}, err
	}
	if !validToken(response.Access) || !strings.EqualFold(response.Type, "bearer") || (response.Refresh != "" && !validToken(response.Refresh)) {
		return Tokens{}, failure(AuthorityUnavailable, "malformed-token-response")
	}
	seconds, err := boundedLifetime(response.Lifetime)
	if err != nil {
		return Tokens{}, err
	}
	expires := started.Add(time.Duration(seconds) * time.Second)
	if !c.now().Before(expires) {
		return Tokens{}, failure(AuthorityUnavailable, "expired-refresh-response")
	}
	if response.Refresh == "" {
		response.Refresh = refreshToken
	}
	return Tokens{AccessToken: response.Access, RefreshToken: response.Refresh, ExpiresAt: expires}, nil
}

func validToken(value string) bool {
	return value != "" && len(value) <= maxTokenBytes && !strings.ContainsAny(value, "\r\n\t ")
}

func validSubject(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 512 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func boundedLifetime(value json.Number) (int64, error) {
	seconds, err := value.Int64()
	if err != nil || seconds <= 0 || seconds > maxLifetimeSeconds {
		return 0, failure(AuthorityUnavailable, "invalid-token-lifetime")
	}
	return seconds, nil
}

func (c *Client) request(ctx context.Context, method, endpoint, accessToken string, body io.Reader, dst any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return failure(ConfigError, "invalid-request")
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return failure(AuthorityUnavailable, "request-failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return failure(AuthorityUnavailable, "invalid-response-size")
	}
	if response.StatusCode != http.StatusOK {
		outcome, reason := AuthorityUnavailable, "provider-unavailable"
		var provider struct {
			Code string `json:"error"`
		}
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 429 && json.Unmarshal(data, &provider) == nil {
			switch provider.Code {
			case "invalid_client", "unauthorized_client", "insufficient_scope":
				outcome, reason = ConfigError, "client-authorization-failed"
			case "invalid_grant":
				if endpoint == "https://api.amazon.com/auth/o2/token" && response.StatusCode == 400 {
					outcome, reason = Revoked, "invalid-grant"
				}
			case "invalid_token":
				if method == http.MethodGet && (response.StatusCode == 400 || response.StatusCode == 401) {
					outcome, reason = InvalidAccessToken, "invalid-access-token"
				}
			}
		}
		return &Error{Outcome: outcome, Reason: reason, HTTPStatus: response.StatusCode}
	}
	if json.Unmarshal(data, dst) != nil {
		return failure(AuthorityUnavailable, "malformed-response")
	}
	return nil
}
