package mobile

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

type mapDownloadTransport func(*http.Request) (*http.Response, error)

func (f mapDownloadTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLargeMapDownloadIsScopedVerifiedAndNormalized(t *testing.T) {
	var encoded bytes.Buffer
	png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 3072, 2048)))
	sum := sha256.Sum256(encoded.Bytes())
	now := time.Now().UTC()
	id := strings.Repeat("a", 32)
	q := url.Values{"X-Amz-Algorithm": {"AWS4-HMAC-SHA256"}, "X-Amz-Credential": {"synthetic/" + now.Format("20060102") + "/us-east-1/s3/aws4_request"}, "X-Amz-Date": {now.Format("20060102T150405Z")}, "X-Amz-Expires": {"120"}, "X-Amz-SignedHeaders": {"host"}, "X-Amz-Signature": {strings.Repeat("a", 64)}}
	address := "https://s3.us-east-1.amazonaws.com/synthetic-map/map-cache/" + id + ".png?" + q.Encode()
	for _, name := range []string{"valid", "wrong-host", "wrong-image", "expired", "too-long", "wrong-hash", "wrong-size", "oversize", "redirect", "wrong-dimensions"} {
		t.Run(name, func(t *testing.T) {
			v := contract.MapView{ImageID: id, ImageVersion: "1", DownloadURL: address, ImageBytes: int64(encoded.Len()), ImageSHA256: fmt.Sprintf("%x", sum), NoticeRows: 192, Width: 3072, Height: 2048, Zoom: 19, GeneratedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Attribution: "Synthetic"}
			switch name {
			case "wrong-host":
				v.DownloadURL = strings.Replace(address, "s3.us-east-1.amazonaws.com", "untrusted.invalid", 1)
			case "wrong-image":
				v.ImageID = strings.Repeat("b", 32)
			case "expired":
				v.DownloadURL = strings.Replace(address, url.QueryEscape(now.Format("20060102T150405Z")), url.QueryEscape(now.Add(-3*time.Minute).Format("20060102T150405Z")), 1)
			case "too-long":
				v.DownloadURL = strings.Replace(address, "X-Amz-Expires=120", "X-Amz-Expires=300", 1)
			case "wrong-hash":
				v.ImageSHA256 = strings.Repeat("b", 64)
			case "wrong-size":
				v.ImageBytes++
			case "oversize":
				v.ImageBytes = contract.MaxMapImageBytes + 1
			case "wrong-dimensions":
				v.Width--
			}
			calls := 0
			client := NewClient()
			client.HTTP = &http.Client{Transport: mapDownloadTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path == "/api/map/view" {
					if r.URL.Path != "/api/map/view" || r.Header.Get("Authorization") == "" || r.Header.Get("Accept") != contract.MapTicketMediaType {
						t.Fatal("metadata lost owner authorization")
					}
					data, _ := json.Marshal(v)
					return &http.Response{StatusCode: 200, ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}}, nil
				}
				if calls != 2 || r.URL.Host != "s3.us-east-1.amazonaws.com" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("download escaped scope or forwarded authorization")
				}
				status := 200
				if name == "redirect" {
					status = 307
				}
				return &http.Response{StatusCode: status, ContentLength: int64(encoded.Len()), Body: io.NopCloser(bytes.NewReader(encoded.Bytes())), Header: http.Header{"Location": []string{"https://untrusted.invalid"}}}, nil
			})}
			got, img, err := client.MapView(context.Background(), Session{AccessHandle: "synthetic-owner"})
			if name == "valid" {
				if err != nil || got.DownloadURL != "" || got.PNG != nil {
					t.Fatal("valid image unavailable or retained credentials", err)
				}
				if _, ok := img.(*image.NRGBA); !ok {
					t.Fatal("large image not normalized once")
				}
				v.ObservationRevision = "new-position"
				second, reused, e := client.MapView(context.Background(), Session{AccessHandle: "synthetic-owner"})
				if e != nil || second.ObservationRevision != "new-position" || reused != img || calls != 3 {
					t.Fatal("immutable image redownloaded or current position lost", e)
				}
			} else if err == nil {
				t.Fatal("invalid image accepted")
			}
			if (name == "wrong-host" || name == "wrong-image" || name == "expired" || name == "too-long" || name == "oversize") && calls != 1 {
				t.Fatal("unsafe download made network request")
			}
		})
	}
}

