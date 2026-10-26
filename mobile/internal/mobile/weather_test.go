package mobile

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"fmt"
	"fyne.io/fyne/v2/test"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWeatherDeniedOrStaleFixMakesNoRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := &WeatherClient{HTTP: server.Client(), Endpoint: server.URL}
	now := time.Now()
	for _, fix := range []PhoneFix{{Allowed: false}, {Allowed: true, ObservedAt: now.Add(-31 * time.Minute)}, {Allowed: true, ObservedAt: now.Add(5 * time.Minute)}} {
		if _, e := client.Fetch(context.Background(), fix, now); e == nil {
			t.Fatal("invalid phone fix accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid phone location reached weather provider")
	}
}

func TestForegroundLocationRefreshesStaleCacheAndStopsRequest(t *testing.T) {
	now := time.Now()
	reads, starts, stops := 0, 0, 0
	source := phoneLocationSource{
		read: func(time.Time) (PhoneFix, string) {
			reads++
			if reads == 1 {
				return PhoneFix{}, "Phone location needs an update"
			}
			return PhoneFix{Allowed: true, ObservedAt: now}, "ready"
		},
		start: func(context.Context) (func(), string) { starts++; return func() { stops++ }, "" },
	}
	fix, state := acquirePhoneLocation(context.Background(), source, func() time.Time { return now }, func(context.Context) bool { return true })
	if state != "ready" || !fix.usable(now) || starts != 1 || stops != 1 {
		t.Fatalf("stale acquisition did not refresh/clean up: state=%s starts=%d stops=%d", state, starts, stops)
	}
}

func TestForegroundLocationCancellationAndReadBound(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var reads, starts, stops atomic.Int32
			source := phoneLocationSource{read: func(time.Time) (PhoneFix, string) { reads.Add(1); return PhoneFix{}, "Phone location needs an update" }, start: func(requestCtx context.Context) (func(), string) {
				starts.Add(1)
				deadline, ok := requestCtx.Deadline()
				if !ok || time.Until(deadline) > 20*time.Second {
					t.Fatal("request lacks twenty-second bound")
				}
				return func() { stops.Add(1) }, ""
			}}
			_, state := acquirePhoneLocation(ctx, source, time.Now, func(context.Context) bool {
				if cancelled {
					cancel()
					return false
				}
				return true
			})
			if starts.Load() != 1 || stops.Load() != 1 || reads.Load() > 41 {
				t.Fatal("request cleanup or read bound failed")
			}
			if cancelled && state != "Weather paused" {
				t.Fatal("cancelled fix remained usable")
			}
		})
	}
}
func TestForegroundLocationDoesNotRequestWithoutEligibility(t *testing.T) {
	now := time.Now()
	for _, state := range []string{"ready", "Location permission needed", "Phone location is disabled", "Network location unavailable", "Phone location provider is simulated", "Phone location time is invalid"} {
		calls := 0
		_, got := acquirePhoneLocation(context.Background(), phoneLocationSource{read: func(time.Time) (PhoneFix, string) { return PhoneFix{Allowed: true, ObservedAt: now}, state }, start: func(context.Context) (func(), string) { calls++; return func() {}, "" }}, func() time.Time { return now }, func(context.Context) bool { return true })
		if got != state || calls != 0 {
			t.Fatalf("ineligible state requested location: %s", state)
		}
	}
}
func TestForegroundLocationLifetimeAndSingleFlight(t *testing.T) {
	var gate foregroundLocation
	if _, _, _, ok := gate.context(context.Background()); ok {
		t.Fatal("background has a weather lifetime")
	}
	gate.open()
	defer gate.close()
	ctx, session, done, ok := gate.context(context.Background())
	if !ok {
		t.Fatal("foreground unavailable")
	}
	defer done()
	now := time.Now()
	finish, ok := gate.begin(session, now, false)
	if !ok {
		t.Fatal("first request rejected")
	}
	if _, ok := gate.begin(session, now, true); ok {
		t.Fatal("overlapping request")
	}
	gate.close()
	if ctx.Err() == nil {
		t.Fatal("foreground cancellation was not synchronous")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("foreground exit did not cancel request")
	}
	gate.open()
	if _, _, _, ok := gate.context(context.Background()); ok {
		t.Fatal("new foreground overlapped old cleanup")
	}
	finish()
	done()
	_, newSession, newDone, _ := gate.context(context.Background())
	defer newDone()
	if _, ok := gate.begin(session, now, true); ok {
		t.Fatal("old session started new request")
	}
	finish, ok = gate.begin(newSession, now, false)
	if !ok {
		t.Fatal("new foreground cannot request after cleanup")
	}
	finish()
	if _, ok := gate.begin(newSession, now.Add(time.Minute), false); ok {
		t.Fatal("minute tick repeated the request")
	}
	finish, ok = gate.begin(newSession, now.Add(time.Minute), true)
	if !ok {
		t.Fatal("explicit retry rejected")
	}
	finish()
}

