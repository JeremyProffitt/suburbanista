package mobile

import (
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"image"
	"time"
)

// Rotation locking pins appearance only, without changing saved preferences.
func (s *Screen) rotationIsLocked() bool { return s.home != nil && s.home.rotationLocked.Load() }
func (s *Screen) displayAppearance(now time.Time) (appearance.Settings, time.Time, bool) {
	settings, local, available := s.presentationAt(s.appearanceSettings(), now)
	return s.heldAppearance(settings), local, available
}
func (s *Screen) heldAppearance(settings appearance.Settings) appearance.Settings {
	if !s.rotationIsLocked() {
		return settings
	}
	settings = settings.Clone()
	settings.Backdrop = s.home.lockedBackdrop
	settings.BackgroundRotation.Enabled = false
	settings.ButtonRotation.Enabled = false
	style := s.home.lockedStyle
	if style.Holiday == "" {
		settings.Mode = appearance.Normal
		settings.NormalStyle = style.ID
	} else {
		settings.Mode = appearance.Seasonal
		settings.Holiday = style.Holiday
		settings.HolidayStyles[style.Holiday] = style.ID
	}
	return settings
}
func (s *Screen) renderHomeScene(width, height int) image.Image {
	return s.home.renderer.RenderSize(s.home.input, width, height, false).Image
}
func (s *Screen) toggleScreenLock() {
	h := s.home
	if h == nil {
		return
	}
	locked := !h.rotationLocked.Load()
	if locked {
		settings, local, _ := s.presentationAt(s.appearanceSettings(), time.Now())
		style, err := settings.SelectedStyleAt(local)
		if err != nil {
			return
		}
		h.lockedBackdrop, h.lockedStyle = settings.BackdropAt(local), style
	}
	h.rotationLocked.Store(locked)
	button := h.hits["screen-lock"].(*iconAction)
	if locked {
		button.accessibleName = "Resume appearance rotation"
	} else {
		button.accessibleName = "Hold appearance rotation"
	}
	s.paintHome()
}
