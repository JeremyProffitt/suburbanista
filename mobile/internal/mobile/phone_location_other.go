//go:build !android

package mobile

import (
	"context"
	"fyne.io/fyne/v2"
	"time"
)

func platformPhoneFix(fyne.Window, time.Time) (PhoneFix, string) {
	return PhoneFix{}, "Phone location unavailable"
}

func platformStartPhoneFix(context.Context, fyne.Window) (func(), string) {
	return nil, "Phone location unavailable"
}