func TestPreviewCancelsWeatherWithoutBlockingVehicleGuard(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	window := app.NewWindow("Preview cancellation")
	s := &Screen{app: app, window: window, client: NewClient(), status: wrapped(""), detail: wrapped(""), commandStatus: wrapped(""), signIn: newTool("Sign in", nil, nil), refresh: newTool("Refresh", nil, nil), logout: newTool("Logout", nil, nil), cancelButton: newTool("Cancel", nil, nil), retryCommand: newTool("Retry", nil, nil)}
	window.SetContent(s.buildLayout("fixture"))
	s.saved = SavedState{Session: Session{RefreshHandle: "synthetic-retained"}}
	s.phoneLocation.open()
	defer s.phoneLocation.close()
	ctx, _, done, ok := s.phoneLocation.context(context.Background())
	if !ok {
		t.Fatal("weather job unavailable")
	}
	defer done()
	if !s.busy.CompareAndSwap(false, true) {
		t.Fatal("weather occupied owner command guard")
	}
	s.busy.Store(false)
	s.switchMode(true)
	if !s.preview.Load() || s.saved.Session.RefreshHandle != "synthetic-retained" {
		t.Fatal("Preview changed owner session")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Preview did not cancel weather")
	}
	if _, _, _, ok := s.phoneLocation.context(context.Background()); ok {
		t.Fatal("cancelled job released before cleanup")
	}
	done()
	ctx, _, done, ok = s.phoneLocation.context(context.Background())
	if !ok || ctx.Err() != nil {
		t.Fatal("foreground cannot resume weather after Preview")
	}
	done()
}

func TestForegroundWeatherHTTPStopsOnExit(t *testing.T) {
	var gate foregroundLocation
	gate.open()
	defer gate.close()
	ctx, _, done, ok := gate.context(context.Background())
	if !ok {
		t.Fatal("foreground context missing")
	}
	defer done()
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(stopped) }))
	defer server.Close()
	client := WeatherClient{HTTP: server.Client(), Endpoint: server.URL}
	finished := make(chan error, 1)
	go func() {
		_, err := client.Fetch(ctx, PhoneFix{Allowed: true, ObservedAt: time.Now()}, time.Now())
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("weather request not started")
	}
	gate.close()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled weather returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("weather HTTP did not stop")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("weather provider request remained active")
	}
}
func TestPhoneFixFailureStatesArePrecise(t *testing.T) {
	now := time.Now()
	base := PhoneFix{Allowed: true, ObservedAt: now, AccuracyMeters: 50}
	if phoneFixState(base, now) != "ready" {
		t.Fatal("usable fix rejected")
	}
	base.AccuracyMeters = 10001
	if phoneFixState(base, now) != "Phone location is too imprecise" {
		t.Fatal("accuracy mislabeled stale")
	}
	base.AccuracyMeters = 50
	base.ObservedAt = now.Add(-31 * time.Minute)
	if phoneFixState(base, now) != "Phone location needs an update" {
		t.Fatal("stale age lost")
	}
	base.ObservedAt = now.Add(2 * time.Minute)
	if phoneFixState(base, now) != "Phone location time is invalid" {
		t.Fatal("future time mislabeled stale")
	}
}

