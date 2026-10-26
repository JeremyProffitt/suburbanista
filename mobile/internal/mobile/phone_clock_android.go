package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"time"
)

func platformPhoneZone(window fyne.Window) (zone *time.Location, ok bool) {
	native, available := window.(driver.NativeWindow)
	if !available {
		return nil, false
	}
	nativeResult(native, func(value any) bool {
		ctx, valid := value.(*driver.AndroidWindowContext)
		if !valid || ctx.Env == 0 {
			return false
		}
		zone, ok = appearancecore.DeviceTimeZone(jni.EnvFor(ctx.Env))
		return ok
	})
	return
}
