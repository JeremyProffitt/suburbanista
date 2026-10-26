package mapview

import (
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"
)

func BenchmarkMapGestureS9(b *testing.B) {
	v := fixture()
	src := image.NewNRGBA(image.Rect(0, 0, v.Width, v.Height))
	pin := image.NewNRGBA(image.Rect(0, 0, 38, 38))
	for i := range src.Pix {
		src.Pix[i] = uint8(i)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Render(v, src, pin, 1080, 900, Transform{Zoom: 2, X: -float64(i % 100), Y: -30})
	}
}

func TestMapGestureHasNoPerPixelHeapAllocation(t *testing.T) {
	v := fixture()
	src := image.NewNRGBA(image.Rect(0, 0, v.Width, v.Height))
	pin := image.NewNRGBA(image.Rect(0, 0, 38, 38))
	allocations := testing.AllocsPerRun(1, func() { Render(v, src, pin, 1080, 900, Transform{Zoom: 2, X: -30, Y: -30}) })
	if allocations > 100 {
		t.Fatalf("map gesture allocates per pixel: %.0f allocations", allocations)
	}
}

func fixture() contract.MapView {
	now := time.Now()
	return contract.MapView{ImageID: "synthetic", ImageVersion: "1", GeneratedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Latitude: 0, Longitude: 0, CenterLatitude: 0, CenterLongitude: 0, Zoom: 18, Width: 768, Height: 512, Attribution: "Synthetic attribution"}
}

func TestLargeMapStartsAtExistingExtentAndZoomsOut(t *testing.T) {
	v := fixture()
	v.Width, v.Height, v.Zoom, v.NoticeRows = 3072, 2048, 19, 192
	initial := Initial(v, 360, 246)
	if initial.Zoom != 4 || initial.MaxZoom != 32 {
		t.Fatal("large initial scale changed")
	}
	x, y, err := initial.Anchor(v, 360, 246)
	if err != nil || math.Abs(x-180) > 1e-8 || math.Abs(y-float64(GeoHeight(v, 360, 246))/2) > 1e-8 {
		t.Fatal("initial fix not centered")
	}
	if float64(v.Width)/initial.Zoom != 768 {
		t.Fatal("old horizontal geographic extent changed")
	}
	wide := initial.Scale(.25, 180, float64(GeoHeight(v, 360, 246))/2, 360, GeoHeight(v, 360, 246))
	if wide.Zoom != 1 || wide.X != 0 || wide.Y != 0 {
		t.Fatal("full fourfold extent unavailable")
	}
	if initial.Scale(8, 180, float64(GeoHeight(v, 360, 246))/2, 360, GeoHeight(v, 360, 246)).Zoom != 32 {
		t.Fatal("relative zoom-in range reduced")
	}
	old := v
	old.Width, old.Height, old.NoticeRows = 768, 512, 20
	legacy := Transform{Zoom: 2, X: -120, Y: -80}
	converted := Reframe(legacy, old, v, 360, 246)
	if converted.Zoom != 8 || math.Abs(converted.X+1200) > 1e-6 || math.Abs(converted.Y-(111.5-12820047.0/13688)) > 1e-6 {
		t.Fatal("profile transition jumped", converted)
	}
	if Reframe(converted, v, v, 360, 246) != converted {
		t.Fatal("ordinary refresh reset gesture")
	}
	source := image.NewNRGBA(image.Rect(0, 0, v.Width, v.Height))
	for yy := v.Height - v.NoticeRows; yy < v.Height; yy++ {
		for xx := 0; xx < v.Width; xx++ {
			source.SetNRGBA(xx, yy, color.NRGBA{200, 10, 40, 255})
		}
	}
	a := Render(v, source, nil, 360, 246, initial)
	b := Render(v, source, nil, 360, 246, wide)
	for yy := 246 - ViewNoticeHeight(v, 360, 246); yy < 246; yy++ {
		for xx := 0; xx < 360; xx++ {
			if a.At(xx, yy) != b.At(xx, yy) || a.At(xx, yy) != (color.NRGBA{200, 10, 40, 255}) {
				t.Fatal("provider credits clipped or transformed")
			}
		}
	}
}

