package amazon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testClientID = "amzn1.application-oa2-client.synthetic"
const testOwner = "amzn1.account.synthetic-owner"

var testNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func fixture(t *testing.T, transport transportFunc) *Client {
	t.Helper()
	client, err := NewClient(testClientID, "synthetic-client-secret", testOwner, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transport
	client.now = func() time.Time { return testNow }
	return client
}
func tokenInfo(audience, owner string, lifetime any) string {
	data, _ := json.Marshal(map[string]any{"aud": audience, "iss": "https://www.amazon.com", "user_id": owner, "exp": lifetime})
	return string(data)
}
func profile(owner string) string {
	data, _ := json.Marshal(map[string]string{"user_id": owner, "email": "ignored@example.invalid", "name": "Ignored"})
	return string(data)
}
func outcome(t *testing.T, err error, want Outcome) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) || got.Outcome != want {
		t.Fatalf("outcome=%v want=%s", err, want)
	}
	for _, sensitive := range []string{"synthetic-access", "synthetic-client-secret", "synthetic-refresh", "sensitive-response-detail", "api.amazon.com", "amzn1.account"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatal("error leaked request or profile data")
		}
	}
}

func TestValidateAuthority(t *testing.T) {
	calls := 0
	client := fixture(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "api.amazon.com" {
			t.Fatal("wrong authority endpoint")
		}
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Fatal("missing bounded deadline")
		}
		switch r.URL.Path {
		case "/auth/o2/tokeninfo":
			if r.URL.Query().Get("access_token") != "synthetic-access" {
				t.Fatal("incorrect tokeninfo query")
			}
			return reply(200, tokenInfo(testClientID, testOwner, 3597)), nil
		case "/user/profile":
			if r.Header.Get("Authorization") != "Bearer "+"synthetic-access" || r.URL.RawQuery != "" {
				t.Fatal("incorrect profile authorization")
			}
			return reply(200, profile(testOwner)), nil
		default:
			t.Fatal("unexpected endpoint")
			return nil, nil
		}
	})
	for i := 0; i < 2; i++ {
		got, err := client.Validate(context.Background(), "synthetic-access")
		if err != nil || got.Subject != testOwner || !got.ExpiresAt.Equal(testNow.Add(3597*time.Second)) || !got.ValidatedAt.Equal(testNow) {
			t.Fatalf("invalid authority result: %v", err)
		}
	}
	if calls != 4 {
		t.Fatal("validation used a cache or retried")
	}
}

func TestValidationFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name          string
		info, profile string
		profileStatus int
		want          Outcome
		wantCalls     int
	}{
		{"foreign-audience", tokenInfo("other-client", testOwner, 3600), "", 200, ConfigError, 1},
		{"foreign-owner", tokenInfo(testClientID, "amzn1.account.other", 3600), profile("amzn1.account.other"), 200, Revoked, 2},
		{"different-profile", tokenInfo(testClientID, testOwner, 3600), profile("amzn1.account.other"), 200, Revoked, 2},
		{"missing-audience", `{"iss":"https://www.amazon.com","user_id":"owner","exp":3600}`, "", 200, AuthorityUnavailable, 1},
		{"missing-expiry", tokenInfo(testClientID, testOwner, nil), "", 200, AuthorityUnavailable, 1},
		{"epoch-instead-of-lifetime", tokenInfo(testClientID, testOwner, 1800000000), "", 200, AuthorityUnavailable, 1},
		{"expired", tokenInfo(testClientID, testOwner, 0), "", 200, InvalidAccessToken, 1},
		{"malformed-profile", tokenInfo(testClientID, testOwner, 3600), "{}", 200, AuthorityUnavailable, 2},
		{"expired-profile", tokenInfo(testClientID, testOwner, 3600), `{"error":"invalid_token","error_description":"sensitive-response-detail"}`, 401, InvalidAccessToken, 2},
		{"documented-expired-profile", tokenInfo(testClientID, testOwner, 3600), `{"error":"invalid_token"}`, 400, InvalidAccessToken, 2},
		{"insufficient-scope", tokenInfo(testClientID, testOwner, 3600), `{"error":"insufficient_scope"}`, 401, ConfigError, 2},
		{"malformed-body", "broken", "", 200, AuthorityUnavailable, 1},
		{"trailing-json", tokenInfo(testClientID, testOwner, 3600) + " {}", "", 200, AuthorityUnavailable, 1},
		{"oversized", strings.Repeat(" ", maxResponseBytes+1), "", 200, AuthorityUnavailable, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := fixture(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path == "/auth/o2/tokeninfo" {
					return reply(200, tc.info), nil
				}
				return reply(tc.profileStatus, tc.profile), nil
			})
			_, err := client.Validate(context.Background(), "synthetic-access")
			outcome(t, err, tc.want)
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", calls, tc.wantCalls)
			}
		})
	}
}

func TestRefreshAndPostRefreshClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   Outcome
	}{
		{"revoked", 400, `{"error":"invalid_grant","error_description":"sensitive-response-detail"}`, Revoked},
		{"client", 400, `{"error":"invalid_client"}`, ConfigError},
		{"unauthorized", 401, `{"error":"unauthorized_client"}`, ConfigError},
		{"rate-limit", 429, `{"error":"invalid_grant"}`, AuthorityUnavailable},
		{"outage", 503, `{"error":"invalid_client"}`, AuthorityUnavailable},
		{"bad-json", 200, "not-json", AuthorityUnavailable},
		{"missing-expiry", 200, `{"access_token":"synthetic-access","token_type":"bearer"}`, AuthorityUnavailable},
		{"millisecond-expiry", 200, `{"access_token":"synthetic-access","token_type":"bearer","expires_in":3600000}`, AuthorityUnavailable},
		{"wrong-type", 200, `{"access_token":"synthetic-access","token_type":"mac","expires_in":3600}`, AuthorityUnavailable},
		{"redirect", 302, "", AuthorityUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := fixture(t, func(r *http.Request) (*http.Response, error) { calls++; return reply(tc.status, tc.body), nil })
			_, err := client.Refresh(context.Background(), "synthetic-refresh")
			outcome(t, err, tc.want)
			if calls != 1 {
				t.Fatal("refresh retried")
			}
		})
	}
	client := fixture(t, func(r *http.Request) (*http.Response, error) { return reply(401, `{"error":"invalid_token"}`), nil })
	_, err := client.ValidateRefreshed(context.Background(), "synthetic-access")
	outcome(t, err, Revoked)
}

func TestRefreshReturnsRotationBeforeFurtherWork(t *testing.T) {
	for _, replace := range []bool{false, true} {
		calls := 0
		client := fixture(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "POST" || r.URL.String() != "https://api.amazon.com/auth/o2/token" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Fatal("incorrect refresh endpoint")
			}
			if r.ParseForm() != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "synthetic-refresh" || r.Form.Get("client_id") != testClientID || r.Form.Get("client_secret") != "synthetic-client-secret" {
				t.Fatal("incorrect refresh form")
			}
			result := map[string]any{"access_token": "synthetic-access", "token_type": "bearer", "expires_in": 3600}
			if replace {
				result["refresh_token"] = "synthetic-replacement"
			}
			body, _ := json.Marshal(result)
			return reply(200, string(body)), nil
		})
		got, err := client.Refresh(context.Background(), "synthetic-refresh")
		if err != nil || calls != 1 || !got.ExpiresAt.Equal(testNow.Add(time.Hour)) {
			t.Fatalf("refresh result failed: %v", err)
		}
		want := "synthetic-refresh"
		if replace {
			want = "synthetic-replacement"
		}
		if got.RefreshToken != want {
			t.Fatal("refresh token continuity lost")
		}
	}
}

func TestUnavailableTransportAndDeadline(t *testing.T) {
	client := fixture(t, func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("sensitive-response-detail " + r.URL.String())
	})
	_, err := client.Validate(context.Background(), "synthetic-access")
	outcome(t, err, AuthorityUnavailable)
	client.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = client.Refresh(ctx, "synthetic-refresh")
	outcome(t, err, AuthorityUnavailable)
	if time.Since(started) > time.Second {
		t.Fatal("caller deadline ignored")
	}
}

func TestExpiredDuringProfileAndInvalidSettings(t *testing.T) {
	client := fixture(t, nil)
	client.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/auth/o2/tokeninfo" {
			return reply(200, tokenInfo(testClientID, testOwner, 1)), nil
		}
		client.now = func() time.Time { return testNow.Add(2 * time.Second) }
		return reply(200, profile(testOwner)), nil
	})
	_, err := client.Validate(context.Background(), "synthetic-access")
	outcome(t, err, InvalidAccessToken)
	for _, timeout := range []time.Duration{0, 4 * time.Second} {
		if _, err := NewClient(testClientID, "", testOwner, timeout); err == nil {
			t.Fatal("invalid timeout accepted")
		}
	}
	if _, err := NewClient("", "", testOwner, time.Second); err == nil {
		t.Fatal("missing client accepted")
	}
	alexa, err := NewClient(testClientID, "", testOwner, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = alexa.Refresh(context.Background(), "synthetic-refresh")
	outcome(t, err, ConfigError)
}

func TestOwnerIDIsOpaque(t *testing.T) {
	const owner = "opaque-owner-42"
	client, err := NewClient(testClientID, "", owner, 2*time.Second)
	if err != nil {
		t.Fatal("opaque owner ID rejected")
	}
	client.now = func() time.Time { return testNow }
	client.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/auth/o2/tokeninfo" {
			return reply(200, tokenInfo(testClientID, owner, 3600)), nil
		}
		return reply(200, profile(owner)), nil
	})
	identity, err := client.Validate(context.Background(), "synthetic-access")
	if err != nil || identity.Subject != owner {
		t.Fatal("opaque owner equality failed")
	}
	for _, invalid := range []string{"", "  ", "owner\n", strings.Repeat("x", 513)} {
		if _, err := NewClient(testClientID, "", invalid, time.Second); err == nil {
			t.Fatal("invalid owner configuration accepted")
		}
	}
}
