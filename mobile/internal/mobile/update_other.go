//go:build !android

package mobile

import "fyne.io/fyne/v2"

// Desktop/headless builds cannot infer an Android device's compatibility.
func platformAndroidAPI(fyne.Window) int { return 0 }
