package appearancecore

import (
	"git.wow.st/gmp/jni"
	"time"
)

func DeviceTimeZone(env jni.Env) (zone *time.Location, available bool) {
	defer func() {
		if recover() != nil {
			zone = nil
			available = false
		}
	}()
	class := jni.FindClass(env, "java/util/TimeZone")
	if class == 0 {
		return nil, false
	}
	defer jni.DeleteLocalRef(env, jni.Object(class))
	object, err := jni.CallStaticObjectMethod(env, class, jni.GetStaticMethodID(env, class, "getDefault", "()Ljava/util/TimeZone;"))
	if err != nil || object == 0 {
		return nil, false
	}
	defer jni.DeleteLocalRef(env, object)
	id, err := jni.CallObjectMethod(env, object, jni.GetMethodID(env, class, "getID", "()Ljava/lang/String;"))
	if err != nil || id == 0 {
		return nil, false
	}
	defer jni.DeleteLocalRef(env, id)
	return LoadDeviceZone(jni.GoString(env, jni.String(id)))
}

// DeviceCalendarOffset runs on the caller's attached JNI thread. App and widget
// processes query Android at each refresh rather than relying on Go time.Local.
func DeviceCalendarOffset(env jni.Env, instant time.Time) (offset int, available bool) {
	defer func() {
		if recover() != nil {
			offset = 0
			available = false
		}
	}()
	zoneClass := jni.FindClass(env, "java/util/TimeZone")
	if zoneClass == 0 {
		return 0, false
	}
	defer jni.DeleteLocalRef(env, jni.Object(zoneClass))
	zone, err := jni.CallStaticObjectMethod(env, zoneClass, jni.GetStaticMethodID(env, zoneClass, "getDefault", "()Ljava/util/TimeZone;"))
	if err != nil || zone == 0 {
		return 0, false
	}
	defer jni.DeleteLocalRef(env, zone)
	ms, err := jni.CallIntMethod(env, zone, jni.GetMethodID(env, zoneClass, "getOffset", "(J)I"), jni.Value(instant.UnixMilli()))
	if err != nil {
		return 0, false
	}
	_, valid := CalendarAt(instant, int(ms), true)
	return int(ms), valid
}
