package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
)

func platformPermissionInventory(window fyne.Window) (grants []permissionGrant) {
	api := platformAndroidAPI(window)
	grants = []permissionGrant{{Name: coarsePermission, Applicable: api >= 23}, {Name: finePermission, Applicable: api >= 23}, {Name: notificationPermission, Applicable: api >= 33}}
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
		for i := range grants {
			if !grants[i].Applicable {
				grants[i].Known = api > 0
				continue
			}
			permission := jni.JavaString(env, grants[i].Name)
			status, err := jni.CallIntMethod(env, activity, jni.GetMethodID(env, class, "checkSelfPermission", "(Ljava/lang/String;)I"), jni.Value(permission))
			if err == nil {
				grants[i].Granted = status == 0
				grants[i].Rationale, err = jni.CallBooleanMethod(env, activity, jni.GetMethodID(env, class, "shouldShowRequestPermissionRationale", "(Ljava/lang/String;)Z"), jni.Value(permission))
				grants[i].Known = err == nil
			}
			jni.DeleteLocalRef(env, jni.Object(permission))
		}
		return true
	})
	return
}

// A single Android request orders the applicable prompts. Resume rechecks the
// grants but the process-local coordinator prevents a dialog/foreground loop.
func platformRequestPermissions(window fyne.Window, names []string) bool {
	if len(names) == 0 || len(names) > 3 {
		return false
	}
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
		activity := jni.Object(android.Ctx)
		class := jni.GetObjectClass(env, activity)
		defer jni.DeleteLocalRef(env, jni.Object(class))
		strings := jni.FindClass(env, "java/lang/String")
		defer jni.DeleteLocalRef(env, jni.Object(strings))
		permissions := jni.NewObjectArray(env, jni.Size(len(names)), strings, 0)
		defer jni.DeleteLocalRef(env, jni.Object(permissions))
		for i, name := range names {
			if name != coarsePermission && name != finePermission && name != notificationPermission {
				return false
			}
			permission := jni.JavaString(env, name)
			err := jni.SetObjectArrayElement(env, permissions, jni.Size(i), jni.Object(permission))
			jni.DeleteLocalRef(env, jni.Object(permission))
			if err != nil {
				return false
			}
		}
		return jni.CallVoidMethod(env, activity, jni.GetMethodID(env, class, "requestPermissions", "([Ljava/lang/String;I)V"), jni.Value(permissions), 735) == nil
	})
}
