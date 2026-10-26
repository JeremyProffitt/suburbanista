package mobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/mapview"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"image"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type Screen struct {
	foreground                            atomic.Bool
	phoneClock                            phoneClock
	locationRefresh                       locationRefreshUI
	appearance                            appearance.Settings
	appearanceKey                         string
	calendarOffset                        func(time.Time) (int, bool)
	appearanceRefresh                     func()
	startupPermissions                    permissionCoordinator
	appearanceOpening                     string
	appearanceForeground                  bool
	appearanceInternalReturn              bool
	widgetDisplayMu                       sync.Mutex
	widgetDisplaySequence                 atomic.Uint64
	widgetDisplayKey                      string
	widgetSyncSender                      func() bool
	widgetSyncPending                     atomic.Bool
	app                                   fyne.App
	window                                fyne.Window
	client                                *Client
	vault                                 Vault
	saved                                 SavedState
	busy                                  atomic.Bool
	restorePending                        atomic.Bool
	weatherPanelStatus                    *widget.Label
	cancelMu                              sync.Mutex
	cancel                                context.CancelFunc
	status                                *widget.Label
	detail                                *widget.Label
	signIn, refresh, logout, cancelButton actionControl
	start, lock, unlock, retryCommand     actionControl
	commandStatus                         *widget.Label
	allowedActions                        map[string]bool
	commandPending                        atomic.Bool
	preview                               atomic.Bool
	modeLabel                             *widget.Label
	modeButton                            actionControl
	activity                              *widget.Label
	featureAvailability                   map[string]contract.FeatureAvailability
	updater                               *updateView
	quickButtons                          map[string]*iconAction
	settingsContent                       fyne.CanvasObject
	installedVersion                      string
	landscape                             bool
	activityDetail                        string
	home                                  *homeView
	homeTelemetry                         HomeTelemetry
	vehicleMapImage                       image.Image
	mapView                               contract.MapView
	mapTransform                          mapview.Transform
	previewTime                           time.Time
	environmentMu                         sync.Mutex
	environmentCancel                     context.CancelFunc
	phoneLocation                         foregroundLocation
	weatherClient                         *WeatherClient
	weatherData                           *Weather
	weatherState                          string
	weatherCell                           string
	verificationDisplay                   []CommandVerification
	verificationLabels                    map[string]*widget.Label
	verificationObserved                  map[string]*iconAction
	verificationActions                   map[string]*iconAction
}

