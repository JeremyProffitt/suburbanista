package mobile

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/ownerclient"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

const installedSigningCertificate = "70cd737ed7411587a2afee7b31372ceb605ea33aa71f5261313307f8757b8853"
const updateCheckInterval = 6 * time.Hour
const updateLastSuccessPreference = "update-last-success"

type updateView struct {
	mu        sync.Mutex
	lastCheck time.Time
	timer     *time.Timer
	status    *widget.Label
	install   actionControl
	manifest  *contract.ReleaseManifest
}

func verifiedUpdate(data []byte, publicKey string, installedCode int64, androidAPI int, now time.Time) (contract.ReleaseManifest, bool, error) {
	return ownerclient.VerifyUpdate(data, publicKey, installedCode, androidAPI, now)
}

func (s *Screen) updateSettings() fyne.CanvasObject {
	u := &updateView{status: wrapped("Sign in to check updates")}
	if s.app != nil {
		u.lastCheck, _ = time.Parse(time.RFC3339Nano, s.app.Preferences().String(updateLastSuccessPreference))
	}
	u.install = newTool("Download", theme.DownloadIcon(), func() { s.run(s.downloadUpdate) })
	u.install.Hide()
	s.updater = u
	return container.NewVBox(u.status, wrapped("Checks every 6 hours, including in background."), container.NewGridWithColumns(2, newTool("Check", theme.ViewRefreshIcon(), func() { s.run(func(ctx context.Context) error { return s.checkUpdates(ctx, true) }) }), u.install))
}

func (s *Screen) pauseUpdateChecks() {
	if s.updater == nil {
		return
	}
	s.updater.mu.Lock()
	defer s.updater.mu.Unlock()
	if s.updater.timer != nil {
		s.updater.timer.Stop()
		s.updater.timer = nil
	}
}

func (s *Screen) checkUpdatesOnOpen(ctx context.Context) {
	if s.updater == nil || s.app == nil || s.preview.Load() {
		return
	}
	s.cancelUpdateReminder()
	s.requestReminderPermission()
	fyne.Do(s.startBackgroundWork)
	_ = s.checkUpdates(ctx, false)
}

func (s *Screen) checkUpdates(ctx context.Context, force bool) error {
	if s.updater == nil {
		return nil
	}
	u := s.updater
	if s.preview.Load() {
		fyne.Do(func() {
			u.status.SetText("Preview: update checks are paused. No network request was sent.")
			u.install.Hide()
		})
		return nil
	}
	if s.saved.Session.AccessHandle == "" {
		fyne.Do(func() { u.status.SetText("Sign in to check owner releases."); u.install.Hide() })
		return nil
	}
	now := time.Now()
	u.mu.Lock()
	due := force || u.lastCheck.IsZero() || u.lastCheck.After(now) || now.Sub(u.lastCheck) >= updateCheckInterval
	delay := updateCheckInterval
	if !due {
		delay = updateCheckInterval - now.Sub(u.lastCheck)
	}
	if u.timer != nil {
		u.timer.Stop()
	}
	// The lifecycle exit callback cancels this foreground-only timer.
	u.timer = time.AfterFunc(delay, func() { s.run(func(ctx context.Context) error { return s.checkUpdates(ctx, false) }) })
	lastCheck := u.lastCheck
	noManifest := u.manifest == nil
	u.mu.Unlock()
	if !due {
		if noManifest {
			fyne.Do(func() {
				u.status.SetText("Last update check: " + s.displayTime(lastCheck) + ". Tap Check to check now.")
			})
		}
		return nil
	}
	if !s.saved.Session.AccessExpiresAt.After(now.Add(time.Minute)) {
		if err := s.rotate(ctx); err != nil {
			fyne.Do(func() { u.status.SetText("Sign in again to check owner releases."); u.install.Hide() })
			return nil
		}
	}
	fyne.Do(func() { u.status.SetText("Checking signed owner release…"); u.install.Hide() })
	var raw json.RawMessage
	if err := s.client.Request(ctx, "GET", "/api/releases/latest", s.saved.Session.AccessHandle, nil, &raw); err != nil {
		fyne.Do(func() { u.status.SetText("Update check could not finish. The installed app remains available.") })
		return nil
	}
	// Fyne embeds this public key at build time; it never comes from the server.
	manifest, newer, err := verifiedUpdate(raw, s.app.Metadata().Custom["updateManifestPublicKey"], int64(s.app.Metadata().Build), platformAndroidAPI(s.window), now)
	if err != nil {
		fyne.Do(func() { u.status.SetText(err.Error()) })
		return nil
	}
	u.mu.Lock()
	u.lastCheck = now
	if newer {
		u.manifest = &manifest
	} else {
		u.manifest = nil
	}
	u.mu.Unlock()
	s.app.Preferences().SetString(updateLastSuccessPreference, now.UTC().Format(time.RFC3339Nano))
	fyne.Do(func() {
		if newer {
			u.status.SetText(fmt.Sprintf("Signed update %s is available. Tap Download, then open the downloaded APK to install it.", manifest.VersionName))
			u.install.Show()
		} else {
			u.status.SetText("You have the current release. Checked " + s.displayTime(now))
			u.install.Hide()
		}
	})
	return nil
}

func (s *Screen) downloadUpdate(ctx context.Context) error {
	if s.preview.Load() {
		return errors.New("Return to Live to download an update.")
	}
	if s.updater == nil {
		return errors.New("Check for an update first.")
	}
	s.updater.mu.Lock()
	if s.updater.manifest == nil {
		s.updater.mu.Unlock()
		return errors.New("Check for an update first.")
	}
	manifest := *s.updater.manifest
	s.updater.mu.Unlock()
	if !manifest.ExpiresAt.After(time.Now()) {
		return errors.New("Check for an update again; the saved release information expired.")
	}
	if !s.saved.Session.AccessExpiresAt.After(time.Now().Add(time.Minute)) {
		if err := s.rotate(ctx); err != nil {
			return err
		}
	}
	var ticket struct {
		DownloadURL      string `json:"downloadUrl"`
		ExpiresInSeconds int    `json:"expiresInSeconds"`
		APKSHA256        string `json:"apkSha256"`
		APKBytes         int64  `json:"apkBytes"`
	}
	if err := s.client.Request(ctx, "GET", manifest.DownloadRoute, s.saved.Session.AccessHandle, nil, &ticket); err != nil {
		return err
	}
	address, err := url.Parse(ticket.DownloadURL)
	if err != nil || address.Scheme != "https" || address.Host != "s3.us-east-1.amazonaws.com" || address.User != nil || address.Fragment != "" || ticket.ExpiresInSeconds <= 0 || ticket.ExpiresInSeconds > 300 || ticket.APKSHA256 != manifest.APKSHA256 || ticket.APKBytes != manifest.APKBytes {
		return errors.New("The download did not match the signed release.")
	}
	// The temporary URL grants download access. Never retain or log it.
	fyne.Do(func() {
		s.showPagedInfo("Install update", []string{"The browser downloads the signed APK from private S3 storage.", "When the download finishes, open the APK. If Android asks, allow this browser to install apps, then tap Install.", "Keep the existing app installed to retain its settings."})
		s.open(ticket.DownloadURL)
	})
	return nil
}
