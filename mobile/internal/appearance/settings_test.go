package appearance

import (
	"encoding/json"
	"fyne.io/fyne/v2/test"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCatalogCompleteAndIndependent(t *testing.T) {
	if len(Styles("")) != 37 || len(Holidays()) != 9 {
		t.Fatal("missing regular style or holiday")
	}
	seen := map[string]bool{}
	for _, s := range Styles("") {
		if seen[s.ID] {
			t.Fatal("duplicate style")
		}
		seen[s.ID] = true
	}
	for _, h := range Holidays() {
		if len(Styles(h.ID)) != 10 {
			t.Fatalf("%s needs ten complete styles", h.ID)
		}
		if h.ID == "birthday" && h.Period != "January 2 - January 20" {
			t.Fatal("birthday dates changed")
		}
		for _, s := range Styles(h.ID) {
			if seen[s.ID] {
				t.Fatal("duplicate style")
			}
			seen[s.ID] = true
		}
	}
	if len(seen) != 127 {
		t.Fatal("wrong total")
	}
	copy := Styles("")
	copy[0].ID = "changed"
	if Styles("")[0].ID != "reactor-forge" {
		t.Fatal("caller mutated catalog")
	}
}

func TestWidgetReadsSameExistingPreferencesWithoutRewriting(t *testing.T) {
	settings := Default()
	settings.Mode = Seasonal
	settings.Holiday = "christmas"
	settings.HolidayStyles["christmas"] = Styles("christmas")[4].ID
	raw, err := appearancecore.Encode(settings)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{preferenceKey: raw, "unrelated-setting": true})
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	read, err := appearancecore.LoadPreferencesFile(path)
	if err != nil {
		t.Fatal(err)
	}
	style, err := read.SelectedStyle()
	if err != nil || style.ID != Styles("christmas")[4].ID {
		t.Fatal("widget preference differs from app")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("widget reader rewrote app preferences")
	}
	for _, bad := range []string{"{", "null", `{"appearance.v1":123}`, `{"appearance.v1":"{"}`} {
		if err = os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = appearancecore.LoadPreferencesFile(path); err == nil {
			t.Fatal("malformed widget preference silently accepted")
		}
	}
}

func TestAutomaticCalendarBoundariesAndOverride(t *testing.T) {
	s := Default()
	s.Mode = Automatic
	for _, tc := range []struct{ date, want string }{
		{"2026-01-01", "new-year"}, {"2026-01-02", "birthday"}, {"2026-01-20", "birthday"}, {"2026-01-21", ""},
		{"2026-02-20", "valentines"}, {"2026-02-21", ""}, {"2024-02-29", ""},
		{"2026-06-14", ""}, {"2026-06-15", "july-fourth"}, {"2026-07-10", "july-fourth"}, {"2026-07-11", ""},
		{"2026-12-27", "christmas"}, {"2026-12-28", "new-year"}, {"2026-12-31", "new-year"},
		{"2026-03-21", ""}, {"2026-03-22", "easter"}, {"2026-04-05", "easter"}, {"2026-04-06", ""},
		{"2024-03-17", "st-patricks"}, {"2024-03-18", "easter"},
		{"2026-11-26", "thanksgiving"}, {"2026-11-27", ""},
	} {
		date, _ := time.Parse("2006-01-02", tc.date)
		if got := s.ActiveHoliday(date); got != tc.want {
			t.Errorf("%s got %q want %q", tc.date, got, tc.want)
		}
	}
	instant := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
	if s.ActiveHoliday(instant) != "birthday" || s.ActiveHoliday(instant.In(time.FixedZone("west", -5*3600))) != "new-year" {
		t.Fatal("device calendar zone ignored")
	}
	a := test.NewApp()
	defer a.Quit()
	s.Mode = Seasonal
	s.Holiday = "christmas"
	s.HolidayStyles["christmas"] = Styles("christmas")[3].ID
	if err := Save(a.Preferences(), s); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(a.Preferences())
	if err != nil {
		t.Fatal(err)
	}
	style, err := loaded.SelectedStyleAt(instant)
	if err != nil || style.ID != s.HolidayStyles["christmas"] {
		t.Fatal("manual override lost across restart")
	}
	loaded.Mode = Normal
	if loaded.ActiveHoliday(instant) != "" {
		t.Fatal("normal override ignored")
	}
	loaded.Mode = Automatic
	if loaded.ActiveHoliday(instant) != "birthday" {
		t.Fatal("automatic not restored")
	}
}

