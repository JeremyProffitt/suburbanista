package mobile

import (
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"sync"
	"time"
)

type phoneClock struct {
	sync.RWMutex
	zone *time.Location
	read func() (*time.Location, bool)
}

func (s *Screen) refreshPhoneClock() {
	s.phoneClock.RLock()
	read := s.phoneClock.read
	s.phoneClock.RUnlock()
	if read == nil {
		read = func() (*time.Location, bool) { return platformPhoneZone(s.window) }
	}
	zone, ok := read()
	s.phoneClock.Lock()
	if ok {
		s.phoneClock.zone = zone
	} else {
		s.phoneClock.zone = nil
	}
	s.phoneClock.Unlock()
}
func (s *Screen) phoneCalendarNow(now time.Time) (time.Time, bool) {
	s.phoneClock.RLock()
	zone := s.phoneClock.zone
	s.phoneClock.RUnlock()
	if zone == nil {
		return now, false
	}
	return now.In(zone), true
}
func (s *Screen) displayTime(at time.Time) string {
	s.phoneClock.RLock()
	zone := s.phoneClock.zone
	s.phoneClock.RUnlock()
	return appearancecore.DisplayTime(at, time.Now(), zone)
}