func TestStartupPermissionsGrantDenialAndResume(t *testing.T) {
	known := func(name string, granted, rationale bool) permissionGrant {
		return permissionGrant{Name: name, Applicable: true, Known: true, Granted: granted, Rationale: rationale}
	}
	asked := map[string]bool{}
	previous := func(name string) bool { return asked[name] }
	var coordinator permissionCoordinator
	fresh := []permissionGrant{known(coarsePermission, false, false), known(notificationPermission, false, false)}
	request, recovery := coordinator.requests(fresh, previous, false)
	if len(request) != 2 || request[0] != coarsePermission || request[1] != notificationPermission || recovery {
		t.Fatal("fresh install did not order the two applicable requests")
	}
	for _, name := range request {
		asked[name] = true
	}
	denied := []permissionGrant{known(coarsePermission, false, true), known(notificationPermission, true, false)}
	if next, _ := coordinator.requests(denied, previous, false); len(next) != 0 {
		t.Fatal("dialog resume prompted again")
	}
	var restarted permissionCoordinator
	if next, recover := restarted.requests(denied, previous, false); len(next) != 1 || next[0] != coarsePermission || recover {
		t.Fatal("temporary denial suppressed request on next startup")
	}
	permanent := []permissionGrant{known(coarsePermission, false, false), known(notificationPermission, true, false)}
	if next, recover := restarted.requests(permanent, previous, true); len(next) != 0 || !recover {
		t.Fatal("permanent denial did not require Settings recovery")
	}
	if next, recover := restarted.requests([]permissionGrant{known(coarsePermission, true, false), known(notificationPermission, true, false)}, previous, false); len(next) != 0 || recover {
		t.Fatal("actual grants were replaced by stored denial")
	}
	legacy := []permissionGrant{known(coarsePermission, true, false), {Name: notificationPermission, Known: true, Applicable: false}}
	if next, _ := restarted.requests(legacy, previous, false); len(next) != 0 {
		t.Fatal("legacy Android requested runtime notifications")
	}
}

func TestStartupPermissionsDoNotTouchRetainedSession(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	window := app.NewWindow("permission fixture")
	s := &Screen{app: app, window: window}
	s.saved = SavedState{Session: Session{AccessHandle: "synthetic-access", RefreshHandle: "synthetic-refresh"}, Command: &CommandIntent{Action: "lock", RequestID: "retained-unknown"}}
	s.startupPermissionCheck(false)
	s.startupPermissionCheck(false)
	if s.saved.Session.RefreshHandle != "synthetic-refresh" || s.saved.Command == nil || s.saved.Command.RequestID != "retained-unknown" {
		t.Fatal("startup permission flow changed session or uncertain intent")
	}
	s.preview.Store(true)
	s.startupPermissionCheck(true)
	if len(s.startupPermissions.attempted) != 0 {
		t.Fatal("preview requested a permission")
	}
}

