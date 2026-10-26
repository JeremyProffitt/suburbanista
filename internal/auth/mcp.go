package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const MCPIssuer = "https://suburban.jeremy.ninja"
const MCPResource = MCPIssuer + "/mcp"
const MCPScope = "vehicle"

// MCPOAuth reuses the owner login and grant lifecycle. Its opaque tokens are
// distinct from mobile and Amazon tokens and are valid only for this resource.
type MCPOAuth struct{ Mobile *MobileService }

// MCPFailure carries only a fixed diagnostic label, never OAuth input values.
type MCPFailure struct {
	Reason string
	Cause  error
}

func (e *MCPFailure) Error() string               { return "MCP OAuth: " + e.Reason }
func (e *MCPFailure) Unwrap() error               { return e.Cause }
func mcpFailure(reason string, cause error) error { return &MCPFailure{reason, cause} }

type MCPClient struct {
	ID            string   `json:"client_id"`
	Name          string   `json:"client_name"`
	Redirects     []string `json:"redirect_uris"`
	AuthMethod    string   `json:"token_endpoint_auth_method"`
	GrantTypes    []string `json:"grant_types"`
	ResponseTypes []string `json:"response_types"`
}
type MCPAuthorization struct {
	ClientID, RedirectURI, State, Challenge, Resource, Scope string
}
type mcpRequest struct {
	Authorization                                        MCPAuthorization
	BrowserHash, Status, Handoff, Verifier, MobileCookie string
}
type mcpCode struct {
	Authorization MCPAuthorization
	Session       MobileSession
	FamilyID      string
	Used          bool
}
type mcpAccess struct{ Access, ClientID, Resource string }
type mcpRefresh struct {
	Refresh, FamilyID, ClientID, Resource string
	Used                                  bool
}
type MCPToken struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func validRedirect(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(raw, "\r\n\\") {
		return false
	}
	// Redirect origins also form a CSP source. Reject authority punctuation
	// that could introduce another policy directive rather than a host.
	if net.ParseIP(u.Hostname()) == nil && !regexp.MustCompile(`^[A-Za-z0-9.-]+$`).MatchString(u.Hostname()) {
		return false
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "[::1]" || u.Hostname() == "::1" || u.Hostname() == "localhost")) {
		return false
	}
	q, e := url.ParseQuery(u.RawQuery)
	return e == nil && !q.Has("code") && !q.Has("state") && !q.Has("iss")
}
func (o MCPOAuth) Register(ctx context.Context, client MCPClient, ip string) (MCPClient, error) {
	s := o.Mobile
	if client.ID != "" || len(client.Name) > 80 || strings.ContainsAny(client.Name, "\r\n\t") || len(client.Redirects) < 1 || len(client.Redirects) > 5 || (client.AuthMethod != "" && client.AuthMethod != "none") {
		return MCPClient{}, ErrInvalid
	}
	for _, r := range client.Redirects {
		if !validRedirect(r) {
			return MCPClient{}, ErrInvalid
		}
	}
	for _, g := range client.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return MCPClient{}, ErrInvalid
		}
	}
	for _, r := range client.ResponseTypes {
		if r != "code" {
			return MCPClient{}, ErrInvalid
		}
	}
	if e := s.limit(ctx, ip, "mcp-register", 10); e != nil {
		return MCPClient{}, e
	}
	id, e := randomHandle()
	if e != nil {
		return MCPClient{}, e
	}
	client.ID, client.AuthMethod = id, "none"
	client.GrantTypes, client.ResponseTypes = []string{"authorization_code", "refresh_token"}, []string{"code"}
	if client.Name == "" {
		client.Name = "MCP client"
	}
	e = s.Store.Commit(ctx, withData(Document{Key: "mcp-client#" + digestString(id), ExpiresAt: s.now().AddDate(5, 0, 0)}, client))
	return client, e
}
func (o MCPOAuth) client(ctx context.Context, id string) (MCPClient, error) {
	if !validHandle(id) {
		return MCPClient{}, mcpFailure("invalid-client-id", ErrInvalid)
	}
	doc, e := o.Mobile.live(ctx, "mcp-client#"+digestString(id))
	if e != nil {
		return MCPClient{}, mcpFailure("client-unavailable", e)
	}
	return decodeDocument[MCPClient](doc)
}
func (o MCPOAuth) Prepare(ctx context.Context, a MCPAuthorization, ip string) (ticket, cookie string, client MCPClient, err error) {
	client, err = o.client(ctx, a.ClientID)
	if err != nil {
		return
	}
	challenge, e := base64.RawURLEncoding.Strict().DecodeString(a.Challenge)
	if e != nil || len(challenge) != 32 || len(a.Challenge) != 43 || !slices.Contains(client.Redirects, a.RedirectURI) || a.Resource != MCPResource || a.Scope != MCPScope || len(a.State) > 2048 || strings.ContainsAny(a.State, "\r\n") {
		err = ErrInvalid
		return
	}
	if err = o.Mobile.limit(ctx, ip, "mcp-authorize", 10); err != nil {
		return
	}
	ticket, err = randomHandle()
	if err != nil {
		return
	}
	cookie, err = randomHandle()
	if err != nil {
		return
	}
	doc := Document{Key: "mcp-request#" + digestString(ticket), ExpiresAt: o.Mobile.now().Add(10 * time.Minute)}
	err = o.Mobile.Store.Commit(ctx, withData(doc, mcpRequest{Authorization: a, BrowserHash: digestString(cookie), Status: "consent"}))
	return
}
func (o MCPOAuth) request(ctx context.Context, key, cookie string) (Document, mcpRequest, error) {
	if !validHandle(cookie) {
		return Document{}, mcpRequest{}, ErrInvalid
	}
	doc, e := o.Mobile.live(ctx, key)
	if e != nil {
		return doc, mcpRequest{}, e
	}
	r, e := decodeDocument[mcpRequest](doc)
	if e != nil || subtle.ConstantTimeCompare([]byte(r.BrowserHash), []byte(digestString(cookie))) != 1 {
		return doc, r, ErrInvalid
	}
	return doc, r, nil
}
func (o MCPOAuth) Consent(ctx context.Context, ticket, cookie, ip string) (string, error) {
	if !validHandle(ticket) {
		return "", mcpFailure("consent-ticket-format", ErrInvalid)
	}
	s := o.Mobile
	doc, r, e := o.request(ctx, "mcp-request#"+digestString(ticket), cookie)
	if e != nil {
		return "", mcpFailure("consent-cookie-or-state", e)
	}
	if r.Status != "consent" {
		return "", mcpFailure("consent-already-used", ErrInvalid)
	}
	r.Status = "starting"
	if e = s.Store.Commit(ctx, withData(doc, r)); e != nil {
		return "", e
	}
	doc.Version++
	// Mobile handoffs hash decoded verifier bytes; the MCP-facing PKCE below
	// follows RFC 7636 and hashes the ASCII verifier string.
	r.Verifier, e = randomHandle()
	if e != nil {
		return "", e
	}
	raw, _ := base64.RawURLEncoding.DecodeString(r.Verifier)
	digest := sha256.Sum256(raw)
	clear(raw)
	start, e := s.Start(ctx, base64.RawURLEncoding.EncodeToString(digest[:]), "MCP", ip)
	if e != nil {
		return "", mcpFailure("consent-handoff-start", e)
	}
	redirect, cookie, e := s.Begin(ctx, start.Code)
	if e != nil {
		return "", mcpFailure("consent-handoff-begin", e)
	}
	hdoc, e := s.live(ctx, "mobile#"+digestString(start.Code))
	if e != nil {
		return "", e
	}
	h, e := decodeDocument[handoff](hdoc)
	if e != nil {
		return "", e
	}
	r.Handoff, r.MobileCookie, r.Status = start.Code, cookie, "browser"
	pointer := Document{Key: "mcp-upstream#" + h.State, ExpiresAt: doc.ExpiresAt}
	if e = s.Store.Commit(ctx, withData(doc, r), withData(pointer, doc.Key)); e != nil {
		return "", e
	}
	return redirect, nil
}

