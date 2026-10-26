package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mcpOAuthConformance(t *testing.T, docs Documents) {
	t.Helper()
	s, a, now := mobileFixture(t, docs)
	o := MCPOAuth{Mobile: s}
	ctx := context.Background()
	client, e := o.Register(ctx, MCPClient{Name: "Synthetic connector", Redirects: []string{"https://client.example/callback"}}, "192.0.2.10")
	if e != nil {
		t.Fatal(e)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	authz := MCPAuthorization{ClientID: client.ID, RedirectURI: client.Redirects[0], State: "client-state", Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Resource: MCPResource, Scope: MCPScope}
	for _, alter := range []func(*MCPAuthorization){func(a *MCPAuthorization) { a.RedirectURI = "https://attacker.example/callback" }, func(a *MCPAuthorization) { a.Resource = MCPIssuer + "/api" }, func(a *MCPAuthorization) { a.Scope = "other" }, func(a *MCPAuthorization) { a.Challenge = "short" }} {
		bad := authz
		alter(&bad)
		if _, _, _, e = o.Prepare(ctx, bad, "192.0.2.10"); e == nil {
			t.Fatal("invalid authorization accepted")
		}
	}
	ticket, cookie, _, e := o.Prepare(ctx, authz, "192.0.2.10")
	if e != nil {
		t.Fatal(e)
	}
	wrongCookie, _ := randomHandle()
	if _, e = o.Consent(ctx, ticket, wrongCookie, "192.0.2.10"); e == nil {
		t.Fatal("consent cookie bypass")
	}
	if a.calls != 0 || a.state != "" {
		t.Fatal("Amazon reached before consent")
	}
	if _, e = o.Consent(ctx, ticket, cookie, "192.0.2.10"); e != nil {
		t.Fatal(e)
	}
	if _, e = o.Consent(ctx, ticket, cookie, "192.0.2.10"); e == nil {
		t.Fatal("consent replay accepted")
	}
	if _, handled, e := o.Callback(ctx, a.state, "synthetic-code", wrongCookie, "192.0.2.10"); !handled || e == nil {
		t.Fatal("callback cookie bypass")
	}
	redirect, handled, e := o.Callback(ctx, a.state, "synthetic-code", cookie, "192.0.2.10")
	if e != nil || !handled {
		t.Fatal("callback failed", e)
	}
	u, _ := url.Parse(redirect)
	code := u.Query().Get("code")
	if u.Scheme+"://"+u.Host+u.Path != client.Redirects[0] || u.Query().Get("state") != authz.State || u.Query().Get("iss") != MCPIssuer {
		t.Fatal("callback binding")
	}
	if _, _, e = o.Callback(ctx, a.state, "synthetic-code", cookie, "192.0.2.10"); e == nil {
		t.Fatal("callback replay accepted")
	}
	for _, values := range [][4]string{{client.ID, MCPResource, client.Redirects[0], strings.Repeat("w", 43)}, {client.ID, MCPIssuer + "/other", client.Redirects[0], verifier}, {client.ID, MCPResource, "https://attacker.example/", verifier}, {wrongCookie, MCPResource, client.Redirects[0], verifier}} {
		if _, e = o.Exchange(ctx, values[0], values[1], code, values[2], values[3]); e == nil {
			t.Fatal("unbound code exchange accepted")
		}
	}
	tokens, e := o.Exchange(ctx, client.ID, MCPResource, code, client.Redirects[0], verifier)
	if e != nil {
		t.Fatal(e)
	}
	if tokens.TokenType != "Bearer" || tokens.Scope != MCPScope || tokens.ExpiresIn <= 0 || tokens.ExpiresIn > 900 {
		t.Fatal("token contract")
	}
	if _, e = o.Exchange(ctx, client.ID, MCPResource, code, client.Redirects[0], verifier); e == nil {
		t.Fatal("code replay accepted")
	}
	family, e := o.Authorize(ctx, tokens.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authorize(ctx, tokens.AccessToken); e == nil {
		t.Fatal("MCP token accepted by mobile")
	}
	if _, e = o.Authorize(ctx, "synthetic-access"); e == nil {
		t.Fatal("Amazon token accepted by MCP")
	}
	if _, e = o.Refresh(ctx, wrongCookie, MCPResource, tokens.RefreshToken); e == nil {
		t.Fatal("refresh client bypass")
	}
	if _, e = o.Refresh(ctx, client.ID, MCPIssuer, tokens.RefreshToken); e == nil {
		t.Fatal("refresh resource bypass")
	}
	*now = now.Add(16 * time.Minute)
	if _, e = o.Authorize(ctx, tokens.AccessToken); e == nil {
		t.Fatal("expired access accepted")
	}
	a.expired = true
	rotated, e := o.Refresh(ctx, client.ID, MCPResource, tokens.RefreshToken)
	if e != nil {
		t.Fatal("refresh failed", e)
	}
	if rotated.AccessToken == tokens.AccessToken || rotated.RefreshToken == tokens.RefreshToken {
		t.Fatal("refresh did not rotate")
	}
	if got, e := o.Authorize(ctx, rotated.AccessToken); e != nil || got != family {
		t.Fatal("rotated access failed", e)
	}
	if _, e = o.Refresh(ctx, client.ID, MCPResource, tokens.RefreshToken); e == nil {
		t.Fatal("refresh replay accepted")
	}
	if _, e = o.Authorize(ctx, rotated.AccessToken); e == nil {
		t.Fatal("refresh replay did not revoke family")
	}
}
func TestMCPOAuthConformance(t *testing.T) { mcpOAuthConformance(t, nil) }

func TestMCPOAuthExpiresCodesAndConsent(t *testing.T) {
	s, a, now := mobileFixture(t, nil)
	o := MCPOAuth{Mobile: s}
	ctx := context.Background()
	client, e := o.Register(ctx, MCPClient{Redirects: []string{"https://client.example/callback"}}, "192.0.2.11")
	if e != nil {
		t.Fatal(e)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	input := MCPAuthorization{ClientID: client.ID, RedirectURI: client.Redirects[0], Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Resource: MCPResource, Scope: MCPScope}
	ticket, cookie, _, e := o.Prepare(ctx, input, "192.0.2.11")
	if e != nil {
		t.Fatal(e)
	}
	*now = now.Add(11 * time.Minute)
	if _, e = o.Consent(ctx, ticket, cookie, "192.0.2.11"); e == nil {
		t.Fatal("expired consent accepted")
	}
	ticket, cookie, _, e = o.Prepare(ctx, input, "192.0.2.11")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = o.Consent(ctx, ticket, cookie, "192.0.2.11"); e != nil {
		t.Fatal(e)
	}
	redirect, _, e := o.Callback(ctx, a.state, "synthetic-code", cookie, "192.0.2.11")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(redirect)
	*now = now.Add(61 * time.Second)
	if _, e = o.Exchange(ctx, client.ID, MCPResource, u.Query().Get("code"), client.Redirects[0], verifier); e == nil {
		t.Fatal("expired code accepted")
	}
}
func TestMCPOAuthRejectsRedirectsAndForeignOwner(t *testing.T) {
	s, a, _ := mobileFixture(t, nil)
	o := MCPOAuth{Mobile: s}
	ctx := context.Background()
	for _, uri := range []string{"https://client.example/cb#fragment", "http://client.example/cb", "https://user@client.example/cb", "javascript:alert(1)", "https://client.example/cb?code=bad", "https://client.example;script-src*/cb"} {
		if _, e := o.Register(ctx, MCPClient{Redirects: []string{uri}}, "192.0.2.1"); e == nil {
			t.Fatal("unsafe redirect accepted")
		}
	}
	client, e := o.Register(ctx, MCPClient{Redirects: []string{"http://127.0.0.1:12345/callback"}}, "192.0.2.1")
	if e != nil {
		t.Fatal(e)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	ticket, cookie, _, e := o.Prepare(ctx, MCPAuthorization{ClientID: client.ID, RedirectURI: client.Redirects[0], Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Resource: MCPResource, Scope: MCPScope}, "192.0.2.1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = o.Consent(ctx, ticket, cookie, "192.0.2.1"); e != nil {
		t.Fatal(e)
	}
	a.owner = "different-owner"
	if _, handled, e := o.Callback(ctx, a.state, "synthetic-code", cookie, "192.0.2.1"); e == nil || !handled {
		t.Fatal("foreign owner accepted")
	}
}
