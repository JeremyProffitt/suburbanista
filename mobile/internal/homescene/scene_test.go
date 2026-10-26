package homescene

import (
	"bytes"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScreenLockGeometryAndState(t *testing.T) {
	in := fixture()
	if _, ok := LayoutRegions(in, 360, 720, false)["screen-lock"]; ok {
		t.Fatal("phone lock leaked into other surfaces")
	}
	in.ScreenLockVisible = true
	var renderer Renderer
	unlocked := renderer.RenderSize(in, 360, 720, false)
	box := unlocked.Regions["screen-lock"]
	arrow := unlocked.Regions["background-next"]
	if box.Dx() != 20 || box.Dy() != 20 || box.Min.X != arrow.Min.X || arrow.Min.Y-box.Max.Y != 20 {
		t.Fatal("screen lock geometry changed")
	}
	in.ScreenLocked = true
	locked := renderer.RenderSize(in, 360, 720, false)
	if bytes.Equal(unlocked.Image.Pix, locked.Image.Pix) {
		t.Fatal("locked glyph did not change")
	}
	for y := 0; y < locked.Image.Bounds().Dy(); y++ {
		for x := 0; x < locked.Image.Bounds().Dx(); x++ {
			if !image.Pt(x, y).In(box) && unlocked.Image.NRGBAAt(x, y) != locked.Image.NRGBAAt(x, y) {
				t.Fatal("screen lock moved unrelated imagery")
			}
		}
	}
}

func BenchmarkSceneWarmS9(b *testing.B) {
	in := fixture()
	in.Now = time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	RenderSize(in, 1080, 2040, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RenderSize(in, 1080, 2040, false)
	}
}

func BenchmarkBackgroundSwitchS9(b *testing.B) {
	in := fixture()
	ids := []string{"charlotte", "beach"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		in.Settings.Backdrop = ids[i%len(ids)]
		RenderSize(in, 1080, 2040, false)
	}
}

func BenchmarkCachedSceneS9(b *testing.B) {
	in := fixture()
	in.MapRevision = 1
	in.MapRenderer = func(w, h int) image.Image { return image.NewNRGBA(image.Rect(0, 0, w, h)) }
	var renderer Renderer
	renderer.RenderSize(in, 1080, 2040, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderer.RenderSize(in, 1080, 2040, false)
	}
}

func TestCachedSceneReusesAssetsAndInvalidatesOnlyChangedMap(t *testing.T) {
	in := fixture()
	calls := 0
	in.MapRevision = 1
	in.MapRenderer = func(w, h int) image.Image {
		calls++
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for i := range img.Pix {
			img.Pix[i] = uint8(i)
		}
		return img
	}
	var r Renderer
	first := r.RenderSize(in, 360, 640, false)
	cache.Lock()
	assetClock := cache.clock
	cache.Unlock()
	second := r.RenderSize(in, 360, 640, false)
	if calls != 1 || !bytes.Equal(first.Image.Pix, second.Image.Pix) {
		t.Fatal("unchanged scene did not reuse map pixels")
	}
	cache.Lock()
	after := cache.clock
	cache.Unlock()
	if after != assetClock {
		t.Fatal("unchanged scene revisited artwork")
	}
	in.LocationRefreshing = true
	in.RefreshAngle = .5
	r.RenderSize(in, 360, 640, false)
	if calls != 1 {
		t.Fatal("spinner resampled the map")
	}
	in.MapRevision++
	r.RenderSize(in, 360, 640, false)
	if calls != 2 {
		t.Fatal("new gesture/image revision did not compose map")
	}
	in.MapRenderer = nil
	in.Map = nil
	r.RenderSize(in, 360, 640, false)
	if r.mapImage != nil {
		t.Fatal("removed/unauthorized map retained by surface cache")
	}
}

func TestNavigationBoxesMatchWeatherOpacityAndClickBounds(t *testing.T) {
	in := fixture()
	without := Render(in, 360, false)
	in.Settings.ButtonRotation.Enabled = true
	r := Render(in, 360, false)
	for _, name := range []string{"background-previous", "background-next", "style-previous", "style-next"} {
		box := r.Regions[name]
		if box.Dx() != 20 || box.Dy() != 20 {
			t.Fatalf("%s box is not20x20", name)
		}
	}
	if r.Regions["location-refresh"].Overlaps(r.Regions["recenter"]) || r.Regions["location-refresh"].Overlaps(r.Regions["settings"]) {
		t.Fatal("refresh steals footer controls")
	}
	before, after := without.Image.NRGBAAt(113, 328), r.Image.NRGBAAt(113, 328)
	for i, pair := range [][2]uint8{{before.R, after.R}, {before.G, after.G}, {before.B, after.B}} {
		want := int(pair[0]) * 229 / 255
		delta := int(pair[1]) - want
		if delta < -1 || delta > 1 {
			t.Fatalf("navigation weather alpha differs channel%d", i)
		}
	}
}

func TestApprovedMockupVisualContract(t *testing.T) {
	in := fixture()
	percent := 80.0
	in.FuelPercent = &percent
	in.Weather = "Partly cloudy"
	in.Updated = "Last Updated 2:41 PM"
	in.Settings.ButtonRotation.Enabled = true
	frame := Render(in, 360, false)
	for _, sample := range []struct {
		point image.Point
		want  color.NRGBA
	}{{image.Pt(2, 2), color.NRGBA{16, 51, 38, 255}}, {image.Pt(100, 630), color.NRGBA{10, 32, 19, 255}}, {image.Pt(10, 610), color.NRGBA{41, 63, 81, 255}}} {
		if frame.Image.NRGBAAt(sample.point.X, sample.point.Y) != sample.want {
			t.Errorf("approved frame color differs at %v: %v", sample.point, frame.Image.NRGBAAt(sample.point.X, sample.point.Y))
		}
	}
	if got := frame.Regions["start"]; got != image.Rect(32, 266, 110, 344) {
		t.Errorf("approved visible-art control bounds: %v", got)
	}
	in.Odometer = " "
	without := Render(in, 360, false)
	bounds := image.Rectangle{}
	for y := 235; y < 265; y++ {
		for x := 0; x < 360; x++ {
			if frame.Image.NRGBAAt(x, y) != without.Image.NRGBAAt(x, y) {
				point := image.Rect(x, y, x+1, y+1)
				if bounds.Empty() {
					bounds = point
				} else {
					bounds = bounds.Union(point)
				}
			}
		}
	}
	if bounds.Empty() || bounds.Min.X < 230 || bounds.Max.X > 345 {
		t.Errorf("odometer is not right-aligned to reference: %v", bounds)
	}
	if folder := os.Getenv("MOCKUP_PROOF_DIR"); folder != "" {
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(folder, "approved-scene-corrected.png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, frame.Image)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTallViewportExtendsOnlyMap(t *testing.T) {
	in := fixture()
	calls := 0
	var got image.Point
	in.MapRenderer = func(w, h int) image.Image {
		calls++
		got = image.Pt(w, h)
		return image.NewNRGBA(image.Rect(0, 0, w, h))
	}
	tall := RenderSize(in, 360, 720, false)
	if calls != 1 || got != image.Pt(360, 326) {
		t.Fatalf("map callback=%d %v", calls, got)
	}
	if tall.Image.Bounds() != image.Rect(0, 0, 360, 720) || tall.Regions["settings"] != image.Rect(320, 680, 355, 715) || tall.Regions["map"] != image.Rect(0, 349, 360, 675) {
		t.Fatal("tall viewport bounds", tall.Regions)
	}
	normal := Render(in, 360, false)
	for y := 0; y < 349; y++ {
		for x := 0; x < 360; x++ {
			if normal.Image.NRGBAAt(x, y) != tall.Image.NRGBAAt(x, y) {
				t.Fatal("tall viewport changed fixed top scene")
			}
		}
	}
	calls = 0
	compact := RenderSize(in, 360, 720, true)
	if calls != 0 || compact.Image.Bounds().Dy() != 349 {
		t.Fatal("compact expanded or rendered a map")
	}
}

func TestVisibleArtworkIgnoresTransparentPadding(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 192, 192))
	for y := 30; y < 170; y++ {
		for x := 40; x < 160; x++ {
			source.SetNRGBA(x, y, color.NRGBA{20, 220, 30, 255})
		}
	}
	cropped := visibleArtwork(source)
	if cropped.Bounds() != image.Rect(0, 0, 120, 140) {
		t.Fatal("visible command art retained transparent margins", cropped.Bounds())
	}
}

func TestRoutineReadyDoesNotAddMockupText(t *testing.T) {
	in := fixture()
	without := Render(in, 360, false)
	for _, status := range []string{"Ready", "Ready · commands are actions"} {
		in.Status = status
		frame := Render(in, 360, false)
		if _, exists := frame.Regions["status"]; exists {
			t.Fatal("routine Ready retained an invisible status hit")
		}
		if !bytes.Equal(without.Image.Pix, frame.Image.Pix) {
			t.Fatal("routine ready changed the approved scene")
		}
	}
	in.Status = "Outcome unknown · check status"
	if bytes.Equal(without.Image.Pix, Render(in, 360, false).Image.Pix) {
		t.Fatal("unknown-command warning hidden")
	}
}

func TestFreshnessRemainsMetadataWithoutVisibleStaleLabels(t *testing.T) {
	in := fixture()
	fresh := Render(in, 360, false)
	in.RangeFreshness = "stale"
	stale := Render(in, 360, false)
	if !bytes.Equal(fresh.Image.Pix, stale.Image.Pix) {
		t.Fatal("visible stale range label remains")
	}
	in.FuelFreshness = "stale"
	in.Odometer += " · stale"
	in.Range += " · stale"
	if !bytes.Equal(fresh.Image.Pix, Render(in, 360, false).Image.Pix) {
		t.Fatal("visible stale reading suffix remains")
	}
	in.RangeFreshness = "unknown"
	if !bytes.Equal(fresh.Image.Pix, Render(in, 360, false).Image.Pix) {
		t.Fatal("unknown freshness invented a stale label")
	}
}

func TestStatusHitNeverOverlapsCommandOrStyleControls(t *testing.T) {
	for _, width := range []int{180, 360, 720, 1080} {
		in := fixture()
		in.Settings.ButtonRotation.Enabled = true
		in.Status = "Outcome unknown · check status"
		frame := Render(in, width, false)
		status, exists := frame.Regions["status"]
		if !exists || status.Empty() {
			t.Fatal("actual warning lost status hit")
		}
		for _, name := range []string{"start", "unlock", "lock", "style-previous", "style-next", "background-previous", "background-next"} {
			if status.Overlaps(frame.Regions[name]) {
				t.Fatalf("status steals %s at width%d", name, width)
			}
		}
		in.Status = ""
		if _, exists := Render(in, width, false).Regions["status"]; exists {
			t.Fatal("empty status retained invisible hit")
		}
	}
}

func TestWeatherCardFollowsReadingAndFontScale(t *testing.T) {
	in := fixture()
	small := Render(in, 360, false).Regions["weather"]
	in.Temperature = "105°"
	in.FontScale = 1.1
	large := Render(in, 360, false).Regions["weather"]
	if large.Dx() <= small.Dx() || large.Min != small.Min || large.Dy() != small.Dy() {
		t.Fatal("weather card failed content sizing", small, large)
	}
}

func TestAttributionLinkDoesNotReplaceOrCoverProviderNotices(t *testing.T) {
	in := fixture()
	in.MapNoticeHeight = 31
	in.Attribution = "https://untrusted.invalid/not-credits"
	a := Render(in, 360, false)
	in.Attribution = "Invented provider copyright"
	b := Render(in, 360, false)
	if !bytes.Equal(a.Image.Pix, b.Image.Pix) {
		t.Fatal("raw server attribution rendered")
	}
	r, ok := a.Regions["attribution"]
	if !ok || r.Max.X != 359 || r.Max.Y != 594 {
		t.Fatal("AWS link lacks requested lower-right one-pixel inset", r)
	}
	if _, ok = Render(in, 360, true).Regions["attribution"]; ok {
		t.Fatal("compact added map link")
	}
}

func fixture() Input {
	return Input{Settings: appearancecore.Default(), Now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), HighLow: "80° / 64°", Temperature: "75°", Range: "310 Miles", Fuel: "62%", Odometer: "12,480 Miles", Weather: "Clear daylight", Allowed: map[string]bool{"start": true, "unlock": true, "lock": true}}
}
func TestSharedSceneGeometryAndCompactCrop(t *testing.T) {
	in := fixture()
	in.Settings.ButtonRotation.Enabled = true
	full, compact := Render(in, 360, false), Render(in, 360, true)
	if compact.Image.Bounds().Dy() != 349 {
		t.Fatal("compact height")
	}
	for y := 0; y < 349; y++ {
		for x := 0; x < 360; x++ {
			if full.Image.NRGBAAt(x, y) != compact.Image.NRGBAAt(x, y) {
				t.Fatalf("compact mismatch %d,%d", x, y)
			}
		}
	}
	for name, want := range map[string]image.Rectangle{"map": image.Rect(0, 349, 360, 595), "settings": image.Rect(320, 600, 355, 635), "recenter": image.Rect(5, 600, 40, 635), "background-previous": image.Rect(1, 323, 21, 343), "style-previous": image.Rect(110, 323, 130, 343)} {
		if full.Regions[name] != want {
			t.Fatalf("%s: %v", name, full.Regions[name])
		}
	}
	if _, exists := compact.Regions["map"]; exists {
		t.Fatal("compact includes map")
	}
	if full.Regions["status"].Overlaps(full.Regions["style-previous"]) {
		t.Fatal("status steals navigation")
	}
}
func TestWeatherStaysBehindForegroundAndClears(t *testing.T) {
	in := fixture()
	base := Render(in, 360, false)
	in.Weather = "Snow"
	snow := Render(in, 360, false)
	if base.Image.NRGBAAt(10, 140) == snow.Image.NRGBAAt(10, 140) {
		t.Fatal("snow not applied")
	}
	for _, pt := range []image.Point{{180, 170}, {60, 293}, {300, 293}, {180, 400}} {
		if base.Image.NRGBAAt(pt.X, pt.Y) != snow.Image.NRGBAAt(pt.X, pt.Y) {
			t.Fatalf("weather alters foreground %v", pt)
		}
	}
	in.Weather = "Clear daylight"
	clear := Render(in, 360, false)
	if clear.Image.NRGBAAt(10, 140) != base.Image.NRGBAAt(10, 140) {
		t.Fatal("weather did not clear")
	}
	in.Weather = "Thunderstorm"
	storm := Render(in, 360, false)
	if storm.Image.NRGBAAt(40, 65) == base.Image.NRGBAAt(40, 65) {
		t.Fatal("storm clouds missing")
	}
}

