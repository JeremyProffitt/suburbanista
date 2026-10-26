//go:build !android

package mobile

import "fyne.io/fyne/v2"

func platformTracking(fyne.Window, bool) bool { return false }