func TestWidgetCommandPersistsBeforeSubmitAndDoesNotRetryUnknown(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	for _, action := range []string{"start", "unlock", "lock"} {
		t.Run(action, func(t *testing.T) {
			vault := Vault{Path: filepath.Join(t.TempDir(), "session.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
			if err := vault.Save(SavedState{Session: Session{AccessHandle: "test-owner", RefreshHandle: "test-refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}); err != nil {
				t.Fatal(err)
			}
			var submitted atomic.Int32
			var statusCalls atomic.Int32
			var status atomic.Value
			status.Store("outcome-unknown")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/features":
					_ = json.NewEncoder(w).Encode(contract.FeaturesResponse{Availability: []contract.FeatureAvailability{{ID: action, Available: true}}})
				case "/api/commands":
					submitted.Add(1)
					var intent CommandIntent
					_ = json.NewDecoder(r.Body).Decode(&intent)
					state, err := vault.Load()
					if err != nil || state.Command == nil || state.Command.RequestID != intent.RequestID || intent.Action != action {
						t.Error("submission was not durably prepared")
					}
					w.WriteHeader(http.StatusServiceUnavailable)
				case "/api/commands/status":
					statusCalls.Add(1)
					_ = json.NewEncoder(w).Encode(CommandResult{Action: action, State: status.Load().(string), UpdatedAt: 123})
				default:
					t.Error("unexpected request")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client := NewClient()
			client.BaseURL = server.URL
			if _, err := WidgetCommand(context.Background(), vault, client, action); err == nil {
				t.Fatal("unknown submission reported success")
			}
			before, _ := vault.Load()
			if _, err := WidgetCommand(context.Background(), vault, client, action); err == nil {
				t.Fatal("pending request was retried")
			}
			after, _ := vault.Load()
			if submitted.Load() != 1 || before.Command == nil || after.Command == nil || before.Command.RequestID != after.Command.RequestID {
				t.Fatal("unknown command identity changed")
			}
			if _, err := WidgetCommand(context.Background(), vault, client, "ev-charge"); err == nil {
				t.Fatal("EV action allowed")
			}
			s := Screen{vault: vault, client: client, saved: after, commandStatus: wrapped(""), allowedActions: map[string]bool{"start": true, "unlock": true, "lock": true}, start: newIconAction("Start", nil, nil), unlock: newIconAction("Unlock", nil, nil), lock: newIconAction("Lock", nil, nil), retryCommand: newTool("Retry", nil, nil)}
			if err := s.watchIntent(context.Background()); err != nil {
				t.Fatal(err)
			}
			retained, err := vault.Load()
			if err != nil || retained.Command == nil || retained.Command.RequestID != before.Command.RequestID || !s.commandPending.Load() || !s.start.Disabled() || !s.lock.Disabled() || !s.unlock.Disabled() {
				t.Fatal("app discarded native unknown intent or enabled repeat controls")
			}
			if statusCalls.Load() != 1 {
				t.Fatal("terminal unknown kept polling")
			}
			if err = s.beginCommand(context.Background(), action); err == nil {
				t.Fatal("app allowed new command while unknown")
			}
			if err = s.retryIntent(context.Background()); err != nil {
				t.Fatal(err)
			}
			if submitted.Load() != 1 || statusCalls.Load() != 2 {
				t.Fatal("checking unknown resubmitted instead of reading same request")
			}
			status.Store("failed")
			if err = s.retryIntent(context.Background()); err != nil {
				t.Fatal(err)
			}
			resolved, err := vault.Load()
			if err != nil || resolved.Command != nil || s.commandPending.Load() || s.start.Disabled() || submitted.Load() != 1 {
				t.Fatal("known terminal outcome did not release same intent")
			}
		})
	}
}

func TestVaultLockCoordinatesRefreshAndReleasesAfterError(t *testing.T) {
	vault := Vault{Path: filepath.Join(t.TempDir(), "session.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
	if err := vault.Save(SavedState{Session: Session{AccessHandle: "old", RefreshHandle: "refresh", AccessExpiresAt: time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session/refresh" {
			refreshes.Add(1)
			_ = json.NewEncoder(w).Encode(Session{AccessHandle: "new", RefreshHandle: "rotated", AccessExpiresAt: time.Now().Add(time.Hour)})
			return
		}
		if r.URL.Path == "/api/features" {
			_ = json.NewEncoder(w).Encode(contract.FeaturesResponse{})
			return
		}
		t.Error("unavailable command was submitted")
	}))
	defer server.Close()
	client := NewClient()
	client.BaseURL = server.URL
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := WidgetCommand(context.Background(), vault, client, "lock"); done <- err }()
	}
	for range 2 {
		if <-done == nil {
			t.Fatal("unavailable action accepted")
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refresh count=%d", refreshes.Load())
	}
	if err := vault.WithLock(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if err := vault.WithLock(ctx, func() error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled operation ran")
	}
}

func TestFreshSessionRestoreSyncsWidgetsAfterLockWithoutRotation(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	vault := Vault{Path: filepath.Join(t.TempDir(), "session.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
	if err := vault.Save(SavedState{Session: Session{AccessHandle: "synthetic-fresh", RefreshHandle: "synthetic-refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/dashboard":
			_ = json.NewEncoder(w).Encode(Dashboard{ProviderState: "ready"})
		case "/api/features":
			_ = json.NewEncoder(w).Encode(contract.FeaturesResponse{})
		default:
			t.Error("restore made a command or unnecessary refresh request")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	client := NewClient()
	client.BaseURL = server.URL
	s := Screen{vault: vault, client: client, status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), signIn: widget.NewButton("Sign in", nil), refresh: widget.NewButton("Refresh", nil), logout: widget.NewButton("Logout", nil), cancelButton: widget.NewButton("Cancel", nil)}
	synced := make(chan error, 2)
	s.widgetSyncSender = func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := vault.WithLock(ctx, func() error { return nil })
		synced <- err
		return err == nil
	}
	s.run(s.restore)
	select {
	case err := <-synced:
		if err != nil {
			t.Fatal("receiver ran before lock released", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh restored session did not synchronize cold widgets")
	}
	deadline := time.Now().Add(time.Second)
	for s.busy.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.busy.Load() {
		t.Fatal("restore did not finish")
	}
	if requests.Load() != 2 {
		t.Fatal("restore added a command or token rotation")
	}
	select {
	case <-synced:
		t.Fatal("restore sent duplicate sync")
	case <-time.After(25 * time.Millisecond):
	}
	s.preview.Store(true)
	s.run(s.restore)
	deadline = time.Now().Add(time.Second)
	for s.busy.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if requests.Load() != 2 {
		t.Fatal("Preview used authenticated network")
	}
	select {
	case <-synced:
		t.Fatal("Preview synced authenticated widgets")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestWidgetSyncWaitsForOuterVaultTransactionAndCoalesces(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	vault := Vault{Path: filepath.Join(t.TempDir(), "owner-session.enc")}
	notified := make(chan error, 4)
	s := Screen{vault: vault, status: wrapped(""), signIn: widget.NewButton("Sign in", nil), cancelButton: widget.NewButton("Cancel", nil)}
	s.widgetSyncSender = func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := vault.WithLock(ctx, func() error { return nil })
		notified <- err
		return err == nil
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	s.run(func(context.Context) error {
		s.syncWidgets()
		s.syncWidgets()
		s.syncWidgets()
		close(entered)
		<-release
		return nil
	})
	<-entered
	select {
	case err := <-notified:
		close(release)
		t.Fatalf("notification ran while app held vault: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-notified:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred sync was lost")
	}
	select {
	case <-notified:
		t.Fatal("session sync events were not coalesced")
	case <-time.After(50 * time.Millisecond):
	}
	deadline := time.Now().Add(time.Second)
	for s.busy.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.busy.Load() {
		t.Fatal("app transaction did not finish")
	}
	s.syncWidgets()
	select {
	case err := <-notified:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("direct session sync was lost")
	}
	s.preview.Store(true)
	s.syncWidgets()
	select {
	case <-notified:
		t.Fatal("Preview triggered authenticated sync")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestSignOutRefreshOutageKeepsLocalSession(t *testing.T) {
	for _, status := range []int{401, 410} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/session/logout" {
					w.WriteHeader(status)
				} else {
					w.WriteHeader(503)
				}
			}))
			defer server.Close()
			client := NewClient()
			client.BaseURL = server.URL
			path := filepath.Join(t.TempDir(), "session.enc")
			if e := os.WriteFile(path, []byte("encrypted"), 0600); e != nil {
				t.Fatal(e)
			}
			screen := Screen{client: client, vault: Vault{Path: path}, saved: SavedState{Session: Session{AccessHandle: "expired", RefreshHandle: "retained"}}}
			if e := screen.signOut(context.Background()); e == nil {
				t.Fatal("logout claimed success during refresh outage")
			}
			if _, e := os.Stat(path); e != nil || screen.saved.Session.RefreshHandle != "retained" {
				t.Fatal("failed logout discarded session")
			}
		})
	}
}

func TestCanceledInflightPollClearsEncryptedPending(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	entered := make(chan struct{})
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request body")
		}
		switch r.URL.Path {
		case "/auth/mobile/poll":
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		case "/auth/mobile/cancel":
			if r.Context().Err() != nil || body["code"] != "code" || body["verifier"] != "proof" {
				t.Error("cancel did not use a live verifier-bound request")
			}
			canceled <- struct{}{}
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	defer close(release)
	client := NewClient()
	client.BaseURL = server.URL
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	vault := Vault{Path: filepath.Join(t.TempDir(), "session.enc"), Crypt: func(b []byte, seal bool) ([]byte, error) {
		nonce := make([]byte, aead.NonceSize())
		if seal {
			return aead.Seal(nil, nonce, b, nil), nil
		}
		return aead.Open(nil, nonce, b, nil)
	}}
	pending := PendingLogin{Login: Login{Code: "code", ExpiresAt: time.Now().Add(time.Minute)}, Verifier: "proof"}
	screen := Screen{client: client, vault: vault, saved: SavedState{Pending: &pending}, status: widget.NewLabel(""), cancelButton: widget.NewButton("Cancel", nil), signIn: widget.NewButton("Sign in", nil)}
	if e := vault.Save(screen.saved); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- screen.poll(ctx, pending) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not start")
	}
	cancel()
	select {
	case e := <-result:
		if e == nil || e.Error() != "Sign-in canceled." {
			t.Fatal("canceled poll did not clear pending")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled poll stalled")
	}
	select {
	case <-canceled:
	default:
		t.Fatal("server cancellation not sent with fresh context")
	}
	saved, e := vault.Load()
	if e != nil || saved.Pending != nil || screen.saved.Pending != nil {
		t.Fatal("pending verifier survived cancellation")
	}
}

func TestVerifierAndBrowserOrigin(t *testing.T) {
	v, c, e := NewVerifier()
	if e != nil {
		t.Fatal(e)
	}
	b, e := base64.RawURLEncoding.DecodeString(v)
	h := sha256.Sum256(b)
	if e != nil || len(b) != 32 || c != base64.RawURLEncoding.EncodeToString(h[:]) {
		t.Fatal("invalid proof challenge")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Login{Code: "code", BrowserURL: "https://evil.example/auth/mobile/begin"})
	}))
	defer server.Close()
	client := NewClient()
	client.BaseURL = server.URL
	if _, e = client.Start(context.Background(), c); e == nil {
		t.Fatal("accepted foreign browser destination")
	}
}
func TestPollSessionAndDashboard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/mobile/poll":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if in["verifier"] != "proof" || in["code"] != "code" {
				t.Error("missing verifier binding")
			}
			json.NewEncoder(w).Encode(Session{AccessHandle: "opaque", RefreshHandle: "refresh"})
		case "/api/dashboard":
			if r.Header.Get("Authorization") != "Bearer "+"opaque" {
				t.Error("missing bearer")
			}
			json.NewEncoder(w).Encode(Dashboard{ProviderState: "ready", ControlsEnabled: false})
		}
	}))
	defer server.Close()
	c := NewClient()
	c.BaseURL = server.URL
	s, e := c.Poll(context.Background(), PendingLogin{Login: Login{Code: "code"}, Verifier: "proof"})
	if e != nil {
		t.Fatal(e)
	}
	d, e := c.Dashboard(context.Background(), s)
	if e != nil || d.ProviderState != "ready" || d.ControlsEnabled {
		t.Fatal("dashboard contract")
	}
}
func TestErrorsDoNotExposeProviderBody(t *testing.T) {
	for _, status := range []int{202, 401, 410, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte("private-provider-body"))
			}))
			defer server.Close()
			c := NewClient()
			c.BaseURL = server.URL
			_, e := c.Poll(context.Background(), PendingLogin{})
			if e == nil || strings.Contains(e.Error(), "private-provider") {
				t.Fatal("raw response leaked")
			}
			if status == 202 && !errors.Is(e, ErrPending) {
				t.Fatal("pending became failure")
			}
		})
	}
}

func TestCommandAcceptedIsNotCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(CommandResult{State: "queued", Action: "lock", CommandID: "original"})
	}))
	defer server.Close()
	c := NewClient()
	c.BaseURL = server.URL
	r, e := c.SubmitCommand(context.Background(), Session{AccessHandle: "owner"}, CommandIntent{Action: "lock", RequestID: "original"})
	if e != nil || r.State != "queued" || r.Terminal() || strings.Contains(commandText(r), "confirmed successful") {
		t.Fatal("acceptance presented as vehicle completion")
	}
	for _, state := range []string{"dispatching", "submitted", "failed", "expired", "outcome-unknown", "rejected", "dry-run"} {
		if strings.Contains(commandText(CommandResult{State: state, Action: "start"}), "confirmed successful") {
			t.Fatal("non-success presented as completion")
		}
	}
}

