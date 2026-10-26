package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/commands"
	"github.com/JeremyProffittOrg/suburban-remote/internal/lambdahttp"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/gofiber/fiber/v3"
)

const loginCookie = "__Host-mobile-login"

func publicError(c fiber.Ctx, e error) error {
	status, reason := 503, "service-unavailable"
	switch {
	case errors.Is(e, auth.ErrInvalid):
		status, reason = 400, "invalid-request"
	case errors.Is(e, auth.ErrPending):
		return c.Status(202).JSON(fiber.Map{"status": "pending"})
	case errors.Is(e, auth.ErrNotOwner), errors.Is(e, auth.ErrHandle), errors.Is(e, auth.ErrRevoked):
		status, reason = 401, "sign-in-required"
	case errors.Is(e, auth.ErrExpired), errors.Is(e, auth.ErrMissing):
		status, reason = 410, "expired"
	case errors.Is(e, auth.ErrConflict), errors.Is(e, auth.ErrRefreshRace):
		status, reason = 409, "session-conflict"
	case errors.Is(e, auth.ErrRate):
		status, reason = 429, "rate-limited"
	}
	return c.Status(status).JSON(fiber.Map{"error": reason})
}
func sessionError(c fiber.Ctx, e error) error {
	if errors.Is(e, auth.ErrExpired) || errors.Is(e, auth.ErrMissing) {
		return c.Status(401).JSON(fiber.Map{"error": "sign-in-required"})
	}
	return publicError(c, e)
}
func input(c fiber.Ctx, dst any) error {
	if !strings.HasPrefix(c.Get("Content-Type"), "application/json") || len(c.Body()) > 8192 {
		return auth.ErrInvalid
	}
	// Browser-origin writes must come from this service. Native clients send no Origin.
	if origin := c.Get("Origin"); origin != "" && origin != "https://suburban.jeremy.ninja" {
		return auth.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil {
		return auth.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return auth.ErrInvalid
	}
	return nil
}
func bearer(c fiber.Ctx) string {
	v := c.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}
func registerMobile(app *fiber.App, services Services) {
	service := services.Mobile
	app.Use(func(c fiber.Ctx) error {
		budget := 8 * time.Second
		if c.Path() == "/mcp" {
			budget = 25 * time.Second
		}
		ctx, cancel := context.WithTimeout(c.Context(), budget)
		defer cancel()
		c.SetContext(ctx)
		c.Set("Referrer-Policy", "no-referrer")
		c.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		c.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		return c.Next()
	})
	app.Post("/auth/mobile/start", func(c fiber.Ctx) error {
		var body struct {
			Challenge   string `json:"challenge"`
			DeviceLabel string `json:"deviceLabel"`
		}
		if e := input(c, &body); e != nil {
			return publicError(c, e)
		}
		result, e := service.Start(c.Context(), body.Challenge, body.DeviceLabel, lambdahttp.RequestMetadata(c.Context()).SourceIP)
		if e != nil {
			return publicError(c, e)
		}
		return c.JSON(result)
	})
	app.Get("/auth/mobile/begin", func(c fiber.Ctx) error {
		if len(c.Request().URI().QueryArgs().PeekMulti("code")) != 1 {
			return publicError(c, auth.ErrInvalid)
		}
		url, cookie, e := service.Begin(c.Context(), c.Query("code"))
		if e != nil {
			return publicError(c, e)
		}
		c.Cookie(&fiber.Cookie{Name: loginCookie, Value: cookie, Path: "/", Secure: true, HTTPOnly: true, SameSite: "Lax", MaxAge: 600})
		return c.Redirect().Status(302).To(url)
	})
	app.Get("/auth/lwa/callback", func(c fiber.Ctx) error {
		args := c.Request().URI().QueryArgs()
		if len(args.PeekMulti("state")) != 1 || len(args.PeekMulti("code")) != 1 {
			return publicError(c, auth.ErrInvalid)
		}
		redirect, handled, err := (auth.MCPOAuth{Mobile: service}).Callback(c.Context(), c.Query("state"), c.Query("code"), c.Cookies(mcpCookie), lambdahttp.RequestMetadata(c.Context()).SourceIP)
		if handled {
			c.Cookie(&fiber.Cookie{Name: mcpCookie, Value: "", Path: "/", Secure: true, HTTPOnly: true, SameSite: "Lax", MaxAge: -1})
			if err != nil {
				return oauthError(c, err)
			}
			return c.Redirect().Status(302).To(redirect)
		}
		e := service.Callback(c.Context(), c.Query("state"), c.Query("code"), c.Cookies(loginCookie))
		c.Cookie(&fiber.Cookie{Name: loginCookie, Value: "", Path: "/", Secure: true, HTTPOnly: true, SameSite: "Lax", MaxAge: -1})
		if e != nil {
			return publicError(c, e)
		}
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Signed in</title><main><h1>Owner sign-in complete</h1><p>Return to Suburban Remote. You can close this browser tab.</p></main></html>`)
	})
	app.Post("/auth/mobile/poll", func(c fiber.Ctx) error {
		var body struct {
			Code     string `json:"code"`
			Verifier string `json:"verifier"`
		}
		if e := input(c, &body); e != nil {
			return publicError(c, e)
		}
		result, e := service.Poll(c.Context(), body.Code, body.Verifier, lambdahttp.RequestMetadata(c.Context()).SourceIP)
		if e != nil {
			return publicError(c, e)
		}
		return c.JSON(result)
	})
	app.Post("/auth/mobile/cancel", func(c fiber.Ctx) error {
		var body struct {
			Code     string `json:"code"`
			Verifier string `json:"verifier"`
		}
		if e := input(c, &body); e != nil {
			return publicError(c, e)
		}
		if e := service.Cancel(c.Context(), body.Code, body.Verifier); e != nil {
			return publicError(c, e)
		}
		return c.SendStatus(204)
	})
	app.Post("/auth/session/refresh", func(c fiber.Ctx) error {
		var body struct {
			RefreshHandle string `json:"refreshHandle"`
		}
		if e := input(c, &body); e != nil {
			return publicError(c, e)
		}
		result, e := service.Refresh(c.Context(), body.RefreshHandle)
		if e != nil {
			return sessionError(c, e)
		}
		return c.JSON(result)
	})
	app.Post("/auth/session/logout", func(c fiber.Ctx) error {
		if origin := c.Get("Origin"); origin != "" && origin != "https://suburban.jeremy.ninja" {
			return publicError(c, auth.ErrInvalid)
		}
		if e := service.Logout(c.Context(), bearer(c)); e != nil {
			return sessionError(c, e)
		}
		return c.SendStatus(204)
	})
	app.Get("/api/dashboard", func(c fiber.Ctx) error {
		if _, e := service.Authorize(c.Context(), bearer(c)); e != nil {
			return sessionError(c, e)
		}
		out, e := dashboard(c.Context(), services)
		if errors.Is(e, state.ErrConflict) {
			return c.Status(409).JSON(fiber.Map{"error": "provider-busy"})
		}
		if e != nil {
			return publicError(c, auth.ErrUnavailable)
		}
		return c.JSON(out)
	})
	registerCommands(app, services)
	registerFeatures(app, services)
	registerReleases(app, services)
	registerMap(app, services)
	registerMCP(app, services)
}

func dashboard(ctx context.Context, services Services) (map[string]any, error) {
	now := time.Now().UTC()
	holder := "status-" + lambdahttp.RequestMetadata(ctx).RequestID
	lease, e := services.Provider.Acquire(ctx, holder, now)
	if e != nil {
		return nil, e
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = services.Provider.Release(releaseCtx, lease)
	}()
	record, e := services.Provider.Load(ctx, lease, now)
	if e != nil {
		return nil, e
	}
	providerState := "pending-verification"
	switch record.ProviderState {
	case "ready", "pending-verification", "vehicle-not-linked", "reauth-required":
		providerState = record.ProviderState
	}
	if record.CooldownUntil > now.Unix() {
		providerState = "cooldown"
	}
	reason := ""
	switch record.LastProofReason {
	case "provider-unavailable", "reauth-required", "vehicle-not-linked", "access-denied", "rate-limited", "deadline", "invalid-location-response", "account-mismatch", "failure-state-write-failed":
		reason = record.LastProofReason
	}
	flags := commands.Flags{ProviderPaused: true}
	if services.Flags != nil {
		flags, e = services.Flags.ReadFlags(ctx)
		if e != nil {
			return nil, e
		}
	}
	capabilities := []fiber.Map{{"name": "owner-sign-in", "available": true, "reason": ""}, {"name": "location-history", "available": false, "reason": "location-schema-unverified"}}
	for _, action := range []string{"start", "lock", "unlock"} {
		disabled := commands.DisabledReason(flags, action)
		if disabled == "" && providerState != "ready" {
			disabled = "provider-not-ready"
		}
		capabilities = append(capabilities, fiber.Map{"name": action, "available": disabled == "", "reason": disabled})
	}
	return map[string]any{"providerState": providerState, "lastProofReason": reason, "locationPending": record.LocationPending, "statusReadAt": now, "pollingEnabled": false, "controlsEnabled": flags.ActuationEnabled && !flags.ProviderPaused && providerState == "ready", "capabilities": capabilities}, nil
}
