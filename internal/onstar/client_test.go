package onstar

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	synthetic "github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func fakeClient(handler transportFunc) *Client {
	return &Client{http: &http.Client{Transport: handler}, now: func() time.Time { return time.Unix(1800000000, 0) }}
}
func TestReadTracePrivateParentsAndEncodedKeys(t *testing.T) {
	raw := []byte(`{"owner":{"name":"PRIVATE OWNER"},"contact":{"name":"PRIVATE CONTACT"},"em\u0061il":{"name":"PRIVATE MAIL"},"phone":{"value":"1234567"},"diagnostics":{"name":"FUEL_LEVEL","value":"75","unit":"%"}}`)
	clean := string(traceJSON(raw))
	for _, forbidden := range []string{"PRIVATE", "1234567"} {
		if strings.Contains(clean, forbidden) {
			t.Fatal("private ancestor leaked")
		}
	}
	if !strings.Contains(clean, "FUEL_LEVEL") {
		t.Fatal("safe source metadata removed")
	}
}

func TestReadTraceNeverCapturesAuthenticationOrActuation(t *testing.T) {
	for _, endpoint := range []string{TokenEndpoint, JWKSURL, APIBase + "/sec/authz/v3/oauth/token", APIBase + "/veh/cmd/v3/start/" + synthetic.VIN} {
		ctx, capture := WithReadTrace(context.Background())
		client := fakeClient(func(*http.Request) (*http.Response, error) {
			return response(`{"value":"synthetic-private"}`, 200), nil
		})
		var out any
		_ = client.request(ctx, http.MethodPost, endpoint, "application/json", "synthetic-token", nil, &out)
		if capture.Result() != nil {
			t.Fatal("forbidden endpoint captured")
		}
	}
}
func TestReadTraceStructuralRedactionAndBound(t *testing.T) {
	ctx, capture := WithReadTrace(context.Background())
	client := fakeClient(func(r *http.Request) (*http.Response, error) {
		res := response(`{"vin":"`+synthetic.VIN+`","deviceId":"synthetic-device-private","accessToken":"synthetic-token-private","position":{"latitude":`+"12"+".345"+`},"data":{"name":"FUEL_LEVEL","value":"75","uom":"%"}}`, 200)
		res.Header.Set("Set-Cookie", "synthetic-cookie-private")
		return res, nil
	})
	var out any
	query := `query getVehicle {vehicleDetails(vin:"` + synthetic.VIN + `"){vin}}`
	if e := client.request(ctx, http.MethodPost, APIBase+"/mbff/garage/v1", "text/plain", "synthetic-header-private", strings.NewReader(query), &out); e != nil {
		t.Fatal(e)
	}
	if capture.Result() == nil {
		t.Fatal("real read not traced")
	}
	data, _ := json.Marshal(capture.Result())
	for _, bad := range []string{synthetic.VIN, "synthetic-device-private", "synthetic-token-private", "synthetic-cookie-private", "synthetic-header-private", "12.345"} {
		if strings.Contains(string(data), bad) {
			t.Fatal("private trace value escaped")
		}
	}
	if !strings.Contains(string(data), "FUEL_LEVEL") || len(traceJSON([]byte(strings.Repeat("x", 20000)))) > 100 {
		t.Fatal("useful trace metadata or limit failed")
	}
}
func kind(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return "unexpected"
}