func TestCommandPersistsBeforeSubmitAndRetriesSameIntent(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	vault := Vault{Path: filepath.Join(t.TempDir(), "session.enc"), Crypt: func(b []byte, seal bool) ([]byte, error) {
		nonce := make([]byte, aead.NonceSize())
		if seal {
			return aead.Seal(nil, nonce, b, nil), nil
		}
		return aead.Open(nil, nonce, b, nil)
	}}
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+"owner" {
			t.Error("missing owner authorization")
		}
		if r.Method == "POST" {
			var intent CommandIntent
			if json.NewDecoder(r.Body).Decode(&intent) != nil {
				t.Error("bad intent")
			}
			stored, e := vault.Load()
			if e != nil || stored.Command == nil || stored.Command.RequestID != intent.RequestID {
				t.Error("intent sent before durable storage")
			}
			ids = append(ids, intent.RequestID)
			if len(ids) == 1 {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(CommandResult{Action: intent.Action, CommandID: intent.RequestID, State: "submitted"})
			return
		}
		if r.URL.Path != "/api/commands/status" || r.URL.Query().Get("requestId") != ids[0] {
			t.Error("status not tied to original intent")
		}
		json.NewEncoder(w).Encode(CommandResult{Action: "lock", CommandID: ids[0], State: "succeeded"})
	}))
	defer server.Close()
	client := NewClient()
	client.BaseURL = server.URL
	s := Screen{client: client, vault: vault, commandStatus: widget.NewLabel(""), saved: SavedState{Session: Session{AccessHandle: "owner", RefreshHandle: "refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}}
	if e := s.beginCommand(context.Background(), "lock"); e == nil {
		t.Fatal("lost response accepted as completion")
	}
	if s.saved.Command == nil || !s.commandPending.Load() {
		t.Fatal("ambiguous intent lost")
	}
	if e := s.beginCommand(context.Background(), "unlock"); e == nil || len(ids) != 1 {
		t.Fatal("second command submitted while outcome unresolved")
	}
	// A new process reads the encrypted intent and reuses the exact request ID.
	stored, e := vault.Load()
	if e != nil {
		t.Fatal(e)
	}
	s.saved = stored
	if e = s.retryIntent(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(ids) != 2 || ids[0] != ids[1] || ids[0] == "" {
		t.Fatal("retry created a second intent")
	}
	stored, e = vault.Load()
	if e != nil || stored.Command != nil || s.commandPending.Load() {
		t.Fatal("confirmed terminal request remained pending")
	}
}

func TestCommandStorageFailureMakesNoRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	client := NewClient()
	client.BaseURL = server.URL
	s := Screen{client: client, vault: Vault{Path: filepath.Join(t.TempDir(), "session.enc"), Crypt: func([]byte, bool) ([]byte, error) { return nil, errors.New("key unavailable") }}, saved: SavedState{Session: Session{RefreshHandle: "owner"}}}
	if e := s.beginCommand(context.Background(), "start"); e == nil || requests != 0 || s.saved.Command != nil {
		t.Fatal("command escaped failed secure persistence")
	}
}

func TestPendingCommandDisablesAllVehicleButtons(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := Screen{start: widget.NewButton("Start", nil), lock: widget.NewButton("Lock", nil), unlock: widget.NewButton("Unlock", nil), retryCommand: widget.NewButton("Retry", nil), allowedActions: map[string]bool{"start": true, "lock": true, "unlock": false}}
	s.commandPending.Store(true)
	s.updateControlButtons()
	if !s.start.Disabled() || !s.lock.Disabled() || !s.unlock.Disabled() || !s.retryCommand.Visible() {
		t.Fatal("pending request permits a second vehicle intent")
	}
	s.commandPending.Store(false)
	s.updateControlButtons()
	if s.start.Disabled() || s.lock.Disabled() || !s.unlock.Disabled() || s.retryCommand.Visible() {
		t.Fatal("capability gates not restored after terminal result")
	}
}

func TestRejectedCommandIsExplicitlyNotSent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		json.NewEncoder(w).Encode(CommandResult{State: "rejected", Action: "unlock", Reason: "controls-disabled"})
	}))
	defer server.Close()
	c := NewClient()
	c.BaseURL = server.URL
	r, e := c.SubmitCommand(context.Background(), Session{AccessHandle: "owner"}, CommandIntent{Action: "unlock", RequestID: "same"})
	if e != nil || !r.Terminal() || !strings.Contains(commandText(r), "was not sent") {
		t.Fatal("definitive rejection treated as an ambiguous sent command")
	}
}

func TestLogoutClearsPendingIntentAcrossSessionFamilies(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/session/logout" {
			t.Error("unexpected request during logout")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	client := NewClient()
	client.BaseURL = server.URL
	path := filepath.Join(t.TempDir(), "session.enc")
	if e := os.WriteFile(path, []byte("encrypted-session-and-intent"), 0600); e != nil {
		t.Fatal(e)
	}
	s := Screen{client: client, vault: Vault{Path: path}, saved: SavedState{Session: Session{AccessHandle: "owner"}, Command: &CommandIntent{Action: "start", RequestID: "old-family-intent"}}, status: widget.NewLabel(""), detail: widget.NewLabel(""), commandStatus: widget.NewLabel(""), signIn: widget.NewButton("Sign in", nil), refresh: widget.NewButton("Refresh", nil), logout: widget.NewButton("Sign out", nil)}
	s.commandPending.Store(true)
	if e := s.signOut(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(path); !errors.Is(e, os.ErrNotExist) || s.saved.Command != nil || s.saved.Session.AccessHandle != "" || s.commandPending.Load() {
		t.Fatal("old family intent retained for retry after logout")
	}
	if !strings.Contains(s.commandStatus.Text, "may still finish") {
		t.Fatal("logout implied cancellation of a submitted vehicle command")
	}
}

func TestAuthorizationRejectionDoesNotResolvePriorIntent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		json.NewEncoder(w).Encode(CommandResult{State: "rejected", Action: "lock", Reason: "owner-authority-unverified"})
	}))
	defer server.Close()
	c := NewClient()
	c.BaseURL = server.URL
	r, e := c.SubmitCommand(context.Background(), Session{AccessHandle: "owner"}, CommandIntent{Action: "lock", RequestID: "previous"})
	if e == nil || r.Terminal() {
		t.Fatal("authority rejection incorrectly resolved possibly submitted command")
	}
}

func TestNewLoginDoesNotRetryPriorFamilyIntent(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/mobile/poll":
			json.NewEncoder(w).Encode(Session{AccessHandle: "new-family", RefreshHandle: "new-refresh", AccessExpiresAt: time.Now().Add(time.Hour)})
		case "/api/dashboard":
			json.NewEncoder(w).Encode(Dashboard{ProviderState: "ready"})
		case "/api/features":
			json.NewEncoder(w).Encode(map[string]any{"features": []any{}, "availability": []any{}})
		default:
			t.Error("old command retried after new login")
		}
	}))
	defer server.Close()
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	v := Vault{Path: filepath.Join(t.TempDir(), "session.enc"), Crypt: func(b []byte, seal bool) ([]byte, error) {
		nonce := make([]byte, aead.NonceSize())
		if seal {
			return aead.Seal(nil, nonce, b, nil), nil
		}
		return aead.Open(nil, nonce, b, nil)
	}}
	c := NewClient()
	c.BaseURL = server.URL
	s := Screen{client: c, vault: v, saved: SavedState{Command: &CommandIntent{Action: "lock", RequestID: "old-family"}}, status: widget.NewLabel(""), detail: widget.NewLabel(""), commandStatus: widget.NewLabel(""), signIn: widget.NewButton("Sign in", nil), refresh: widget.NewButton("Refresh", nil), logout: widget.NewButton("Sign out", nil), cancelButton: widget.NewButton("Cancel", nil)}
	s.commandPending.Store(true)
	if e := s.poll(context.Background(), PendingLogin{Login: Login{Code: "new", ExpiresAt: time.Now().Add(time.Minute)}, Verifier: "proof"}); e != nil {
		t.Fatal(e)
	}
	stored, e := v.Load()
	if e != nil || stored.Command != nil || s.commandPending.Load() || stored.Session.AccessHandle != "new-family" {
		t.Fatal("new family retained old retry intent")
	}
	if !strings.Contains(s.commandStatus.Text, "may still finish") {
		t.Fatal("new login hid unresolved previous command")
	}
}