func Show(app fyne.App, window fyne.Window, version string) {
	app.Settings().SetTheme(vehicleTheme{})
	s := &Screen{app: app, window: window, client: NewClient()}
	s.vault = Vault{Path: filepath.Join(app.Storage().RootURI().Path(), "owner-session.enc"), Crypt: DeviceCrypt(window)}
	s.status = widget.NewLabel("Opening secure storage…")
	s.status.Wrapping = fyne.TextWrapWord
	s.detail = widget.NewLabel("Sign in with the Amazon owner account used during setup.")
	s.detail.Wrapping = fyne.TextWrapWord
	s.signIn = newTool("Sign in with Amazon", theme.LoginIcon(), func() { s.run(s.login) })
	s.refresh = newTool("Refresh connection status", theme.ViewRefreshIcon(), s.refreshStatus)
	s.logout = newTool("Sign out", theme.LogoutIcon(), func() { s.run(s.signOut) })
	s.cancelButton = newTool("Cancel sign-in", theme.CancelIcon(), func() {
		s.cancelMu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		s.cancelMu.Unlock()
	})
	s.cancelButton.Hide()
	s.commandStatus = widget.NewLabel("")
	s.commandStatus.Wrapping = fyne.TextWrapWord
	s.start = newIconAction("Start engine", featureIcon("start"), func() { s.chooseCommand("start") })
	s.lock = newIconAction("Lock", featureIcon("lock"), func() { s.chooseCommand("lock") })
	s.unlock = newIconAction("Unlock", featureIcon("unlock"), func() { s.chooseCommand("unlock") })
	s.retryCommand = newGlyph("Check or retry the same request", theme.ViewRefreshIcon(), func() { s.run(s.retryIntent) })
	s.start.Disable()
	s.lock.Disable()
	s.unlock.Disable()
	s.retryCommand.Hide()
	window.SetContent(s.buildLayout(version))
	s.refresh.Hide()
	s.logout.Hide()
	s.signIn.Disable()
	app.Lifecycle().SetOnEnteredForeground(func() { s.foreground.Store(true); s.appearanceOpened(); s.startEnvironmentClock(); s.run(s.restore) })
	app.Lifecycle().SetOnExitedForeground(func() {
		s.foreground.Store(false)
		s.pauseLocationRefresh()
		s.appearanceClosed()
		s.stopEnvironmentClock()
		s.pauseUpdateChecks()
		s.scheduleUpdateReminder()
	})
	window.Resize(fyne.NewSize(390, 650))
	s.foreground.Store(true)
	window.Show()
	s.appearanceOpened()
	s.startEnvironmentClock()
	s.run(s.restore)
}
func (s *Screen) run(fn func(context.Context) error) {
	if !s.busy.CompareAndSwap(false, true) {
		return
	}
	fyne.Do(s.updateControlButtons)
	go func() {
		defer func() {
			s.busy.Store(false)
			fyne.Do(s.updateControlButtons)
			if s.restorePending.Swap(false) && !s.preview.Load() {
				s.run(s.restore)
			}
		}()
		ctx, cancel := context.WithCancel(context.Background())
		s.cancelMu.Lock()
		s.cancel = cancel
		s.cancelMu.Unlock()
		defer func() { cancel(); s.cancelMu.Lock(); s.cancel = nil; s.cancelMu.Unlock() }()
		// A session change can be followed by a slow read in this transaction.
		// Wake the native receiver only after WithLock has released the vault.
		defer s.flushWidgetSync()
		if e := s.vault.WithLock(ctx, func() error {
			fresh, err := s.vault.Load()
			if err != nil {
				return err
			}
			s.saved = fresh
			s.commandPending.Store(fresh.Command != nil)
			return fn(ctx)
		}); e != nil {
			fyne.Do(func() {
				s.status.SetText(e.Error())
				if errors.Is(e, ErrUnauthorized) {
					s.signIn.Show()
					s.refresh.Hide()
					s.logout.Hide()
					s.allowedActions = nil
					s.vehicleMapImage = nil
					s.homeTelemetry = HomeTelemetry{}
					s.paintHome()
					s.updateControlButtons()
				}
			})
		}
		fyne.Do(func() { s.signIn.Enable(); s.cancelButton.Hide(); s.updateControlButtons() })
	}()
}
func (s *Screen) open(address string) {
	if s.preview.Load() {
		s.showPagedInfo("Preview", []string{"Return to Live to open links"})
		return
	}
	u, e := url.Parse(address)
	if e == nil {
		s.appearanceInternalReturn = true
		if e = s.app.OpenURL(u); e != nil {
			s.showPagedInfo("Browser unavailable", []string{"Try again in Live mode"})
		}
	}
}
func (s *Screen) restore(ctx context.Context) error {
	if s.preview.Load() {
		fyne.Do(s.showPreviewOverview)
		return nil
	}
	saved, e := s.vault.Load()
	if e != nil {
		return e
	}
	s.saved = saved
	records := append([]CommandVerification{}, saved.Verifications...)
	fyne.Do(func() { s.verificationUI(records) })
	s.queueEnvironment(false)
	s.commandPending.Store(saved.Command != nil)
	if saved.Pending != nil {
		return s.poll(ctx, *saved.Pending)
	}
	if saved.Session.RefreshHandle != "" {
		if e = s.dashboard(ctx); e != nil {
			return e
		}
		if saved.LocationRefreshID != "" {
			go s.resumeLocationRefresh()
		}
		if saved.Command != nil {
			return s.watchIntent(ctx)
		}
		s.checkUpdatesOnOpen(ctx)
		return nil
	}
	fyne.Do(func() {
		s.status.SetText("Signed out")
		s.commandStatus.SetText("Sign in · settings")
		s.signIn.Show()
		s.refresh.Hide()
		s.logout.Hide()
		s.paintHome()
	})
	return nil
}
func (s *Screen) login(ctx context.Context) error {
	if s.preview.Load() {
		return errors.New("Return to Live mode to sign in.")
	}
	verifier, challenge, e := NewVerifier()
	if e != nil {
		return errors.New("Could not start secure sign-in.")
	}
	login, e := s.client.Start(ctx, challenge)
	if e != nil {
		return e
	}
	pending := PendingLogin{Login: login, Verifier: verifier}
	s.saved.Pending = &pending
	if e = s.vault.Save(s.saved); e != nil {
		_ = s.client.Cancel(ctx, pending)
		s.saved.Pending = nil
		return e
	}
	fyne.Do(func() { s.open(login.BrowserURL) })
	return s.poll(ctx, pending)
}
func (s *Screen) poll(ctx context.Context, pending PendingLogin) error {
	if s.preview.Load() {
		return errors.New("Preview cannot poll a live sign-in.")
	}
	fyne.Do(func() {
		s.status.SetText("Complete sign-in in your browser, then return here.")
		s.cancelButton.Show()
		s.signIn.Disable()
	})
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		if !time.Now().Before(pending.Login.ExpiresAt) {
			s.saved.Pending = nil
			_ = s.vault.Save(s.saved)
			return errors.New("Sign-in expired. Start again.")
		}
		session, e := s.client.Poll(ctx, pending)
		if e == nil {
			previousCommand := s.saved.Command != nil
			s.saved.Session = session
			s.saved.Pending = nil
			s.saved.Command = nil
			s.commandPending.Store(false)
			if e = s.vault.Save(s.saved); e != nil {
				_ = s.client.Logout(ctx, session)
				s.saved = SavedState{}
				return e
			}
			s.syncWidgets()
			e = s.dashboard(ctx)
			if e == nil {
				s.checkUpdatesOnOpen(ctx)
				s.queueEnvironment(false)
			}
			if previousCommand {
				fyne.Do(func() {
					s.commandStatus.SetText("The previous session's command may still finish. Check the vehicle before sending another command.")
				})
			}
			return e
		}
		if ctx.Err() != nil {
			return s.cancelLogin(pending)
		}
		if !errors.Is(e, ErrPending) {
			return e
		}
		select {
		case <-ctx.Done():
			return s.cancelLogin(pending)
		case <-ticker.C:
		}
	}
}
func (s *Screen) cancelLogin(pending PendingLogin) error {
	if s.preview.Load() {
		return errors.New("Preview cannot cancel a live sign-in.")
	}
	cancelCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_ = s.client.Cancel(cancelCtx, pending)
	s.saved.Pending = nil
	if e := s.vault.Save(s.saved); e != nil {
		return e
	}
	return errors.New("Sign-in canceled.")
}
func (s *Screen) dashboard(ctx context.Context) error {
	if s.preview.Load() {
		fyne.Do(s.showPreviewOverview)
		return nil
	}
	fyne.Do(func() {
		s.status.SetText("Refreshing connection status…")
		s.refresh.SetText("Refreshing…")
		s.refresh.Disable()
	})
	defer fyne.Do(func() { s.refresh.SetText("Refresh connection status"); s.refresh.Enable() })
	if s.saved.Session.RefreshHandle == "" {
		return ErrUnauthorized
	}
	if time.Until(s.saved.Session.AccessExpiresAt) < time.Minute {
		if e := s.rotate(ctx); e != nil {
			return e
		}
	}
	data, e := s.client.Dashboard(ctx, s.saved.Session)
	if errors.Is(e, ErrUnauthorized) {
		if e = s.rotate(ctx); e != nil {
			return e
		}
		data, e = s.client.Dashboard(ctx, s.saved.Session)
	}
	if e != nil {
		return e
	}
	text := "Service checked " + s.displayTime(data.StatusReadAt)
	if data.LastProofReason != "" {
		text += "\n" + strings.ReplaceAll(data.LastProofReason, "-", " ")
	}
	if data.LocationPending {
		text += "\nLocation pending"
	}
	features, featureErr := s.client.Features(ctx, s.saved.Session)
	if featureErr != nil {
		text += "\nAvailability offline"
	}
	fyne.Do(func() {
		s.status.SetText("GM connection: " + strings.ReplaceAll(data.ProviderState, "-", " "))
		s.detail.SetText(text)
		s.signIn.Hide()
		s.cancelButton.Hide()
		s.refresh.Show()
		s.logout.Show()
		s.allowedActions = make(map[string]bool)
		for _, capability := range data.Capabilities {
			s.allowedActions[capability.Name] = data.ControlsEnabled && capability.Available
		}
		if featureErr == nil {
			s.featureAvailability = make(map[string]contract.FeatureAvailability)
			for _, feature := range features.Availability {
				s.allowedActions[feature.ID] = data.ControlsEnabled && feature.Available
				s.featureAvailability[feature.ID] = feature
			}
		}
		if !data.ControlsEnabled {
			s.commandStatus.SetText("Controls paused")
		} else if !s.commandPending.Load() {
			s.commandStatus.SetText("Ready")
		}
		s.updateControlButtons()
		s.paintHome()
	})
	s.refreshHomeTelemetry(ctx, false)
	s.refreshCachedMap(ctx)
	// A restarted widget process has no validated in-memory availability even
	// when the restored owner access handle is still fresh. Queue one read-only
	// synchronization after the surrounding vault transaction releases.
	s.syncWidgets()
	return nil
}

