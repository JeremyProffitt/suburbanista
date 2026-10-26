//go:build !android

package mobile

import (
	"fyne.io/fyne/v2"
	"time"
)

func platformPhoneZone(fyne.Window) (*time.Location, bool) { return time.Local, true }
