package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
)

func platformWidgetsAvailable() bool { return true }
func platformRefreshWidgets(window fyne.Window) bool {
	return platformWidgetBroadcast(window, "WIDGET_REFRESH")
}
func platformSyncWidgets(window fyne.Window) bool {
	return platformWidgetBroadcast(window, "WIDGET_SYNC")
}
func platformWidgetBroadcast(window fyne.Window, action string) bool {
	native, ok := window.(driver.NativeWindow)
	if !ok {
		return false
	}
	return nativeResult(native, func(value any) bool {
		android, ok := value.(*driver.AndroidWindowContext)
		if !ok || android.Ctx == 0 {
			return false
		}
		env := jni.EnvFor(android.Env)
		intentClass := jni.FindClass(env, "android/content/Intent")
		defer jni.DeleteLocalRef(env, jni.Object(intentClass))
		uri := jni.JavaString(env, "intent:#Intent;action=ninja.jeremy.suburbanremote."+action+";component=ninja.jeremy.suburbanremote/nativewidgets.CompactProvider;end")
		defer jni.DeleteLocalRef(env, jni.Object(uri))
		intent, err := jni.CallStaticObjectMethod(env, intentClass, jni.GetStaticMethodID(env, intentClass, "parseUri", "(Ljava/lang/String;I)Landroid/content/Intent;"), jni.Value(uri), 0)
		if err != nil || intent == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, intent)
		activity := jni.Object(android.Ctx)
		activityClass := jni.GetObjectClass(env, activity)
		defer jni.DeleteLocalRef(env, jni.Object(activityClass))
		return jni.CallVoidMethod(env, activity, jni.GetMethodID(env, activityClass, "sendBroadcast", "(Landroid/content/Intent;)V"), jni.Value(intent)) == nil
	})
}
