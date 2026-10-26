package mobile

import (
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"time"
)

// calendarWithOffset preserves the instant and attaches the device's offset at
// that instant. It is for calendar selection only, not cached timezone rules.
func calendarWithOffset(now time.Time, milliseconds int, available bool) (time.Time, bool) {
	return appearancecore.CalendarAt(now, milliseconds, available)
}

func (s *Screen) presentationAt(settings appearance.Settings, now time.Time) (appearance.Settings, time.Time, bool) {
	read := s.calendarOffset
	if read == nil {
		local, ok := s.phoneCalendarNow(now)
		if !ok && settings.Mode == appearance.Automatic {
			settings.Mode = appearance.Normal
		}
		return settings, local, ok
	}
	offset, ok := read(now)
	return appearancecore.PresentationAt(settings, now, offset, ok)
}
