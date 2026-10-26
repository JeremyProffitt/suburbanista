package mobile

import (
	"context"
	"fyne.io/fyne/v2"
	"time"
)

// Weather does not occupy the owner's command/login busy guard. This separate
// single-flight job is tied only to the foreground and is cancelled by Preview.
func (s *Screen) queueEnvironment(explicit bool) {
	if s.preview.Load() || s.app == nil || s.window == nil {
		return
	}
	ctx, session, done, active := s.phoneLocation.context(context.Background())
	if !active {
		return
	}
	go func() { defer done(); s.refreshEnvironment(ctx, session, explicit) }()
}
func (s *Screen) refreshEnvironment(ctx context.Context, session context.Context, explicit bool) {
	if s.preview.Load() || ctx.Err() != nil {
		return
	}
	now := time.Now()
	if explicit {
		s.startupPermissionCheck(true)
	}
	fix, state := acquirePhoneLocation(ctx, phoneLocationSource{
		read: func(at time.Time) (PhoneFix, string) { return platformPhoneFix(s.window, at) },
		start: func(requestCtx context.Context) (func(), string) {
			finish, ok := s.phoneLocation.begin(session, time.Now(), explicit)
			if !ok {
				return nil, "Phone location refresh is waiting"
			}
			stop, state := platformStartPhoneFix(requestCtx, s.window)
			if stop == nil {
				finish()
				return nil, state
			}
			return func() { stop(); finish() }, ""
		},
	}, time.Now, waitForPhoneFix)
	if ctx.Err() != nil {
		return
	}
	now = time.Now()
	if state != "ready" {
		s.environmentMu.Lock()
		s.weatherState = state
		s.environmentMu.Unlock()
		fyne.Do(func() { s.paintHome(); s.refreshWeatherPanel() })
		return
	}
	cell := approximateComponent(fix.Latitude) + ":" + approximateComponent(fix.Longitude)
	s.environmentMu.Lock()
	cached := s.weatherData
	sameCell := cell == s.weatherCell
	s.environmentMu.Unlock()
	if !explicit && sameCell && cached != nil && now.Sub(cached.FetchedAt) < 15*time.Minute {
		s.environmentMu.Lock()
		s.weatherState = "ready"
		s.environmentMu.Unlock()
		fyne.Do(func() { s.paintHome(); s.refreshWeatherPanel() })
		return
	}
	if s.weatherClient == nil {
		s.weatherClient = newWeatherClient()
	}
	weather, e := s.weatherClient.Fetch(ctx, fix, now)
	if ctx.Err() != nil {
		return
	}
	s.environmentMu.Lock()
	if e != nil {
		s.weatherState = "Weather unavailable"
	} else {
		s.weatherData = &weather
		s.weatherState = "ready"
		s.weatherCell = cell
	}
	s.environmentMu.Unlock()
	fyne.Do(func() { s.paintHome(); s.refreshWeatherPanel() })
}
func (s *Screen) startEnvironmentClock() {
	if s.app == nil {
		return
	}
	s.startupPermissionCheck(false)
	s.environmentMu.Lock()
	if s.environmentCancel != nil {
		s.environmentMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.environmentCancel = cancel
	s.environmentMu.Unlock()
	s.phoneLocation.open()
	fyne.Do(func() { s.paintHome(); s.refreshWeatherPanel() })
	s.queueEnvironment(false)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		var ticks uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ticks++
				tick := ticks
				fyne.Do(func() { s.paintHome(); s.refreshWeatherPanel(); s.refreshMapOnClock(tick) })
				if !s.preview.Load() {
					s.queueEnvironment(false)
				}
			}
		}
	}()
}
func (s *Screen) stopEnvironmentClock() {
	s.phoneLocation.close()
	s.environmentMu.Lock()
	if s.environmentCancel != nil {
		s.environmentCancel()
		s.environmentCancel = nil
	}
	s.environmentMu.Unlock()
}
func (s *Screen) refreshHomeTelemetry(ctx context.Context, refresh bool) {
	if s.preview.Load() || s.home == nil || s.saved.Session.AccessHandle == "" {
		return
	}
	snapshot, e := s.client.VehicleRead(ctx, s.saved.Session, "diagnostics", refresh)
	if e != nil {
		return
	}
	readings := telemetryFromSnapshot(snapshot, verifiedTelemetryBindings)
	fyne.Do(func() { s.homeTelemetry = readings; s.paintHome() })
}
