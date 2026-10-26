package mobile

import "fyne.io/fyne/v2/driver"

// nativeResult runs a native call on the thread selected by the framework.
func nativeResult(native driver.NativeWindow, call func(any) bool) (result bool) {
	native.RunNative(func(value any) {
		// RunNative may dispatch onto another goroutine. Recovery must be
		// inside that callback; a defer in the caller cannot protect it.
		defer func() {
			if recover() != nil {
				result = false
			}
		}()
		result = call(value)
	})
	return result
}
