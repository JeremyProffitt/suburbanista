//go:build !android

package mobile

import (
	"fyne.io/fyne/v2"
	"time"
)

func platformCalendarOffset(_ fyne.Window, instant time.Time) (int, bool) {
	_, offset := instant.In(time.Local).Zone()
	return offset * 1000, true
}
