package amazon

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MobileCallback = "https://suburban.jeremy.ninja/auth/lwa/callback"

func (c *Client) AuthorizationURL(state, verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return "https://www.amazon.com/ap/oa?" + url.Values{"client_id": {c.clientID}, "scope": {"profile:user_id"}, "response_type": {"code"}, "redirect_uri": {MobileCallback}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}}.Encode()
}
func (c *Client) ExchangeCode(ctx context.Context, code, verifier string) (Tokens, error) {
	if !validToken(code) || !validToken(verifier) || c.clientSecret == "" {
		return Tokens{}, failure(ConfigError, "invalid-code-exchange")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {MobileCallback}, "client_id": {c.clientID}, "client_secret": {c.clientSecret}, "code_verifier": {verifier}}
	var response struct {
		Access   string      `json:"access_token"`
		Refresh  string      `json:"refresh_token"`
		Type     string      `json:"token_type"`
		Lifetime json.Number `json:"expires_in"`
	}
	now := c.now()
	if e := c.request(ctx, http.MethodPost, "https://api.amazon.com/auth/o2/token", "", strings.NewReader(form.Encode()), &response); e != nil {
		return Tokens{}, e
	}
	lifetime, e := boundedLifetime(response.Lifetime)
	if e != nil || !validToken(response.Access) || !validToken(response.Refresh) || !strings.EqualFold(response.Type, "bearer") {
		return Tokens{}, failure(AuthorityUnavailable, "invalid-code-response")
	}
	return Tokens{AccessToken: response.Access, RefreshToken: response.Refresh, ExpiresAt: now.Add(time.Duration(lifetime) * time.Second)}, nil
}
