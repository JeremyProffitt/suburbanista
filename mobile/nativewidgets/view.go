package nativewidgets

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/ownerclient"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/widgetdisplay"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"image"
	"path/filepath"
	"sync"
	"time"
)

type viewState struct {
	status      string
	settings    appearancecore.Settings
	display     widgetdisplay.Snapshot
	signedIn    bool
	allowed     map[string]bool
	mapImage    image.Image
	mapView     contract.MapView
	sessionMark [32]byte
}

// dispatchControl is the exact entry used by the generated native callback.
// Display events cannot enter the command client even when invoked directly.
func dispatchControl(ctx context.Context, vault ownerclient.Vault, client *ownerclient.Client, action string) (ownerclient.CommandResult, error) {
	command, ok := commandFor(action)
	if !ok {
		return ownerclient.CommandResult{}, errors.New("Unsupported widget action.")
	}
	return ownerclient.WidgetCommand(ctx, vault, client, command)
}

var latestView struct {
	sync.Mutex
	state viewState
}

func navigateAppearance(ctx context.Context, root, action string, now time.Time) error {
	buttons, direction, ok := appearanceNavigation(action)
	if !ok {
		return errors.New("Unsupported appearance action")
	}
	_, err := appearancecore.UpdateShared(ctx, root, func(settings *appearancecore.Settings) error {
		if buttons {
			if !settings.ButtonRotation.Enabled {
				return errors.New("Button rotation is off")
			}
			settings.NavigateButtons(direction, now)
		} else {
			settings.NavigateBackground(direction, now)
		}
		return nil
	})
	return err
}

func readState(ctx context.Context, root string, vault ownerclient.Vault, client *ownerclient.Client, network bool) viewState {
	state := viewState{settings: appearancecore.Default(), status: "Open Suburbanista to sign in", allowed: map[string]bool{}}
	if settings, err := appearancecore.LoadShared(root); err == nil {
		state.settings = settings
	}
	var mapSession ownerclient.Session
	var mapPendingID string
	err := vault.WithLock(ctx, func() error {
		saved, e := vault.Load()
		if e != nil {
			return e
		}
		if saved.Session.RefreshHandle == "" {
			return ownerclient.ErrUnauthorized
		}
		if !network {
			// A saved session is not a current authorization proof, but a cold
			// process cache is not evidence that the owner must sign in again.
			state.status = "Tap to refresh"
			mark := sha256.Sum256([]byte(saved.Session.RefreshHandle))
			latestView.Lock()
			cached := latestView.state
			latestView.Unlock()
			if cached.signedIn && cached.sessionMark == mark && time.Until(saved.Session.AccessExpiresAt) > 0 {
				state.signedIn = true
				state.allowed = cached.allowed
				state.mapImage = cached.mapImage
				state.mapView = cached.mapView
				if !time.Now().Before(state.mapView.ExpiresAt) {
					state.mapImage = nil
					state.mapView = contract.MapView{}
				}
				state.status = cached.status
				state.sessionMark = mark
			}
			if saved.Command != nil {
				state.allowed = map[string]bool{}
				state.status = "Command pending · check in app"
				for _, v := range saved.Verifications {
					if v.RequestID == saved.Command.RequestID {
						state.status = commandLabel(v.ProviderState)
					}
				}
			}
			state.display, _ = widgetdisplay.Load(filepath.Join(root, "widget-display.json"))
			return nil
		}
		if time.Until(saved.Session.AccessExpiresAt) < time.Minute {
			session, e := client.Refresh(ctx, saved.Session)
			if e != nil {
				return e
			}
			saved.Session = session
			if e = vault.Save(saved); e != nil {
				return e
			}
		}
		features, e := client.Features(ctx, saved.Session)
		if e != nil {
			return e
		}
		state.signedIn = true
		state.sessionMark = sha256.Sum256([]byte(saved.Session.RefreshHandle))
		state.status = "Ready"
		state.display, _ = widgetdisplay.Load(filepath.Join(root, "widget-display.json"))
		if saved.Command != nil {
			result, statusErr := client.CommandStatus(ctx, saved.Session, saved.Command.RequestID)
			if statusErr == nil && result.Action == saved.Command.Action && result.State != "not-found" {
				saved.Verifications, _ = ownerclient.VerificationResult(saved.Verifications, saved.Command.RequestID, result)
				if result.Terminal() && result.State != "outcome-unknown" {
					saved.Command = nil
				}
				if e = vault.Save(saved); e != nil {
					return e
				}
				state.status = commandLabel(result.State)
			}
			if errors.Is(statusErr, ownerclient.ErrUnauthorized) {
				return statusErr
			}
		}
		if saved.Command != nil {
			state.status = "Command pending · tap to check"
			for _, v := range saved.Verifications {
				if v.RequestID == saved.Command.RequestID {
					state.status = commandLabel(v.ProviderState)
				}
			}
		} else {
			for _, f := range features.Availability {
				if f.ID == "start" || f.ID == "unlock" || f.ID == "lock" {
					state.allowed[f.ID] = f.Available
				}
			}
		}
		mapSession = saved.Session
		if saved.Command != nil {
			mapPendingID = saved.Command.RequestID
		}
		return nil
	})
	if err == nil && network {
		var background image.Image
		var e error
		state.mapView, background, e = client.MapView(ctx, mapSession)
		if errors.Is(e, ownerclient.ErrUnauthorized) {
			err = e
		}
		if e == nil {
			state.mapImage = background
		} else if errors.Is(e, ownerclient.ErrMapPending) {
			latestView.Lock()
			cached := latestView.state
			latestView.Unlock()
			if cached.signedIn && cached.sessionMark == state.sessionMark && time.Now().Before(cached.mapView.ExpiresAt) {
				state.mapView, state.mapImage = cached.mapView, cached.mapImage
			}
			if state.status == "Ready" {
				state.status = "Map preparing · tap to refresh"
			}
		}
		// Fence the completed download against sign-out or a new session, and
		// do not re-enable commands that became pending while it was in flight.
		if err == nil {
			err = vault.WithLock(ctx, func() error {
				fresh, e := vault.Load()
				if e != nil {
					return e
				}
				if fresh.Session.RefreshHandle == "" {
					return ownerclient.ErrUnauthorized
				}
				if fresh.Session.RefreshHandle != mapSession.RefreshHandle {
					return errors.New("Widget session changed")
				}
				if fresh.Command != nil && fresh.Command.RequestID != mapPendingID {
					state.allowed = map[string]bool{}
					state.status = "Command pending · check in app"
				}
				return nil
			})
		}
	}

	if err != nil {
		state.signedIn = false
		state.allowed = map[string]bool{}
		state.mapImage = nil
		if errors.Is(err, ownerclient.ErrUnauthorized) {
			state.status = "Open Suburbanista to sign in"
		} else {
			state.status = "Unavailable · open app to check"
		}
	}
	if network {
		latestView.Lock()
		latestView.state = state
		latestView.Unlock()
	}
	return state
}