func TestAllPackagedIconsAndChristmasColors(t *testing.T) {
	s := Default()
	palette, _ := s.Palette()
	allStyles := Styles("")
	for _, holiday := range Holidays() {
		allStyles = append(allStyles, Styles(holiday.ID)...)
	}
	for _, style := range allStyles {
		for _, action := range []string{"start", "unlock", "lock"} {
			resource, err := Icon(style, action, palette)
			if err != nil || len(resource.Content()) == 0 {
				t.Fatalf("%s %s missing: %v", style.ID, action, err)
			}
		}
	}
	s.Mode = Automatic
	p, err := s.PaletteAt(time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC))
	if err != nil || p.Accent.G <= p.Accent.R || p.Secondary.R <= p.Secondary.G {
		t.Fatal("automatic Christmas palette lost")
	}
}

func TestPreferencesRoundTripKeepsNormalAndEachHoliday(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	p := a.Preferences()
	original, err := Load(p)
	if err != nil || original.NormalStyle != "reactor-forge" || original.Mode != Normal {
		t.Fatal("wrong defaults")
	}
	draft := original.Clone()
	draft.NormalStyle = "ion-halo"
	draft.Background = "#362149"
	draft.Holiday = "birthday"
	draft.Mode = Seasonal
	draft.HolidayStyles["birthday"] = "birthday-gift-vault"
	if original.HolidayStyles["birthday"] == draft.HolidayStyles["birthday"] {
		t.Fatal("draft mutated saved map")
	}
	if err = Save(p, draft); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := loaded.SelectedStyle()
	if err != nil || selected.ID != "birthday-gift-vault" {
		t.Fatal("birthday selection lost")
	}
	loaded.Mode = Normal
	selected, err = loaded.SelectedStyle()
	if err != nil || selected.ID != "ion-halo" {
		t.Fatal("normal choice lost")
	}
	before := p.String(preferenceKey)
	loaded.HolidayStyles["birthday"] = "christmas-gift-wrap"
	if Save(p, loaded) == nil || p.String(preferenceKey) != before {
		t.Fatal("invalid cross-holiday choice saved")
	}
}

func TestInvalidPreferencesStayVisibleAndUnchanged(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	p := a.Preferences()
	for _, raw := range []string{"{", `{"mode":"unknown"}`, `{"normalStyle":"missing"}`, `{"background":"red"}`, strings.Repeat("x", 8193)} {
		p.SetString(preferenceKey, raw)
		if _, err := Load(p); err == nil || p.String(preferenceKey) != raw {
			t.Fatal("invalid preferences hidden or overwritten")
		}
	}
	s := Default()
	s.Mode = Seasonal
	if s.Validate() == nil {
		t.Fatal("holiday mode allowed without holiday")
	}
}

func TestColorLinksNormalIconsAndPreservesChristmasPalette(t *testing.T) {
	s := Default()
	green, _ := s.Palette()
	s.Background = "#362149"
	purple, _ := s.Palette()
	if green.Background == purple.Background || green.Accent == purple.Accent {
		t.Fatal("background and icon hue not linked")
	}
	for _, v := range []string{"#FFFFFF", "#000000", "#FF0000", "#362149"} {
		s.Background = v
		p, err := s.Palette()
		if err != nil || max(p.Background.R, p.Background.G, p.Background.B) > 28 || p.Foreground.R < 240 {
			t.Fatal("unreadable surface")
		}
	}
	s.Mode = Seasonal
	s.Holiday = "christmas"
	s.Background = "#362149"
	p, err := s.Palette()
	if err != nil || p.Accent.G <= p.Accent.R || p.Secondary.R <= p.Secondary.G {
		t.Fatal("Christmas must retain green and red")
	}
}
