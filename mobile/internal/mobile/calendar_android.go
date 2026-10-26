package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"time"
)

// Query Android for every presentation refresh so timezone and DST changes do
// not wait for process restart. Go's Android time.Local is UTC, not device time.
func platformCalendarOffset(window fyne.Window, instant time.Time) (offset int, available bool) {
	native, ok := window.(driver.NativeWindow)
	if !ok {
		return 0, false
	}
	available = nativeResult(native, func(value any) bool {
		android, ok := value.(*driver.AndroidWindowContext)
		if !ok || android.Env == 0 {
			return false
		}
		var valid bool
		offset, valid = appearancecore.DeviceCalendarOffset(jni.EnvFor(android.Env), instant)
		return valid
	})
	return offset, available
}
