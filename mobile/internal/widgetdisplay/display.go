// Package widgetdisplay transfers measured presentation data between app and
// widgets. It contains no owner credentials, coordinates or vehicle commands.
package widgetdisplay

import (
	"encoding/json"
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"

	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Reading struct {
	Text       string     `json:"text"`
	Available  bool       `json:"available"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	Freshness  string     `json:"freshness,omitempty"`
}
type Snapshot struct {
	SchemaVersion int       `json:"schemaVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Weather       Reading   `json:"weather"`
	Range         Reading   `json:"range"`
	Fuel          Reading   `json:"fuel"`
	Odometer      Reading   `json:"odometer"`
	HighLow       string    `json:"highLow,omitempty"`
	Temperature   string    `json:"temperature,omitempty"`
	Condition     string    `json:"condition,omitempty"`
	WeatherCode   int       `json:"weatherCode,omitempty"`
	Sunrise       time.Time `json:"sunrise,omitempty"`
	Sunset        time.Time `json:"sunset,omitempty"`
}
type View struct{ Weather, Range, Fuel, Odometer, Updated string }

// ConditionAt uses source weather and solar timestamps, never the phone or car
// location. Widgets recompute time-of-day on every render instead of freezing it.
func ConditionAt(code int, now, rise, set time.Time) string {
	switch code {
	case 45, 48:
		return "Fog"
	case 51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 80, 81, 82:
		return "Rain"
	case 71, 73, 75, 77, 85, 86:
		return "Snow"
	case 95, 96, 99:
		return "Thunderstorm"
	case 3:
		return "Overcast"
	case 0, 1, 2:
	default:
		return ""
	}
	if rise.IsZero() || !set.After(rise) {
		return ""
	}
	if now.Before(rise.Add(-30*time.Minute)) || !now.Before(set.Add(45*time.Minute)) {
		return "Night"
	}
	if now.Before(rise) {
		return "Dawn"
	}
	if now.Before(rise.Add(30 * time.Minute)) {
		return "Sunrise"
	}
	if !now.Before(set) {
		return "Blue hour"
	}
	if !now.Before(set.Add(-30 * time.Minute)) {
		return "Sunset"
	}
	return "Clear daylight"
}

func (s Snapshot) validate() error {
	if s.SchemaVersion != 1 || s.GeneratedAt.IsZero() {
		return errors.New("widget display unavailable")
	}
	for _, text := range []string{s.HighLow, s.Temperature, s.Condition} {
		if len(text) > 80 || strings.ContainsAny(text, "\r\n\x00") {
			return errors.New("widget weather invalid")
		}
	}
	for _, r := range []Reading{s.Weather, s.Range, s.Fuel, s.Odometer} {
		if len(r.Text) > 160 || strings.ContainsAny(r.Text, "\r\n\x00") || r.Available && r.Text == "" {
			return errors.New("widget display invalid")
		}
		if r.Freshness != "" && r.Freshness != "fresh" && r.Freshness != "stale" && r.Freshness != "unknown" {
			return errors.New("widget freshness invalid")
		}
	}
	return nil
}

func Save(path string, s Snapshot) error {
	if err := s.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".widget-display-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func Load(path string) (Snapshot, error) {
	var s Snapshot
	file, err := os.Open(path)
	if err != nil {
		return s, errors.New("widget display unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(data) > 8192 {
		return s, errors.New("widget display invalid")
	}
	if json.Unmarshal(data, &s) != nil {
		return Snapshot{}, errors.New("widget display invalid")
	}
	if err = s.validate(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// Absolute source time cannot become a frozen relative age on the launcher.
func age(observed time.Time, now time.Time) string {
	if observed.IsZero() || observed.After(now.Add(time.Minute)) {
		return "age unknown"
	}
	return "observed " + appearancecore.DisplayTime(observed, now, now.Location())
}
func display(r Reading, name string, now time.Time) string {
	if !r.Available || r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
		return name + " unavailable"
	}
	text := r.Text
	if r.ObservedAt == nil {
		return text + " · age unknown"
	}
	return text + " · " + age(*r.ObservedAt, now)
}
func (s Snapshot) Display(now time.Time) View {
	if s.validate() != nil || s.GeneratedAt.After(now.Add(time.Minute)) {
		return View{"Weather unavailable", "Range unavailable", "Fuel unavailable", "Odometer unavailable", "Source time unknown"}
	}
	v := View{Weather: display(s.Weather, "Weather", now), Range: display(s.Range, "Range", now), Fuel: display(s.Fuel, "Fuel", now), Odometer: display(s.Odometer, "Odometer", now), Updated: "Source time unknown"}
	var observed time.Time
	for _, r := range []Reading{s.Range, s.Fuel, s.Odometer} {
		if r.Available && r.ObservedAt != nil && !r.ObservedAt.After(now.Add(time.Minute)) && r.ObservedAt.After(observed) {
			observed = *r.ObservedAt
		}
	}
	if !observed.IsZero() {
		v.Updated = "Vehicle " + age(observed, now)
	}
	return v
}
