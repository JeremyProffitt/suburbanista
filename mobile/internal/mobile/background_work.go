package mobile

import "fyne.io/fyne/v2"

// Start only from a visible, authenticated Live activity. Android owns the
// ongoing service once it starts; closing the activity does not stop tracking.
func (s *Screen) startBackgroundWork() {
	if s.window == nil || !s.foreground.Load() || s.preview.Load() || s.saved.Session.RefreshHandle == "" {
		return
	}
	platformWidgetBroadcast(s.window, "UPDATE_SCHEDULE")
	for _, grant := range platformPermissionInventory(s.window) {
		if grant.Name == finePermission && grant.Known && grant.Granted {
			platformTracking(s.window, true)
			return
		}
	}
}

func (s *Screen) stopBackgroundWork() {
	if s.window == nil {
		return
	}
	fyne.Do(func() {
		platformTracking(s.window, false)
		platformWidgetBroadcast(s.window, "UPDATE_CANCEL")
	})
}
