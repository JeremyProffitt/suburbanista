// Package appearance owns local presentation preferences, not vehicle state.
package appearancecore

import (
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/color"
	"strings"
	"time"
)

//go:embed catalog.json
var assets embed.FS

const preferenceKey = "appearance.v1"
const PreferenceKey = preferenceKey

// Preferences is the existing Fyne string setting contract, without importing
// the UI runtime into Android's independent widget process.
type Preferences interface {
	String(string) string
	SetString(string, string)
}

const Normal = "normal"
const Seasonal = "holiday"
const Automatic = "automatic"

type Style struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Holiday string `json:"holiday,omitempty"`
}
type Holiday struct{ ID, Name, Period string }

var holidays = []Holiday{
	{"new-year", "New Year", "December 28 - January 1"},
	{"birthday", "Birthday", "January 2 - January 20"},
	{"valentines", "Valentine's", "February 1 - February 20"},
	{"st-patricks", "St. Patrick's", "March 1 - March 17"},
	{"easter", "Easter", "14 days before Easter - Easter Sunday"},
	{"july-fourth", "July 4", "June 15 - July 10"},
	{"halloween", "Halloween", "October 1 - October 31"},
	{"thanksgiving", "Thanksgiving", "November 1 - Thanksgiving Day"},
	{"christmas", "Christmas", "December 1 - December 27"},
}

var catalog = func() []Style {
	data, err := assets.ReadFile("catalog.json")
	if err != nil {
		panic("appearance catalog missing")
	}
	var out []Style
	if json.Unmarshal(data, &out) != nil {
		panic("appearance catalog invalid")
	}
	return out
}()

// Holidays returns the owner's calendar periods.
func Holidays() []Holiday { return append([]Holiday(nil), holidays...) }

// Styles lists only normal styles for an empty holiday, or that holiday's sets.
func Styles(holiday string) []Style {
	out := []Style{}
	for _, s := range catalog {
		if s.Holiday == holiday {
			out = append(out, s)
		}
	}
	return out
}

type Settings struct {
	Mode               string              `json:"mode"`
	NormalStyle        string              `json:"normalStyle"`
	Holiday            string              `json:"holiday"`
	HolidayStyles      map[string]string   `json:"holidayStyles"`
	Background         string              `json:"background"`
	Backdrop           string              `json:"backdrop"`
	AutomaticSky       bool                `json:"automaticSky"`
	BackgroundRotation Rotation            `json:"backgroundRotation"`
	ButtonRotation     Rotation            `json:"buttonRotation"`
	HolidayRotation    map[string][]string `json:"holidayRotation"`
}

func Default() Settings {
	s := Settings{Mode: Normal, NormalStyle: "reactor-forge", Background: "#173F2A", HolidayStyles: map[string]string{}, Backdrop: "charlotte", AutomaticSky: true, BackgroundRotation: Rotation{Minutes: 60, Selected: []string{"charlotte"}}, ButtonRotation: Rotation{Minutes: 60, Selected: []string{"reactor-forge"}}, HolidayRotation: map[string][]string{}}
	for _, h := range holidays {
		s.HolidayStyles[h.ID] = Styles(h.ID)[0].ID
		s.HolidayRotation[h.ID] = []string{Styles(h.ID)[0].ID}
	}
	return s
}

// Clone isolates a configuration draft so Cancel never mutates saved choices.
func (s Settings) Clone() Settings {
	out := s
	out.HolidayStyles = make(map[string]string, len(s.HolidayStyles))
	for k, v := range s.HolidayStyles {
		out.HolidayStyles[k] = v
	}
	out.BackgroundRotation.Selected = append([]string(nil), s.BackgroundRotation.Selected...)
	out.ButtonRotation.Selected = append([]string(nil), s.ButtonRotation.Selected...)
	out.HolidayRotation = make(map[string][]string, len(s.HolidayRotation))
	for k, v := range s.HolidayRotation {
		out.HolidayRotation[k] = append([]string(nil), v...)
	}
	return out
}

func styleExists(id, holiday string) bool {
	for _, style := range catalog {
		if style.ID == id && style.Holiday == holiday {
			return true
		}
	}
	return false
}
func holidayExists(id string) bool {
	for _, holiday := range holidays {
		if holiday.ID == id {
			return true
		}
	}
	return false
}
func parseColor(value string) (color.NRGBA, error) {
	if len(value) != 7 || value[0] != '#' {
		return color.NRGBA{}, errors.New("background must use #RRGGBB")
	}
	raw, err := hex.DecodeString(value[1:])
	if err != nil {
		return color.NRGBA{}, errors.New("background must use #RRGGBB")
	}
	return color.NRGBA{R: raw[0], G: raw[1], B: raw[2], A: 255}, nil
}

