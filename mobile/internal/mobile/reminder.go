package mobile

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
)

const reminderPreference = "update-reminder-enabled"
const reminderIDPreference = "update-reminder-id"

func (s *Screen) reminderSettings() fyne.CanvasObject {
	var toggle *iconAction
	toggle = newIconAction("Reminder on", theme.MailSendIcon(), func() {
		if s.preview.Load() {
			return
		}
		enabled := !s.app.Preferences().BoolWithFallback(reminderPreference, true)
		s.app.Preferences().SetBool(reminderPreference, enabled)
		if enabled {
			toggle.SetText("Reminder on")
		} else {
			toggle.SetText("Reminder off")
		}
		if !enabled {
			s.cancelUpdateReminder()
		} else {
			s.requestReminderPermission()
		}
	})
	if !s.app.Preferences().BoolWithFallback(reminderPreference, true) {
		toggle.SetText("Reminder off")
	}
	settings := newIconAction("Permission", theme.SettingsIcon(), func() {
		if s.preview.Load() {
			return
		}
		if !platformOpenNotificationSettings(s.window) {
			s.status.SetText("Open Android Settings, Apps, Suburban, Notifications to change permission.")
		}
	})
	return container.NewVBox(container.NewGridWithColumns(2, toggle, settings), wrapped("4h after closing. Reopen after restart."))
}

func (s *Screen) requestReminderPermission() {
	if s.preview.Load() {
		return
	}
	if !s.app.Preferences().BoolWithFallback(reminderPreference, true) {
		return
	}
	s.startupPermissionCheck(false)
}

func (s *Screen) cancelUpdateReminder() {
	id := s.app.Preferences().String(reminderIDPreference)
	if id == "" {
		return
	}
	if s.app.CancelScheduledNotification(id) == nil {
		s.app.Preferences().RemoveValue(reminderIDPreference)
	}
}

func (s *Screen) scheduleUpdateReminder() { s.scheduleReminderAt(time.Now()) }
func (s *Screen) scheduleReminderAt(now time.Time) {
	if s.preview.Load() || s.app == nil {
		return
	}
	if !s.app.Preferences().BoolWithFallback(reminderPreference, true) {
		s.cancelUpdateReminder()
		return
	}
	s.cancelUpdateReminder()
	if !platformNotificationsAllowed(s.window) {
		return
	}
	notification, err := s.app.ScheduleNotification(fyne.NewNotification("Suburban update check", "Open Suburban to check for a signed app update."), now.Add(4*time.Hour))
	if err == nil && notification != nil {
		s.app.Preferences().SetString(reminderIDPreference, notification.ID())
	}
}
