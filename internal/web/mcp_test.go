package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-lambda-go/events"
)

type mcpDocuments struct {
	mu    sync.Mutex
	items map[string]auth.Document
}

func (m *mcpDocuments) Load(_ context.Context, key string) (auth.Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.items[key]
	if !ok {
		return d, auth.ErrMissing
	}
	return d, nil
}
func (m *mcpDocuments) Commit(_ context.Context, docs ...auth.Document) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range docs {
		if m.items[d.Key].Version != d.Version {
			return auth.ErrConflict
		}
	}
	for _, d := range docs {
		d.Version++
		m.items[d.Key] = d
	}
	return nil
}

type mcpAuthority struct{ state string }

func (a *mcpAuthority) AuthorizationURL(state, verifier string) string {
	a.state = state
	return "https://www.amazon.com/ap/oa?state=" + state
}
func (*mcpAuthority) ExchangeCode(context.Context, string, string) (amazon.Tokens, error) {
	return amazon.Tokens{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (*mcpAuthority) Validate(context.Context, string) (amazon.Identity, error) {
	return amazon.Identity{Subject: "synthetic-owner", ValidatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (a *mcpAuthority) ValidateRefreshed(ctx context.Context, token string) (amazon.Identity, error) {
	return a.Validate(ctx, token)
}
func (a *mcpAuthority) Refresh(ctx context.Context, _ string) (amazon.Tokens, error) {
	return a.ExchangeCode(ctx, "", "")
}

type mcpBroker struct {
	brokerStub
	deadline bool
	reject   bool
}

func (b *mcpBroker) Command(ctx context.Context, r contract.AlexaCommandRequest) (contract.AlexaCommandResponse, error) {
	_, b.deadline = ctx.Deadline()
	if b.reject && r.Operation == "submit" {
		return contract.AlexaCommandResponse{State: "rejected", Reason: "disabled"}, nil
	}
	if r.Operation == "features" {
		b.calls = append(b.calls, r)
		return contract.AlexaCommandResponse{State: "available", Availability: []contract.FeatureAvailability{{ID: "diagnostics", Available: true}, {ID: "start", Available: true}, {ID: "unlock", Available: false}}}, nil
	}
	return b.brokerStub.Command(ctx, r)
}

func TestLambdaMCPOAuthAndStatelessProtocol(t *testing.T) {
	docs := &mcpDocuments{items: map[string]auth.Document{}}
	authority := &mcpAuthority{}
	broker := &mcpBroker{}
	service := &auth.MobileService{Store: docs, Amazon: authority, Owner: "synthetic-owner"}
	adapter, e := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-24T12:00:00Z"}, Services{Mobile: service, Provider: &state.Store{}, Commands: broker})
	if e != nil {
		t.Fatal(e)
	}
	consentOrigin := ""
	send := func(method, path, query, body, contentType, token string, cookies []string) events.APIGatewayV2HTTPResponse {
		event := request(t)
		event.RawPath = path
		event.RawQueryString = query
		event.RequestContext.HTTP.Method = method
		event.RequestContext.HTTP.SourceIP = "192.0.2.40"
		event.RequestContext.RequestID = "synthetic-api-request"
		event.RequestContext.DomainName = "suburban.jeremy.ninja"
		event.Body = body
		event.Cookies = cookies
		event.Headers = map[string]string{"content-type": contentType, "accept": "application/json, text/event-stream", "mcp-protocol-version": "2025-11-25"}
		if path == "/oauth/consent" && consentOrigin != "" {
			event.Headers["origin"] = consentOrigin
		}
		if token != "" {
			event.Headers["authorization"] = "Bearer " + token
		}
		out, err := adapter.Handle(context.Background(), event)
		if err != nil {
			t.Fatal("Lambda adapter failed")
		}
		if !strings.Contains(out.Headers["cache-control"], "no-store") {
			t.Fatal("cached private response", method, path, out.StatusCode, out.Headers["cache-control"])
		}
		return out
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server"} {
		out := send("GET", path, "", "", "", "", nil)
		if out.StatusCode != 200 || !strings.Contains(out.Body, auth.MCPIssuer) {
			t.Fatal("discovery failed", path, out.StatusCode)
		}
	}
	out := send("POST", "/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"proof","version":"1"}}}`, "application/json", "", nil)
	if out.StatusCode != 401 || !strings.Contains(out.Headers["www-authenticate"], "oauth-protected-resource/mcp") || len(broker.calls) != 0 {
		t.Fatal("missing auth challenge")
	}
	out = send("POST", "/oauth/register", "", `{"client_name":"Synthetic connector","redirect_uris":["https://client.example/callback"],"token_endpoint_auth_method":"none"}`, "application/json", "", nil)
	var client auth.MCPClient
	if out.StatusCode != 201 || json.Unmarshal([]byte(out.Body), &client) != nil {
		t.Fatal("registration failed", out.StatusCode)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {client.ID}, "redirect_uri": {client.Redirects[0]}, "response_type": {"code"}, "state": {"synthetic-client-state"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {auth.MCPResource}, "scope": {auth.MCPScope}}
	out = send("GET", "/oauth/authorize", q.Encode()+"&resource=other", "", "", "", nil)
	if out.StatusCode != 400 {
		t.Fatal("duplicate query accepted")
	}
	out = send("GET", "/oauth/authorize", q.Encode(), "", "", "", nil)
	if out.Headers["referrer-policy"] != "strict-origin" {
		t.Fatal("consent navigation suppresses the browser Origin header")
	}
	if !strings.Contains(out.Headers["content-security-policy"], "form-action 'self' https://www.amazon.com https://client.example;") {
		t.Fatal("consent form policy blocks the registered OAuth redirect chain")
	}
	if out.StatusCode != 200 || !strings.Contains(out.Body, client.Redirects[0]) || !strings.Contains(out.Body, "start, lock and unlock") || authority.state != "" || out.Headers["x-frame-options"] != "DENY" {
		t.Fatal("missing client consent")
	}
	match := regexp.MustCompile(`name="ticket" value="([A-Za-z0-9_-]+)"`).FindStringSubmatch(out.Body)
	if len(match) != 2 {
		t.Fatal("missing CSRF ticket")
	}
	cookieResponse := http.Response{Header: http.Header{"Set-Cookie": out.Cookies}}
	cookies := []string{}
	for _, c := range cookieResponse.Cookies() {
		if c.Name == mcpCookie {
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Fatal("unsafe cookie")
			}
			cookies = append(cookies, c.Name+"="+c.Value)
		}
	}
	body := url.Values{"ticket": {match[1]}}.Encode()
	consentOrigin = "null"
	out = send("POST", "/oauth/consent", "", body, "application/x-www-form-urlencoded", "", cookies)
	if out.StatusCode != 400 || !strings.Contains(out.Body, "form-origin") {
		t.Fatal("opaque origin was accepted")
	}
	consentOrigin = auth.MCPIssuer
	out = send("POST", "/oauth/consent", "", body, "application/x-www-form-urlencoded", "", nil)
	if out.StatusCode != 400 || !strings.Contains(out.Body, "consent-cookie-or-state") {
		t.Fatal("consent without cookie")
	}
	out = send("POST", "/oauth/consent", "", body, "application/x-www-form-urlencoded", "", cookies)
	if out.StatusCode != 302 || !strings.HasPrefix(out.Headers["location"], "https://www.amazon.com/ap/oa") {
		t.Fatal("LWA redirect failed")
	}
	out = send("GET", "/auth/lwa/callback", url.Values{"state": {authority.state}, "code": {"synthetic-code"}}.Encode(), "", "", "", cookies)
	if out.StatusCode != 302 {
		t.Fatal("LWA owner callback failed", out.StatusCode)
	}
	redirect, _ := url.Parse(out.Headers["location"])
	if redirect.Host != "client.example" || redirect.Query().Get("state") != "synthetic-client-state" {
		t.Fatal("client return binding")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {client.ID}, "redirect_uri": {client.Redirects[0]}, "code": {redirect.Query().Get("code")}, "code_verifier": {verifier}, "resource": {auth.MCPResource}}
	form.Set("resource", "https://other.example/mcp")
	out = send("POST", "/oauth/token", "", form.Encode(), "application/x-www-form-urlencoded", "", nil)
	if out.StatusCode != 400 || !strings.Contains(out.Body, `"error":"invalid_grant"`) || !strings.Contains(out.Body, "resource-mismatch") || strings.Contains(out.Body, form.Get("code")) {
		t.Fatal("unsafe or missing token failure reason")
	}
	form.Set("resource", auth.MCPResource)
	out = send("POST", "/oauth/token", "", form.Encode(), "application/x-www-form-urlencoded", "synthetic-client-auth", nil)
	if out.StatusCode != 400 || !strings.Contains(out.Body, "unexpected-client-authentication") || strings.Contains(out.Body, "synthetic-client-auth") {
		t.Fatal("client authentication failure reason")
	}
	out = send("POST", "/oauth/token", "", form.Encode(), "application/x-www-form-urlencoded", "", nil)
	var token auth.MCPToken
	if out.StatusCode != 200 || json.Unmarshal([]byte(out.Body), &token) != nil || token.AccessToken == "" {
		t.Fatal("token exchange failed", out.StatusCode)
	}
	if strings.Contains(out.Body, "synthetic-access") || strings.Contains(out.Body, "synthetic-refresh") {
		t.Fatal("upstream credentials leaked")
	}
	for _, rpc := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"proof","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"vehicle_diagnostics","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"vehicle_start","arguments":{"request_id":"synthetic-stable-intent"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"latest_command_status","arguments":{"request_id":"synthetic-stable-intent"}}}`,
	} {
		out = send("POST", "/mcp", "", rpc, "application/json", token.AccessToken, nil)
		var result struct {
			Result json.RawMessage
			Error  json.RawMessage
		}
		_ = json.Unmarshal([]byte(out.Body), &result)
		if out.StatusCode != 200 || len(result.Error) > 0 || len(result.Result) == 0 || strings.Contains(string(result.Result), `"isError":true`) {
			t.Fatal("MCP protocol failed", out.StatusCode, string(result.Error))
		}
		if strings.Contains(rpc, `"tools/list"`) && (!strings.Contains(out.Body, "vehicle_diagnostics") || strings.Contains(out.Body, "vehicle_unlock")) {
			t.Fatal("availability list wrong")
		}
		if out.Headers["mcp-session-id"] != "" {
			t.Fatal("Lambda retained MCP session")
		}
	}
	if !broker.deadline {
		t.Fatal("Lambda deadline lost")
	}
	var submitted, lookedUp bool
	for _, call := range broker.calls {
		if call.PersonAccessToken != "synthetic-access" || call.FamilyID == "" || call.Source != "mobile" {
			t.Fatal("broker authority wrong")
		}
		if call.Operation == "submit" {
			submitted = call.RequestID == "synthetic-stable-intent"
		}
		if call.Operation == "status" {
			lookedUp = call.CommandID != ""
		}
	}
	if !submitted || !lookedUp {
		t.Fatal("stable command routing missing")
	}
	broker.reject = true
	out = send("POST", "/mcp", "", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"vehicle_start","arguments":{"request_id":"synthetic-denied-intent"}}}`, "application/json", token.AccessToken, nil)
	var rejected struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if out.StatusCode != 200 || json.Unmarshal([]byte(out.Body), &rejected) != nil || !rejected.Result.IsError || !strings.Contains(out.Body, "rejected") {
		t.Fatal("rejected command reported as tool success")
	}
	out = send("GET", "/mcp", "", "", "", token.AccessToken, nil)
	if out.StatusCode != 405 {
		t.Fatal("Lambda opened long-lived stream")
	}
	out = send("POST", "/oauth/token", "", form.Encode(), "application/x-www-form-urlencoded", "", nil)
	if out.StatusCode != 400 || !strings.Contains(out.Body, "code-already-used") {
		t.Fatal("code replay accepted")
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {client.ID}, "resource": {auth.MCPResource}, "refresh_token": {token.RefreshToken}}
	out = send("POST", "/oauth/token", "", refresh.Encode(), "application/x-www-form-urlencoded", "", nil)
	if out.StatusCode != 200 {
		t.Fatal("HTTP refresh failed", out.StatusCode)
	}
}
