//go:build !android

package mobile

import "fyne.io/fyne/v2"

func platformNotificationsAllowed(fyne.Window) bool { return false }

func platformOpenNotificationSettings(fyne.Window) bool { return false }