// Callback recognizes only a durable MCP state. Unrecognized states continue
// through the existing mobile callback, even when an old MCP cookie is present.
func (o MCPOAuth) Callback(ctx context.Context, state, code, cookie, ip string) (redirect string, handled bool, err error) {
	if !validHandle(state) {
		return "", false, nil
	}
	s := o.Mobile
	pointer, e := s.live(ctx, "mcp-upstream#"+digestString(state))
	if errors.Is(e, ErrMissing) {
		return "", false, nil
	}
	if e != nil {
		return "", true, e
	}
	key, e := decodeDocument[string](pointer)
	if e != nil {
		return "", true, e
	}
	doc, r, e := o.request(ctx, key, cookie)
	if e != nil {
		return "", true, mcpFailure("callback-browser-binding", e)
	}
	if r.Status != "browser" {
		return "", true, ErrInvalid
	}
	r.Status = "processing"
	if e = s.Store.Commit(ctx, withData(doc, r)); e != nil {
		return "", true, e
	}
	doc.Version++
	if e = s.Callback(ctx, state, code, r.MobileCookie); e != nil {
		return "", true, mcpFailure("lwa-owner-callback", e)
	}
	session, e := s.Poll(ctx, r.Handoff, r.Verifier, ip)
	if e != nil {
		return "", true, mcpFailure("owner-session-issue", e)
	}
	family, e := s.Authorize(ctx, session.AccessHandle)
	if e != nil {
		return "", true, e
	}
	grant, e := randomHandle()
	if e != nil {
		return "", true, e
	}
	r.Status, r.Verifier, r.MobileCookie, r.Handoff = "complete", "", "", ""
	grantDoc := Document{Key: "mcp-code#" + digestString(grant), ExpiresAt: s.now().Add(time.Minute)}
	if e = s.Store.Commit(ctx, withData(doc, r), withData(grantDoc, mcpCode{Authorization: r.Authorization, Session: session, FamilyID: family})); e != nil {
		return "", true, e
	}
	u, _ := url.Parse(r.Authorization.RedirectURI)
	q := u.Query()
	q.Set("code", grant)
	q.Set("state", r.Authorization.State)
	q.Set("iss", MCPIssuer)
	u.RawQuery = q.Encode()
	return u.String(), true, nil
}

