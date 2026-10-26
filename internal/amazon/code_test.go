package amazon

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestMobileCodeExchangePKCEAndExpiry(t *testing.T) {
	client := fixture(t, func(r *http.Request) (*http.Response, error) {
		if r.ParseForm() != nil || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != MobileCallback || r.Form.Get("client_secret") != "synthetic-client-secret" || r.Form.Get("code_verifier") != "synthetic-verifier" {
			t.Fatal("wrong code exchange form")
		}
		return reply(200, `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","token_type":"bearer","expires_in":3600}`), nil
	})
	parsed, e := url.Parse(client.AuthorizationURL("synthetic-state", "synthetic-verifier"))
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256([]byte("synthetic-verifier"))
	if parsed.Scheme != "https" || parsed.Host != "www.amazon.com" || parsed.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("PKCE missing")
	}
	tokens, e := client.ExchangeCode(context.Background(), "synthetic-code", "synthetic-verifier")
	if e != nil || !tokens.ExpiresAt.Equal(testNow.Add(time.Hour)) || tokens.RefreshToken != "synthetic-refresh" {
		t.Fatal("code exchange failed", e)
	}
}
