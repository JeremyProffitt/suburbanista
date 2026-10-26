//go:build !android

package mobile

import (
	"errors"
	"fyne.io/fyne/v2"
)

func DeviceCrypt(fyne.Window) func([]byte, bool) ([]byte, error) {
	return func([]byte, bool) ([]byte, error) { return nil, errors.New("Android Keystore is required.") }
}
