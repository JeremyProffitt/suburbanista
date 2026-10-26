package mobile

import (
	"encoding/json"
	"fmt"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/widgetdisplay"
	"path/filepath"
	"time"
)

func widgetReading(r TelemetryReading, text string) widgetdisplay.Reading {
	return widgetdisplay.Reading{Text: text, Available: r.Available, ObservedAt: r.ObservedAt, Freshness: r.Freshness}
}
func (s *Screen) recordWidgetDisplay(now time.Time) {
	if !platformWidgetsAvailable() || s.app == nil || s.window == nil {
		return
	}
	readings := s.homeTelemetry
	snapshot := widgetdisplay.Snapshot{SchemaVersion: 1,
		Fuel:     widgetReading(readings.Fuel, fmt.Sprintf("%.0f%%", readings.Fuel.Value)),
		Range:    widgetReading(readings.Range, toolbarRange(readings.Range.Value, readings.Range.Unit)),
		Odometer: widgetReading(readings.Odometer, groupedNumber(readings.Odometer.Value)+" Miles")}
	s.environmentMu.Lock()
	if s.weatherData != nil && s.weatherState == "ready" {
		weather := *s.weatherData
		expires := weather.FetchedAt.Add(30 * time.Minute)
		snapshot.Weather = widgetdisplay.Reading{Text: fmt.Sprintf("%.0f° · H %.0f° L %.0f°", weather.TemperatureF, weather.HighF, weather.LowF), Available: true, ExpiresAt: &expires}
		snapshot.HighLow = fmt.Sprintf("%.0f° / %.0f°", weather.HighF, weather.LowF)
		snapshot.Temperature = fmt.Sprintf("%.0f°", weather.TemperatureF)
		snapshot.Condition = sceneWeatherCondition(now, &weather)
		snapshot.WeatherCode = weather.Code
		snapshot.Sunrise, snapshot.Sunset = weather.Sunrise, weather.Sunset
		if !weather.ObservedAt.IsZero() {
			observed := weather.ObservedAt
			snapshot.Weather.ObservedAt = &observed
		}
	}
	s.environmentMu.Unlock()
	encoded, _ := json.Marshal(snapshot)
	settings := s.appearanceSettings().Clone()
	preferences, err := appearancecore.Encode(settings)
	if err != nil {
		return
	}
	key := string(encoded) + preferences + s.appearanceKey
	key += fmt.Sprint(s.commandPending.Load())
	root := s.app.Storage().RootURI().Path()
	snapshot.GeneratedAt = now
	sequence := s.widgetDisplaySequence.Add(1)
	go func() {
		s.widgetDisplayMu.Lock()
		defer s.widgetDisplayMu.Unlock()
		if sequence != s.widgetDisplaySequence.Load() || key == s.widgetDisplayKey {
			return
		}
		if widgetdisplay.Save(filepath.Join(root, "widget-display.json"), snapshot) != nil {
			return
		}
		// Read back the shared atomic appearance document before signalling.
		for attempt := 0; attempt < 10; attempt++ {
			if sequence != s.widgetDisplaySequence.Load() {
				return
			}
			stored, err := appearancecore.LoadShared(root)
			if err == nil {
				raw, _ := appearancecore.Encode(stored)
				if raw == preferences {
					if platformRefreshWidgets(s.window) {
						s.widgetDisplayKey = key
					}
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

func (s *Screen) syncWidgets() {
	if s.preview.Load() {
		return
	}
	s.widgetSyncPending.Store(true)
	if !s.busy.Load() {
		s.flushWidgetSync()
	}
}

func (s *Screen) flushWidgetSync() {
	if !s.widgetSyncPending.Swap(false) || s.preview.Load() {
		return
	}
	send := s.widgetSyncSender
	if send == nil {
		if !platformWidgetsAvailable() || s.window == nil {
			return
		}
		send = func() bool { return platformSyncWidgets(s.window) }
	}
	go send()
}