func TestProviderCreditsAreNeverSampledAsGeography(t *testing.T) {
	for _, profile := range []struct{ width, height, notice int }{{768, 512, 20}, {3072, 2048, 192}} {
		v := fixture()
		v.Width, v.Height, v.NoticeRows = profile.width, profile.height, profile.notice
		geography := color.NRGBA{11, 77, 131, 255}
		credit := color.NRGBA{231, 19, 53, 255}
		source := image.NewNRGBA(image.Rect(0, 0, v.Width, v.Height))
		for y := 0; y < v.Height; y++ {
			c := geography
			if y >= v.Height-v.NoticeRows {
				c = credit
			}
			for x := 0; x < v.Width; x++ {
				source.SetNRGBA(x, y, c)
			}
		}
		for _, size := range []image.Point{{360, 326}, {1080, 978}, {768, 512}} {
			footer := ViewNoticeHeight(v, size.X, size.Y)
			for _, tr := range []Transform{{Zoom: 1}, {Zoom: 4}, {Zoom: 4, Y: -1e9}, {Zoom: 8, X: -1e9, Y: -1e9}} {
				got := Render(v, source, nil, size.X, size.Y, tr)
				for y := 0; y < size.Y; y++ {
					want := geography
					if y >= size.Y-footer {
						want = credit
					}
					for x := 0; x < size.X; x++ {
						if got.At(x, y) != want {
							t.Fatalf("credits sampled as geography: profile%dx%d viewport%v zoom%.0f at%d,%d", v.Width, v.Height, size, tr.Zoom, x, y)
						}
					}
				}
			}
		}
	}
}

