package nativewidgets

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/homescene"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/mobile"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/ownerclient"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/widgetdisplay"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPhoneTrackingFiltersMovementCadenceAndReleasesVault(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	base := ownerclient.PhoneSample{ObservedAt: now, AccuracyMeters: 5}
	offset := func(metres float64, at time.Time) ownerclient.PhoneSample {
		sample := base
		sample.Latitude = metres / 6371008.8 * 180 / math.Pi
		sample.ObservedAt = at
		return sample
	}
	previous := ownerclient.PhoneTrackingState{Sample: base, UploadedAt: now, AttemptedAt: now}
	if ownerclient.PhoneUploadDue(previous, offset(100, now.Add(time.Minute)), now.Add(time.Minute)) {
		t.Fatal("uploaded before two minutes")
	}
	if ownerclient.PhoneUploadDue(previous, offset(15, now.Add(2*time.Minute)), now.Add(2*time.Minute)) {
		t.Fatal("uploaded within fifty feet")
	}
	if !ownerclient.PhoneUploadDue(previous, offset(16, now.Add(2*time.Minute)), now.Add(2*time.Minute)) {
		t.Fatal("movement over fifty feet ignored")
	}
	bad := offset(100, now.Add(2*time.Minute))
	bad.AccuracyMeters = 100
	if ownerclient.PhoneUploadDue(previous, bad, now.Add(2*time.Minute)) {
		t.Fatal("imprecise location accepted")
	}
	vault := ownerclient.Vault{Path: filepath.Join(t.TempDir(), "owner.enc"), Crypt: func(data []byte, _ bool) ([]byte, error) { return append([]byte(nil), data...), nil }}
	saved := ownerclient.SavedState{Session: ownerclient.Session{AccessHandle: "synthetic-access", RefreshHandle: "synthetic-refresh", AccessExpiresAt: now.Add(time.Hour)}}
	if err := vault.Save(saved); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/location/phone" || r.Method != "POST" {
			t.Error("wrong tracking route")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := vault.WithLock(ctx, func() error { return nil }); err != nil {
			t.Error("upload held shared vault lock")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"accepted":true}`)
	}))
	defer server.Close()
	client := ownerclient.NewClient()
	client.BaseURL = server.URL
	client.HTTP = server.Client()
	if accepted, err := ownerclient.UploadPhone(context.Background(), vault, client, base, now); err != nil || !accepted {
		t.Fatal("initial upload failed", err)
	}
	if accepted, err := ownerclient.UploadPhone(context.Background(), vault, client, offset(100, now.Add(time.Minute)), now.Add(time.Minute)); err != nil || accepted {
		t.Fatal("too-soon upload", err)
	}
	if accepted, err := ownerclient.UploadPhone(context.Background(), vault, client, offset(100, now.Add(2*time.Minute)), now.Add(2*time.Minute)); err != nil || !accepted {
		t.Fatal("moving upload failed", err)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected network request count")
	}
	restored, err := vault.Load()
	if err != nil || !restored.PhoneTracking.UploadedAt.Equal(now.Add(2*time.Minute)) {
		t.Fatal("restart watermark not saved", err)
	}
	if err = vault.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err = ownerclient.UploadPhone(context.Background(), vault, client, offset(200, now.Add(4*time.Minute)), now.Add(4*time.Minute)); err != ownerclient.ErrUnauthorized {
		t.Fatal("signedout upload accepted", err)
	}
	if calls.Load() != 2 {
		t.Fatal("signedout tracking contacted server")
	}
}

func TestWidgetMapFetchReleasesVaultAndRejectsSignout(t *testing.T) {
	for _, signout := range []bool{false, true} {
		t.Run(fmt.Sprint(signout), func(t *testing.T) {
			root := t.TempDir()
			vault := ownerclient.Vault{Path: filepath.Join(root, "owner.enc"), Crypt: func(data []byte, _ bool) ([]byte, error) { return append([]byte(nil), data...), nil }}
			now := time.Now()
			saved := ownerclient.SavedState{Session: ownerclient.Session{AccessHandle: "synthetic-access", RefreshHandle: "synthetic-refresh", AccessExpiresAt: now.Add(time.Hour)}}
			if err := vault.Save(saved); err != nil {
				t.Fatal(err)
			}
			var pngData bytes.Buffer
			png.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 8, 8)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/features" {
					fmt.Fprint(w, `{"availability":[]}`)
					return
				}
				if r.URL.Path != "/api/map/view" {
					t.Error("unexpected request")
					w.WriteHeader(404)
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := vault.WithLock(ctx, func() error {
					if signout {
						return vault.Clear()
					}
					return nil
				}); err != nil {
					t.Error("map image fetch holds shared vault", err)
				}
				json.NewEncoder(w).Encode(contract.MapView{ImageID: "synthetic", ImageVersion: "1", PNG: pngData.Bytes(), Width: 8, Height: 8, GeneratedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Attribution: "Synthetic"})
			}))
			defer server.Close()
			client := ownerclient.NewClient()
			client.BaseURL = server.URL
			client.HTTP = server.Client()
			state := readState(context.Background(), root, vault, client, true)
			if signout {
				if state.signedIn || state.mapImage != nil {
					t.Fatal("map survived concurrent signout")
				}
			} else if !state.signedIn || state.mapImage == nil {
				t.Fatal("authorized map missing")
			}
		})
	}
}

func TestSharedSceneFullParityCompactOmissionAndHitRegions(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, state := range []string{"signed-out", "enabled", "disabled", "pending", "unknown", "stale", "preview", "christmas", "custom-colors"} {
		for _, width := range []int{180, 360, 720} {
			for _, fontScale := range []float64{1, 1.25} {
				t.Run(fmt.Sprintf("%s/%d/%.2f", state, width, fontScale), func(t *testing.T) {
					settings := appearancecore.Default()
					percent := 62.0
					input := homescene.Input{Settings: settings, Now: now, Status: state, FontScale: fontScale, Range: "310 Miles", Fuel: "62%", FuelPercent: &percent, Odometer: "12,480 Miles", HighLow: "80° / 64°", Temperature: "75°", Weather: "Clear daylight"}
					fuel := 62.0
					input.FuelPercent = &fuel
					if state == "enabled" {
						input.Allowed = map[string]bool{"start": true, "unlock": true, "lock": true}
					}
					if state == "signed-out" {
						input.Range = "Range --"
						input.Fuel = "Fuel unknown"
						input.Odometer = "Mileage --"
						input.HighLow = "--° / --°"
						input.FuelPercent = nil
						input.Temperature = "--°"
						input.Weather = ""
						input.FuelPercent = nil
					}
					if state == "christmas" {
						input.Settings.Mode = appearancecore.Automatic
						input.Now = time.Date(2026, 12, 25, 12, 0, 0, 0, time.UTC)
					}
					if state == "preview" {
						input.Status = "PREVIEW · fictional readings"
					}
					if state == "stale" {
						input.Fuel += " · stale"
						input.FuelFreshness = "stale"
						input.RangeFreshness = "stale"
					}
					if state == "custom-colors" {
						input.Settings.Background = "#112233"
					}
					full, err := renderScene(input, width, false)
					if err != nil {
						t.Fatal(err)
					}
					app := mobile.AppSceneFrame(input, width, false)
					assembled := decodeNativeScene(t, full)
					if !bytes.Equal(assembled.Pix, app.Image.Pix) || !reflect.DeepEqual(full.Regions, app.Regions) {
						t.Fatal("full adapter pixels or hit geometry differ")
					}
					compact, err := renderScene(input, width, true)
					if err != nil {
						t.Fatal(err)
					}
					appCompact := mobile.AppSceneFrame(input, width, true)
					joined := decodeNativeScene(t, compact)
					if !bytes.Equal(joined.Pix, appCompact.Image.Pix) || !reflect.DeepEqual(compact.Regions, appCompact.Regions) {
						t.Fatal("compact adapter pixels or hit geometry differ")
					}
					for _, name := range []string{"map", "recenter", "settings"} {
						if _, ok := compact.Regions[name]; ok {
							t.Fatal("compact retained map or footer")
						}
					}
					for y := 0; y < joined.Bounds().Dy(); y++ {
						if !bytes.Equal(joined.Pix[y*joined.Stride:y*joined.Stride+joined.Bounds().Dx()*4], assembled.Pix[y*assembled.Stride:y*assembled.Stride+joined.Bounds().Dx()*4]) {
							t.Fatal("compact changed retained scene pixels")
						}
					}
					for name, region := range full.Regions {
						if region.Empty() || !region.In(full.Bounds) {
							t.Fatalf("hit region %s escaped bounds", name)
						}
						if name == "start" || name == "unlock" || name == "lock" {
							if compact.Regions[name] != region {
								t.Fatal("compact command geometry changed")
							}
						}
					}
					if dir := os.Getenv("SUBURBAN_PARITY_GALLERY_DIR"); dir != "" && width == 360 && fontScale == 1 {
						writeParityGallery(t, dir, state, app.Image, assembled, joined)
					}
				})
			}
		}
	}
}

func TestTallPortraitUsesSharedMapExtension(t *testing.T) {
	in := homescene.Input{Settings: appearancecore.Default(), Now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), HighLow: "80° / 64°", Temperature: "75°"}
	app := mobile.AppSceneFrameSize(in, 360, 720, false)
	native, err := renderSceneSize(in, 360, 720, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := decodeNativeScene(t, native)
	if !bytes.Equal(app.Image.Pix, joined.Pix) || !reflect.DeepEqual(app.Regions, native.Regions) {
		t.Fatal("tall app/widget layout differs")
	}
	if app.Image.Bounds().Dy() != 720 || app.Regions["map"].Min.Y != 349 || app.Regions["settings"].Max.Y < 714 || app.Regions["settings"].Max.Y > 716 {
		t.Fatal("tall screen does not preserve scene and bottom footer")
	}
}

func decodeNativeScene(t *testing.T, scene nativeScene) *image.NRGBA {
	t.Helper()
	decoded, err := png.Decode(bytes.NewReader(scene.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds() != scene.Bounds {
		t.Fatal("native bitmap bounds differ from hit coordinate space")
	}
	image := image.NewNRGBA(scene.Bounds)
	draw.Draw(image, image.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	return image
}

func TestNativeWidgetRootDoesNotPaintOutsideSharedScene(t *testing.T) {
	for _, kind := range []string{"compact", "full"} {
		data, err := os.ReadFile("res/layout/suburban_widget_" + kind + ".xml")
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(strings.NewReader(string(data)))
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		root, ok := token.(xml.StartElement)
		if !ok {
			t.Fatal("missing layout root")
		}
		background := ""
		for _, a := range root.Attr {
			if a.Name.Local == "background" {
				background = a.Value
			}
		}
		if background != "@android:color/transparent" {
			t.Errorf("%s widget paints %q outside shared scene", kind, background)
		}
	}
}

func TestNativeRangeInputSeparatesValueAndFreshness(t *testing.T) {
	now := time.Now()
	expired := now.Add(-time.Second)
	for _, tc := range []struct {
		name                    string
		available               bool
		expiry                  *time.Time
		wantText, wantFreshness string
	}{
		{"stale", true, nil, "310 Miles", "stale"},
		{"missing", false, nil, "Range --", ""},
		{"expired", true, &expired, "Range --", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := viewState{signedIn: true, settings: appearancecore.Default(), status: "Ready", display: widgetdisplay.Snapshot{SchemaVersion: 1, GeneratedAt: now, Range: widgetdisplay.Reading{Text: "310 Miles", Available: tc.available, Freshness: "stale", ExpiresAt: tc.expiry}}}
			input := sceneInput(state, now, 1)
			if input.Range != tc.wantText || input.RangeFreshness != tc.wantFreshness {
				t.Fatalf("range=%q freshness=%q", input.Range, input.RangeFreshness)
			}
		})
	}
}

func writeParityGallery(t *testing.T, dir, state string, app, full, compact image.Image) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, 1080, 680))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.NRGBA{11, 15, 25, 255}), image.Point{}, draw.Src)
	for i, img := range []image.Image{app, full, compact} {
		draw.Draw(canvas, image.Rect(i*360, 40, (i+1)*360, 40+img.Bounds().Dy()), img, img.Bounds().Min, draw.Src)
	}
	label := font.Drawer{Dst: canvas, Src: image.White, Face: basicfont.Face7x13}
	for i, text := range []string{"FIXTURE app - " + state, "FIXTURE full widget - " + state, "FIXTURE compact widget - " + state} {
		label.Dot = fixed.P(i*360+8, 22)
		label.DrawString(text)
	}
	file, err := os.Create(filepath.Join(dir, state+"-app-full-compact.png"))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(file, canvas)
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestColdPresentationRefreshDoesNotClaimSignInIsRequired(t *testing.T) {
	latestView.Lock()
	previous := latestView.state
	latestView.state = viewState{}
	latestView.Unlock()
	t.Cleanup(func() { latestView.Lock(); latestView.state = previous; latestView.Unlock() })
	root := t.TempDir()
	vault := ownerclient.Vault{Path: filepath.Join(root, "owner-session.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
	saved := ownerclient.SavedState{Session: ownerclient.Session{AccessHandle: "persisted-owner", RefreshHandle: "persisted-refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}
	if err := vault.Save(saved); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	client := ownerclient.NewClient()
	client.BaseURL = server.URL
	view := readState(context.Background(), root, vault, client, false)
	if view.status != "Tap to refresh" {
		t.Fatalf("cold cache status=%q", view.status)
	}
	if view.signedIn || view.mapImage != nil || len(view.allowed) != 0 || requests.Load() != 0 {
		t.Fatal("cold refresh invented validation, exposed a map or used network")
	}
	retained, err := vault.Load()
	if err != nil || retained.Session.AccessHandle != saved.Session.AccessHandle || retained.Session.RefreshHandle != saved.Session.RefreshHandle || !retained.Session.AccessExpiresAt.Equal(saved.Session.AccessExpiresAt) {
		t.Fatal("cold refresh changed persisted session")
	}
	if err := vault.Clear(); err != nil {
		t.Fatal(err)
	}
	view = readState(context.Background(), root, vault, client, false)
	if view.status != "Open Suburbanista to sign in" || requests.Load() != 0 {
		t.Fatal("missing session should still require sign in without network")
	}
}

func TestNativeAppearanceArrowsPersistSelectedListWithoutCommands(t *testing.T) {
	root := t.TempDir()
	settings := appearancecore.Default()
	settings.BackgroundRotation.Selected = []string{"charlotte", "beach", "mountains"}
	settings.ButtonRotation.Enabled = true
	styles := appearancecore.Styles("")
	settings.ButtonRotation.Selected = []string{styles[0].ID, styles[1].ID}
	settings.NormalStyle = styles[0].ID
	settings.Mode = appearancecore.Normal
	if err := appearancecore.SaveShared(context.Background(), root, settings); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, entry := range []struct{ action, backdrop, style string }{{"BACKGROUND_NEXT", "beach", styles[0].ID}, {"BACKGROUND_PREVIOUS", "charlotte", styles[0].ID}, {"STYLE_NEXT", "charlotte", styles[1].ID}, {"STYLE_PREVIOUS", "charlotte", styles[0].ID}} {
		action := actionPrefix + entry.action
		if needsNetwork(action) {
			t.Fatal("appearance navigation requested network")
		}
		if _, command := commandFor(action); command {
			t.Fatal("appearance navigation became vehicle command")
		}
		if err := navigateAppearance(context.Background(), root, action, now); err != nil {
			t.Fatal(err)
		}
		stored, err := appearancecore.LoadShared(root)
		if err != nil || stored.Backdrop != entry.backdrop || stored.NormalStyle != entry.style {
			t.Fatal("native arrow did not persist selected-list navigation")
		}
	}
}

func TestAppearanceRefreshUsesCacheAndAuthoritativePendingState(t *testing.T) {
	root := t.TempDir()
	vault := ownerclient.Vault{Path: filepath.Join(root, "owner-session.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
	saved := ownerclient.SavedState{Session: ownerclient.Session{AccessHandle: "synthetic-owner", RefreshHandle: "synthetic-refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}
	if err := vault.Save(saved); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var mapPending atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" {
			t.Error("widget refresh submitted an action")
		}
		switch r.URL.Path {
		case "/api/features":
			_ = json.NewEncoder(w).Encode(contract.FeaturesResponse{Availability: []contract.FeatureAvailability{{ID: "lock", Available: true}}})
		case "/api/map/view":
			if mapPending.Load() {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			var encoded bytes.Buffer
			_ = png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2)))
			_ = json.NewEncoder(w).Encode(contract.MapView{PNG: encoded.Bytes(), Width: 2, Height: 2, ImageID: "synthetic-image", ImageVersion: "synthetic-version", Attribution: "Synthetic fixture", GeneratedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)})
		default:
			t.Error("unexpected route")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := ownerclient.NewClient()
	client.BaseURL = server.URL
	first := readState(context.Background(), root, vault, client, true)
	if first.status != "Ready" {
		t.Errorf("widget-only ready text=%q", first.status)
	}
	if !first.signedIn || first.mapImage == nil || !first.allowed["lock"] || calls.Load() != 2 {
		t.Fatalf("initial snapshot not authorized: calls=%d", calls.Load())
	}
	mapPending.Store(true)
	pending := readState(context.Background(), root, vault, client, true)
	if !pending.signedIn || pending.mapImage != first.mapImage || pending.status != "Map preparing · tap to refresh" || calls.Load() != 4 {
		t.Fatal("pending map lost cached pixels or refresh feedback")
	}
	saved.Command = &ownerclient.CommandIntent{Action: "lock", RequestID: "existing-request"}
	if err := vault.Save(saved); err != nil {
		t.Fatal(err)
	}
	next := readState(context.Background(), root, vault, client, false)
	if calls.Load() != 4 || !next.signedIn || next.mapImage == nil || next.allowed["lock"] || next.status != "Command pending · check in app" {
		t.Fatal("style refresh used network or lost pending state")
	}
	if err := vault.Clear(); err != nil {
		t.Fatal(err)
	}
	signedOut := readState(context.Background(), root, vault, client, false)
	if signedOut.signedIn || signedOut.mapImage != nil || len(signedOut.allowed) != 0 || calls.Load() != 4 {
		t.Fatal("signed-out widget retained private map")
	}
}

func TestRevokedAndOfflineUpdatesClearMapAndControls(t *testing.T) {
	for _, status := range []int{401, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			root := t.TempDir()
			vault := ownerclient.Vault{Path: filepath.Join(root, "owner-session.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
			if err := vault.Save(ownerclient.SavedState{Session: ownerclient.Session{AccessHandle: "synthetic", RefreshHandle: "synthetic-refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			client := ownerclient.NewClient()
			client.BaseURL = server.URL
			view := readState(context.Background(), root, vault, client, true)
			if view.signedIn || view.mapImage != nil || len(view.allowed) != 0 {
				t.Fatal("failed authorization or unavailable service exposed cached data")
			}
		})
	}
}

func TestNativeDispatchPathPersistsExactIntentAndNeverRetriesUnknown(t *testing.T) {
	for _, action := range []string{"START", "UNLOCK", "LOCK"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			vault := ownerclient.Vault{Path: filepath.Join(root, "owner-session.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
			if err := vault.Save(ownerclient.SavedState{Session: ownerclient.Session{AccessHandle: "widget-synthetic", RefreshHandle: "widget-refresh", AccessExpiresAt: time.Now().Add(time.Hour)}}); err != nil {
				t.Fatal(err)
			}
			command, _ := commandFor(actionPrefix + action)
			var submits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/features":
					_ = json.NewEncoder(w).Encode(contract.FeaturesResponse{Availability: []contract.FeatureAvailability{{ID: command, Available: true}}})
				case "/api/commands":
					submits.Add(1)
					var intent ownerclient.CommandIntent
					_ = json.NewDecoder(r.Body).Decode(&intent)
					saved, err := vault.Load()
					if err != nil || saved.Command == nil || saved.Command.Action != command || saved.Command.RequestID != intent.RequestID {
						t.Error("native callback did not persist exact intent")
					}
					w.WriteHeader(http.StatusServiceUnavailable)
				default:
					t.Error("unexpected native command route")
				}
			}))
			defer server.Close()
			client := ownerclient.NewClient()
			client.BaseURL = server.URL
			if _, err := dispatchControl(context.Background(), vault, client, actionPrefix+action); err == nil {
				t.Fatal("unknown native submission claimed success")
			}
			before, _ := vault.Load()
			if _, err := dispatchControl(context.Background(), vault, client, actionPrefix+action); err == nil {
				t.Fatal("native tap retried pending intent")
			}
			after, _ := vault.Load()
			if submits.Load() != 1 || before.Command == nil || after.Command == nil || before.Command.RequestID != after.Command.RequestID {
				t.Fatal("native uncertain request was repeated or changed")
			}
		})
	}
	for _, action := range []string{actionPrefix + "REFRESH", actionPrefix + "SYNC", "android.appwidget.action.APPWIDGET_UPDATE", actionPrefix + "EV_CHARGE", actionPrefix + "ATTRIBUTION"} {
		if _, err := dispatchControl(context.Background(), ownerclient.Vault{}, nil, action); err == nil {
			t.Fatalf("display or EV event reached command client: %s", action)
		}
	}
}

func TestWidgetChecksStoredOutcomeWithoutSubmittingAgain(t *testing.T) {
	for _, outcome := range []string{"failed", "outcome-unknown"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			vault := ownerclient.Vault{Path: filepath.Join(root, "owner-session.enc"), Crypt: func(b []byte, _ bool) ([]byte, error) { return append([]byte(nil), b...), nil }}
			intent := ownerclient.CommandIntent{Action: "lock", RequestID: "same-pending-request"}
			saved := ownerclient.SavedState{Session: ownerclient.Session{AccessHandle: "synthetic", RefreshHandle: "refresh", AccessExpiresAt: time.Now().Add(time.Hour)}, Command: &intent}
			saved.Verifications = ownerclient.AppendVerification(nil, intent)
			if err := vault.Save(saved); err != nil {
				t.Fatal(err)
			}
			var statusReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("status check attempted a submission")
				}
				switch r.URL.Path {
				case "/api/features":
					_ = json.NewEncoder(w).Encode(contract.FeaturesResponse{Availability: []contract.FeatureAvailability{{ID: "lock", Available: true}}})
				case "/api/commands/status":
					statusReads.Add(1)
					if r.URL.Query().Get("requestId") != intent.RequestID {
						t.Error("status ID changed")
					}
					_ = json.NewEncoder(w).Encode(ownerclient.CommandResult{Action: "lock", State: outcome, UpdatedAt: 1})
				case "/api/map/view":
					w.WriteHeader(404)
				default:
					t.Error("unexpected route")
				}
			}))
			defer server.Close()
			client := ownerclient.NewClient()
			client.BaseURL = server.URL
			view := readState(context.Background(), root, vault, client, true)
			persisted, err := vault.Load()
			if err != nil {
				t.Fatal(err)
			}
			if statusReads.Load() != 1 || view.status != commandLabel(outcome) {
				t.Fatal("stored outcome was not shown")
			}
			if outcome == "failed" && (persisted.Command != nil || !view.allowed["lock"]) {
				t.Fatal("definitive failure left controls pending")
			}
			if outcome == "outcome-unknown" && (persisted.Command == nil || view.allowed["lock"]) {
				t.Fatal("unknown outcome allowed fresh actuation")
			}
		})
	}
}
func TestWidgetFooterPrefersMapObservation(t *testing.T) {
	now := time.Date(2026, 10, 25, 10, 45, 31, 0, time.FixedZone("phone-zone", -4*60*60))
	observed := now.Add(-time.Hour)
	state := viewState{signedIn: true, mapImage: image.NewNRGBA(image.Rect(0, 0, 360, 246)), mapView: contract.MapView{ExpiresAt: now.Add(time.Hour), ReceivedAt: now, GeneratedAt: now.Add(-time.Minute)}}
	if sceneInput(state, now, 1).Updated != "Last Updated · source time unknown" {
		t.Fatal("delivery time used as observation")
	}
	state.mapView.ObservedAt = observed
	if sceneInput(state, now, 1).Updated != "Last Updated · 9:45 AM" {
		t.Fatal("map observation ignored")
	}
	state.display = widgetdisplay.Snapshot{SchemaVersion: 1, GeneratedAt: now, Fuel: widgetdisplay.Reading{Text: "62%", Available: true, Freshness: "stale", ObservedAt: &observed}, Odometer: widgetdisplay.Reading{Text: "310 mi", Available: true, Freshness: "stale", ObservedAt: &observed}}
	in := sceneInput(state, now, 1)
	if strings.Contains(in.Fuel, "stale") || strings.Contains(in.Odometer, "stale") || in.FuelFreshness != "stale" {
		t.Fatal("widget exposed stale labels or lost freshness metadata")
	}
}
