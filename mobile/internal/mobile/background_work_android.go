package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
)

func platformTracking(window fyne.Window, start bool) bool {
	native, ok := window.(driver.NativeWindow)
	if !ok {
		return false
	}
	api := platformAndroidAPI(window)
	return nativeResult(native, func(value any) bool {
		android, ok := value.(*driver.AndroidWindowContext)
		if !ok || android.Ctx == 0 {
			return false
		}
		env := jni.EnvFor(android.Env)
		ic := jni.FindClass(env, "android/content/Intent")
		defer jni.DeleteLocalRef(env, jni.Object(ic))
		uri := jni.JavaString(env, "intent:#Intent;action=ninja.jeremy.suburbanremote.TRACKING_START;component=ninja.jeremy.suburbanremote/nativewidgets.TrackingService;end")
		defer jni.DeleteLocalRef(env, jni.Object(uri))
		intent, err := jni.CallStaticObjectMethod(env, ic, jni.GetStaticMethodID(env, ic, "parseUri", "(Ljava/lang/String;I)Landroid/content/Intent;"), jni.Value(uri), 0)
		if err != nil || intent == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, intent)
		activity := jni.Object(android.Ctx)
		ac := jni.GetObjectClass(env, activity)
		defer jni.DeleteLocalRef(env, jni.Object(ac))
		if !start {
			_, err := jni.CallBooleanMethod(env, activity, jni.GetMethodID(env, ac, "stopService", "(Landroid/content/Intent;)Z"), jni.Value(intent))
			return err == nil
		}
		method := "startService"
		if api >= 26 {
			method = "startForegroundService"
		}
		component, err := jni.CallObjectMethod(env, activity, jni.GetMethodID(env, ac, method, "(Landroid/content/Intent;)Landroid/content/ComponentName;"), jni.Value(intent))
		if component != 0 {
			defer jni.DeleteLocalRef(env, component)
		}
		return err == nil && component != 0
	})
}