func TestPKCEStateExpiryReplay(t *testing.T) {
	now := time.Unix(1800000000, 0)
	address, auth, err := newAuthorization(now)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(address)
	q := u.Query()
	digest := sha256.Sum256([]byte(auth.verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != RedirectURI {
		t.Fatal("incorrect PKCE contract")
	}
	for _, bad := range []string{"https://attacker.invalid/?state=" + q.Get("state") + "&code=synthetic", "msauth.com.gm.myChevrolet://auth?state=wrong&code=synthetic", RedirectURI + "?state=" + q.Get("state") + "&state=duplicate&code=synthetic"} {
		if _, _, err := auth.consume(bad, now); err == nil {
			t.Fatal("bad redirect accepted")
		}
	}
	redirect := RedirectURI + "?state=" + q.Get("state") + "&code=synthetic"
	if _, _, err := auth.consume(redirect, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.consume(redirect, now); err == nil {
		t.Fatal("replay accepted")
	}
	_, expired, _ := newAuthorization(now)
	if _, _, err := expired.consume(redirect, now.Add(10*time.Minute)); err == nil {
		t.Fatal("expired authorization accepted")
	}
}

func TestPublishedOAuthEndpoints(t *testing.T) {
	// GM's public discovery document uses /oauth2/ for OAuth endpoints,
	// but not for the discovery document itself. Do not derive one from the other.
	const authorize = "https://custlogin.gm.com/gmb2cprod.onmicrosoft.com/b2c_1a_seamless_mobile_signuporsignin/oauth2/v2.0/authorize"
	const token = "https://custlogin.gm.com/gmb2cprod.onmicrosoft.com/b2c_1a_seamless_mobile_signuporsignin/oauth2/v2.0/token"
	address, _, err := newAuthorization(time.Unix(1800000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(address)
	u.RawQuery = ""
	if u.String() != authorize {
		t.Fatal("authorization path differs from GM discovery")
	}
	c := fakeClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != Authority+".well-known/openid-configuration" {
			t.Fatal("discovery path changed")
		}
		data, _ := json.Marshal(metadata{Issuer: "https://custlogin.gm.com/948f443d-b588-482a-8ffa-2e49ee13689a/v2.0/", AuthorizationEndpoint: authorize, TokenEndpoint: token, JWKSURI: JWKSURL})
		return response(string(data), 200), nil
	})
	if _, err := c.discover(context.Background()); err != nil {
		t.Fatal("published metadata rejected", err)
	}
}

func TestCallbackRootPath(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, path := range []string{"", "/", "/other", "//", "/./", "/%2e", "/%2F", "/.."} {
		address, a, err := newAuthorization(now)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(address)
		redirect := RedirectURI + path + "?" + url.Values{"state": {u.Query().Get("state")}, "code": {"synthetic-code"}}.Encode()
		_, _, err = a.consume(redirect, now)
		if path == "" || path == "/" {
			if err != nil {
				t.Fatal("callback root form rejected")
			}
			if _, _, err = a.consume(redirect, now); err == nil {
				t.Fatal("root form bypassed replay check")
			}
		} else if err == nil {
			t.Fatal("non-root callback path accepted")
		}
	}
}

func signedIdentity(t *testing.T, key *rsa.PrivateKey, subject string, expires int64) string {
	t.Helper()
	encode := func(v any) string { data, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(data) }
	head := encode(map[string]any{"alg": "RS256", "kid": "synthetic-key"})
	body := encode(map[string]any{"sub": subject, "iss": Authority, "aud": ClientID, "exp": expires, "iat": 1800000000})
	digest := sha256.Sum256([]byte(head + "." + body))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return head + "." + body + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func authFixture(t *testing.T, subject string, refreshReplacement bool) (*Client, *int) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := fakeClient(func(req *http.Request) (*http.Response, error) {
		calls++
		switch req.URL.String() {
		case Authority + ".well-known/openid-configuration":
			data, _ := json.Marshal(metadata{Issuer: Authority, AuthorizationEndpoint: AuthorizationEndpoint, TokenEndpoint: TokenEndpoint, JWKSURI: JWKSURL})
			return response(string(data), 200), nil
		case JWKSURL:
			data, _ := json.Marshal(map[string]any{"keys": []jwk{{Kty: "RSA", Use: "sig", Alg: "RS256", Kid: "synthetic-key", N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
			return response(string(data), 200), nil
		case TokenEndpoint:
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if req.Form.Get("client_id") != ClientID || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Fatal("wrong token form")
			}
			token := signedIdentity(t, key, subject, 1800003600)
			result := map[string]any{"access_token": token, "expires_in": 3600}
			if req.Form.Get("grant_type") == "authorization_code" {
				if len(req.Form.Get("code_verifier")) != 43 || req.Form.Get("redirect_uri") != RedirectURI {
					t.Fatal("PKCE verifier missing")
				}
				result["id_token"] = token
				result["refresh_token"] = "synthetic-refresh"
			} else if refreshReplacement {
				result["refresh_token"] = "synthetic-rotated-refresh"
			}
			data, _ := json.Marshal(result)
			return response(string(data), 200), nil
		default:
			t.Fatal("unexpected endpoint")
			return nil, nil
		}
	})
	return client, &calls
}

func TestEnrollmentAndRefreshIdentity(t *testing.T) {
	c, _ := authFixture(t, synthetic.Subject, false)
	address, a, _ := newAuthorization(c.now())
	u, _ := url.Parse(address)
	grant, err := c.Enroll(context.Background(), a, RedirectURI+"?state="+u.Query().Get("state")+"&code=synthetic-code")
	if err != nil {
		t.Fatal(err)
	}
	if grant.ExpiresAt != 1800003600 || grant.Subject != synthetic.Subject || grant.RefreshObtainedAt != 1800000000 {
		t.Fatal("wrong grant metadata or expiry units")
	}
	old := grant
	old.RefreshObtainedAt = 1799990000
	refreshed, err := c.Refresh(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.RefreshToken != old.RefreshToken || refreshed.RefreshObtainedAt != old.RefreshObtainedAt {
		t.Fatal("omitted refresh token erased original")
	}
	rotating, _ := authFixture(t, synthetic.Subject, true)
	refreshed, err = rotating.Refresh(context.Background(), old)
	if err != nil || refreshed.RefreshToken == old.RefreshToken || refreshed.RefreshObtainedAt != 1800000000 {
		t.Fatal("rotation metadata incorrect")
	}
	wrong, _ := authFixture(t, "synthetic-other-subject", false)
	if _, err := wrong.Refresh(context.Background(), old); kind(err) != "account-mismatch" {
		t.Fatal("wrong account accepted")
	}
}

func TestIdentityRejectsUnverifiedAndExpired(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	client := fakeClient(func(*http.Request) (*http.Response, error) {
		data, _ := json.Marshal(map[string]any{"keys": []jwk{{Kty: "RSA", Kid: "synthetic-key", N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()), E: "AQAB"}}})
		return response(string(data), 200), nil
	})
	token := signedIdentity(t, key, synthetic.Subject, 1799999999)
	if _, err := client.verifyIdentity(context.Background(), metadata{Issuer: Authority, JWKSURI: JWKSURL}, token); kind(err) != "identity-expired-or-not-yet-valid" {
		t.Fatal("expired token accepted")
	}
	token = signedIdentity(t, key, synthetic.Subject, 1800003600)
	parts := strings.Split(token, ".")
	parts[2] = "invalid"
	if _, err := client.verifyIdentity(context.Background(), metadata{Issuer: Authority, JWKSURI: JWKSURL}, strings.Join(parts, ".")); err == nil {
		t.Fatal("bad signature accepted")
	}
}

func TestExchangeVehicleBindingAndBootstrap(t *testing.T) {
	for _, vin := range []string{synthetic.VIN, synthetic.OtherVIN, ""} {
		t.Run(vin, func(t *testing.T) {
			client := fakeClient(func(req *http.Request) (*http.Response, error) {
				_ = req.ParseForm()
				if req.URL.String() != APIBase+"/sec/authz/v3/oauth/token" || req.Form.Get("device_id") != synthetic.DeviceID || req.Form.Get("subject_token") != "synthetic-access" {
					t.Fatal("wrong exchange contract")
				}
				payload, _ := json.Marshal(map[string]any{"vehs": []any{map[string]string{"vin": vin}}})
				token := "synthetic." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic"
				body, _ := json.Marshal(map[string]any{"access_token": token, "expires_in": "3600"})
				return response(string(body), 200), nil
			})
			grant := Grant{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", Subject: synthetic.Subject, ExpiresAt: 1800003600, RefreshObtainedAt: 1800000000}
			token, err := client.Exchange(context.Background(), grant, synthetic.DeviceID, synthetic.VIN)
			if vin == synthetic.VIN {
				if err != nil || !token.VehicleLinked || token.ExpiresAt != 1800003600 {
					t.Fatal("valid membership failed")
				}
			} else {
				if kind(err) != "vehicle-not-linked" || grant.RefreshToken != "synthetic-refresh" {
					t.Fatal("missing vehicle did not preserve grant")
				}
			}
			b, err := NewBootstrap(grant, synthetic.DeviceID, synthetic.VIN, 1, "pending-verification")
			if err != nil || len(b.DeviceIDSHA256) != 64 {
				t.Fatal("invalid bootstrap")
			}
			encoded, _ := json.Marshal(b)
			if strings.Contains(string(encoded), grant.AccessToken) || strings.Contains(string(encoded), synthetic.DeviceID) {
				t.Fatal("bootstrap exposed access token or device identity")
			}
		})
	}
}

func TestReadonlyFailuresNeverRetry(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		calls := 0
		c := fakeClient(func(*http.Request) (*http.Response, error) {
			calls++
			r := response(`{"message":"sensitive-provider-detail"}`, status)
			r.Header.Set("Retry-After", "12")
			return r, nil
		})
		_, err := c.Vehicles(context.Background(), GMToken{AccessToken: "synthetic", ExpiresAt: 1800003600})
		if err == nil || calls != 1 || strings.Contains(err.Error(), "sensitive") {
			t.Fatal("failure retried or leaked")
		}
	}
	for _, body := range []string{`{"errors":[{"message":"synthetic partial failure"}],"data":{"vehicles":[]}}`, `{"data":{}}`, `malformed`} {
		c := fakeClient(func(*http.Request) (*http.Response, error) { return response(body, 200), nil })
		if _, err := c.Vehicles(context.Background(), GMToken{AccessToken: "synthetic", ExpiresAt: 1800003600}); err == nil {
			t.Fatal("invalid garage response accepted")
		}
	}
}

func TestLocationPendingAndUnknownFix(t *testing.T) {
	for _, status := range []string{"PENDING", "COMPLETE"} {
		c := fakeClient(func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("sms") != "false" || req.Header.Get("appversion") != "myOwner-chevrolet-android-8.5.0-0" {
				t.Fatal("wrong cached-read contract")
			}
			return response(`{"telemetry":{"data":{"session":{"updatePending":"`+status+`"}}}}`, 200), nil
		})
		token := GMToken{AccessToken: "synthetic", ExpiresAt: 1800003600, VehicleLinked: true, VIN: synthetic.VIN}
		got, err := c.Location(context.Background(), token, synthetic.VIN, false)
		if err != nil || got.Pending != (status == "PENDING") || got.FixAvailable || got.FixReason != "fix-schema-unverified" {
			t.Fatal("invented fix or incorrect pending status")
		}
		if _, err := c.Location(context.Background(), token, synthetic.OtherVIN, false); kind(err) != "vehicle-not-linked" {
			t.Fatal("wrong vehicle allowed")
		}
	}
}

func TestPinnedGarageWireFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/garage-request.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Method, URL, Body string
		Headers           map[string]string
	}
	if json.Unmarshal(data, &fixture) != nil {
		t.Fatal("invalid fixture")
	}
	c := fakeClient(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if req.Method != fixture.Method || req.URL.String() != fixture.URL || string(body) != fixture.Body {
			t.Fatal("pinned method/path/body differ")
		}
		for name, want := range fixture.Headers {
			if req.Header.Get(name) != want {
				t.Fatal("pinned header differs:", name)
			}
		}
		if req.Header.Get("Authorization") != "Bearer "+"synthetic-gm-access" {
			t.Fatal("missing GM authorization")
		}
		result, _ := json.Marshal(map[string]any{"data": map[string]any{"vehicles": []any{map[string]string{"vin": synthetic.VIN, "vehicleId": synthetic.VehicleID}}}})
		return response(string(result), 200), nil
	})
	vehicles, err := c.Vehicles(context.Background(), GMToken{AccessToken: "synthetic-gm-access", ExpiresAt: 1800003600})
	if err != nil || len(vehicles) != 1 {
		t.Fatal("garage fixture failed")
	}
}

func TestMalformedLocationAndDeadline(t *testing.T) {
	for _, body := range []string{`{}`, `{"telemetry":{"data":{"session":{"updatePending":null}}}}`, `not-json`} {
		c := fakeClient(func(*http.Request) (*http.Response, error) { return response(body, 200), nil })
		_, err := c.Location(context.Background(), GMToken{AccessToken: "synthetic", ExpiresAt: 1800003600, VehicleLinked: true, VIN: synthetic.VIN}, synthetic.VIN, false)
		if err == nil {
			t.Fatal("malformed location accepted")
		}
	}
	c := fakeClient(func(req *http.Request) (*http.Response, error) {
		if _, ok := req.Context().Deadline(); !ok {
			t.Fatal("request deadline absent")
		}
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Vehicles(ctx, GMToken{AccessToken: "synthetic", ExpiresAt: 1800003600}); err == nil {
		t.Fatal("cancelled request passed")
	}
}

func TestMissingVehicleClaimRetainsToken(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"synthetic":true}`))
	c := fakeClient(func(*http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"access_token": "synthetic." + payload + ".synthetic", "expires_in": 3600})
		return response(string(body), 200), nil
	})
	token, err := c.Exchange(context.Background(), Grant{AccessToken: "synthetic", ExpiresAt: 1800003600}, synthetic.DeviceID, synthetic.VIN)
	if kind(err) != "vehicle-not-linked" || token.AccessToken == "" {
		t.Fatal("missing claim did not retain state")
	}
}

func TestAuthHTMLDenialIsTypedWithoutBody(t *testing.T) {
	body, err := os.ReadFile("testdata/access-denied.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{Authority + ".well-known/openid-configuration", TokenEndpoint, JWKSURL, APIBase + "/sec/authz/v3/oauth/token"} {
		t.Run(endpoint, func(t *testing.T) {
			c := fakeClient(func(*http.Request) (*http.Response, error) { return response(string(body), 200), nil })
			var dst any
			err := c.request(context.Background(), http.MethodGet, endpoint, "application/json", "", nil, &dst)
			var typed *Error
			if !errors.As(err, &typed) || !typed.AuthEndpoint || typed.Kind != "access-denied" || typed.Status != 200 {
				t.Fatal("HTML denial not classified")
			}
			if strings.Contains(err.Error(), "synthetic-provider-detail") {
				t.Fatal("provider body leaked")
			}
		})
	}
}

func TestTraceRedactsDescriptorTaggedPrivateValues(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"diagnosticElements": []any{map[string]any{"name": "LATITUDE", "value": synthetic.Latitude}, map[string]any{"name": "LONGITUDE", "value": synthetic.Longitude}}, "customer": map[string]string{"name": "SYNTHETIC PERSON"}})
	clean := traceJSON(input)
	var parsed map[string]any
	if json.Unmarshal(clean, &parsed) != nil {
		t.Fatal("redacted trace invalid")
	}
	if strings.Contains(string(clean), "SYNTHETIC PERSON") {
		t.Error("uppercase customer identity survived trace redaction")
	}
	if elements, ok := parsed["diagnosticElements"].([]any); ok {
		for _, element := range elements {
			if row, ok := element.(map[string]any); ok {
				if _, numeric := row["value"].(float64); numeric {
					t.Error("descriptor-tagged location value survived trace redaction")
				}
			}
		}
	}
}

func TestTraceDescriptorAssociationNestedEncodedAndUnknownValues(t *testing.T) {
	input := []byte(`{"elements":[{"na\u006de":"LATITUDE","value":"123.456"},{"descriptor":{"name":"LONGITUDE"},"value":123.456},{"name":"UNKNOWN_READING","value":"123.456"},{"name":"FUEL_LEVEL","value":"75","unit":"%"},{"name":"ODOMETER","value":100,"unit":"mi"}],"custo\u006der":{"name":"SYNTHETIC PERSON"},"other":{"name":"SYNTHETIC PERSON"}}`)
	clean := string(traceJSON(input))
	for _, forbidden := range []string{"123.456", "SYNTHETIC PERSON", "LATITUDE", "LONGITUDE"} {
		if strings.Contains(clean, forbidden) {
			t.Fatal("private or unclassified descriptor value survived")
		}
	}
	if !strings.Contains(clean, `"value":"75"`) || !strings.Contains(clean, `"value":100`) || !strings.Contains(clean, "ODOMETER") {
		t.Fatal("recognized safe measurements lost")
	}
}
