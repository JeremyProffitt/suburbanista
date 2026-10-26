package mobile

import (
	"errors"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/keystore"
)

func DeviceCrypt(window fyne.Window) func([]byte, bool) ([]byte, error) {
	return func(input []byte, encrypt bool) (output []byte, err error) {
		native, ok := window.(driver.NativeWindow)
		if !ok {
			return nil, errors.New("Keystore unavailable")
		}
		err = errors.New("Keystore unavailable")
		native.RunNative(func(ctx any) {
			android, ok := ctx.(*driver.AndroidWindowContext)
			if !ok {
				return
			}
			output, err = keystore.Crypt(jni.EnvFor(android.Env), input, encrypt)
		})
		return
	}
}
