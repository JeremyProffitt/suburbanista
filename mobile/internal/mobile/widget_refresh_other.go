//go:build !android

package mobile

import "fyne.io/fyne/v2"

func platformWidgetsAvailable() bool                   { return false }
func platformRefreshWidgets(fyne.Window) bool          { return false }
func platformSyncWidgets(fyne.Window) bool             { return false }
func platformWidgetBroadcast(fyne.Window, string) bool { return false }
