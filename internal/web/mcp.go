package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/commands"
	"github.com/JeremyProffittOrg/suburban-remote/internal/lambdahttp"
	"github.com/JeremyProffittOrg/suburban-remote/internal/mcpbridge"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpCookie = "__Host-mcp-consent"

var mcpConsentPage = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Connect Suburban</title><style>body{font:18px system-ui;background:#fff;color:#111;max-width:640px;margin:3rem auto;padding:1rem}button{font:inherit;padding:1rem}code{overflow-wrap:anywhere}</style><h1>Connect {{.Name}} to Suburban?</h1><p>This client can read your vehicle status and use enabled vehicle controls, including start, lock and unlock.</p><p>Return address: <code>{{.Redirect}}</code></p><p>Continue only if you started this connection. Sign in with the vehicle owner's Amazon account.</p><form method="post" action="/oauth/consent"><input type="hidden" name="ticket" value="{{.Ticket}}"><button type="submit">Continue with Amazon</button></form></html>`))

func oauthError(c fiber.Ctx, e error) error {
	status, code := 400, "invalid_grant"
	reason := "invalid-request"
	var failure *auth.MCPFailure
	if errors.As(e, &failure) {
		reason = failure.Reason
	}
	if c.Path() != "/oauth/token" {
		code = "invalid_request"
	}
	if errors.Is(e, auth.ErrUnavailable) {
		status, code = 503, "temporarily_unavailable"
	}
	if errors.Is(e, auth.ErrRate) {
		status, code = 429, "temporarily_unavailable"
	}
	return c.Status(status).JSON(fiber.Map{"error": code, "error_description": reason})
}
func singleValues(raw string) (url.Values, error) {
	v, e := url.ParseQuery(raw)
	if e != nil {
		return nil, auth.ErrInvalid
	}
	for _, values := range v {
		if len(values) != 1 {
			return nil, auth.ErrInvalid
		}
	}
	return v, nil
}
func oauthForm(c fiber.Ctx) (url.Values, error) {
	if len(c.Body()) > 8192 || !strings.HasPrefix(c.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return nil, &auth.MCPFailure{Reason: "form-content-type-or-size", Cause: auth.ErrInvalid}
	}
	if origin := c.Get("Origin"); origin != "" && origin != auth.MCPIssuer {
		return nil, &auth.MCPFailure{Reason: "form-origin", Cause: auth.ErrInvalid}
	}
	return singleValues(string(c.Body()))
}
func registerMCP(app *fiber.App, services Services) {
	oauth := auth.MCPOAuth{Mobile: services.Mobile}
	metadata := func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"resource": auth.MCPResource, "authorization_servers": []string{auth.MCPIssuer}, "scopes_supported": []string{auth.MCPScope}, "bearer_methods_supported": []string{"header"}, "resource_name": "Suburban"})
	}
	app.Get("/.well-known/oauth-protected-resource", metadata)
	app.Get("/.well-known/oauth-protected-resource/mcp", metadata)
	app.Get("/.well-known/oauth-authorization-server", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"issuer": auth.MCPIssuer, "authorization_endpoint": auth.MCPIssuer + "/oauth/authorize", "token_endpoint": auth.MCPIssuer + "/oauth/token", "registration_endpoint": auth.MCPIssuer + "/oauth/register", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{auth.MCPScope}, "authorization_response_iss_parameter_supported": true})
	})
	app.Post("/oauth/register", func(c fiber.Ctx) error {
		// Ignore optional RFC 7591 display metadata; never fetch supplied URLs.
		var client auth.MCPClient
		if len(c.Body()) > 8192 || !strings.HasPrefix(c.Get("Content-Type"), "application/json") || json.Unmarshal(c.Body(), &client) != nil {
			return oauthError(c, auth.ErrInvalid)
		}
		if origin := c.Get("Origin"); origin != "" && origin != auth.MCPIssuer {
			return oauthError(c, auth.ErrInvalid)
		}
		out, e := oauth.Register(c.Context(), client, lambdahttp.RequestMetadata(c.Context()).SourceIP)
		if e != nil {
			return oauthError(c, e)
		}
		return c.Status(201).JSON(out)
	})
	app.Get("/oauth/authorize", func(c fiber.Ctx) error {
		q, e := singleValues(string(c.Request().URI().QueryString()))
		if e != nil || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
			return oauthError(c, auth.ErrInvalid)
		}
		scope := q.Get("scope")
		if scope == "" {
			scope = auth.MCPScope
		}
		ticket, cookie, client, e := oauth.Prepare(c.Context(), auth.MCPAuthorization{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), State: q.Get("state"), Challenge: q.Get("code_challenge"), Resource: q.Get("resource"), Scope: scope}, lambdahttp.RequestMetadata(c.Context()).SourceIP)
		if e != nil {
			return oauthError(c, e)
		}
		var body bytes.Buffer
		if mcpConsentPage.Execute(&body, struct{ Name, Redirect, Ticket string }{client.Name, q.Get("redirect_uri"), ticket}) != nil {
			return oauthError(c, auth.ErrUnavailable)
		}
		c.Cookie(&fiber.Cookie{Name: mcpCookie, Value: cookie, Path: "/", Secure: true, HTTPOnly: true, SameSite: "Lax", MaxAge: 600})
		// Fetch serializes Origin as null on form POSTs from no-referrer pages.
		// Preserve the origin without forwarding the authorization query string.
		c.Set("Referrer-Policy", "strict-origin")
		// Chrome enforces form-action across the POST's redirect chain.
		// Prepare matched this URI against the validated registered client.
		returnURI, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			return oauthError(c, auth.ErrInvalid)
		}
		returnOrigin := returnURI.Scheme + "://" + returnURI.Host
		c.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://www.amazon.com "+returnOrigin+"; frame-ancestors 'none'")
		c.Set("X-Frame-Options", "DENY")
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.Send(body.Bytes())
	})
	app.Post("/oauth/consent", func(c fiber.Ctx) error {
		v, e := oauthForm(c)
		if e != nil {
			return oauthError(c, e)
		}
		redirect, e := oauth.Consent(c.Context(), v.Get("ticket"), c.Cookies(mcpCookie), lambdahttp.RequestMetadata(c.Context()).SourceIP)
		if e != nil {
			return oauthError(c, e)
		}
		return c.Redirect().Status(302).To(redirect)
	})
	app.Post("/oauth/token", func(c fiber.Ctx) error {
		v, e := oauthForm(c)
		if e != nil {
			return oauthError(c, e)
		}
		if c.Get("Authorization") != "" || v.Get("client_secret") != "" {
			return oauthError(c, &auth.MCPFailure{Reason: "unexpected-client-authentication", Cause: auth.ErrInvalid})
		}
		if v.Get("scope") != "" && v.Get("scope") != auth.MCPScope {
			return oauthError(c, &auth.MCPFailure{Reason: "scope-mismatch", Cause: auth.ErrInvalid})
		}
		var token auth.MCPToken
		switch v.Get("grant_type") {
		case "authorization_code":
			token, e = oauth.Exchange(c.Context(), v.Get("client_id"), v.Get("resource"), v.Get("code"), v.Get("redirect_uri"), v.Get("code_verifier"))
		case "refresh_token":
			token, e = oauth.Refresh(c.Context(), v.Get("client_id"), v.Get("resource"), v.Get("refresh_token"))
		default:
			return c.Status(400).JSON(fiber.Map{"error": "unsupported_grant_type"})
		}
		if e != nil {
			return oauthError(c, e)
		}
		return c.JSON(token)
	})
	app.All("/mcp", func(c fiber.Ctx) error {
		if origin := c.Get("Origin"); origin != "" && origin != auth.MCPIssuer {
			return c.SendStatus(403)
		}
		family, e := oauth.Authorize(c.Context(), bearer(c))
		if e != nil {
			if errors.Is(e, auth.ErrUnavailable) {
				return c.SendStatus(503)
			}
			c.Set("WWW-Authenticate", strings.Join([]string{"Bearer", `resource_metadata="` + auth.MCPIssuer + `/.well-known/oauth-protected-resource/mcp", scope="vehicle"`}, " "))
			return c.Status(401).JSON(fiber.Map{"error": "invalid_token"})
		}
		api := mcpOwnerAPI{services: services, family: family}
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpbridge.New(api) }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 32768, PropagateRequestCancellation: true})
		// Restore the Lambda deadline instead of using fasthttp's non-canceling context.
		return adaptor.HTTPHandlerWithContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, ok := adaptor.LocalContextFromHTTPRequest(r)
			if !ok {
				http.Error(w, "request context unavailable", 503)
				return
			}
			handler.ServeHTTP(w, r.WithContext(ctx))
		}))(c)
	})
}

