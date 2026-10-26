package mobile

import (
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
)

func (s *Screen) refreshWeatherPanel() {
	if s.weatherPanelStatus == nil {
		return
	}
	if s.preview.Load() {
		s.weatherPanelStatus.SetText("Preview uses sample weather.")
		return
	}
	s.environmentMu.Lock()
	state := s.weatherState
	s.environmentMu.Unlock()
	if state == "" {
		state = "Weather has not been checked yet."
	}
	if state == "ready" {
		state = "Weather is available for the phone's approximate location."
	}
	s.weatherPanelStatus.SetText(state)
}
func (s *Screen) showWeatherPanel() {
	s.weatherPanelStatus = wrapped("")
	s.refreshWeatherPanel()
	refresh := newTool("Refresh weather", theme.ViewRefreshIcon(), func() {
		if s.preview.Load() {
			return
		}
		s.weatherPanelStatus.SetText("Refreshing phone location and weather…")
		s.queueEnvironment(true)
	})
	if s.preview.Load() {
		refresh.Disable()
	}
	s.openPanel("Weather", container.NewVBox(s.weatherPanelStatus, wrapped("Uses the phone's approximate location while the app is open."), refresh))
}
