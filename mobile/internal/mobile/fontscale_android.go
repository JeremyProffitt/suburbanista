package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
)

func platformFontScale(window fyne.Window) (scale float64) {
	scale = 1
	native, ok := window.(driver.NativeWindow)
	if !ok {
		return
	}
	nativeResult(native, func(value any) bool {
		android, ok := value.(*driver.AndroidWindowContext)
		if !ok || android.Ctx == 0 {
			return false
		}
		env := jni.EnvFor(android.Env)
		activity := jni.Object(android.Ctx)
		class := jni.GetObjectClass(env, activity)
		defer jni.DeleteLocalRef(env, jni.Object(class))
		resources, err := jni.CallObjectMethod(env, activity, jni.GetMethodID(env, class, "getResources", "()Landroid/content/res/Resources;"))
		if err != nil || resources == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, resources)
		rc := jni.GetObjectClass(env, resources)
		defer jni.DeleteLocalRef(env, jni.Object(rc))
		config, err := jni.CallObjectMethod(env, resources, jni.GetMethodID(env, rc, "getConfiguration", "()Landroid/content/res/Configuration;"))
		if err != nil || config == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, config)
		cc := jni.GetObjectClass(env, config)
		defer jni.DeleteLocalRef(env, jni.Object(cc))
		factor := jni.GetFloatField(env, config, jni.GetFieldID(env, cc, "fontScale", "F"))
		if factor > 0 {
			scale = float64(factor)
		}
		return true
	})
	return
}
