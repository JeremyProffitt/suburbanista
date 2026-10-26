package mobile

import (
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/homescene"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"sync/atomic"
	"time"
)

type homeView struct {
	rotationLocked                                                atomic.Bool
	lockedBackdrop                                                string
	lockedStyle                                                   appearance.Style
	input                                                         homescene.Input
	renderer                                                      homescene.Renderer
	metadataAt                                                    time.Time
	fontScale                                                     float64
	mapRevision                                                   uint64
	root                                                          *fyne.Container
	hits                                                          map[string]fyne.CanvasObject
	preview                                                       homescene.Preview
	previewGeneration                                             uint64
	sky                                                           *canvas.Raster
	weatherIcon                                                   *canvas.Image
	temperature, highLow, gas, rangeText, mileage, mapLabel, mode *canvas.Text
	car                                                           *canvas.Image
	actions                                                       *fyne.Container
	mapImage                                                      *canvas.Raster
	gear                                                          *iconAction
	lastUpdated                                                   *canvas.Text
	fuelGauge                                                     *canvas.Raster
	weatherPanel, gasPanel, mapPanel, footer                      fyne.CanvasObject
}

func (s *Screen) homeWeather() (time.Time, *Weather) {
	if s.preview.Load() {
		now := s.previewTime
		if now.IsZero() {
			now = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
		}
		rise := time.Date(now.Year(), now.Month(), now.Day(), 6, 0, 0, 0, now.Location())
		temperature := 75.0
		if now.Hour() >= 21 || now.Hour() < 6 {
			temperature = 64
		} else if now.Hour() >= 17 {
			temperature = 69
		}
		return now, &Weather{TemperatureF: temperature, HighF: 80, LowF: 64, Code: 2, CloudPercent: 25, Sunrise: rise, Sunset: rise.Add(12 * time.Hour), ObservedAt: now, FetchedAt: now}
	}
	s.environmentMu.Lock()
	defer s.environmentMu.Unlock()
	if s.weatherData == nil {
		return time.Now(), nil
	}
	copy := *s.weatherData
	return time.Now(), &copy
}
func (s *Screen) paintHome() {
	if s.home == nil {
		return
	}
	h := s.home
	now, weather := s.homeWeather()
	s.environmentMu.Lock()
	state := s.weatherState
	s.environmentMu.Unlock()
	ready := weather != nil && (s.preview.Load() || state == "ready" && now.Sub(weather.FetchedAt) <= 30*time.Minute)
	if ready {
		day, _ := daylightMix(now, weather.Sunrise, weather.Sunset)
		h.weatherIcon.Resource = weatherIcon(weather.Code, day < .5)
		h.temperature.Text = fmt.Sprintf("%.0f°", weather.TemperatureF)
		h.highLow.Text = fmt.Sprintf("%.0f° / %.0f°", weather.HighF, weather.LowF)
	} else {
		h.weatherIcon.Resource = theme.QuestionIcon()
		h.temperature.Text = "--°"
		h.highLow.Text = "--° / --°"
	}
	telemetry := s.homeTelemetry
	if s.preview.Load() {
		telemetry = HomeTelemetry{Fuel: TelemetryReading{Value: 62, Unit: "%", Available: true}, Range: TelemetryReading{Value: 310, Unit: "mi", Available: true}, Odometer: TelemetryReading{Value: 12480, Unit: "mi", Available: true}}
		h.mode.Text = "PREVIEW"
		h.mapLabel.Text = "Preview map · fictional"
	} else {
		h.mode.Text = ""
		h.mapLabel.Text = "Vehicle location unavailable"
		if s.vehicleMapImage != nil {
			h.mapLabel.Text = "Cached vehicle map · source age in image"
		}
	}
	h.gas.Text = "Fuel unknown"
	h.rangeText.Text = "Range --"
	h.mileage.Text = "Mileage --"
	if telemetry.Fuel.Available {
		h.gas.Text = fmt.Sprintf("%.0f%%", telemetry.Fuel.Value)
		if !s.preview.Load() && telemetry.Fuel.ObservedAt == nil {
			h.gas.Text += " · age unknown"
		}
	}
	if telemetry.Range.Available {
		h.rangeText.Text = toolbarRange(telemetry.Range.Value, telemetry.Range.Unit)
	}
	if telemetry.Odometer.Available {
		h.mileage.Text = groupedNumber(telemetry.Odometer.Value) + " Miles"
		if !s.preview.Load() && telemetry.Odometer.ObservedAt == nil {
			h.mileage.Text += " · age unknown"
		}
	}
	s.applyAppearance(time.Now())
	if s.appearanceRefresh != nil {
		s.appearanceRefresh()
	}
	h.mapRevision++
	s.refreshSceneInput(time.Now())
	h.sky.Refresh()
	s.recordWidgetDisplay(time.Now())
}

