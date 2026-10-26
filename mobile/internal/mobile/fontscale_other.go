//go:build !android

package mobile

import "fyne.io/fyne/v2"

func platformFontScale(fyne.Window) float64 { return 1 }