type mcpOwnerAPI struct {
	services Services
	family   string
}

func (a mcpOwnerAPI) invoke(ctx context.Context, operation string, configure func(*contract.AlexaCommandRequest)) (contract.AlexaCommandResponse, error) {
	if a.services.Commands == nil {
		return contract.AlexaCommandResponse{}, auth.ErrUnavailable
	}
	token, e := a.services.Mobile.CommandFamily(ctx, a.family)
	if e != nil {
		return contract.AlexaCommandResponse{}, e
	}
	r := contract.AlexaCommandRequest{Kind: "alexa-command", Source: "mobile", Operation: operation, FamilyID: a.family, PersonAccessToken: token, RequestID: lambdahttp.RequestMetadata(ctx).RequestID, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
	if configure != nil {
		configure(&r)
	}
	callCtx, cancel := context.WithTimeout(ctx, 4500*time.Millisecond)
	defer cancel()
	return a.services.Commands.Command(callCtx, r)
}
func (a mcpOwnerAPI) Features(ctx context.Context) (contract.FeaturesResponse, error) {
	out, e := a.invoke(ctx, "features", nil)
	if e != nil {
		return contract.FeaturesResponse{}, e
	}
	if out.State == "rejected" || out.State == "unconfirmed" {
		return contract.FeaturesResponse{}, auth.ErrUnavailable
	}
	return contract.FeaturesResponse{Features: contract.FeatureCatalog(), Availability: out.Availability, FetchedAt: time.Now().UTC()}, nil
}
func (a mcpOwnerAPI) Dashboard(ctx context.Context) (map[string]any, error) {
	return dashboard(ctx, a.services)
}
func (a mcpOwnerAPI) Status(ctx context.Context, id string) (contract.AlexaCommandResponse, error) {
	if len(id) > 128 {
		return contract.AlexaCommandResponse{}, auth.ErrInvalid
	}
	return a.invoke(ctx, "status", func(r *contract.AlexaCommandRequest) {
		if id != "" {
			r.CommandID = commands.IntentID("mobile", a.family, id)
		}
	})
}
func (a mcpOwnerAPI) Read(ctx context.Context, id string, _ bool) (contract.AlexaCommandResponse, error) {
	return a.invoke(ctx, "read", func(r *contract.AlexaCommandRequest) { r.FeatureID = id })
}
func (a mcpOwnerAPI) Submit(ctx context.Context, action, id string, options contract.CommandOptions) (contract.AlexaCommandResponse, error) {
	return a.invoke(ctx, "submit", func(r *contract.AlexaCommandRequest) { r.Action = action; r.RequestID = id; r.Options = &options })
}