// Native display metadata is refreshed at foreground entry and minute ticks,
// never as part of map sampling, Fyne layout, or each raster callback.
func (s *Screen) refreshSceneInput(now time.Time) {
	if s.home == nil {
		return
	}
	h := s.home
	if h.metadataAt.IsZero() || now.Sub(h.metadataAt) >= time.Minute {
		s.refreshPhoneClock()
		h.fontScale = platformFontScale(s.window)
		h.metadataAt = now
	}
	h.input = s.sceneInput(now)
}
func (s *Screen) refreshMapView() {
	if s.home != nil && s.home.sky != nil {
		s.home.mapRevision++
		s.home.input.MapRevision = s.home.mapRevision
		s.home.sky.Refresh()
	}
}
func groupedNumber(value float64) string {
	digits := fmt.Sprintf("%.0f", value)
	var out strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(r)
	}
	return out.String()
}
func (s *Screen) mapPicture(w, h int) image.Image {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.NRGBA{R: 11, G: 32, B: 46, A: 255}), image.Point{}, draw.Src)
	if s.preview.Load() {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if (x+y/2)%70 < 3 || (y+x/5)%45 < 2 {
					out.Set(x, y, color.NRGBA{R: 68, G: 103, B: 129, A: 255})
				}
			}
		}
		cx, cy := w/2, h/2
		for y := cy - 5; y <= cy+5; y++ {
			for x := cx - 5; x <= cx+5; x++ {
				if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= 25 {
					out.Set(x, y, color.NRGBA{R: 75, G: 188, B: 255, A: 255})
				}
			}
		}
		return out
	}
	return s.renderCachedMap(w, h)
}
func weatherIcon(code int, night bool) fyne.Resource {
	shape := `<circle cx="10" cy="10" r="5" fill="#ffcd58"/>`
	if night {
		shape = `<path d="M14 3a8 8 0 1 0 7 12A8 8 0 0 1 14 3" fill="#dcecff"/>`
	}
	if code > 0 {
		shape += `<path d="M5 19a4 4 0 1 1 2-7 6 6 0 0 1 11 1 3 3 0 1 1 1 6z" fill="#d9e8f3"/>`
	}
	kind := weatherKind(code)
	if kind == "rain" {
		shape += `<path d="M8 20l-1 3m6-3-1 3m6-3-1 3" stroke="#7ac8ff" stroke-width="1.5"/>`
	}
	if kind == "snow" {
		shape += `<path d="M9 20v5m-2-4 4 3m0-3-4 3m10-4v5m-2-4 4 3m0-3-4 3" stroke="#eaf7ff" stroke-width="1"/>`
	}
	if kind == "thunder" {
		shape += `<path d="m13 17-4 5h3l-1 4 6-6h-4l2-3z" fill="#ffe08a"/>`
	}
	if kind == "fog" {
		shape += `<path d="M4 21h18M6 24h14" stroke="#aebdca" stroke-width="1.4"/>`
	}
	return fyne.NewStaticResource(fmt.Sprintf("weather-%d-%t.svg", code, night), []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 26 26">`+shape+`</svg>`))
}