func (s *Screen) refreshStatus() {
	if s.busy.Load() {
		s.status.SetText("A request is already in progress. Please wait.")
		return
	}
	s.run(s.dashboard)
}
func (s *Screen) rotate(ctx context.Context) error {
	if s.preview.Load() {
		return errors.New("Preview does not refresh live sessions.")
	}
	session, e := s.client.Refresh(ctx, s.saved.Session)
	if e != nil {
		return e
	}
	s.saved.Session = session
	if e = s.vault.Save(s.saved); e != nil {
		_ = s.client.Logout(ctx, session)
		s.saved = SavedState{}
		return e
	}
	s.syncWidgets()
	return nil
}
func (s *Screen) signOut(ctx context.Context) error {
	s.pauseLocationRefresh()
	if s.preview.Load() {
		return errors.New("Return to Live mode to sign out.")
	}
	e := s.client.Logout(ctx, s.saved.Session)
	if errors.Is(e, ErrUnauthorized) {
		if refreshErr := s.rotate(ctx); refreshErr != nil {
			return refreshErr
		}
		e = s.client.Logout(ctx, s.saved.Session)
	}
	if e != nil && !errors.Is(e, ErrUnauthorized) {
		return fmt.Errorf("Sign-out not confirmed. %s", e)
	}
	previousCommand := s.saved.Command != nil
	e = s.vault.Clear()
	if e != nil {
		return errors.New("Could not clear secure storage.")
	}
	s.saved = SavedState{}
	s.stopBackgroundWork()
	s.commandPending.Store(false)
	s.syncWidgets()
	fyne.Do(func() {
		s.vehicleMapImage = nil
		s.homeTelemetry = HomeTelemetry{}
		s.paintHome()
		s.status.SetText("Signed out")
		s.detail.SetText("Sign in with the Amazon owner account used during setup.")
		s.signIn.Show()
		s.refresh.Hide()
		s.logout.Hide()
		s.allowedActions = nil
		if previousCommand {
			s.commandStatus.SetText("Signed out. The previous command may still finish. Check the vehicle before sending another command.")
		}
		s.updateControlButtons()
	})
	return nil
}

