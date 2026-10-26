// Package worker runs the explicitly requested, read-only provider feasibility proof.
package worker

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
)

type Store interface {
	Acquire(context.Context, string, time.Time) (state.Lease, error)
	Load(context.Context, state.Lease, time.Time) (state.Record, error)
	Save(context.Context, state.Lease, state.Record, time.Time) (state.Record, error)
	Release(context.Context, state.Lease) error
}
type Provider interface {
	Refresh(context.Context, onstar.Grant) (onstar.Grant, error)
	Exchange(context.Context, onstar.Grant, string, string) (onstar.GMToken, error)
	Vehicles(context.Context, onstar.GMToken) ([]onstar.Vehicle, error)
	Location(context.Context, onstar.GMToken, string, bool) (onstar.LocationStatus, error)
}
type Request struct {
	Live        bool   `json:"live"`
	RunID       string `json:"runId"`
	DeployNonce string `json:"deployNonce"`
}
type Check struct {
	Name         string `json:"name"`
	Passed       bool   `json:"passed"`
	Milliseconds int64  `json:"milliseconds"`
}
type Result struct {
	Status             string  `json:"status"`
	Reason             string  `json:"reason,omitempty"`
	RunID              string  `json:"runId"`
	RequestID          string  `json:"requestId"`
	LogStream          string  `json:"logStream"`
	DeployNonce        string  `json:"deployNonce"`
	Version            string  `json:"version"`
	ColdStart          bool    `json:"coldStart"`
	LocationPending    bool    `json:"locationPending"`
	Checks             []Check `json:"checks"`
	TokenVersionBefore uint64  `json:"tokenVersionBefore"`
	TokenVersionAfter  uint64  `json:"tokenVersionAfter"`
	EnrollmentVersion  int64   `json:"enrollmentVersion"`
}
type Worker struct {
	Store             Store
	Provider          Provider
	DeviceID          string
	EnrollmentVersion int64
	DeployNonce       string
	Version           string
	Now               func() time.Time
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// Run never retries a provider request. The durable budget is spent before the
// first call, so a crash or replay cannot silently repeat the same proof.
func (w *Worker) Run(ctx context.Context, request Request, requestID string, cold bool) Result {
	out := Result{Status: "blocked", Reason: "invalid-proof-request", Version: w.Version, DeployNonce: w.DeployNonce, RequestID: requestID, ColdStart: cold, Checks: []Check{}}
	if !request.Live || !safeID.MatchString(request.RunID) || request.DeployNonce != w.DeployNonce || w.DeployNonce == "" || requestID == "" {
		return out
	}
	out.RunID = request.RunID
	if !cold {
		out.Reason = "warm-proof-disallowed"
		return out
	}
	now := w.Now
	if now == nil {
		now = time.Now
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Sub(now()) <= 20*time.Second {
		out.Reason = "insufficient-deadline"
		return out
	}
	providerCtx, cancel := context.WithDeadline(ctx, deadline.Add(-20*time.Second))
	defer cancel()
	lease, err := w.Store.Acquire(ctx, requestID, now())
	if err != nil {
		out.Reason = "lease-unavailable"
		return out
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = w.Store.Release(releaseCtx, lease)
	}()
	record, err := w.Store.Load(ctx, lease, now())
	if err != nil {
		out.Reason = "credential-load-failed"
		return out
	}
	if record.Bootstrap.ValidateDevice(w.DeviceID) != nil || record.Bootstrap.EnrollmentVersion != w.EnrollmentVersion {
		out.Reason = "credential-binding-mismatch"
		return out
	}
	out.TokenVersionBefore = record.TokenVersion
	out.TokenVersionAfter = record.TokenVersion
	out.EnrollmentVersion = record.Bootstrap.EnrollmentVersion
	if record.CooldownUntil > now().Unix() {
		out.Reason = "provider-cooldown"
		return out
	}
	if record.ProviderState == "reauth-required" || record.ProviderState == "disconnected" {
		out.Reason = "reauth-required"
		return out
	}
	if record.ProviderState == "vehicle-not-linked" {
		out.Reason = "vehicle-not-linked"
		return out
	}
	if record.ProofRunID == request.RunID || slices.Contains(record.ProofRunIDs, request.RunID) || record.ProofAttempts >= 3 || len(record.ProofRunIDs) >= 3 {
		out.Reason = "proof-budget-exhausted"
		return out
	}
	// Preserve the previous single-ID record when upgrading an existing grant.
	if record.ProofRunID != "" && !slices.Contains(record.ProofRunIDs, record.ProofRunID) {
		record.ProofRunIDs = append(record.ProofRunIDs, record.ProofRunID)
	}
	if len(record.ProofRunIDs) >= 3 {
		out.Reason = "proof-budget-exhausted"
		return out
	}
	record.ProofRunIDs = append(record.ProofRunIDs, request.RunID)
	record.ProofRunID = request.RunID
	record.ProofDeploymentNonce = request.DeployNonce
	record.ProofAttempts++
	record, err = w.Store.Save(ctx, lease, record, now())
	if err != nil {
		out.Reason = "proof-budget-write-failed"
		return out
	}
	out.Checks = append(out.Checks, Check{Name: "encrypted-credential-read", Passed: true})
	out.TokenVersionAfter = record.TokenVersion
	fail := func(cause error) Result {
		reason := "provider-unavailable"
		var e *onstar.Error
		if errors.As(cause, &e) {
			switch e.Kind {
			case "reauth-required", "vehicle-not-linked", "access-denied", "rate-limited", "deadline", "invalid-location-response", "account-mismatch":
				reason = e.Kind
			}
			if e.Kind == "reauth-required" || e.Kind == "account-mismatch" {
				record.ProviderState = "reauth-required"
			}
			if e.Kind == "vehicle-not-linked" {
				record.ProviderState = "vehicle-not-linked"
			}
			if e.AuthEndpoint && (e.Status == 401 || e.Status == 403 || e.Kind == "access-denied") {
				record.CooldownUntil = now().Add(6 * time.Hour).Unix()
				record.CooldownReason = "auth-endpoint-denied"
			}
		}
		record.LastProofReason = reason
		if saved, saveErr := w.Store.Save(ctx, lease, record, now()); saveErr != nil {
			reason = "failure-state-write-failed"
		} else {
			out.TokenVersionAfter = saved.TokenVersion
		}
		out.Status = "failed"
		out.Reason = reason
		return out
	}
	start := now()
	grant, err := w.Provider.Refresh(providerCtx, record.Grant)
	out.Checks = append(out.Checks, Check{Name: "b2c-refresh", Passed: err == nil, Milliseconds: now().Sub(start).Milliseconds()})
	if err != nil {
		return fail(err)
	}
	record.Grant = grant
	// The rotated refresh token must survive a crash before any GM operation.
	record, err = w.Store.Save(ctx, lease, record, now())
	if err != nil {
		out.Status = "failed"
		out.Reason = "refresh-persistence-failed"
		return out
	}
	out.Checks = append(out.Checks, Check{Name: "refresh-persisted-before-exchange", Passed: true})
	out.TokenVersionAfter = record.TokenVersion
	start = now()
	token, err := w.Provider.Exchange(providerCtx, grant, w.DeviceID, record.Bootstrap.VIN)
	out.Checks = append(out.Checks, Check{Name: "gm-exchange", Passed: err == nil, Milliseconds: now().Sub(start).Milliseconds()})
	if err != nil {
		return fail(err)
	}
	record.GMToken = token.AccessToken
	record.GMExpiresAt = token.ExpiresAt
	start = now()
	vehicles, err := w.Provider.Vehicles(providerCtx, token)
	matched := false
	for _, vehicle := range vehicles {
		if vehicle.VIN == record.Bootstrap.VIN {
			matched = true
		}
	}
	if err == nil && !matched {
		err = &onstar.Error{Kind: "vehicle-not-linked"}
	}
	out.Checks = append(out.Checks, Check{Name: "garage-membership", Passed: err == nil, Milliseconds: now().Sub(start).Milliseconds()})
	if err != nil {
		return fail(err)
	}
	start = now()
	location, err := w.Provider.Location(providerCtx, token, record.Bootstrap.VIN, false)
	out.Checks = append(out.Checks, Check{Name: "cached-location-response", Passed: err == nil, Milliseconds: now().Sub(start).Milliseconds()})
	if err != nil {
		return fail(err)
	}
	record.LocationPending = location.Pending
	out.LocationPending = location.Pending
	record.ProviderState = "ready"
	record.LastProofReason = ""
	record.CooldownUntil = 0
	record.CooldownReason = ""
	if record, err = w.Store.Save(ctx, lease, record, now()); err != nil {
		out.Status = "failed"
		out.Reason = "proof-state-write-failed"
		return out
	}
	out.TokenVersionAfter = record.TokenVersion
	out.Status = "passed"
	out.Reason = ""
	return out
}