func TestWeatherUsesRoundedCellAndProviderTimestamps(t *testing.T) {
	now := time.Unix(1800000000, 0)
	unit := "°F"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("latitude") != "0.00" || r.URL.Query().Get("longitude") != "0.00" || r.URL.Query().Get("temperature_unit") != "fahrenheit" {
			t.Error("weather request did not minimize location or set units")
		}
		json.NewEncoder(w).Encode(map[string]any{"current": map[string]any{"time": now.Unix(), "temperature_2m": 75, "weather_code": 2, "cloud_cover": 30}, "current_units": map[string]string{"temperature_2m": unit}, "daily": map[string]any{"temperature_2m_max": []int{80}, "temperature_2m_min": []int{64}, "sunrise": []int64{now.Add(-6 * time.Hour).Unix()}, "sunset": []int64{now.Add(6 * time.Hour).Unix()}}})
	}))
	defer server.Close()
	c := &WeatherClient{HTTP: server.Client(), Endpoint: server.URL}
	fix := PhoneFix{Latitude: 0, Longitude: 0, Allowed: true, ObservedAt: now}
	weather, e := c.Fetch(context.Background(), fix, now)
	if e != nil || weather.Code != 2 || weather.TemperatureF != 75 || !weather.ObservedAt.Equal(now) {
		t.Fatal("weather contract failed")
	}
	unit = "°C"
	if _, e = c.Fetch(context.Background(), fix, now); e == nil {
		t.Fatal("wrong weather unit accepted")
	}
}
func TestWeatherRoundingPrecision(t *testing.T) {
	if approximateComponent(.004) != "0.00" || approximateComponent(.006) != "0.01" {
		t.Fatal("weather cell precision changed")
	}
}
func TestDaylightBlendIsContinuous(t *testing.T) {
	rise := time.Date(2026, 6, 1, 6, 0, 0, 0, time.UTC)
	set := rise.Add(12 * time.Hour)
	for _, boundary := range []time.Time{rise, set} {
		left, twLeft := daylightMix(boundary.Add(-time.Second), rise, set)
		right, twRight := daylightMix(boundary.Add(time.Second), rise, set)
		if math.Abs(left-right) > .001 || math.Abs(twLeft-twRight) > .001 {
			t.Fatal("sky transition has a hard edge")
		}
	}
	noon, _ := daylightMix(rise.Add(6*time.Hour), rise, set)
	night, _ := daylightMix(set.Add(5*time.Hour), rise, set)
	if noon != 1 || night != 0 {
		t.Fatal("day/night boundaries incorrect")
	}
}
func TestWeatherIconCacheSeparatesDayAndNight(t *testing.T) {
	day := weatherIcon(2, false)
	night := weatherIcon(2, true)
	if day.Name() == night.Name() {
		t.Fatal("day and night share a Fyne resource cache key")
	}
}
func TestTelemetryVerifiedBindingsConvertMilesAndPreserveSourceAge(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	snapshot := contract.VehicleSnapshot{FeatureID: "diagnostics", State: "available", Fields: []contract.SnapshotField{
		{Key: "diagnostic-value", SourceName: "FUEL_LEVEL", Value: "42", Unit: "%", ObservedAt: &at, Freshness: "stale"},
	}, Items: []contract.SnapshotItem{{Fields: []contract.SnapshotField{
		{Key: "diagnostic-value", SourceName: "FUEL_RANGE", Value: "160.9344", Unit: "KM", ObservedAt: &at, Freshness: "stale"},
		{Key: "diagnostic-value", SourceName: "ODO_READ", Value: "16093.44", Unit: "KM", Freshness: "unknown"},
	}}}}
	got := telemetryFromSnapshot(snapshot, verifiedTelemetryBindings)
	if !got.Fuel.Available || !got.Range.Available || !got.Odometer.Available {
		t.Fatal("verified provider readings remain unavailable")
	}
	if got.Fuel.Value != 42 || got.Fuel.Unit != "%" || math.Abs(got.Range.Value-100) > .000001 || got.Range.Unit != "mi" || math.Abs(got.Odometer.Value-10000) > .000001 || got.Odometer.Unit != "mi" {
		t.Fatal("verified readings have incorrect display units or conversion")
	}
	if got.Fuel.ObservedAt != &at || got.Range.ObservedAt != &at || got.Fuel.Freshness != "stale" || got.Range.Freshness != "stale" || got.Odometer.ObservedAt != nil || got.Odometer.Freshness != "unknown" {
		t.Fatal("provider source time or freshness changed")
	}
}