func (s *Screen) updateControlButtons() {
	if s.preview.Load() {
		if s.retryCommand != nil {
			s.retryCommand.Hide()
		}
		s.updateFeatureAvailability()
		return
	}
	if s.start == nil {
		return
	}
	for action, button := range map[string]actionControl{"start": s.start, "lock": s.lock, "unlock": s.unlock} {
		if s.allowedActions[action] && !s.commandPending.Load() {
			button.Enable()
		} else {
			button.Disable()
		}
	}
	if s.commandPending.Load() && len(s.allowedActions) > 0 {
		if s.commandOutcomeUnknown() {
			s.retryCommand.SetText("Check request status")
		} else {
			s.retryCommand.SetText("Check or retry the same request")
		}
		s.retryCommand.Show()
	} else {
		s.retryCommand.Hide()
	}
	s.updateFeatureAvailability()
}
func (s *Screen) chooseCommand(action string) {
	if s.preview.Load() {
		s.commandStatus.SetText("Preview · " + featureCaption(action) + " not sent")
		s.paintHome()
		return
	}
	if s.busy.Load() || !s.allowedActions[action] || s.commandPending.Load() {
		return
	}
	s.run(func(ctx context.Context) error { return s.beginCommand(ctx, action) })
}
func (s *Screen) beginCommand(ctx context.Context, action string) error {
	return s.beginCommandOptions(ctx, action, contract.CommandOptions{})
}
func (s *Screen) beginCommandOptions(ctx context.Context, action string, options contract.CommandOptions) error {
	if s.preview.Load() {
		return errors.New("Preview cannot submit live commands.")
	}
	if s.saved.Command != nil {
		return errors.New("Resolve the current command before starting another.")
	}
	if (action != "start" && action != "unlock" && action != "lock") || contract.ValidateCommandOptions(action, options) != nil {
		return errors.New("Unsupported vehicle command.")
	}
	if s.saved.Session.RefreshHandle == "" {
		return ErrUnauthorized
	}
	id := make([]byte, 24)
	if _, e := rand.Read(id); e != nil {
		return errors.New("Could not create a command request.")
	}
	s.saved.Command = &CommandIntent{RequestID: base64.RawURLEncoding.EncodeToString(id), Action: action, CreatedAt: time.Now().UTC(), Options: options}
	previousRecords := s.saved.Verifications
	s.saved.Verifications = appendVerification(s.saved.Verifications, *s.saved.Command)
	if e := s.vault.Save(s.saved); e != nil {
		s.saved.Command = nil
		s.saved.Verifications = previousRecords
		return e
	}
	s.commandPending.Store(true)
	records := append([]CommandVerification{}, s.saved.Verifications...)
	fyne.Do(func() {
		s.verificationUI(records)
		s.commandStatus.SetText("Sending…")
		s.updateControlButtons()
		s.paintHome()
	})
	return s.retryIntent(ctx)
}
func (s *Screen) retryIntent(ctx context.Context) error {
	if s.preview.Load() {
		return errors.New("Preview cannot retry live commands.")
	}
	if s.saved.Command == nil {
		return nil
	}
	// A terminal unknown result must never become another submission. The
	// review control can only read the original request until its outcome is known.
	if s.commandOutcomeUnknown() {
		return s.watchIntent(ctx)
	}
	s.commandPending.Store(true)
	if time.Until(s.saved.Session.AccessExpiresAt) < time.Minute {
		if e := s.rotate(ctx); e != nil {
			return e
		}
	}
	for i := range s.saved.Verifications {
		if s.saved.Verifications[i].RequestID == s.saved.Command.RequestID && s.saved.Verifications[i].ProviderState == "prepared" {
			before := append([]CommandVerification{}, s.saved.Verifications...)
			s.saved.Verifications[i].ProviderState = "submission-unconfirmed"
			if e := s.vault.Save(s.saved); e != nil {
				s.saved.Verifications = before
				return e
			}
			break
		}
	}
	result, e := s.client.SubmitCommand(ctx, s.saved.Session, *s.saved.Command)
	if errors.Is(e, ErrUnauthorized) {
		if e = s.rotate(ctx); e != nil {
			return e
		}
		result, e = s.client.SubmitCommand(ctx, s.saved.Session, *s.saved.Command)
	}
	if e != nil {
		s.commandUncertain()
		return e
	}
	if done, e := s.acceptCommandResult(result); done || e != nil {
		return e
	}
	return s.watchIntent(ctx)
}
func (s *Screen) commandUncertain() {
	fyne.Do(func() {
		s.commandStatus.SetText("Unconfirmed · use same request")
		s.updateControlButtons()
	})
}
func (s *Screen) watchIntent(ctx context.Context) error {
	if s.preview.Load() {
		return errors.New("Preview cannot poll live commands.")
	}
	if s.saved.Command == nil {
		return nil
	}
	s.commandPending.Store(true)
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		result, e := s.client.CommandStatus(ctx, s.saved.Session, s.saved.Command.RequestID)
		if errors.Is(e, ErrUnauthorized) {
			if e = s.rotate(ctx); e != nil {
				return e
			}
			result, e = s.client.CommandStatus(ctx, s.saved.Session, s.saved.Command.RequestID)
		}
		if e != nil {
			s.commandUncertain()
			return e
		}
		if result.State == "not-found" {
			s.commandUncertain()
			return errors.New("Request not found. Retry uses the same request ID.")
		}
		if done, e := s.acceptCommandResult(result); done || e != nil {
			return e
		}
		select {
		case <-ctx.Done():
			s.commandUncertain()
			return ctx.Err()
		case <-deadline.C:
			s.commandUncertain()
			return errors.New("Command is still pending. Check the same request again.")
		case <-ticker.C:
		}
	}
}
func (s *Screen) commandOutcomeUnknown() bool {
	if s.saved.Command == nil {
		return false
	}
	for i := len(s.saved.Verifications) - 1; i >= 0; i-- {
		record := s.saved.Verifications[i]
		if record.RequestID == s.saved.Command.RequestID {
			return record.ProviderState == "outcome-unknown"
		}
	}
	return false
}
func (s *Screen) acceptCommandResult(result CommandResult) (bool, error) {
	if s.saved.Command == nil || result.Action != s.saved.Command.Action {
		return false, errors.New("Command response did not match this request.")
	}
	text := compactCommandText(result)
	previous := s.saved.Command
	previousRecords := s.saved.Verifications
	records, changed := verificationResult(s.saved.Verifications, previous.RequestID, result)
	s.saved.Verifications = records
	knownTerminal := result.Terminal() && result.State != "outcome-unknown"
	if knownTerminal {
		s.saved.Command = nil
	}
	if result.Terminal() || changed {
		if e := s.vault.Save(s.saved); e != nil {
			s.saved.Command = previous
			s.saved.Verifications = previousRecords
			return false, e
		}
	}
	s.commandPending.Store(s.saved.Command != nil)
	fyne.Do(func() {
		s.verificationUI(records)
		s.commandStatus.SetText(text)
		s.activityDetail = commandText(result)
		if s.activity != nil {
			s.activity.SetText(text)
		}
		s.updateControlButtons()
		s.paintHome()
	})
	return result.Terminal(), nil
}
func compactCommandText(result CommandResult) string {
	label := featureCaption(result.Action)
	switch result.State {
	case "succeeded":
		return label + " · confirmed"
	case "outcome-unknown":
		return label + " unknown · check vehicle"
	case "rejected":
		return label + " · not sent"
	case "dry-run":
		return "Preview · no vehicle action"
	default:
		return label + " · " + strings.ReplaceAll(result.State, "-", " ")
	}
}
func commandText(result CommandResult) string {
	action := map[string]string{"start": "Engine start", "lock": "Door lock", "unlock": "Door unlock"}[result.Action]
	if action == "" {
		if feature, ok := contract.FindFeature(result.Action); ok {
			action = feature.Title
		} else {
			action = "Vehicle command"
		}
	}
	switch result.State {
	case "succeeded":
		return action + " confirmed successful by the vehicle service."
	case "failed":
		return action + " failed. " + strings.ReplaceAll(result.Reason, "-", " ")
	case "expired":
		return action + " expired before it could complete."
	case "outcome-unknown":
		return action + " outcome is unknown. Check the vehicle before sending another command."
	case "rejected":
		return action + " was not sent: " + strings.ReplaceAll(result.Reason, "-", " ")
	case "dry-run":
		return "No vehicle command sent (dry run)."
	case "submitted":
		return action + " submitted. Waiting for the vehicle service to confirm completion."
	default:
		return action + " request " + strings.ReplaceAll(result.State, "-", " ") + ". Completion is not yet confirmed."
	}
}
