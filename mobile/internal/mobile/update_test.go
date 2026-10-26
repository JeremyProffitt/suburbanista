package mobile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

func TestUpdateCheckRestoresSixHourCadenceAndManualCheckForces(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	now := time.Now()
	last := now.Add(-5 * time.Hour)
	app.Preferences().SetString("update-last-success", last.Format(time.RFC3339Nano))
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s := Screen{app: app, client: &Client{HTTP: server.Client(), BaseURL: server.URL}, saved: SavedState{Session: Session{AccessHandle: "test-owner", AccessExpiresAt: now.Add(time.Hour)}}}
	s.updateSettings()
	defer s.pauseUpdateChecks()
	if err := s.checkUpdates(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || !s.updater.lastCheck.Equal(last) {
		t.Fatal("reopening lost the saved successful check or checked before six hours")
	}
	if err := s.checkUpdates(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || app.Preferences().String("update-last-success") != last.Format(time.RFC3339Nano) {
		t.Fatal("manual check did not force a request or failure replaced successful time")
	}
	s.updater.lastCheck = now.Add(-6 * time.Hour)
	if err := s.checkUpdates(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatal("six-hour check did not run")
	}
}

func TestSignedUpdateUsesPinnedKeyAndActualInstalledVersion(t *testing.T) {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{17}, ed25519.SeedSize))
	public := base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey))
	now := time.Now().UTC()
	manifest := contract.ReleaseManifest{SchemaVersion: 1, PackageID: contract.AndroidPackageID, VersionName: "1.0.7", VersionCode: 7, MinimumAndroidAPI: 23, ReleasedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), ArtifactID: contract.ReleaseArtifactID(7), APKBytes: 100, APKSHA256: strings.Repeat("a", 64), SigningCertificateSHA256: installedSigningCertificate, DownloadRoute: contract.ReleaseDownloadRoute(7)}
	payload, _ := json.Marshal(manifest)
	envelope := contract.SignedRelease{SchemaVersion: 1, KeyID: "primary", Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload))}
	data, _ := json.Marshal(envelope)
	for _, tc := range []struct {
		installed int64
		newer     bool
	}{{6, true}, {7, false}, {8, false}} {
		actual, newer, err := verifiedUpdate(data, public, tc.installed, 29, now)
		if err != nil || newer != tc.newer || actual.VersionCode != 7 {
			t.Fatal("update version comparison")
		}
	}
	if _, _, err := verifiedUpdate(data, public, 6, 22, now); err == nil {
		t.Fatal("incompatible Android update accepted")
	}
	if _, _, err := verifiedUpdate(data, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{34}, 32)), 6, 29, now); err == nil {
		t.Fatal("untrusted update key accepted")
	}
	envelope.Payload = base64.StdEncoding.EncodeToString(append(payload, byte(' ')))
	tampered, _ := json.Marshal(envelope)
	if _, _, err := verifiedUpdate(tampered, public, 6, 29, now); err == nil {
		t.Fatal("tampered signed update accepted")
	}
}
