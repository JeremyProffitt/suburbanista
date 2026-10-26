package appearancecore

import "time"

func CalendarAt(now time.Time, milliseconds int, available bool) (time.Time, bool) {
	if !available || now.IsZero() || milliseconds < -24*60*60*1000 || milliseconds > 24*60*60*1000 || milliseconds%1000 != 0 {
		return time.Time{}, false
	}
	return now.In(time.FixedZone("device", milliseconds/1000)), true
}

// PresentationAt resolves calendar availability without changing saved settings.
// Both app and widgets use Normal when Automatic has no verified device zone.
func PresentationAt(settings Settings, now time.Time, milliseconds int, available bool) (Settings, time.Time, bool) {
	local, ok := CalendarAt(now, milliseconds, available)
	if !ok {
		if settings.Mode == Automatic {
			settings.Mode = Normal
		}
		return settings, now, false
	}
	return settings, local, true
}

// ActiveHoliday uses calendar dates in now's location. An explicit selection
// always wins. When periods overlap, the first period to end wins.
func (s Settings) ActiveHoliday(now time.Time) string {
	if s.Mode == Normal {
		return ""
	}
	if s.Mode == Seasonal {
		return s.Holiday
	}
	if s.Mode != Automatic {
		return ""
	}
	y := now.Year()
	date := func(year int, month time.Month, day int) time.Time {
		return time.Date(year, month, day, 0, 0, 0, 0, now.Location())
	}
	today := date(y, now.Month(), now.Day())
	easter := easterDate(y, now.Location())
	nov := date(y, time.November, 1)
	thanksgiving := nov.AddDate(0, 0, (int(time.Thursday)-int(nov.Weekday())+7)%7+21)
	periods := []struct {
		id         string
		start, end time.Time
	}{
		{"new-year", date(y-1, 12, 28), date(y, 1, 1)},
		{"new-year", date(y, 12, 28), date(y+1, 1, 1)},
		{"birthday", date(y, 1, 2), date(y, 1, 20)},
		{"valentines", date(y, 2, 1), date(y, 2, 20)},
		{"st-patricks", date(y, 3, 1), date(y, 3, 17)},
		{"easter", easter.AddDate(0, 0, -14), easter},
		{"july-fourth", date(y, 6, 15), date(y, 7, 10)},
		{"halloween", date(y, 10, 1), date(y, 10, 31)},
		{"thanksgiving", nov, thanksgiving},
		{"christmas", date(y, 12, 1), date(y, 12, 27)},
	}
	selected := ""
	var end time.Time
	for _, p := range periods {
		if !today.Before(p.start) && !today.After(p.end) && (selected == "" || p.end.Before(end)) {
			selected, end = p.id, p.end
		}
	}
	return selected
}

// Gregorian computus, using integer calendar arithmetic.
func easterDate(year int, location *time.Location) time.Time {
	a, b, c := year%19, year/100, year%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	n := h + l - 7*m + 114
	return time.Date(year, time.Month(n/31), n%31+1, 0, 0, 0, 0, location)
}