func TestDashboardRefreshShowsProgressAndSameMinuteCompletion(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	base := time.Date(2026, 10, 23, 21, 1, 10, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/features" {
			json.NewEncoder(w).Encode(map[string]any{"features": []any{}, "availability": []any{}})
			return
		}
		n := calls.Add(1)
		if n == 1 {
			close(entered)
			<-release
		}
		json.NewEncoder(w).Encode(Dashboard{ProviderState: "ready", StatusReadAt: base.Add(time.Duration(n) * time.Second)})
	}))
	defer server.Close()
	c := NewClient()
	c.BaseURL = server.URL
	s := Screen{client: c, saved: SavedState{Session: Session{AccessHandle: "owner", RefreshHandle: "refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}, status: widget.NewLabel("GM connection: ready"), detail: widget.NewLabel(""), commandStatus: widget.NewLabel(""), signIn: widget.NewButton("Sign in", nil), refresh: widget.NewButton("Refresh connection status", nil), logout: widget.NewButton("Sign out", nil), cancelButton: widget.NewButton("Cancel", nil)}
	done := make(chan error, 1)
	go func() { done <- s.dashboard(context.Background()) }()
	<-entered
	if !s.refresh.Disabled() || !strings.Contains(s.status.Text, "Refreshing") {
		t.Error("refresh has no visible in-progress feedback")
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	first := s.detail.Text
	if s.refresh.Disabled() {
		t.Error("refresh remained disabled after completion")
	}
	if e := s.dashboard(context.Background()); e != nil {
		t.Fatal(e)
	}
	if first != s.detail.Text || strings.Contains(s.detail.Text, "UTC") {
		t.Error("display time should remain at minute precision")
	}
	s.busy.Store(true)
	s.refreshStatus()
	if !strings.Contains(s.status.Text, "already in progress") || calls.Load() != 2 {
		t.Error("busy refresh tap was silent or duplicated the request")
	}
}

func TestSingleHomePreviewCannotUseNetwork(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	c := NewClient()
	c.BaseURL = server.URL
	s := Screen{client: c, status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), saved: SavedState{Session: Session{AccessHandle: "live", RefreshHandle: "live"}, Command: &CommandIntent{Action: "lock", RequestID: "retain"}}}
	s.preview.Store(true)
	for _, action := range []string{"start", "unlock", "lock"} {
		_ = s.beginCommand(context.Background(), action)
		s.chooseCommand(action)
	}
	s.refreshHomeTelemetry(context.Background(), true)
	s.queueEnvironment(true)
	_ = s.setPhysicalObserved(context.Background(), "lock")
	_ = s.restore(context.Background())
	_ = s.dashboard(context.Background())
	_ = s.latestActivity(context.Background())
	_ = s.login(context.Background())
	_ = s.poll(context.Background(), PendingLogin{})
	_ = s.cancelLogin(PendingLogin{})
	_ = s.rotate(context.Background())
	_ = s.signOut(context.Background())
	_ = s.retryIntent(context.Background())
	_ = s.watchIntent(context.Background())
	if requests.Load() != 0 || s.saved.Command == nil || s.saved.Command.RequestID != "retain" || s.saved.Session.RefreshHandle != "live" {
		t.Fatal("preview reached network or altered live state")
	}
}

func TestSnapshotSeparatesFetchTimeFromObservation(t *testing.T) {
	snapshot := contract.VehicleSnapshot{FeatureID: "diagnostics", State: "partial", FetchedAt: time.Now(), Fields: []contract.SnapshotField{{SourceName: "SYNTHETIC_FUEL", Value: "62", Unit: "%", Freshness: "unknown"}}}
	readings := telemetryFromSnapshot(snapshot, TelemetryBindings{FuelName: "SYNTHETIC_FUEL", FuelUnit: "%"})
	if !readings.Fuel.Available || readings.Fuel.ObservedAt != nil || readings.Fuel.Freshness != "unknown" {
		t.Fatal("fetch time became observation")
	}
}

func TestAndroidCalendarUsesCurrentDeviceOffsetWithoutChangingInstants(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	now := time.Date(2026, 1, 2, 1, 30, 0, 0, time.UTC)
	saved := appearance.Default()
	saved.Mode = appearance.Automatic
	offset := -5 * 60 * 60 * 1000
	available := true
	s := Screen{calendarOffset: func(at time.Time) (int, bool) {
		if !at.Equal(now) {
			t.Fatal("instant changed before native lookup")
		}
		return offset, available
	}}
	settings, local, ok := s.presentationAt(saved, now)
	if !ok || !local.Equal(now) || settings.ActiveHoliday(local) != "new-year" {
		t.Fatal("Android UTC selected birthday instead of local New Year")
	}
	s.appearance = saved
	s.quickButtons = map[string]*iconAction{"start": newIconAction("Start", nil, nil)}
	s.status = wrapped("")
	s.applyAppearance(now)
	if !strings.Contains(s.quickButtons["start"].Icon.Name(), "new-year") {
		t.Fatal("app renderer ignored device calendar")
	}
	offset = 2 * 60 * 60 * 1000
	settings, local, ok = s.presentationAt(saved, now)
	if !ok || !local.Equal(now) || settings.ActiveHoliday(local) != "birthday" {
		t.Fatal("device timezone change not applied")
	}
	s.applyAppearance(now)
	if !strings.Contains(s.quickButtons["start"].Icon.Name(), "birthday") {
		t.Fatal("app renderer retained previous zone style")
	}
	available = false
	settings, local, ok = s.presentationAt(saved, now)
	if ok || settings.Mode != appearance.Normal || !local.Equal(now) || saved.Mode != appearance.Automatic {
		t.Fatal("unavailable device calendar guessed UTC or changed preferences")
	}
	saved.Mode = appearance.Seasonal
	saved.Holiday = "christmas"
	settings, local, ok = s.presentationAt(saved, now)
	if ok || settings.ActiveHoliday(local) != "christmas" {
		t.Fatal("unavailable device zone changed manual holiday")
	}
	if _, ok := calendarWithOffset(now, 25*60*60*1000, true); ok {
		t.Fatal("invalid native offset accepted")
	}
	if _, ok := calendarWithOffset(now, 0, true); !ok {
		t.Fatal("explicit device UTC rejected")
	}
}

func TestAppearanceSaveCancelAndRestartWithoutVehicleRequest(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	window := app.NewWindow("Appearance")
	window.Resize(fyne.NewSize(360, 640))
	window.Show()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	s := Screen{app: app, window: window, client: &Client{HTTP: server.Client(), BaseURL: server.URL}, status: wrapped("")}
	var selections []*widget.Select
	var tiles []*styleTile
	var colorEntry *widget.Entry
	buttons := map[string]*widget.Button{}
	var inspect func(fyne.CanvasObject)
	inspect = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *fyne.Container:
			for _, child := range v.Objects {
				inspect(child)
			}
		case *widget.Select:
			selections = append(selections, v)
		case *styleTile:
			tiles = append(tiles, v)
		case *widget.Entry:
			colorEntry = v
		case *widget.Button:
			buttons[v.Text] = v
		}
	}
	popup := s.showAppearance()
	inspect(popup.Content)
	if len(tiles) != 5 || buttons["Previous"].Disabled() != true {
		t.Fatal("missing four-tile gallery and selected preview")
	}
	for range 9 {
		buttons["Next"].OnTapped()
	}
	if !buttons["Next"].Disabled() || !tiles[1].Visible() || tiles[2].Visible() || tiles[1].style.ID != appearance.Styles("")[36].ID {
		t.Fatal("37 normal styles need ten bounded pages")
	}
	selections[0].SetSelected("Christmas")
	buttons["Next"].OnTapped()
	buttons["Next"].OnTapped()
	if !buttons["Next"].Disabled() || !tiles[2].Visible() || tiles[3].Visible() {
		t.Fatal("10 holiday styles need three bounded pages")
	}
	buttons["Previous"].OnTapped()
	buttons["Previous"].OnTapped()
	tiles[3].OnTapped()
	buttons["Cancel"].OnTapped()
	loaded, err := appearance.Load(app.Preferences())
	if err != nil || loaded.Mode != appearance.Normal {
		t.Fatal("cancel saved draft")
	}
	selections = nil
	tiles = nil
	popup = s.showAppearance()
	inspect(popup.Content)
	selections[0].SetSelected("Christmas")
	tiles[3].OnTapped()
	colorEntry.SetText("#1234AB")
	buttons["Custom color"].OnTapped()
	colorEntry.SetText("invalid")
	buttons["Custom color"].OnTapped()
	buttons["Save"].OnTapped()
	restarted := Screen{app: app}
	restored := restarted.appearanceSettings()
	style, err := restored.SelectedStyle()
	if err != nil || restored.Mode != appearance.Seasonal || style.ID != appearance.Styles("christmas")[2].ID {
		t.Fatal("save did not persist holiday choice")
	}
	if restored.Background != "#1234AB" {
		t.Fatal("custom color not saved or invalid color accepted")
	}
	selections = nil
	tiles = nil
	popup = s.showAppearance()
	inspect(popup.Content)
	selections[0].SetSelected("Automatic")
	buttons["Save"].OnTapped()
	loaded, err = appearance.Load(app.Preferences())
	if err != nil || loaded.Mode != appearance.Automatic {
		t.Fatal("automatic selection not saved")
	}
	if calls.Load() != 0 {
		t.Fatal("appearance sent a network request")
	}
}

func TestAppearanceOpeningNavigationAndIntervalReset(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	settings := appearance.Default()
	settings.BackgroundRotation.Selected = []string{"charlotte", "beach", "mountains"}
	settings.BackgroundRotation.OnOpening = true
	if err := appearance.Save(app.Preferences(), settings); err != nil {
		t.Fatal(err)
	}
	s := Screen{app: app, status: wrapped(""), client: &Client{HTTP: server.Client(), BaseURL: server.URL}}
	s.appearanceOpened()
	if s.appearance.Backdrop != "beach" {
		t.Fatal("opening not advanced")
	}
	s.appearanceOpened()
	if s.appearance.Backdrop != "beach" {
		t.Fatal("same opening advanced twice")
	}
	s.appearanceClosed()
	s.appearanceInternalReturn = true
	s.appearanceOpened()
	if s.appearance.Backdrop != "beach" {
		t.Fatal("permission return advanced")
	}
	s.navigateAppearance(false, 1)
	if s.appearance.Backdrop != "mountains" {
		t.Fatal("manual navigation not persisted")
	}
	stored, err := appearance.Load(app.Preferences())
	if err != nil || stored.Backdrop != "mountains" {
		t.Fatal("manual selection lost")
	}
	now := time.Now()
	before := stored.Clone()
	before.BackgroundRotation.Enabled = true
	before.BackgroundRotation.Epoch = now.Add(-time.Hour)
	draft := before.Clone()
	draft.BackgroundRotation.Minutes = 15
	current := before.BackdropAt(now)
	normalizeRotationEpochs(&draft, before, now)
	if draft.BackdropAt(now) != current || !draft.BackgroundRotation.Epoch.Equal(now) {
		t.Fatal("interval change jumps or keeps old timer")
	}
	if calls.Load() != 0 {
		t.Fatal("appearance sent vehicle or provider request")
	}
}

