package ownerclient

import (
	"context"
	"errors"
	"time"
)

// WidgetCommand executes one explicit launcher action. It never retries an
// ambiguous submission; the saved intent is shared with the application.
func WidgetCommand(ctx context.Context, vault Vault, client *Client, action string) (result CommandResult, err error) {
	if action != "start" && action != "unlock" && action != "lock" {
		return result, errors.New("Unsupported widget action.")
	}
	err = vault.WithLock(ctx, func() error {
		state, e := vault.Load()
		if e != nil {
			return e
		}
		if state.Session.RefreshHandle == "" {
			return ErrUnauthorized
		}
		if state.Command != nil {
			return errors.New("A command is pending. Check it in Suburbanista.")
		}
		if time.Until(state.Session.AccessExpiresAt) < time.Minute {
			session, e := client.Refresh(ctx, state.Session)
			if e != nil {
				return e
			}
			state.Session = session
			if e = vault.Save(state); e != nil {
				return e
			}
		}
		features, e := client.Features(ctx, state.Session)
		if e != nil {
			return e
		}
		allowed := false
		for _, feature := range features.Availability {
			if feature.ID == action && feature.Available {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("This command is unavailable.")
		}
		id, _, e := NewVerifier()
		if e != nil {
			return errors.New("Could not prepare this command.")
		}
		intent := CommandIntent{RequestID: id, Action: action, CreatedAt: time.Now().UTC()}
		state.Command = &intent
		state.Verifications = AppendVerification(state.Verifications, intent)
		// Persist the possible-submission state before touching the network.
		state.Verifications[len(state.Verifications)-1].ProviderState = "submission-unconfirmed"
		if e = vault.Save(state); e != nil {
			return e
		}
		result, e = client.SubmitCommand(ctx, state.Session, intent)
		if e != nil {
			return errors.New("Command outcome unknown. Check it in Suburbanista.")
		}
		state.Verifications, _ = VerificationResult(state.Verifications, id, result)
		if result.Terminal() && result.State != "outcome-unknown" {
			state.Command = nil
		}
		return vault.Save(state)
	})
	return result, err
}