var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func (o MCPOAuth) Exchange(ctx context.Context, clientID, resource, code, redirect, verifier string) (MCPToken, error) {
	if resource != MCPResource {
		return MCPToken{}, mcpFailure("resource-mismatch", ErrInvalid)
	}
	if !validHandle(code) {
		return MCPToken{}, mcpFailure("invalid-code-format", ErrInvalid)
	}
	if !verifierPattern.MatchString(verifier) {
		return MCPToken{}, mcpFailure("invalid-verifier-format", ErrInvalid)
	}
	if _, e := o.client(ctx, clientID); e != nil {
		return MCPToken{}, e
	}
	doc, e := o.Mobile.live(ctx, "mcp-code#"+digestString(code))
	if e != nil {
		label := "code-unavailable"
		if errors.Is(e, ErrExpired) {
			label = "code-expired"
		}
		if errors.Is(e, ErrMissing) {
			label = "code-not-found"
		}
		return MCPToken{}, mcpFailure(label, e)
	}
	c, e := decodeDocument[mcpCode](doc)
	if e != nil {
		return MCPToken{}, e
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if c.Used {
		return MCPToken{}, mcpFailure("code-already-used", ErrInvalid)
	}
	if c.Authorization.ClientID != clientID {
		return MCPToken{}, mcpFailure("code-client-mismatch", ErrInvalid)
	}
	if c.Authorization.Resource != resource {
		return MCPToken{}, mcpFailure("code-resource-mismatch", ErrInvalid)
	}
	if c.Authorization.RedirectURI != redirect {
		return MCPToken{}, mcpFailure("redirect-mismatch", ErrInvalid)
	}
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(c.Authorization.Challenge)) != 1 {
		return MCPToken{}, mcpFailure("pkce-mismatch", ErrInvalid)
	}
	c.Used = true
	session := c.Session
	c.Session = MobileSession{}
	return o.issue(ctx, clientID, session, c.FamilyID, withData(doc, c))
}
func (o MCPOAuth) issue(ctx context.Context, client string, session MobileSession, family string, consumed Document) (MCPToken, error) {
	s := o.Mobile
	if _, e := s.Authorize(ctx, session.AccessHandle); e != nil {
		return MCPToken{}, e
	}
	access, e := randomHandle()
	if e != nil {
		return MCPToken{}, e
	}
	refresh, e := randomHandle()
	if e != nil {
		return MCPToken{}, e
	}
	fdoc, e := s.live(ctx, "family#"+family)
	if e != nil {
		return MCPToken{}, e
	}
	adoc := withData(Document{Key: "mcp-access#" + digestString(access), ExpiresAt: session.AccessExpiresAt}, mcpAccess{session.AccessHandle, client, MCPResource})
	rdoc := withData(Document{Key: "mcp-refresh#" + digestString(refresh), ExpiresAt: fdoc.ExpiresAt}, mcpRefresh{Refresh: session.RefreshHandle, FamilyID: family, ClientID: client, Resource: MCPResource})
	if e = s.Store.Commit(ctx, consumed, adoc, rdoc); e != nil {
		return MCPToken{}, e
	}
	return MCPToken{access, "Bearer", int64(session.AccessExpiresAt.Sub(s.now()).Seconds()), refresh, MCPScope}, nil
}
func (o MCPOAuth) Refresh(ctx context.Context, client, resource, handle string) (MCPToken, error) {
	if resource != MCPResource || !validHandle(handle) {
		return MCPToken{}, ErrInvalid
	}
	if _, e := o.client(ctx, client); e != nil {
		return MCPToken{}, e
	}
	s := o.Mobile
	doc, e := s.live(ctx, "mcp-refresh#"+digestString(handle))
	if e != nil {
		return MCPToken{}, e
	}
	r, e := decodeDocument[mcpRefresh](doc)
	if e != nil {
		return MCPToken{}, e
	}
	if r.ClientID != client || r.Resource != resource {
		return MCPToken{}, ErrInvalid
	}
	if r.Used {
		if e = s.revokeFamily(ctx, r.FamilyID); e != nil {
			return MCPToken{}, e
		}
		return MCPToken{}, ErrRevoked
	}
	r.Used = true
	if e = s.Store.Commit(ctx, withData(doc, r)); e != nil {
		return MCPToken{}, e
	}
	doc.Version++
	session, e := s.Refresh(ctx, r.Refresh)
	if e != nil {
		return MCPToken{}, e
	}
	r.Refresh = ""
	return o.issue(ctx, client, session, r.FamilyID, withData(doc, r))
}
func (o MCPOAuth) Authorize(ctx context.Context, token string) (string, error) {
	if !validHandle(token) {
		return "", ErrHandle
	}
	doc, e := o.Mobile.live(ctx, "mcp-access#"+digestString(token))
	if e != nil {
		return "", e
	}
	a, e := decodeDocument[mcpAccess](doc)
	if e != nil {
		return "", e
	}
	if a.Resource != MCPResource {
		return "", ErrHandle
	}
	if _, e = o.client(ctx, a.ClientID); e != nil {
		return "", e
	}
	return o.Mobile.Authorize(ctx, a.Access)
}
