package appearancecore

import (
	"errors"
	"fmt"
	"time"
)

// Rotation holds a fixed epoch. Readers derive the same selection without a
// widget independently advancing the preference or issuing a network request.
type Rotation struct {
	Enabled     bool      `json:"enabled"`
	OnOpening   bool      `json:"onOpening"`
	Minutes     int       `json:"minutes"`
	Selected    []string  `json:"selected"`
	Epoch       time.Time `json:"epoch,omitempty"`
	LastOpening string    `json:"lastOpening,omitempty"`
}
type Backdrop struct{ ID, Name string }

func Backdrops() []Backdrop {
	out := []Backdrop{{"charlotte", "Charlotte"}, {"beach", "Beach"}, {"mountains", "Mountains"}, {"country", "Country"}}
	for _, b := range []Backdrop{{"neon-harbor", "Neon Harbor"}, {"sky-gardens", "Sky Gardens"}, {"orbital-gateway", "Orbital Gateway"}, {"crystal-spires", "Crystal Spires"}, {"solar-arcology", "Solar Arcology"}, {"floating-metropolis", "Floating Metropolis"}, {"desert-oasis", "Desert Oasis"}, {"aurora-city", "Aurora City"}, {"ocean-platforms", "Ocean Platforms"}, {"lunar-colony", "Lunar Colony"}} {
		out = append(out, b)
	}
	for i := 1; i <= 10; i++ {
		out = append(out, Backdrop{fmt.Sprintf("grand-canyon-%02d", i), fmt.Sprintf("Grand Canyon %d", i)})
	}
	for i := 1; i <= 5; i++ {
		out = append(out, Backdrop{fmt.Sprintf("yosemite-%02d", i), fmt.Sprintf("Yosemite %d", i)})
	}
	return out
}
func contains(list []string, id string) bool {
	for _, s := range list {
		if s == id {
			return true
		}
	}
	return false
}
func ordered(selected, catalog []string) []string {
	out := []string{}
	for _, id := range catalog {
		if contains(selected, id) {
			out = append(out, id)
		}
	}
	return out
}
func (s Settings) BackgroundSelection() []string {
	ids := []string{}
	for _, b := range Backdrops() {
		ids = append(ids, b.ID)
	}
	return ordered(s.BackgroundRotation.Selected, ids)
}
func (s Settings) styleSelection(holiday string) []string {
	ids := []string{}
	for _, b := range Styles(holiday) {
		ids = append(ids, b.ID)
	}
	selected := s.ButtonRotation.Selected
	if holiday != "" {
		selected = s.HolidayRotation[holiday]
	}
	return ordered(selected, ids)
}
func (s Settings) ButtonSelection(now time.Time) []string {
	return s.styleSelection(s.ActiveHoliday(now))
}
func (r Rotation) Current(list []string, base string, now time.Time) string {
	if !r.Enabled {
		return base
	}
	if len(list) == 0 {
		return base
	}
	index := 0
	for i, id := range list {
		if id == base {
			index = i
			break
		}
	}
	if r.Enabled && r.Minutes > 0 && !r.Epoch.IsZero() && now.After(r.Epoch) {
		index = (index + int(now.Sub(r.Epoch)/(time.Duration(r.Minutes)*time.Minute))%len(list)) % len(list)
	}
	return list[index]
}
func next(list []string, current string, direction int) string {
	if len(list) == 0 {
		return current
	}
	index := 0
	for i, id := range list {
		if id == current {
			index = i
			break
		}
	}
	index = (index + direction) % len(list)
	if index < 0 {
		index += len(list)
	}
	return list[index]
}
func (s Settings) BackdropAt(now time.Time) string {
	return s.BackgroundRotation.Current(s.BackgroundSelection(), s.Backdrop, now)
}
func (s *Settings) NavigateBackground(direction int, now time.Time) {
	s.Backdrop = next(s.BackgroundSelection(), s.BackdropAt(now), direction)
	s.BackgroundRotation.Epoch = now
}
func (s *Settings) NavigateButtons(direction int, now time.Time) {
	holiday := s.ActiveHoliday(now)
	base := s.NormalStyle
	if holiday != "" {
		base = s.HolidayStyles[holiday]
	}
	id := next(s.styleSelection(holiday), s.ButtonRotation.Current(s.styleSelection(holiday), base, now), direction)
	if holiday == "" {
		s.NormalStyle = id
	} else {
		s.HolidayStyles[holiday] = id
	}
	s.ButtonRotation.Epoch = now
}

// Open advances once from the saved base, coalescing any simultaneous timer
// expiry into that same advance. The caller supplies one id per genuine opening.
func (s *Settings) Open(id string, now time.Time) bool {
	changed := false
	if s.BackgroundRotation.OnOpening && id != "" && s.BackgroundRotation.LastOpening != id {
		s.Backdrop = s.BackgroundRotation.openSelection(s.BackgroundSelection(), s.Backdrop, now)
		s.BackgroundRotation.Epoch = now
		s.BackgroundRotation.LastOpening = id
		changed = true
	}
	if s.ButtonRotation.OnOpening && id != "" && s.ButtonRotation.LastOpening != id {
		holiday := s.ActiveHoliday(now)
		base := s.NormalStyle
		if holiday != "" {
			base = s.HolidayStyles[holiday]
		}
		value := s.ButtonRotation.openSelection(s.styleSelection(holiday), base, now)
		if holiday == "" {
			s.NormalStyle = value
		} else {
			s.HolidayStyles[holiday] = value
		}
		s.ButtonRotation.Epoch = now
		s.ButtonRotation.LastOpening = id
		changed = true
	}
	return changed
}
func (r Rotation) openSelection(list []string, base string, now time.Time) string {
	if r.Enabled && r.Minutes > 0 && !r.Epoch.IsZero() && now.Sub(r.Epoch) >= time.Duration(r.Minutes)*time.Minute {
		return r.Current(list, base, now)
	}
	return next(list, base, 1)
}
func validateList(list, catalog []string) bool {
	if len(list) == 0 || len(list) > len(catalog) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range list {
		if seen[id] || !contains(catalog, id) {
			return false
		}
		seen[id] = true
	}
	return true
}
func (s Settings) validateRotation() error {
	ids := []string{}
	for _, b := range Backdrops() {
		ids = append(ids, b.ID)
	}
	if !contains(ids, s.Backdrop) || !validateList(s.BackgroundRotation.Selected, ids) {
		return errors.New("choose at least one valid background")
	}
	for _, r := range []Rotation{s.BackgroundRotation, s.ButtonRotation} {
		if r.Minutes < 1 || r.Minutes > 1440 || len(r.LastOpening) > 80 {
			return errors.New("rotation interval must be 1 to 1440 minutes")
		}
	}
	ids = nil
	for _, v := range Styles("") {
		ids = append(ids, v.ID)
	}
	if !validateList(s.ButtonRotation.Selected, ids) {
		return errors.New("choose at least one normal button style")
	}
	if len(s.HolidayRotation) != len(holidays) {
		return errors.New("holiday rotation choices are incomplete")
	}
	for _, h := range holidays {
		ids = nil
		for _, v := range Styles(h.ID) {
			ids = append(ids, v.ID)
		}
		if !validateList(s.HolidayRotation[h.ID], ids) {
			return errors.New("choose at least one style for each holiday")
		}
	}
	return nil
}
