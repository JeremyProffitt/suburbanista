package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
)

func platformAndroidAPI(window fyne.Window) (api int) {
	defer func() {
		if recover() != nil {
			api = 0
		}
	}()
	native, ok := window.(driver.NativeWindow)
	if !ok {
		return 0
	}
	native.RunNative(func(value any) {
		android, ok := value.(*driver.AndroidWindowContext)
		if !ok {
			return
		}
		env := jni.EnvFor(android.Env)
		version := jni.FindClass(env, "android/os/Build$VERSION")
		defer jni.DeleteLocalRef(env, jni.Object(version))
		api = int(jni.GetStaticIntField(env, version, jni.GetStaticFieldID(env, version, "SDK_INT", "I")))
	})
	return api
}