func TestSolarGradientUsesValidSameLocationTimes(t *testing.T) {
	in := fixture()
	in.Sunrise = in.Now.Add(-6 * time.Hour)
	in.Sunset = in.Now.Add(6 * time.Hour)
	day := Render(in, 360, false)
	in.Now = in.Sunrise
	dawn := Render(in, 360, false)
	if day.Image.NRGBAAt(10, 140) == dawn.Image.NRGBAAt(10, 140) {
		t.Fatal("solar transition absent")
	}
	if day.Image.NRGBAAt(180, 170) != dawn.Image.NRGBAAt(180, 170) {
		t.Fatal("solar grading dimmed vehicle")
	}
	in.Now = in.Now.AddDate(0, 0, 2)
	expired := Render(in, 360, false)
	in.Sunrise = time.Time{}
	in.Sunset = time.Time{}
	unknown := Render(in, 360, false)
	if expired.Image.NRGBAAt(10, 140) != unknown.Image.NRGBAAt(10, 140) {
		t.Fatal("expired solar data applied")
	}
}
func TestWeatherPreviewHoldBoundariesAndRestoration(t *testing.T) {
	now := time.Now()
	var p Preview
	p.Press(now, "Rain")
	if _, active := p.Condition(now.Add(14999*time.Millisecond), "Rain"); active {
		t.Fatal("early start")
	}
	p.Release()
	if _, active := p.Condition(now.Add(15*time.Second), "Rain"); active {
		t.Fatal("release did not cancel")
	}
	p.Press(now, "Rain")
	for i, want := range Conditions {
		got, active := p.Condition(now.Add(15*time.Second+time.Duration(i)*5*time.Second), "Rain")
		if !active || got != want {
			t.Fatalf("step %d: %s %t", i, got, active)
		}
		p.Release()
		same, active := p.Condition(now.Add(15*time.Second+time.Duration(i)*5*time.Second+4999*time.Millisecond), "Rain")
		if !active || same != want {
			t.Fatal("step duration")
		}
	}
	got, active := p.Condition(now.Add(70*time.Second), "Snow")
	if active || got != "Rain" {
		t.Fatal("prior condition not restored")
	}
	p.Press(now, "Fog")
	p.Condition(now.Add(15*time.Second), "Fog")
	p.Cancel()
	if _, active = p.Condition(now.Add(16*time.Second), "Fog"); active {
		t.Fatal("visibility cancellation")
	}
}
