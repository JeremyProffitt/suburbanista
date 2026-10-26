package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-lambda-go/events"
)

func terminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "expired" || state == "dry-run"
}
func (s *Service) finish(ctx context.Context, doc *auth.Document, c *command, state, reason string) error {
	// Losing permission or connectivity after submission is not evidence that
	// the vehicle failed to act. Only a provider-confirmed failure is terminal.
	if (state == "failed" || state == "expired") && c.Result.RequestID != "" && c.Result.State != "failed" {
		state = "outcome-unknown"
	}
	c.State = state
	c.Reason = reason
	c.UpdatedAt = s.now().Unix()
	if terminal(state) || (state == "outcome-unknown" && c.Result.RequestID == "") {
		c.PersonAccessToken = ""
	}
	return s.save(ctx, doc, *c)
}

// Dispatch acknowledges every handled outcome. Only durable storage failure
// returns an error; redelivery must never cause a second Submit.
func (s *Service) Dispatch(ctx context.Context, message events.SQSMessage) (err error) {
	var pointer Pointer
	if message.EventSource != "aws:sqs" || message.EventSourceARN != s.Config.QueueARN || message.Attributes["MessageGroupId"] != "cmd-"+s.Config.VehicleKey || len(message.Body) > 512 || json.Unmarshal([]byte(message.Body), &pointer) != nil || pointer.Kind != "command" || !commandIDPattern.MatchString(pointer.CommandID) {
		return nil
	}
	doc, e := s.Docs.Load(ctx, "command#"+pointer.CommandID)
	if errors.Is(e, auth.ErrMissing) {
		return nil
	}
	if e != nil {
		return ErrPersistence
	}
	c, e := decoded[command](doc)
	if e != nil {
		return ErrPersistence
	}
	if c.ID != pointer.CommandID {
		return nil
	}
	defer func() {
		if recover() != nil {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			err = s.finish(saveCtx, &doc, &c, "outcome-unknown", "worker-panic")
		}
	}()
	if terminal(c.State) {
		return nil
	}
	if contract.ValidateCommandOptions(c.Action, c.Options) != nil {
		return s.finish(ctx, &doc, &c, "failed", "invalid-command-options")
	}
	if e = s.derive(ctx, &doc, &c); e != nil {
		return e
	}
	if terminal(c.State) {
		return nil
	}
	// A crash between persisted dispatching and the request cannot be distinguished
	// from a crash immediately after GM accepted it. Never submit again.
	if c.State == "dispatching" || (c.State == "outcome-unknown" && c.Result.RequestID == "") {
		return s.finish(ctx, &doc, &c, "outcome-unknown", "submission-not-replayed")
	}
	receiveCount, e := strconv.Atoi(message.Attributes["ApproximateReceiveCount"])
	if e != nil || receiveCount < 1 {
		return s.finish(ctx, &doc, &c, "expired", "invalid-delivery")
	}
	reconcile := (c.State == "submitted" || c.State == "outcome-unknown") && c.Result.RequestID != "" && c.Result.StatusURL != ""
	if receiveCount > 1 && !reconcile {
		return s.finish(ctx, &doc, &c, "expired", "redelivery-not-dispatched")
	}
	if reason := s.gated(ctx, c.Action, c.DryRun); reason != "" {
		if reconcile {
			return s.finish(ctx, &doc, &c, "outcome-unknown", reason)
		}
		return s.finish(ctx, &doc, &c, "failed", reason)
	}
	ownerDoc, e := s.ownerDocument(ctx)
	if e != nil {
		return s.finish(ctx, &doc, &c, "failed", "owner-binding-unverified")
	}
	userDoc, user, e := s.user(ctx, c.UserHash, false)
	if e != nil {
		return ErrPersistence
	}
	personDoc, e := s.Docs.Load(ctx, "alexa-person#"+c.PersonHash)
	if errors.Is(e, auth.ErrMissing) {
		return s.finish(ctx, &doc, &c, "failed", "binding-missing")
	}
	if e != nil {
		return ErrPersistence
	}
	person, e := decoded[personBinding](personDoc)
	if e != nil {
		return ErrPersistence
	}
	if !user.Active || user.Generation != c.UserGeneration || person.UserGeneration != user.Generation || person.Owner != s.Config.Owner || person.UserHash != c.UserHash || person.PersonHash != c.PersonHash {
		return s.finish(ctx, &doc, &c, "failed", "binding-revoked")
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Sub(s.now()) <= 20*time.Second {
		return s.finish(ctx, &doc, &c, "failed", "insufficient-deadline")
	}
	operationCtx, cancel := context.WithDeadline(ctx, deadline.Add(-20*time.Second))
	defer cancel()
	// Every dispatch checks the owner again. Stored token data is never a cache
	// of a prior authorization decision, including when reconciliation is needed.
	if c.Source == "mobile" {
		if s.Families == nil {
			return s.finish(ctx, &doc, &c, "failed", "session-revoked")
		}
		token, err := s.Families.CommandFamily(operationCtx, c.FamilyID)
		if err != nil {
			return s.finish(ctx, &doc, &c, "failed", "session-revoked")
		}
		c.PersonAccessToken = token
	}
	identity, e := s.Authority.Validate(operationCtx, c.PersonAccessToken)
	if e != nil || identity.Subject != s.Config.Owner {
		return s.finish(ctx, &doc, &c, "failed", "owner-authority-unverified")
	}
	if c.DryRun {
		return s.finish(ctx, &doc, &c, "dry-run", "no-provider-call")
	}
	var lease state.Lease
	for {
		lease, e = s.Credentials.Acquire(operationCtx, "command-"+c.ID, s.now())
		if e == nil {
			break
		}
		if !errors.Is(e, state.ErrConflict) {
			return s.finish(ctx, &doc, &c, "failed", "credential-unavailable")
		}
		if s.now().Unix()-c.CreatedAt >= 60 {
			return s.finish(ctx, &doc, &c, "expired", "vehicle-busy")
		}
		if s.sleep(operationCtx, time.Second) != nil {
			return s.finish(ctx, &doc, &c, "expired", "vehicle-busy")
		}
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = s.Credentials.Release(releaseCtx, lease)
	}()
	record, e := s.Credentials.Load(operationCtx, lease, s.now())
	if e != nil {
		return s.finish(ctx, &doc, &c, "failed", "credential-unavailable")
	}
	if record.Bootstrap.ValidateDevice(s.Config.DeviceID) != nil || record.ProviderState != "ready" || record.CooldownUntil > s.now().Unix() {
		return s.finish(ctx, &doc, &c, "failed", "provider-not-ready")
	}
	token := onstar.GMToken{AccessToken: record.GMToken, ExpiresAt: record.GMExpiresAt, VIN: record.Bootstrap.VIN, VehicleLinked: record.ProviderState == "ready"}
	if token.AccessToken == "" || token.ExpiresAt <= s.now().Add(110*time.Second).Unix() {
		if record.Grant.AccessToken == "" || record.Grant.ExpiresAt <= s.now().Add(110*time.Second).Unix() {
			grant, refreshErr := s.Provider.Refresh(operationCtx, record.Grant)
			if refreshErr != nil {
				return s.providerFailure(ctx, &doc, &c, lease, record, refreshErr)
			}
			record.Grant = grant
			record, e = s.Credentials.Save(ctx, lease, record, s.now())
			if e != nil {
				return ErrPersistence
			}
		}
		token, e = s.Provider.Exchange(operationCtx, record.Grant, s.Config.DeviceID, record.Bootstrap.VIN)
		if e != nil {
			return s.providerFailure(ctx, &doc, &c, lease, record, e)
		}
		record.GMToken = token.AccessToken
		record.GMExpiresAt = token.ExpiresAt
		record, e = s.Credentials.Save(ctx, lease, record, s.now())
		if e != nil {
			return ErrPersistence
		}
	}
	if !reconcile {
		if s.now().Unix()-c.CreatedAt >= 60 {
			return s.finish(ctx, &doc, &c, "expired", "not-dispatched")
		}
		if c.Source == "mobile" {
			current, err := s.Families.CommandFamily(operationCtx, c.FamilyID)
			if err != nil {
				return s.finish(ctx, &doc, &c, "failed", "session-revoked")
			}
			c.PersonAccessToken = current
		}
		identity, err := s.Authority.Validate(operationCtx, c.PersonAccessToken)
		if err != nil || identity.Subject != s.Config.Owner || !s.now().Before(identity.ExpiresAt) {
			return s.finish(ctx, &doc, &c, "failed", "owner-authority-unverified")
		}
		// Re-read the bindings after network refresh; committing them with the state
		// transition serializes dispatch against unlink/disable events.
		userDoc, user, e = s.user(ctx, c.UserHash, false)
		if e != nil {
			return ErrPersistence
		}
		personDoc, e = s.Docs.Load(ctx, personDoc.Key)
		if e != nil {
			return ErrPersistence
		}
		person, e = decoded[personBinding](personDoc)
		if e != nil {
			return ErrPersistence
		}
		if !user.Active || user.Generation != c.UserGeneration || person.UserGeneration != user.Generation {
			return s.finish(ctx, &doc, &c, "failed", "binding-revoked")
		}
		if reason := s.gated(ctx, c.Action, false); reason != "" {
			return s.finish(ctx, &doc, &c, "failed", reason)
		}
		if c.Source == "mobile" {
			if _, e = s.Families.CommandFamily(ctx, c.FamilyID); e != nil {
				return s.finish(ctx, &doc, &c, "failed", "session-revoked")
			}
		}
		if s.now().Unix()-c.CreatedAt >= 60 {
			return s.finish(ctx, &doc, &c, "expired", "not-dispatched")
		}
		c.State = "dispatching"
		c.Reason = "dispatch-recorded"
		c.DispatchAt = s.now().Unix()
		c.UpdatedAt = c.DispatchAt
		ownerDoc, e = s.ownerDocument(ctx)
		if e != nil {
			return s.finish(ctx, &doc, &c, "failed", "owner-binding-unverified")
		}
		writes := []auth.Document{data(doc, c), data(userDoc, user), data(personDoc, person), ownerDoc}
		if c.Source == "mobile" {
			familyDoc, err := s.familyDocument(ctx, c.FamilyID)
			if err != nil {
				return s.finish(ctx, &doc, &c, "failed", "session-revoked")
			}
			writes = append(writes, familyDoc)
		}
		if e = s.Docs.Commit(ctx, writes...); e != nil {
			if errors.Is(e, auth.ErrConflict) {
				return s.dispatchConflict(ctx, doc.Key)
			}
			return ErrPersistence
		}
		doc.Version++
		action := map[string]onstar.Command{"start": onstar.Start, "cancel-start": onstar.CancelStart, "lock": onstar.LockDoor, "unlock": onstar.UnlockDoor, "lock-trunk": onstar.LockTrunk, "unlock-trunk": onstar.UnlockTrunk, "horn-and-lights": onstar.Alert, "cancel-alert": onstar.CancelAlert, "flash-lights": onstar.FlashLights, "stop-lights": onstar.StopLights}[c.Action]
		started := s.now()
		result, submitErr := s.Provider.Submit(operationCtx, token, record.Bootstrap.VIN, action, onstar.CommandOptions{CabinTemperature: c.Options.CabinTemperature, Delay: c.Options.Delay, Duration: c.Options.Duration, Actions: c.Options.Actions})
		s.observe(ctx, "submit", c.ID, result, submitErr, started)
		c.Result = result
		c.State = result.State
		c.Reason = safeCommandReason(result.Reason)
		c.UpdatedAt = s.now().Unix()
		if submitErr != nil && c.State != "failed" {
			c.State = "outcome-unknown"
		}
		if c.State != "submitted" && c.State != "succeeded" && c.State != "failed" {
			c.State = "outcome-unknown"
		}
		if e = s.save(ctx, &doc, c); e != nil {
			return e
		}
		if c.State != "submitted" {
			return s.finish(ctx, &doc, &c, c.State, c.Reason)
		}
	}
	pollDeadline := s.now().Add(90 * time.Second)
	for poll := 0; poll < 45 && s.now().Before(pollDeadline); poll++ {
		if s.sleep(operationCtx, 2*time.Second) != nil {
			return s.finish(ctx, &doc, &c, "outcome-unknown", "poll-deadline")
		}
		started := s.now()
		result, statusErr := s.Provider.CommandStatus(operationCtx, token, record.Bootstrap.VIN, c.Result.RequestID, c.Result.StatusURL)
		s.observe(ctx, "status", c.ID, result, statusErr, started)
		if statusErr != nil {
			return s.finish(ctx, &doc, &c, "outcome-unknown", safeCommandReason(result.Reason))
		}
		if result.State == "succeeded" || result.State == "failed" {
			c.Result = result
			return s.finish(ctx, &doc, &c, result.State, safeCommandReason(result.Reason))
		}
		if result.State != "submitted" {
			return s.finish(ctx, &doc, &c, "outcome-unknown", safeCommandReason(result.Reason))
		}
	}
	return s.finish(ctx, &doc, &c, "outcome-unknown", "poll-deadline")
}

// A failed pre-submit condition means another state transition won. Read it
// once and acknowledge handled state; never retry the provider submission.
func (s *Service) dispatchConflict(ctx context.Context, key string) error {
	doc, e := s.Docs.Load(ctx, key)
	if errors.Is(e, auth.ErrMissing) {
		return nil
	}
	if e != nil {
		return ErrPersistence
	}
	c, e := decoded[command](doc)
	if e != nil {
		return ErrPersistence
	}
	if terminal(c.State) || c.State == "dispatching" || c.State == "submitted" || c.State == "outcome-unknown" {
		return nil
	}
	if c.State == "accepted" || c.State == "queued" {
		return s.finish(ctx, &doc, &c, "failed", "dispatch-state-changed")
	}
	return ErrPersistence
}
func (s *Service) providerFailure(ctx context.Context, doc *auth.Document, c *command, lease state.Lease, record state.Record, cause error) error {
	reason := "provider-unavailable"
	var failure *onstar.Error
	if errors.As(cause, &failure) {
		if failure.Kind == "reauth-required" || failure.Kind == "account-mismatch" {
			record.ProviderState = "reauth-required"
			reason = "reauth-required"
		}
		if failure.Kind == "vehicle-not-linked" {
			record.ProviderState = "vehicle-not-linked"
			reason = "vehicle-not-linked"
		}
		if failure.AuthEndpoint && (failure.Status == 401 || failure.Status == 403 || failure.Kind == "access-denied") {
			record.CooldownUntil = s.now().Add(6 * time.Hour).Unix()
			record.CooldownReason = "auth-endpoint-denied"
		}
	}
	if _, e := s.Credentials.Save(ctx, lease, record, s.now()); e != nil {
		return ErrPersistence
	}
	return s.finish(ctx, doc, c, "failed", reason)
}