func TestTelemetryRequiresExactSourceNameAndUnit(t *testing.T) {
	binding := TelemetryBindings{FuelName: "SYNTHETIC_FUEL", FuelUnit: "%", OdometerName: "SYNTHETIC_ODO", OdometerUnit: "mi"}
	base := contract.VehicleSnapshot{FeatureID: "diagnostics", Fields: []contract.SnapshotField{{Key: "diagnostic-value", SourceName: "SYNTHETIC_FUEL", Value: "62", Unit: "%", Freshness: "unknown"}, {SourceName: "SYNTHETIC_ODO", Value: "12480", Unit: "mi", Freshness: "stale"}}}
	r := telemetryFromSnapshot(base, binding)
	if !r.Fuel.Available || r.Fuel.Value != 62 || !r.Odometer.Available || r.Odometer.ObservedAt != nil {
		t.Fatal("typed telemetry failed")
	}
	for _, kind := range []string{"unit", "name", "duplicate", "warranty", "not-number", "negative", "over-percent", "missing"} {
		s := base
		s.Fields = append([]contract.SnapshotField{}, base.Fields...)
		switch kind {
		case "unit":
			s.Fields[0].Unit = "L"
		case "name":
			s.Fields[0].SourceName = ""
			s.Fields[0].Label = "Fuel"
		case "duplicate":
			s.Fields = append(s.Fields, s.Fields[0])
		case "warranty":
			s.FeatureID = "warranty"
		case "not-number":
			s.Fields[0].Value = "NaN"
		case "negative":
			s.Fields[0].Value = "-1"
		case "over-percent":
			s.Fields[0].Value = "101"
		case "missing":
			s.Fields = s.Fields[1:]
		}
		if telemetryFromSnapshot(s, binding).Fuel.Available {
			t.Fatalf("ambiguous fuel accepted: %s", kind)
		}
	}
}
func TestMapUsesOwnerSessionAndOnlyCachedRoute(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/map.png" || r.URL.RawQuery != "" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+"synthetic-map-owner" {
			t.Error("map request changed scope")
		}
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, image.NewNRGBA(image.Rect(0, 0, 64, 32)))
	}))
	defer server.Close()
	c := Client{HTTP: server.Client(), BaseURL: server.URL}
	if _, e := c.MapImage(context.Background(), Session{}); e != ErrUnauthorized {
		t.Fatal("unsigned map accepted")
	}
	if calls != 0 {
		t.Fatal("unsigned request reached service")
	}
	img, err := c.MapImage(context.Background(), Session{AccessHandle: "synthetic-map-owner"})
	if err != nil || img.Bounds().Dx() != 64 || calls != 1 {
		t.Fatalf("cached PNG failed %v", err)
	}
}

func TestMapRejectsUnauthorizedRedirectAndInvalidImage(t *testing.T) {
	for _, status := range []int{401, 403, 410, 302, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/outside")
				w.WriteHeader(status)
			}))
			defer server.Close()
			c := Client{HTTP: server.Client(), BaseURL: server.URL}
			if _, err := c.MapImage(context.Background(), Session{AccessHandle: "synthetic-map-owner"}); err == nil || calls.Load() != 1 {
				t.Fatal("unsafe response accepted or redirect followed")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("invalid"))
	}))
	defer server.Close()
	c := Client{HTTP: server.Client(), BaseURL: server.URL}
	if _, err := c.MapImage(context.Background(), Session{AccessHandle: "synthetic-map-owner"}); err == nil {
		t.Fatal("invalid image accepted")
	}
}
func TestPhysicalObservationNeverChangesProviderOutcome(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	s := Screen{vault: Vault{Path: filepath.Join(t.TempDir(), "verification.enc"), Crypt: func(b []byte, seal bool) ([]byte, error) {
		nonce := make([]byte, aead.NonceSize())
		if seal {
			return aead.Seal(nil, nonce, b, nil), nil
		}
		return aead.Open(nil, nonce, b, nil)
	}}, saved: SavedState{Verifications: []CommandVerification{{RequestID: "synthetic-request", Action: "lock", ProviderState: "outcome-unknown"}}}}
	if e := s.setPhysicalObserved(context.Background(), "lock"); e != nil {
		t.Fatal(e)
	}
	v := s.saved.Verifications[0]
	if v.ProviderState != "outcome-unknown" || v.PhysicalObserved == nil || !*v.PhysicalObserved {
		t.Fatal("owner report changed provider evidence")
	}
	encoded, _ := json.Marshal(s.saved)
	if strings.Contains(string(encoded), "succeeded") {
		t.Fatal("physical check invented provider success")
	}
}