func TestWeatherHoldCancellationAndRelease(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := Screen{app: app, window: app.NewWindow("Weather hold"), client: NewClient(), status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), signIn: newTool("Sign in", nil, nil), refresh: newTool("Refresh", nil, nil), logout: newTool("Logout", nil, nil), cancelButton: newTool("Cancel", nil, nil), retryCommand: newTool("Retry", nil, nil)}
	s.buildLayout("test")
	hold := weatherHold{screen: &s}
	start := func() {
		now := time.Now()
		s.home.preview.Press(now.Add(-15*time.Second), "Rain")
		s.home.preview.Condition(now, "Rain")
		if !s.home.preview.Active() {
			t.Fatal("preview did not start")
		}
	}
	start()
	hold.TouchUp(nil)
	if !s.home.preview.Active() {
		t.Fatal("ordinary release stopped active pass")
	}
	hold.TouchCancel(nil)
	if s.home.preview.Active() || s.home.previewGeneration != 1 {
		t.Fatal("touch cancellation retained active pass")
	}
	start()
	hold.MouseOut()
	if s.home.preview.Active() || s.home.previewGeneration != 2 {
		t.Fatal("pointer exit retained active pass")
	}
}

func TestRotationLockHoldsOnlyAppearance(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := Screen{app: app, window: app.NewWindow("Rotation lock"), client: NewClient(), status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), signIn: newTool("Sign in", nil, nil), refresh: newTool("Refresh", nil, nil), logout: newTool("Logout", nil, nil), cancelButton: newTool("Cancel", nil, nil), retryCommand: newTool("Retry", nil, nil)}
	s.preview.Store(true)
	s.appearance = appearance.Default()
	s.appearance.Mode = appearance.Normal
	s.appearance.BackgroundRotation.Enabled = true
	s.appearance.BackgroundRotation.Selected = []string{"charlotte", "beach"}
	styles := appearance.Styles("")
	s.appearance.NormalStyle = styles[0].ID
	s.appearance.ButtonRotation.Enabled = true
	s.appearance.ButtonRotation.Selected = []string{styles[0].ID, styles[1].ID}
	now := time.Now()
	s.appearance.BackgroundRotation.Epoch = now
	s.appearance.ButtonRotation.Epoch = now
	s.appearance.BackgroundRotation.Minutes = 1
	s.appearance.ButtonRotation.Minutes = 1
	s.appearance.BackgroundRotation.OnOpening = true
	s.appearance.ButtonRotation.OnOpening = true
	s.buildLayout("test")
	s.home.root.Resize(fyne.NewSize(360, 720))
	before, _ := json.Marshal(s.appearanceSettings())
	lock := s.home.hits["screen-lock"].(*iconAction)
	lock.OnTapped()
	for _, action := range []string{"start", "unlock", "lock"} {
		s.home.hits[action].(*iconAction).OnTapped()
		if s.commandStatus.Text != "Preview · "+featureCaption(action)+" not sent" {
			t.Fatal("rotation lock blocked command", action)
		}
		if !strings.Contains(s.sceneInput(now).Status, featureCaption(action)+" not sent") {
			t.Fatal("preview command result is not visible", action)
		}
	}
	in := s.sceneInput(now)
	style, _ := in.Settings.SelectedStyleAt(in.Now)
	background := in.Settings.BackdropAt(in.Now)
	future := s.sceneInput(now.Add(61 * time.Second))
	futureStyle, _ := future.Settings.SelectedStyleAt(future.Now)
	if future.Settings.BackdropAt(future.Now) != background || futureStyle.ID != style.ID {
		t.Fatal("rotation changed locked appearance")
	}
	s.appearanceClosed()
	s.appearanceOpened()
	after, _ := json.Marshal(s.appearanceSettings())
	if !bytes.Equal(before, after) {
		t.Fatal("locking/opening changed stored rotation preferences")
	}
	gesture := s.home.mapPanel.(*mapInteraction)
	gesture.scale(2, 100, 100)
	if s.mapTransform.Zoom != 2 {
		t.Fatal("rotation lock blocked map")
	}
	popup := s.openPanel("Settings", wrapped("usable"))
	if !popup.Visible() {
		t.Fatal("rotation lock blocked settings")
	}
	popup.Hide()
	s.commandStatus.SetText("new result")
	s.paintHome()
	if s.home.input.Status == "" {
		t.Fatal("rotation lock froze telemetry")
	}
	s.navigateAppearance(false, 1)
	manual := s.sceneInput(now)
	if manual.Settings.BackdropAt(manual.Now) == background {
		t.Fatal("manual background navigation blocked")
	}
	later := s.sceneInput(now.Add(5 * time.Minute))
	if later.Settings.BackdropAt(later.Now) != manual.Settings.BackdropAt(manual.Now) {
		t.Fatal("manually selected background was not held")
	}
	s.navigateAppearance(true, 1)
	manual = s.sceneInput(now)
	manualStyle, _ := manual.Settings.SelectedStyleAt(manual.Now)
	if manualStyle.ID == style.ID {
		t.Fatal("manual button navigation blocked")
	}
	later = s.sceneInput(now.Add(5 * time.Minute))
	laterStyle, _ := later.Settings.SelectedStyleAt(later.Now)
	if laterStyle.ID != manualStyle.ID {
		t.Fatal("manually selected button style was not held")
	}
	lock.OnTapped()
	if s.home.input.ScreenLocked {
		t.Fatal("rotation unlock did not clear indicator")
	}
	if !s.home.input.Settings.BackgroundRotation.Enabled || !s.home.input.Settings.ButtonRotation.Enabled {
		t.Fatal("unlock did not resume configured rotation")
	}
}

type attributionApp struct {
	fyne.App
	opened string
}

func (a *attributionApp) OpenURL(u *url.URL) error { a.opened = u.String(); return nil }
func TestMapAttributionHitOpensOnlyOfficialDocumentation(t *testing.T) {
	base := test.NewApp()
	defer base.Quit()
	app := &attributionApp{App: base}
	window := app.NewWindow("Attribution")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	s := Screen{app: app, window: window, client: &Client{HTTP: server.Client(), BaseURL: server.URL}, status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), signIn: newTool("Sign in", nil, nil), refresh: newTool("Refresh", nil, nil), logout: newTool("Logout", nil, nil), cancelButton: newTool("Cancel", nil, nil), retryCommand: newTool("Retry", nil, nil)}
	s.buildLayout("test")
	s.home.hits["attribution"].(*iconAction).OnTapped()
	if app.opened != "https://docs.aws.amazon.com/location/latest/developerguide/data-attribution.html" {
		t.Fatal("wrong attribution target")
	}
	if calls.Load() != 0 {
		t.Fatal("link hit called vehicle/provider client")
	}
}
func TestFooterPrefersDisplayedMapObservation(t *testing.T) {
	now := time.Now()
	metric := now.Add(-time.Minute)
	s := Screen{homeTelemetry: HomeTelemetry{Fuel: TelemetryReading{Available: true, ObservedAt: &metric}}, vehicleMapImage: image.NewNRGBA(image.Rect(0, 0, 768, 512)), mapView: contract.MapView{ImageID: "synthetic", ImageVersion: "1", GeneratedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), ReceivedAt: now, Width: 768, Height: 512, Zoom: 18, Attribution: "Synthetic"}}
	s.phoneClock.zone, _ = appearancecore.LoadDeviceZone("America/New_York")
	if s.sourceUpdateLabel() != "source time unknown" {
		t.Fatal("metric time replaced unknown map observation")
	}
	s.mapView.ObservedAt = now.Add(-2 * time.Hour)
	if s.sourceUpdateLabel() != s.displayTime(s.mapView.ObservedAt) {
		t.Fatal("map source time ignored")
	}
	for _, place := range []string{"Home", "Work"} {
		s.mapView.Place = place
		if s.sourceUpdateLabel() != place+" · "+s.displayTime(s.mapView.ObservedAt) {
			t.Fatal("configured place missing from map footer")
		}
	}
	s.mapView.Place = "unrecognized"
	if s.sourceUpdateLabel() != s.displayTime(s.mapView.ObservedAt) {
		t.Fatal("unrecognized place displayed")
	}
	s.vehicleMapImage = nil
	if s.sourceUpdateLabel() != s.displayTime(metric) {
		t.Fatal("metric fallback absent without map")
	}
}

func TestPhoneTimeUsesDeviceZoneWithoutSeconds(t *testing.T) {
	zone, ok := appearancecore.LoadDeviceZone("America/New_York")
	if !ok {
		t.Fatal("phone zone unavailable")
	}
	summer := time.Date(2026, 7, 1, 16, 4, 59, 0, time.UTC)
	winter := time.Date(2026, 1, 1, 16, 4, 59, 0, time.UTC)
	if got := appearancecore.DisplayTime(summer, summer, zone); got != "12:04 PM" {
		t.Fatal(got)
	}
	if got := appearancecore.DisplayTime(winter, winter, zone); got != "11:04 AM" {
		t.Fatal(got)
	}
	reads := 0
	s := Screen{}
	s.phoneClock.read = func() (*time.Location, bool) { reads++; return zone, true }
	s.refreshPhoneClock()
	for i := 0; i < 100; i++ {
		_ = s.displayTime(summer)
	}
	if reads != 1 {
		t.Fatal("display formatting called native zone repeatedly")
	}
}

