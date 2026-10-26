package appearancecore

import (
	"time"
	_ "time/tzdata"
)

// LoadDeviceZone uses the phone's IANA identity, including historical DST rules.
// Android's Go time.Local is not used as a substitute for the phone setting.
func LoadDeviceZone(name string) (*time.Location, bool) {
	if name == "" {
		return nil, false
	}
	zone, err := time.LoadLocation(name)
	return zone, err == nil
}
func DisplayTime(at, now time.Time, zone *time.Location) string {
	if at.IsZero() {
		return "Time unknown"
	}
	if zone == nil {
		return "Time unavailable"
	}
	at = at.In(zone)
	now = now.In(zone)
	if at.Year() != now.Year() {
		return at.Format("Jan 2 2006, 3:04 PM")
	}
	if at.YearDay() != now.YearDay() {
		return at.Format("Jan 2, 3:04 PM")
	}
	return at.Format("3:04 PM")
}
