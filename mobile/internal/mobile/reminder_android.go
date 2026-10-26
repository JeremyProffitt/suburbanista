package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
)

func platformNotificationsAllowed(window fyne.Window) (allowed bool) {
	defer func() {
		if recover() != nil {
			allowed = false
		}
	}()
	api := platformAndroidAPI(window)
	if api < 23 {
		return false
	}
	if api == 23 {
		return true
	}
	native, ok := window.(driver.NativeWindow)
	if !ok {
		return false
	}
	return nativeResult(native, func(value any) bool {
		android, ok := value.(*driver.AndroidWindowContext)
		if !ok {
			return false
		}
		env := jni.EnvFor(android.Env)
		activity := jni.Object(android.Ctx)
		if activity == 0 {
			return false
		}
		activityClass := jni.GetObjectClass(env, activity)
		defer jni.DeleteLocalRef(env, jni.Object(activityClass))
		service := jni.JavaString(env, "notification")
		defer jni.DeleteLocalRef(env, jni.Object(service))
		manager, err := jni.CallObjectMethod(env, activity, jni.GetMethodID(env, activityClass, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;"), jni.Value(service))
		if err != nil || manager == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, manager)
		managerClass := jni.FindClass(env, "android/app/NotificationManager")
		defer jni.DeleteLocalRef(env, jni.Object(managerClass))
		allowed, err := jni.CallBooleanMethod(env, manager, jni.GetMethodID(env, managerClass, "areNotificationsEnabled", "()Z"))
		return err == nil && allowed
	})
}

func platformOpenNotificationSettings(window fyne.Window) (opened bool) {
	defer func() {
		if recover() != nil {
			opened = false
		}
	}()
	native, ok := window.(driver.NativeWindow)
	if !ok {
		return false
	}
	address := "intent:package:ninja.jeremy.suburbanremote#Intent;action=android.settings.APPLICATION_DETAILS_SETTINGS;end"
	if platformAndroidAPI(window) >= 26 {
		address = "intent:#Intent;action=android.settings.APP_NOTIFICATION_SETTINGS;S.android.provider.extra.APP_PACKAGE=ninja.jeremy.suburbanremote;end"
	}
	return nativeResult(native, func(value any) bool {
		android, ok := value.(*driver.AndroidWindowContext)
		if !ok {
			return false
		}
		env := jni.EnvFor(android.Env)
		activity := jni.Object(android.Ctx)
		if activity == 0 {
			return false
		}
		activityClass := jni.GetObjectClass(env, activity)
		defer jni.DeleteLocalRef(env, jni.Object(activityClass))
		intentClass := jni.FindClass(env, "android/content/Intent")
		defer jni.DeleteLocalRef(env, jni.Object(intentClass))
		text := jni.JavaString(env, address)
		defer jni.DeleteLocalRef(env, jni.Object(text))
		intent, err := jni.CallStaticObjectMethod(env, intentClass, jni.GetStaticMethodID(env, intentClass, "parseUri", "(Ljava/lang/String;I)Landroid/content/Intent;"), jni.Value(text), 0)
		if err != nil || intent == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, intent)
		return jni.CallVoidMethod(env, activity, jni.GetMethodID(env, activityClass, "startActivity", "(Landroid/content/Intent;)V"), jni.Value(intent)) == nil
	})
}