func TestThemedPreviewLayout(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	app.Settings().SetTheme(vehicleTheme{})
	window := app.NewWindow("Suburban")
	s := Screen{app: app, window: window, client: NewClient(), status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), signIn: newTool("Sign in", theme.LoginIcon(), nil), refresh: newTool("Refresh", theme.ViewRefreshIcon(), nil), logout: newTool("Sign out", theme.LogoutIcon(), nil), cancelButton: newTool("Cancel", theme.CancelIcon(), nil), retryCommand: newTool("Retry", theme.ViewRefreshIcon(), nil)}
	s.preview.Store(true)
	content := s.buildLayout("preview")
	window.SetContent(content)
	window.Resize(fyne.NewSize(360, 640))
	s.switchMode(true)
	window.Show()
	if len(s.quickButtons) != 3 {
		t.Fatal("home must expose only three command controls")
	}
	for id, button := range s.quickButtons {
		if button.Disabled() || button.Icon == nil || button.OnTapped == nil || button.AccessibilityLabel() == "" || button.MinSize().Width < 48 || button.MinSize().Height < 48 {
			t.Fatalf("preview feature disabled: %s", id)
		}
	}
	if content.MinSize().Width > 360 {
		t.Fatalf("portrait content requires %.0f dp width", content.MinSize().Width)
	}
	var verifyTree func(fyne.CanvasObject)
	verifyTree = func(object fyne.CanvasObject) {
		if !object.Visible() {
			return
		}
		if _, ok := object.(*container.Scroll); ok {
			t.Fatal("scrolling page found")
		}
		if box, ok := object.(*fyne.Container); ok {
			for _, child := range box.Objects {
				if child.Visible() {
					if child.Position().X < -1 || child.Position().Y < -1 || child.Position().X+child.Size().Width > box.Size().Width+1 || child.Position().Y+child.Size().Height > box.Size().Height+1 {
						t.Fatalf("child %T exceeds parent bounds: pos=%v size=%v parent=%v", child, child.Position(), child.Size(), box.Size())
					}
				}
				verifyTree(child)
			}
		}
	}
	folder := os.Getenv("MOBILE_RENDER_DIR")
	if folder != "" {
		if e := os.MkdirAll(folder, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, size := range []struct {
		name  string
		value fyne.Size
	}{{"portrait", fyne.NewSize(360, 640)}, {"landscape", fyne.NewSize(780, 390)}, {"compact-landscape", fyne.NewSize(780, 350)}} {
		window.Resize(size.value)
		for _, phase := range []struct {
			name string
			hour int
		}{{"day", 12}, {"dusk", 18}, {"night", 23}} {
			s.previewTime = time.Date(2026, 6, 1, phase.hour, 0, 0, 0, time.UTC)
			s.paintHome()
			name := "preview-" + phase.name + "-" + size.name
			captured := window.Canvas().Capture()
			verifyTree(content)
			if folder == "" {
				continue
			}
			file, e := os.Create(filepath.Join(folder, name+".png"))
			if e != nil {
				t.Fatal(e)
			}
			e = png.Encode(file, captured)
			file.Close()
			if e != nil {
				t.Fatal(e)
			}
			grid := image.NewNRGBA(captured.Bounds())
			draw.Draw(grid, grid.Bounds(), captured, captured.Bounds().Min, draw.Src)
			step := int(8 * window.Canvas().Scale())
			if step < 1 {
				t.Fatal("invalid display scale")
			}
			ink := image.NewUniform(color.NRGBA{R: 102, G: 204, B: 255, A: 72})
			for x := 0; x < grid.Bounds().Dx(); x += step {
				draw.Draw(grid, image.Rect(x, 0, x+1, grid.Bounds().Dy()), ink, image.Point{}, draw.Over)
			}
			for y := 0; y < grid.Bounds().Dy(); y += step {
				draw.Draw(grid, image.Rect(0, y, grid.Bounds().Dx(), y+1), ink, image.Point{}, draw.Over)
			}
			file, e = os.Create(filepath.Join(folder, name+"-grid8dp.png"))
			if e != nil {
				t.Fatal(e)
			}
			e = png.Encode(file, grid)
			file.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
		panels := map[string]fyne.CanvasObject{"data": s.pagedBody([]string{strings.Repeat("Long provider value ", 30), "Observed: unknown"}, newTool("Refresh", theme.ViewRefreshIcon(), nil)), "updates": s.settingsContent, "reminder": s.reminderSettings()}
		appearancePopup := s.showAppearance()
		appearanceCapture := window.Canvas().Capture()
		verifyTree(appearancePopup.Content)
		if appearancePopup.Content.MinSize().Width > size.value.Width || appearancePopup.Content.MinSize().Height > size.value.Height {
			t.Fatal("appearance exceeds viewport")
		}
		if folder != "" {
			file, err := os.Create(filepath.Join(folder, "appearance-"+size.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(file, appearanceCapture)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		appearancePopup.Hide()
		s.preview.Store(false)
		s.homeTelemetry = HomeTelemetry{Fuel: TelemetryReading{Value: 100, Unit: "%", Available: true}, Range: TelemetryReading{Value: 1250, Unit: "mi", Available: true}, Odometer: TelemetryReading{Value: 123456, Unit: "mi", Available: true}}
		s.paintHome()
		liveCapture := window.Canvas().Capture()
		if folder != "" {
			file, err := os.Create(filepath.Join(folder, "fixture-age-unknown-"+size.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(file, liveCapture)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		// The app and widgets now draw the same raster rather than separate
		// canvas text labels. Fuel is a typed gauge, with source age in the footer.
		input := s.sceneInput(time.Now())
		if input.FuelPercent == nil || *input.FuelPercent != 100 || input.Range != "1250 Miles" || !strings.Contains(input.Updated, "source time unknown") {
			t.Fatal("shared scene lost fuel, range or unknown source time")
		}
		s.preview.Store(true)
		for name, body := range panels {
			popup := s.openPanel(name, body)
			window.Canvas().Capture()
			verifyTree(popup.Content)
			if popup.Content.MinSize().Width > size.value.Width || popup.Content.MinSize().Height > size.value.Height {
				t.Fatalf("%s panel exceeds viewport", name)
			}
			popup.Hide()
		}
		s.preview.Store(false)
		s.allowedActions = map[string]bool{"start": true, "lock": true}
		s.updateFeatureAvailability()
		if s.quickButtons["unlock"].Visible() {
			t.Fatal("unavailable action remains visible")
		}
		s.preview.Store(true)
		s.updateFeatureAvailability()
	}
	if folder != "" {
		metadata, _ := json.MarshalIndent(map[string]any{"kind": "headless-single-home-preview-no-scroll", "liveVehicleEvidence": false, "gridDp": 8, "portraitDp": "360x640", "landscapeDp": "780x390", "capturedAt": time.Now().UTC()}, "", "  ")
		if e := os.WriteFile(filepath.Join(folder, "render-info.json"), metadata, 0600); e != nil {
			t.Fatal(e)
		}
	}
}

type layoutMapCounter struct {
	image.Image
	reads int
}

func (m *layoutMapCounter) At(x, y int) color.Color { m.reads++; return m.Image.At(x, y) }
func TestSceneLayoutDoesNotRenderMapPixels(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	window := app.NewWindow("Layout benchmark")
	s := Screen{app: app, window: window, client: NewClient(), status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), signIn: newTool("Sign in", nil, nil), refresh: newTool("Refresh", nil, nil), logout: newTool("Logout", nil, nil), cancelButton: newTool("Cancel", nil, nil), retryCommand: newTool("Retry", nil, nil)}
	s.buildLayout("fixture")
	now := time.Now()
	img := &layoutMapCounter{Image: image.NewNRGBA(image.Rect(0, 0, 768, 512))}
	s.vehicleMapImage = img
	s.mapView = contract.MapView{ImageID: "synthetic", ImageVersion: "1", Width: 768, Height: 512, Zoom: 18, GeneratedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Attribution: "Synthetic"}
	(&sceneLayout{screen: &s}).Layout(nil, fyne.NewSize(360, 720))
	if img.reads != 0 {
		t.Fatalf("layout rendered %d map pixels", img.reads)
	}
}

func TestApprovedPortraitViewportHasNoPaddingOrLetterbox(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	window := app.NewWindow("Approved viewport")
	s := Screen{app: app, window: window, client: NewClient(), status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), signIn: newTool("Sign in", nil, nil), refresh: newTool("Refresh", nil, nil), logout: newTool("Logout", nil, nil), cancelButton: newTool("Cancel", nil, nil), retryCommand: newTool("Retry", nil, nil)}
	s.preview.Store(true)
	content := s.buildLayout("fixture")
	window.SetContent(content)
	window.Resize(fyne.NewSize(360, 720))
	window.Show()
	content.Resize(fyne.NewSize(360, 720))
	if window.Padded() {
		t.Fatal("approved edge-to-edge screen has framework padding")
	}
	if s.home.sky.Position() != (fyne.Position{}) || s.home.sky.Size() != fyne.NewSize(360, 720) {
		t.Fatalf("portrait scene letterboxed: %v %v", s.home.sky.Position(), s.home.sky.Size())
	}
	mapHit := s.home.hits["map"]
	if mapHit.Position().X != 0 || mapHit.Size().Width != 360 {
		t.Fatal("map is not edge-to-edge")
	}
	gear := s.home.hits["settings"]
	if gap := float32(720) - gear.Position().Y - gear.Size().Height; gap < 4 || gap > 6 {
		t.Fatalf("footer bottom gap=%v", gap)
	}
}

func TestCheckboxWholeLabelRowToggles(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	changes := 0
	c := newRowCheck("Grand Canyon background", func(bool) { changes++ })
	c.Resize(fyne.NewSize(340, 48))
	c.MinSize()
	c.Tapped(&fyne.PointEvent{Position: fyne.NewPos(330, 24)})
	if !c.Checked || changes != 1 {
		t.Fatal("word/row tap did not check")
	}
	c.Tapped(&fyne.PointEvent{Position: fyne.NewPos(120, 24)})
	if c.Checked || changes != 2 {
		t.Fatal("word tap did not uncheck")
	}
	c.Disable()
	c.Tapped(&fyne.PointEvent{Position: fyne.NewPos(120, 24)})
	if c.Checked || changes != 2 {
		t.Fatal("disabled row changed")
	}
}

func TestManualLocationRefreshPersistsAndPollsMatchingRevision(t *testing.T) {
	for _, outcome := range []string{"ready", "refresh-superseded", "resume", "effective"} {
		t.Run(outcome, func(t *testing.T) {
			app := test.NewApp()
			defer app.Quit()
			vault := Vault{Path: filepath.Join(t.TempDir(), "owner.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
			saved := SavedState{Session: Session{AccessHandle: "synthetic-access", RefreshHandle: "synthetic-refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}
			if outcome == "resume" {
				saved.LocationRefreshID = "synthetic-persisted-request"
			}
			if err := vault.Save(saved); err != nil {
				t.Fatal(err)
			}
			var posts, polls, mapReads atomic.Int32
			requestID := saved.LocationRefreshID
			var encoded bytes.Buffer
			png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 8, 8)))
			now := time.Now()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/map/refresh":
					posts.Add(1)
					var body struct {
						RequestID string `json:"requestId"`
					}
					json.NewDecoder(r.Body).Decode(&body)
					requestID = body.RequestID
					state, err := vault.Load()
					if err != nil || state.LocationRefreshID != requestID || requestID == "" {
						t.Error("request was not persisted before POST")
					}
					w.WriteHeader(http.StatusAccepted)
					json.NewEncoder(w).Encode(contract.LocationRefreshStatus{RequestID: requestID, State: "queued", PollAfterSeconds: 10})
				case "/api/map/refresh/status":
					if r.Method != "GET" || r.URL.Query().Get("requestId") != requestID {
						t.Error("poll submitted or changed request")
					}
					n := polls.Add(1)
					state := "resolving"
					if n >= 2 && mapReads.Load() > 0 && outcome != "effective" {
						state = "ready"
					}
					reason := ""
					if n >= 2 && outcome == "refresh-superseded" {
						state, reason = "failed", outcome
					}
					json.NewEncoder(w).Encode(contract.LocationRefreshStatus{RequestID: requestID, State: state, Reason: reason, RequestedAt: now.Add(-time.Second), ObservationRevision: "synthetic-revision", ImageID: "synthetic-image", ImageVersion: "1"})
				case "/api/map/view":
					if mapReads.Add(1) == 1 {
						w.Header().Set("Retry-After", "10")
						w.WriteHeader(http.StatusAccepted)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					view := contract.MapView{ObservationRevision: "synthetic-revision", ImageID: "synthetic-image", ImageVersion: "1", PNG: encoded.Bytes(), GeneratedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ReceivedAt: now, Zoom: 18, Width: 8, Height: 8, Attribution: "Synthetic"}
					if outcome == "effective" {
						view.ObservationRevision, view.OriginRevision, view.VehicleRevision = "synthetic-effective", "synthetic-phone", "synthetic-revision"
					}
					json.NewEncoder(w).Encode(view)
				default:
					t.Error("unexpected request")
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client := NewClient()
			client.BaseURL = server.URL
			client.HTTP = server.Client()
			screen := Screen{client: client, vault: vault, saved: saved, window: app.NewWindow("Location refresh")}
			screen.locationRefresh.wait = func(ctx context.Context) bool { return ctx.Err() == nil && polls.Load() < 4 }
			screen.startLocationRefresh(outcome != "resume")
			deadline := time.Now().Add(3 * time.Second)
			for screen.locationRefresh.busy.Load() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			wantPosts := int32(1)
			if outcome == "resume" {
				wantPosts = 0
			}
			if screen.locationRefresh.busy.Load() || posts.Load() != wantPosts || polls.Load() != 2 {
				t.Fatalf("refresh incomplete: posts=%d polls=%d", posts.Load(), polls.Load())
			}
			if mapReads.Load() != 2 {
				t.Fatalf("latest matching map was not fetched: reads=%d", mapReads.Load())
			}
			after, err := vault.Load()
			if err != nil || after.LocationRefreshID != "" {
				t.Fatal("completed request retained")
			}
			status := contract.LocationRefreshStatus{State: "ready", ObservationRevision: "new", ImageID: "same", ImageVersion: "1"}
			if refreshImageMatches(status, contract.MapView{ObservationRevision: "old", ImageID: "same", ImageVersion: "1"}) {
				t.Fatal("old cached image revision ended refresh")
			}
			if refreshObservationMatches(status, contract.MapView{ObservationRevision: "old", OriginRevision: "other"}) {
				t.Fatal("unrelated effective map ended refresh")
			}
			if refreshObservationMatches(status, contract.MapView{ObservationRevision: "effective", OriginRevision: "new", VehicleRevision: "old"}) {
				t.Fatal("phone-origin revision substituted for vehicle revision")
			}
		})
	}
}

func TestPendingMapPollReleasesVaultAndCancelsOnExit(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			app := test.NewApp()
			defer app.Quit()
			vault := Vault{Path: filepath.Join(t.TempDir(), "owner.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
			saved := SavedState{Session: Session{AccessHandle: "synthetic-access", RefreshHandle: "synthetic-refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}
			if err := vault.Save(saved); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var encoded bytes.Buffer
			png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 8, 8)))
			now := time.Now()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/map/view" || r.Method != "GET" {
					t.Error("unexpected map operation")
					w.WriteHeader(404)
					return
				}
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", "10")
					w.WriteHeader(http.StatusAccepted)
					return
				}
				json.NewEncoder(w).Encode(contract.MapView{ObservationRevision: "new", ImageID: "new", ImageVersion: "1", PNG: encoded.Bytes(), GeneratedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ReceivedAt: now, Width: 8, Height: 8, Attribution: "Synthetic"})
			}))
			defer server.Close()
			client := NewClient()
			client.BaseURL = server.URL
			client.HTTP = server.Client()
			old := image.NewNRGBA(image.Rect(0, 0, 8, 8))
			screen := Screen{client: client, vault: vault, saved: saved, home: &homeView{}, vehicleMapImage: old}
			waiting, release := make(chan struct{}), make(chan struct{})
			screen.locationRefresh.mapWait = func(ctx context.Context) bool {
				close(waiting)
				select {
				case <-ctx.Done():
					return false
				case <-release:
					return true
				}
			}
			screen.refreshCachedMap(context.Background())
			select {
			case <-waiting:
			case <-time.After(time.Second):
				t.Fatal("pending map did not poll")
			}
			if screen.vehicleMapImage != old {
				t.Fatal("pending response discarded current map")
			}
			lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := vault.WithLock(lockCtx, func() error { return nil }); err != nil {
				t.Fatal("map wait held vault", err)
			}
			// No canvas is needed to check the delivered image and revision.
			screen.home = nil
			if complete {
				close(release)
			} else {
				screen.pauseLocationRefresh()
			}
			deadline := time.Now().Add(2 * time.Second)
			for screen.locationRefresh.mapBusy.Load() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if screen.locationRefresh.mapBusy.Load() {
				t.Fatal("map poll did not finish")
			}
			if complete && (calls.Load() != 2 || screen.mapView.ObservationRevision != "new") {
				t.Fatal("pending map did not complete")
			}
			if !complete && calls.Load() != 1 {
				t.Fatal("map polled after foreground exit")
			}
		})
	}
}

func TestForegroundMapClockRequestsVisibleLiveEvenWithRotationLock(t *testing.T) {
	for _, trigger := range []string{"clock", "center"} {
		t.Run(trigger, func(t *testing.T) {
			app := test.NewApp()
			defer app.Quit()
			vault := Vault{Path: filepath.Join(t.TempDir(), "owner.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
			saved := SavedState{Session: Session{AccessHandle: "synthetic-access", RefreshHandle: "synthetic-refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}
			if err := vault.Save(saved); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/map/view" || r.Method != "GET" {
					t.Error("foreground map clock requested another operation")
				}
				calls.Add(1)
				w.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()
			client := NewClient()
			client.BaseURL = server.URL
			client.HTTP = server.Client()
			for _, state := range []string{"odd-tick", "background", "preview", "signed-out"} {
				s := Screen{client: client, vault: vault, saved: saved, home: &homeView{}}
				s.foreground.Store(state != "background")
				s.preview.Store(state == "preview")
				s.home.rotationLocked.Store(true)
				if state == "signed-out" {
					s.saved = SavedState{}
				}
				tick := uint64(2)
				if state == "odd-tick" {
					tick = 1
				}
				s.refreshMapOnClock(tick)
				if trigger == "center" && state != "odd-tick" {
					s.recenterMap()
				}
				if s.locationRefresh.mapBusy.Load() || calls.Load() != 0 {
					t.Fatal("map request started for", state)
				}
			}
			s := Screen{client: client, vault: vault, saved: saved, home: &homeView{}}
			s.foreground.Store(true)
			s.home.rotationLocked.Store(true)
			waiting, release := make(chan struct{}), make(chan struct{})
			s.locationRefresh.mapWait = func(ctx context.Context) bool {
				close(waiting)
				select {
				case <-ctx.Done():
					return false
				case <-release:
					return true
				}
			}
			if trigger == "center" {
				// An empty map must still fetch; otherwise Center cannot recover it.
				s.recenterMap()
			} else {
				s.refreshMapOnClock(2)
			}
			select {
			case <-waiting:
			case <-time.After(time.Second):
				t.Fatal("two-minute tick did not request map immediately")
			}
			if calls.Load() != 1 {
				t.Fatal("unexpected initial map reads")
			}
			s.recenterMap()
			if calls.Load() != 1 {
				t.Fatal("Center duplicated the pending map request")
			}
			lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := vault.WithLock(lockCtx, func() error { return nil }); err != nil {
				t.Fatal("foreground map wait held vault")
			}
			// Leaving the foreground after a pending response stops automatic requests.
			s.foreground.Store(false)
			close(release)
			deadline := time.Now().Add(time.Second)
			for s.locationRefresh.mapBusy.Load() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if s.locationRefresh.mapBusy.Load() || calls.Load() != 1 {
				t.Fatal("automatic map polling continued in background")
			}
		})
	}
}

func TestAppRangeInputSeparatesValueAndFreshness(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	window := app.NewWindow("Range freshness")
	s := Screen{app: app, window: window, client: NewClient(), status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), signIn: newTool("Sign in", nil, nil), refresh: newTool("Refresh", nil, nil), logout: newTool("Logout", nil, nil), cancelButton: newTool("Cancel", nil, nil), retryCommand: newTool("Retry", nil, nil)}
	s.buildLayout("fixture")
	s.homeTelemetry.Range = TelemetryReading{Available: true, Value: 310, Unit: "mi", Freshness: "stale"}
	s.paintHome()
	in := s.sceneInput(time.Now())
	if in.Range != "310 Miles" || in.RangeFreshness != "stale" {
		t.Fatalf("range=%q freshness=%q", in.Range, in.RangeFreshness)
	}
	s.preview.Store(true)
	s.paintHome()
	if got := s.sceneInput(time.Now()).RangeFreshness; got != "" {
		t.Fatal("preview inherited live freshness", got)
	}
}

func TestExpandedCommandOptionsAndReadRoutes(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/commands":
			var body struct {
				Action    string                  `json:"action"`
				RequestID string                  `json:"requestId"`
				Options   contract.CommandOptions `json:"options"`
			}
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if e := decoder.Decode(&body); e != nil {
				t.Error(e)
			}
			if body.Action != "horn-and-lights" || body.Options.Duration == nil || *body.Options.Duration != 15 || len(body.Options.Actions) != 1 || body.Options.Actions[0] != "Honk" {
				t.Error("command options lost")
			}
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(CommandResult{Action: body.Action, State: "queued"})
		case "/api/vehicle/read/status":
			if r.Method != "GET" || r.URL.Query().Get("featureId") != "diagnostics" {
				t.Error("cached read route wrong")
			}
			json.NewEncoder(w).Encode(readEnvelope{Snapshot: &contract.VehicleSnapshot{FeatureID: "diagnostics", State: "partial", Freshness: "unknown"}})
		case "/api/vehicle/read":
			if r.Method != "POST" {
				t.Error("refresh must be explicit POST")
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["featureId"] != "diagnostics" || body["requestId"] == "" {
				t.Error("read request lacks feature/id")
			}
			json.NewEncoder(w).Encode(readEnvelope{Snapshot: &contract.VehicleSnapshot{FeatureID: "diagnostics", State: "available", Freshness: "unknown"}})
		default:
			t.Error("unexpected API route")
		}
	}))
	defer server.Close()
	c := NewClient()
	c.BaseURL = server.URL
	session := Session{AccessHandle: "owner"}
	duration := 15
	if _, e := c.SubmitCommand(context.Background(), session, CommandIntent{Action: "horn-and-lights", RequestID: "stable", CreatedAt: time.Now(), Options: contract.CommandOptions{Duration: &duration, Actions: []string{"Honk"}}}); e != nil {
		t.Fatal(e)
	}
	for _, refresh := range []bool{false, true} {
		snapshot, e := c.VehicleRead(context.Background(), session, "diagnostics", refresh)
		if e != nil || snapshot.FeatureID != "diagnostics" {
			t.Fatal("read contract failed", e)
		}
	}
	if requests != 3 {
		t.Fatal("read or command retried unexpectedly")
	}
}