func TestMarkerProjectionMatchesCroppedGeography(t *testing.T) {
	v := fixture()
	v.Width, v.Height, v.NoticeRows = 3072, 2048, 192
	source := image.NewNRGBA(image.Rect(0, 0, v.Width, v.Height))
	px, py, err := Position(v)
	if err != nil {
		t.Fatal(err)
	}
	point := color.NRGBA{17, 220, 61, 255}
	for y := int(py) - 10; y <= int(py)+10; y++ {
		for x := int(px) - 10; x <= int(px)+10; x++ {
			source.SetNRGBA(x, y, point)
		}
	}
	for _, size := range []image.Point{{360, 326}, {1080, 978}, {768, 512}} {
		for _, tr := range []Transform{Initial(v, size.X, size.Y), {Zoom: 1}} {
			x, y, err := tr.Anchor(v, size.X, size.Y)
			if err != nil {
				t.Fatal(err)
			}
			if got := Render(v, source, nil, size.X, size.Y, tr).At(int(x), int(y)); got != point {
				t.Fatal("marker anchor and geographic sampling disagree")
			}
			centered := tr.Scale(4, x, y, size.X, GeoHeight(v, size.X, size.Y)).Recenter(v, size.X, size.Y)
			x, y, err = centered.Anchor(v, size.X, size.Y)
			if err != nil || math.Abs(x-float64(size.X)/2) > 1e-7 || math.Abs(y-float64(GeoHeight(v, size.X, size.Y))/2) > 1e-7 {
				t.Fatal("recenter missed geographic center")
			}
		}
	}
}
func TestProviderNoticeWholeStripSurvivesPanZoomAndMarker(t *testing.T) {
	v := fixture()
	src := image.NewNRGBA(image.Rect(0, 0, v.Width, v.Height))
	for y := 0; y < v.Height; y++ {
		for x := 0; x < v.Width; x++ {
			src.SetNRGBA(x, y, color.NRGBA{uint8(x % 251), uint8(y % 251), uint8((x + y) % 251), 255})
		}
	}
	marker := image.NewUniform(color.NRGBA{255, 0, 0, 255})
	// A finite marker can cross the notice band; the original notices must win.
	pin := image.NewNRGBA(image.Rect(0, 0, 38, 38))
	for y := 0; y < 38; y++ {
		for x := 0; x < 38; x++ {
			pin.Set(x, y, marker.C)
		}
	}
	for _, size := range []image.Point{{768, 512}, {360, 246}, {180, 123}} {
		baseline := Render(v, src, nil, size.X, size.Y, Transform{Zoom: 1})
		height := NoticeHeight(v.Width, v.Height, size.X, size.Y)
		for _, tr := range []Transform{{Zoom: 1}, {Zoom: 2, X: -100, Y: -70}, {Zoom: 8, X: -400, Y: -800}} {
			for _, overlay := range []image.Image{nil, pin} {
				got := Render(v, src, overlay, size.X, size.Y, tr)
				for y := size.Y - height; y < size.Y; y++ {
					for x := 0; x < size.X; x++ {
						if got.At(x, y) != baseline.At(x, y) {
							t.Fatalf("notice changed at %v %d,%d", size, x, y)
						}
					}
				}
			}
		}
		if size.X == v.Width {
			for y := v.Height - 20; y < v.Height; y++ {
				for x := 0; x < v.Width; x++ {
					if baseline.At(x, y) != src.At(x, y) {
						t.Fatal("original provider strip altered")
					}
				}
			}
		}
	}
}
func TestMapSceneryAboveProviderNoticeMovesWithZoom(t *testing.T) {
	v := fixture()
	src := image.NewNRGBA(image.Rect(0, 0, v.Width, v.Height))
	for y := 0; y < v.Height; y++ {
		for x := 0; x < v.Width; x++ {
			src.SetNRGBA(x, y, color.NRGBA{uint8(x % 251), uint8(y % 251), 50, 255})
		}
	}
	for _, size := range []image.Point{{768, 512}, {360, 326}, {1080, 978}} {
		plain := Render(v, src, nil, size.X, size.Y, Transform{Zoom: 1})
		zoomed := Render(v, src, nil, size.X, size.Y, Transform{Zoom: 2})
		// This point lies in the scenery previously frozen by the 64-row copy,
		// above the actual 20-row provider text and scale-bar band.
		x := size.X / 2
		y := size.Y - int(math.Ceil(32*float64(size.X)/float64(v.Width)))
		if plain.At(x, y) == zoomed.At(x, y) {
			t.Fatalf("bottom map scenery did not zoom at viewport %v", size)
		}
	}
}
func TestObservationLabelNeverUsesCacheOrDeliveryTime(t *testing.T) {
	now := time.Now()
	v := fixture()
	v.ReceivedAt = now
	v.GeneratedAt = now.Add(-time.Hour)
	if ObservationLabel(v, now) != "source time unknown" {
		t.Fatal("cache time became source time")
	}
	v.ObservedAt = now.Add(-2 * time.Hour)
	if label := ObservationLabel(v, now); !strings.Contains(label, v.ObservedAt.In(now.Location()).Format("3:04 PM")) || strings.Contains(label, "UTC") {
		t.Fatal("source time not used")
	}
	v.ObservedAt = now.Add(2 * time.Minute)
	if ObservationLabel(v, now) != "source time unknown" {
		t.Fatal("future source time accepted")
	}
}
func TestProjectionAndCachedMovement(t *testing.T) {
	v := fixture()
	x, y, e := Position(v)
	if e != nil || x != 384 || y != 256 {
		t.Fatalf("center %v %v %v", x, y, e)
	}
	v.Longitude += .0001
	xx, yy, e := Position(v)
	if e != nil || xx <= x || yy != y {
		t.Fatal("same image must move latest marker")
	}
}
func TestZoomPanRecenter(t *testing.T) {
	v := fixture()
	tr := Transform{Zoom: 1}
	x, y, _ := tr.Anchor(v, 360, 246)
	tr = tr.Scale(2, x, y, 360, GeoHeight(v, 360, 246))
	xx, yy, _ := tr.Anchor(v, 360, 246)
	if math.Abs(xx-x) > .00001 || math.Abs(yy-y) > .00001 {
		t.Fatal("pinch anchor moved")
	}
	tr = tr.Pan(-15, -20, 360, GeoHeight(v, 360, 246))
	xx, yy, _ = tr.Anchor(v, 360, 246)
	if xx != x-15 || yy != y-20 {
		t.Fatal("marker and map pan differ")
	}
	tr = tr.Recenter(v, 360, 246)
	xx, yy, _ = tr.Anchor(v, 360, 246)
	if tr.Zoom != 2 || xx != 180 || math.Abs(yy-float64(GeoHeight(v, 360, 246))/2) > 1e-8 {
		t.Fatal("recenter reset zoom or failed")
	}
	v.Longitude += .0001
	tr = tr.Recenter(v, 360, 246)
	xx, _, _ = tr.Anchor(v, 360, 246)
	if math.Abs(xx-180) > .001 {
		t.Fatal("new fix did not recenter")
	}
}
func TestBoundsAndMetadata(t *testing.T) {
	tr := Transform{Zoom: 100, X: 900, Y: -9000}.Clamp(360, 246)
	if tr.Zoom != 8 || tr.X != 0 || tr.Y != 246*(1-8) {
		t.Fatal("unbounded transform")
	}
	v := fixture()
	img := image.NewNRGBA(image.Rect(0, 0, 768, 512))
	if !Valid(v, img, time.Now()) {
		t.Fatal("valid view rejected")
	}
	v.ExpiresAt = time.Now().Add(-time.Second)
	if Valid(v, img, time.Now()) {
		t.Fatal("expired view")
	}
	v = fixture()
	v.Width = 769
	if Valid(v, img, time.Now()) {
		t.Fatal("mismatched image dimensions")
	}
	v = fixture()
	v.ImageVersion = ""
	if Valid(v, img, time.Now()) {
		t.Fatal("missing image version")
	}
}