func (s Settings) Validate() error {
	if s.Mode != Normal && s.Mode != Seasonal && s.Mode != Automatic {
		return errors.New("unknown icon mode")
	}
	if !styleExists(s.NormalStyle, "") {
		return errors.New("unknown normal icon style")
	}
	if s.Holiday != "" && !holidayExists(s.Holiday) {
		return errors.New("unknown holiday")
	}
	if s.Mode == Seasonal && s.Holiday == "" {
		return errors.New("choose a holiday first")
	}
	if len(s.HolidayStyles) != len(holidays) {
		return errors.New("holiday style choices are incomplete")
	}
	for _, h := range holidays {
		if !styleExists(s.HolidayStyles[h.ID], h.ID) {
			return errors.New("icon style does not belong to holiday")
		}
	}
	if _, err := parseColor(s.Background); err != nil {
		return err
	}
	return s.validateRotation()
}

func (s Settings) SelectedStyle() (Style, error) {
	return s.SelectedStyleAt(time.Now())
}

func (s Settings) SelectedStyleAt(now time.Time) (Style, error) {
	if err := s.Validate(); err != nil {
		return Style{}, err
	}
	id := s.NormalStyle
	if holiday := s.ActiveHoliday(now); holiday != "" {
		id = s.HolidayStyles[holiday]
		id = s.ButtonRotation.Current(s.styleSelection(holiday), id, now)
	} else {
		id = s.ButtonRotation.Current(s.styleSelection(""), id, now)
	}
	for _, style := range catalog {
		if style.ID == id {
			return style, nil
		}
	}
	return Style{}, errors.New("selected style unavailable")
}

// Load uses Fyne's existing app-local preferences. Authentication and encrypted
// owner-session storage are never read or changed by appearance preferences.
// A malformed record is reported and not silently overwritten.
func Load(p Preferences) (Settings, error) {
	if p == nil {
		return Default(), errors.New("appearance preferences unavailable")
	}
	raw := p.String(preferenceKey)
	return Decode(raw)
}

func Decode(raw string) (Settings, error) {
	if raw == "" {
		return Default(), nil
	}
	if len(raw) > 8192 {
		return Default(), errors.New("appearance preferences invalid")
	}
	s := Default()
	if json.Unmarshal([]byte(raw), &s) != nil {
		return Default(), errors.New("appearance preferences invalid")
	}
	if err := s.Validate(); err != nil {
		return Default(), err
	}
	return s, nil
}

func Save(p Preferences, s Settings) error {
	if p == nil {
		return errors.New("appearance preferences unavailable")
	}
	raw, err := Encode(s)
	if err != nil {
		return err
	}
	p.SetString(preferenceKey, raw)
	return nil
}

func Encode(s Settings) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	s.Background = strings.ToUpper(s.Background)
	data, err := json.Marshal(s)
	return string(data), err
}

type Palette struct{ Background, Surface, Foreground, Accent, Secondary color.NRGBA }

func scale(v color.NRGBA, ceiling uint8) color.NRGBA {
	peak := max(v.R, v.G, v.B)
	if peak == 0 {
		return color.NRGBA{A: 255}
	}
	return color.NRGBA{R: uint8(int(v.R) * int(ceiling) / int(peak)), G: uint8(int(v.G) * int(ceiling) / int(peak)), B: uint8(int(v.B) * int(ceiling) / int(peak)), A: 255}
}

// Palette keeps dark surfaces readable and derives the normal icon light from
// the same hue. Christmas explicitly keeps red and green, whatever the backdrop.
// Renderers apply Accent/Secondary only to light masks, not silver bodywork.
func (s Settings) Palette() (Palette, error) {
	return s.PaletteAt(time.Now())
}

func (s Settings) PaletteAt(now time.Time) (Palette, error) {
	if err := s.Validate(); err != nil {
		return Palette{}, err
	}
	base, _ := parseColor(s.Background)
	accent := scale(base, 235)
	if base.R == 0 && base.G == 0 && base.B == 0 {
		accent = color.NRGBA{R: 200, G: 210, B: 205, A: 255}
	}
	p := Palette{Background: scale(base, 28), Surface: scale(base, 52), Foreground: color.NRGBA{R: 245, G: 248, B: 246, A: 255}, Accent: accent, Secondary: accent}
	if s.ActiveHoliday(now) == "christmas" {
		p.Accent = color.NRGBA{R: 39, G: 213, B: 96, A: 255}
		p.Secondary = color.NRGBA{R: 229, G: 48, B: 65, A: 255}
	}
	return p, nil
}
