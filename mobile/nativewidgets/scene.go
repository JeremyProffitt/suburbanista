package nativewidgets

import (
	"bytes"
	"image"
	"image/png"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/homescene"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/mapview"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/vehicleasset"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/widgetdisplay"
)

// Android measures this single density-free bitmap inside an intrinsic-size
// canvas. Hit padding uses these same pixel bounds, never guessed host bounds.
type nativeScene struct {
	Bounds  image.Rectangle
	PNG     []byte
	Regions map[string]image.Rectangle
}

func renderScene(in homescene.Input, width int, compact bool) (nativeScene, error) {
	return encodeScene(homescene.Render(in, width, compact))
}
func renderSceneSize(in homescene.Input, width, height int, compact bool) (nativeScene, error) {
	return encodeScene(homescene.RenderSize(in, width, height, compact))
}
func encodeScene(frame homescene.Frame) (nativeScene, error) {
	scene := nativeScene{Bounds: frame.Image.Bounds(), Regions: frame.Regions}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, frame.Image); err != nil {
		return nativeScene{}, err
	}
	scene.PNG = buffer.Bytes()
	return scene, nil
}

func sceneInput(state viewState, now time.Time, fontScale float64) homescene.Input {
	display := state.display
	if !state.signedIn || display.SchemaVersion != 1 || display.GeneratedAt.IsZero() || display.GeneratedAt.After(now.Add(time.Minute)) {
		display = widgetdisplay.Snapshot{}
	}
	view := display.Display(now)
	reading := func(r widgetdisplay.Reading, unavailable string, markAge bool) string {
		if !r.Available || r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
			return unavailable
		}
		text := r.Text
		if markAge && r.ObservedAt == nil {
			text += " · age unknown"
		}
		return text
	}
	updated := "source time unknown"
	if view.Updated != "Source time unknown" {
		updated = strings.TrimPrefix(view.Updated, "Vehicle observed ")
	}
	// Keep the value and freshness separate, as the app does. The shared
	// renderer owns the small freshness label instead of a widget-only suffix.
	rangeValue := display.Range
	rangeValue.Freshness = ""
	input := homescene.Input{Settings: state.settings, Now: now, HighLow: "--° / --°", Temperature: "--°", Range: reading(rangeValue, "Range --", false), Fuel: reading(display.Fuel, "Fuel unknown", true), Odometer: reading(display.Odometer, "Mileage --", true), Updated: "Last Updated · " + updated, Status: state.status, Allowed: state.allowed, FontScale: fontScale, Map: state.mapImage}
	if display.Range.Available && (display.Range.ExpiresAt == nil || now.Before(*display.Range.ExpiresAt)) {
		input.RangeFreshness = display.Range.Freshness
	}
	input.Attribution = state.mapView.Attribution
	input.MapNoticeHeight = float64(mapview.ViewNoticeHeight(state.mapView, 360, 246))
	if !state.signedIn || !now.Before(state.mapView.ExpiresAt) {
		input.Map = nil
		input.Attribution = ""
	}
	if input.Map != nil {
		input.Updated = "Last Updated · " + mapview.ObservationLabel(state.mapView, now)
		input.MapRenderer = func(w, h int) image.Image {
			return mapview.Render(state.mapView, state.mapImage, nativeMapMarker, w, h, mapview.Initial(state.mapView, w, h))
		}
		input.Map = nil
	}
	if display.Fuel.Available && (display.Fuel.ExpiresAt == nil || now.Before(*display.Fuel.ExpiresAt)) {
		if percent, err := strconv.ParseFloat(strings.TrimSuffix(display.Fuel.Text, "%"), 64); err == nil && percent >= 0 && percent <= 100 {
			input.FuelPercent = &percent
			input.FuelFreshness = display.Fuel.Freshness
		}
	}
	if state.signedIn && display.Weather.Available && display.Weather.ExpiresAt != nil && now.Before(*display.Weather.ExpiresAt) {
		input.HighLow, input.Temperature, input.Weather = display.HighLow, display.Temperature, display.Condition
		input.Weather = widgetdisplay.ConditionAt(display.WeatherCode, now, display.Sunrise, display.Sunset)
		input.Sunrise, input.Sunset = display.Sunrise, display.Sunset
		if !display.Sunrise.IsZero() && display.Sunset.After(display.Sunrise) {
			input.Night = now.Before(display.Sunrise) || !now.Before(display.Sunset)
		}
	}
	return input
}

var nativeMapMarker = func() image.Image { img, _ := png.Decode(bytes.NewReader(vehicleasset.MarkerPNG)); return img }()
