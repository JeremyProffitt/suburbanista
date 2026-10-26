package appearancecore

import (
	"strings"
	"testing"
	"time"
)

func TestRotationIndependentAndStable(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s := Default()
	s.BackgroundRotation.Selected = []string{"mountains", "charlotte", "beach"}
	s.BackgroundRotation.Enabled = true
	s.BackgroundRotation.Epoch = now
	if len(Backdrops()) != 29 || s.BackgroundRotation.Minutes != 60 || s.ButtonRotation.Minutes != 60 {
		t.Fatal("catalog/default interval")
	}
	for i, want := range []string{"charlotte", "beach", "mountains", "charlotte"} {
		if got := s.BackdropAt(now.Add(time.Duration(i) * time.Hour)); got != want {
			t.Fatalf("step %d: %s", i, got)
		}
	}
	before, _ := s.SelectedStyleAt(now)
	after, _ := s.SelectedStyleAt(now.Add(3 * time.Hour))
	if before != after {
		t.Fatal("background changed buttons")
	}
	s.NavigateBackground(-1, now)
	if s.Backdrop != "mountains" || !s.BackgroundRotation.Epoch.Equal(now) {
		t.Fatal("manual wrap/reset")
	}
	s.BackgroundRotation.Enabled = false
	s.NavigateBackground(1, now)
	if s.Backdrop != "charlotte" {
		t.Fatal("manual navigation requires timer")
	}
}
func TestOpeningIndependentAndTimerCoalesced(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for _, bg := range []bool{false, true} {
		for _, buttons := range []bool{false, true} {
			s := Default()
			s.BackgroundRotation.Selected = []string{"charlotte", "beach", "mountains"}
			s.ButtonRotation.Selected = []string{Styles("")[0].ID, Styles("")[1].ID}
			s.BackgroundRotation.OnOpening = bg
			s.ButtonRotation.OnOpening = buttons
			s.Open("first", now)
			if (s.Backdrop == "beach") != bg {
				t.Fatal("background opening coupling")
			}
			if (s.NormalStyle == Styles("")[1].ID) != buttons {
				t.Fatal("button opening coupling")
			}
			copy := s.Clone()
			if s.Open("first", now.Add(time.Second)) {
				t.Fatal("duplicate callback advanced")
			}
			if s.Backdrop != copy.Backdrop || s.NormalStyle != copy.NormalStyle {
				t.Fatal("duplicate callback mutated")
			}
		}
	}
	s := Default()
	s.BackgroundRotation.Selected = []string{"charlotte", "beach", "mountains"}
	s.BackgroundRotation.Enabled = true
	s.BackgroundRotation.OnOpening = true
	s.BackgroundRotation.Epoch = now
	s.Open("due", now.Add(time.Hour))
	if s.Backdrop != "beach" {
		t.Fatal("opening doubled coincident timed advance")
	}
	if !s.BackgroundRotation.Epoch.Equal(now.Add(time.Hour)) {
		t.Fatal("opening did not restart timer")
	}
	s.BackgroundRotation.Selected = []string{"charlotte"}
	s.Open("single", now.Add(2*time.Hour))
	if s.Backdrop != "charlotte" {
		t.Fatal("single list changed")
	}
}
func TestRotationDraftHolidayListsAndMigration(t *testing.T) {
	s := Default()
	draft := s.Clone()
	draft.BackgroundRotation.Selected[0] = "beach"
	draft.HolidayRotation["christmas"][0] = Styles("christmas")[1].ID
	if s.BackgroundRotation.Selected[0] != "charlotte" || s.HolidayRotation["christmas"][0] != Styles("christmas")[0].ID {
		t.Fatal("draft leaked")
	}
	s.ButtonRotation.Enabled = true
	s.Mode = Seasonal
	s.Holiday = "christmas"
	s.HolidayRotation["christmas"] = []string{Styles("christmas")[0].ID, Styles("christmas")[1].ID}
	now := time.Now()
	s.NavigateButtons(1, now)
	if s.HolidayStyles["christmas"] != Styles("christmas")[1].ID || s.NormalStyle != "reactor-forge" {
		t.Fatal("holiday navigation contaminated normal")
	}
	s.Holiday = "easter"
	s.NavigateButtons(1, now)
	s.Holiday = "christmas"
	if len(s.HolidayRotation["christmas"]) != 2 {
		t.Fatal("holiday list lost")
	}
	raw, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil || decoded.HolidayStyles["christmas"] != s.HolidayStyles["christmas"] {
		t.Fatal("roundtrip", err)
	}
	s.BackgroundRotation.Selected = nil
	if s.Validate() == nil {
		t.Fatal("empty list saved")
	}
	raw, _ = Encode(Default())
	raw = strings.Replace(raw, `"minutes":60`, `"minutes":15`, 1)
	decoded, err = Decode(raw)
	if err != nil || decoded.BackgroundRotation.Minutes != 15 {
		t.Fatal("explicit interval overwritten")
	}
}