type asynchronousNativeWindow struct{}

func (asynchronousNativeWindow) RunNative(call func(any)) {
	done := make(chan struct{})
	go func() { defer close(done); call(nil) }()
	<-done
}
func TestNativeCallbackFailureIsContainedOnItsOwnGoroutine(t *testing.T) {
	if nativeResult(asynchronousNativeWindow{}, func(any) bool { panic("synthetic native exception") }) {
		t.Fatal("failed native call reported success")
	}
	if !nativeResult(asynchronousNativeWindow{}, func(any) bool { return true }) {
		t.Fatal("successful native call was suppressed")
	}
}

func TestRotationVisualTilesToggleIndependentDraft(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	original := appearance.Default()
	draft := original.Clone()
	s := Screen{app: app}
	editor := s.rotationEditor(&draft).(*fyne.Container)
	sections := editor.Objects[1].(*widget.Accordion)
	backgroundSection := sections.Items[0].Detail.(*fyne.Container)
	backgroundGallery, ok := backgroundSection.Objects[1].(*fyne.Container)
	if !ok {
		t.Fatal("background selection is not a visual tile gallery")
	}
	backgroundGrid := backgroundGallery.Objects[0].(*fyne.Container)
	if len(backgroundGrid.Objects) != 4 {
		t.Fatal("background gallery must show four paged tiles")
	}
	backgroundGrid.Resize(fyne.NewSize(320, 216))
	if backgroundGrid.Objects[1].Position().X <= backgroundGrid.Objects[0].Position().X || backgroundGrid.Objects[2].Position().Y <= backgroundGrid.Objects[0].Position().Y || backgroundGrid.Objects[2].Position().X != backgroundGrid.Objects[0].Position().X {
		t.Fatal("background choices are not two columns")
	}
	first := appearancecore.Backdrops()[0].ID
	before := hasID(draft.BackgroundRotation.Selected, first)
	test.Tap(backgroundGrid.Objects[0].(fyne.Tappable))
	if hasID(draft.BackgroundRotation.Selected, first) == before || hasID(original.BackgroundRotation.Selected, first) != before {
		t.Fatal("background tile did not toggle only its draft")
	}
	label := backgroundGrid.Objects[0].(interface{ AccessibilityLabel() string }).AccessibilityLabel()
	if !strings.Contains(label, "Disabled") {
		t.Fatal("tile has no explicit disabled selection label")
	}
	test.Tap(backgroundGrid.Objects[0].(fyne.Tappable))
	if hasID(draft.BackgroundRotation.Selected, first) != before {
		t.Fatal("second background tap did not restore selection")
	}
	pager := backgroundGallery.Objects[1].(*fyne.Container)
	test.Tap(pager.Objects[2].(*widget.Button))
	nextID := appearancecore.Backdrops()[4].ID
	test.Tap(backgroundGrid.Objects[0].(fyne.Tappable))
	if !hasID(draft.BackgroundRotation.Selected, nextID) {
		t.Fatal("paged background tile toggled wrong item")
	}
	buttons := sections.Items[1].Detail.(*fyne.Container)
	lists := buttons.Objects[1].(*widget.Accordion)
	normalGallery := lists.Items[0].Detail.(*fyne.Container)
	normalGrid := normalGallery.Objects[0].(*fyne.Container)
	if len(normalGrid.Objects) != 4 {
		t.Fatal("button rotation lacks bounded visual tile gallery")
	}
	normalGrid.Resize(fyne.NewSize(320, 160))
	if normalGrid.Objects[1].Position().X <= normalGrid.Objects[0].Position().X || normalGrid.Objects[2].Position().Y <= normalGrid.Objects[0].Position().Y {
		t.Fatal("button rotation choices are not two columns")
	}
	normal := appearance.Styles("")[0].ID
	normalBefore := hasID(draft.ButtonRotation.Selected, normal)
	test.Tap(normalGrid.Objects[0].(fyne.Tappable))
	if hasID(draft.ButtonRotation.Selected, normal) == normalBefore || !hasID(draft.BackgroundRotation.Selected, nextID) {
		t.Fatal("normal button selection is not independent")
	}
	holiday := appearance.Holidays()[0].ID
	holidayGallery := lists.Items[1].Detail.(*fyne.Container)
	holidayGrid := holidayGallery.Objects[0].(*fyne.Container)
	test.Tap(holidayGrid.Objects[0].(fyne.Tappable))
	if hasID(draft.HolidayRotation[holiday], appearance.Styles(holiday)[0].ID) || hasID(draft.ButtonRotation.Selected, normal) == normalBefore {
		t.Fatal("holiday button list did not toggle independently")
	}
}
