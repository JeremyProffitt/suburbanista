package mobile

import (
	"context"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"git.wow.st/gmp/jni"
	"time"
)

func platformPhoneFix(window fyne.Window, now time.Time) (fix PhoneFix, state string) {
	state = "Phone location unavailable"
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
		activityClass := jni.GetObjectClass(env, activity)
		defer jni.DeleteLocalRef(env, jni.Object(activityClass))
		permission := jni.JavaString(env, "android.permission.ACCESS_COARSE_LOCATION")
		defer jni.DeleteLocalRef(env, jni.Object(permission))
		granted, e := jni.CallIntMethod(env, activity, jni.GetMethodID(env, activityClass, "checkSelfPermission", "(Ljava/lang/String;)I"), jni.Value(permission))
		if e != nil {
			return false
		}
		if granted != 0 {
			state = "Location permission needed"
			return false
		}
		service := jni.JavaString(env, "location")
		defer jni.DeleteLocalRef(env, jni.Object(service))
		manager, e := jni.CallObjectMethod(env, activity, jni.GetMethodID(env, activityClass, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;"), jni.Value(service))
		if e != nil || manager == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, manager)
		managerClass := jni.FindClass(env, "android/location/LocationManager")
		defer jni.DeleteLocalRef(env, jni.Object(managerClass))
		provider := jni.JavaString(env, "network")
		defer jni.DeleteLocalRef(env, jni.Object(provider))
		location, e := jni.CallObjectMethod(env, manager, jni.GetMethodID(env, managerClass, "getLastKnownLocation", "(Ljava/lang/String;)Landroid/location/Location;"), jni.Value(provider))
		if e != nil || location == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, location)
		locationClass := jni.FindClass(env, "android/location/Location")
		defer jni.DeleteLocalRef(env, jni.Object(locationClass))
		mock, e := jni.CallBooleanMethod(env, location, jni.GetMethodID(env, locationClass, "isFromMockProvider", "()Z"))
		if e != nil || mock {
			if mock {
				state = "Phone location provider is simulated"
			}
			return false
		}
		lat, e := jni.CallDoubleMethod(env, location, jni.GetMethodID(env, locationClass, "getLatitude", "()D"))
		if e != nil {
			return false
		}
		lon, e := jni.CallDoubleMethod(env, location, jni.GetMethodID(env, locationClass, "getLongitude", "()D"))
		if e != nil {
			return false
		}
		accuracy, e := jni.CallFloatMethod(env, location, jni.GetMethodID(env, locationClass, "getAccuracy", "()F"))
		if e != nil {
			return false
		}
		stamp, e := jni.CallLongMethod(env, location, jni.GetMethodID(env, locationClass, "getTime", "()J"))
		if e != nil {
			return false
		}
		fix = PhoneFix{Latitude: lat, Longitude: lon, AccuracyMeters: float64(accuracy), ObservedAt: time.UnixMilli(stamp), Allowed: true}
		state = phoneFixState(fix, now)
		if state != "ready" {
			fix = PhoneFix{}
			return false
		}
		state = "ready"
		return true
	})
	return
}

// The fixed action/component are independent of widget commands. Android adds
// its coarse location extra; the private receiver ignores all extras. The
// foreground activity reads only NETWORK_PROVIDER's updated cache.
const phoneFixIntentURI = "intent:#Intent;action=ninja.jeremy.suburbanremote.LOCATION_FIX_READY;component=ninja.jeremy.suburbanremote/nativewidgets.CompactProvider;end"

func platformStartPhoneFix(ctx context.Context, window fyne.Window) (func(), string) {
	if ctx.Err() != nil {
		return nil, "Weather paused"
	}
	native, ok := window.(driver.NativeWindow)
	if !ok {
		return nil, "Phone location unavailable"
	}
	state := "Phone location unavailable"
	started := nativeResult(native, func(value any) bool {
		android, ok := value.(*driver.AndroidWindowContext)
		if !ok || android.Ctx == 0 {
			return false
		}
		env := jni.EnvFor(android.Env)
		activity := jni.Object(android.Ctx)
		ac := jni.GetObjectClass(env, activity)
		defer jni.DeleteLocalRef(env, jni.Object(ac))
		permission := jni.JavaString(env, coarsePermission)
		defer jni.DeleteLocalRef(env, jni.Object(permission))
		grant, err := jni.CallIntMethod(env, activity, jni.GetMethodID(env, ac, "checkSelfPermission", "(Ljava/lang/String;)I"), jni.Value(permission))
		if err != nil {
			return false
		}
		if grant != 0 {
			state = "Location permission needed"
			return false
		}
		manager, err := phoneLocationManager(env, activity)
		if err != nil || manager == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, manager)
		mc := jni.GetObjectClass(env, manager)
		defer jni.DeleteLocalRef(env, jni.Object(mc))
		provider := jni.JavaString(env, "network")
		defer jni.DeleteLocalRef(env, jni.Object(provider))
		enabled, err := jni.CallBooleanMethod(env, manager, jni.GetMethodID(env, mc, "isProviderEnabled", "(Ljava/lang/String;)Z"), jni.Value(provider))
		if err != nil {
			state = "Network location unavailable"
			return false
		}
		if !enabled {
			state = "Phone location is disabled"
			return false
		}
		// Remove a surviving token from a prior process before registering anew.
		if !cancelPhoneFixOnJVM(env, activity, manager) {
			return false
		}
		pending, err := phoneFixPendingIntent(env, activity, false)
		if err != nil || pending == 0 {
			return false
		}
		defer jni.DeleteLocalRef(env, pending)
		if ctx.Err() != nil {
			cancelPhoneFixOnJVM(env, activity, manager)
			state = "Weather paused"
			return false
		}
		err = jni.CallVoidMethod(env, manager, jni.GetMethodID(env, mc, "requestSingleUpdate", "(Ljava/lang/String;Landroid/app/PendingIntent;)V"), jni.Value(provider), jni.Value(pending))
		if err != nil {
			cancelPhoneFixOnJVM(env, activity, manager)
			return false
		}
		return true
	})
	if !started {
		return nil, state
	}
	return func() {
		nativeResult(native, func(value any) bool {
			android, ok := value.(*driver.AndroidWindowContext)
			if !ok || android.Ctx == 0 {
				return false
			}
			env := jni.EnvFor(android.Env)
			manager, err := phoneLocationManager(env, jni.Object(android.Ctx))
			if err != nil || manager == 0 {
				return false
			}
			defer jni.DeleteLocalRef(env, manager)
			return cancelPhoneFixOnJVM(env, jni.Object(android.Ctx), manager)
		})
	}, ""
}

func phoneLocationManager(env jni.Env, activity jni.Object) (jni.Object, error) {
	class := jni.GetObjectClass(env, activity)
	defer jni.DeleteLocalRef(env, jni.Object(class))
	name := jni.JavaString(env, "location")
	defer jni.DeleteLocalRef(env, jni.Object(name))
	return jni.CallObjectMethod(env, activity, jni.GetMethodID(env, class, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;"), jni.Value(name))
}
func phoneFixPendingIntent(env jni.Env, activity jni.Object, existing bool) (jni.Object, error) {
	ic := jni.FindClass(env, "android/content/Intent")
	defer jni.DeleteLocalRef(env, jni.Object(ic))
	uri := jni.JavaString(env, phoneFixIntentURI)
	defer jni.DeleteLocalRef(env, jni.Object(uri))
	intent, err := jni.CallStaticObjectMethod(env, ic, jni.GetStaticMethodID(env, ic, "parseUri", "(Ljava/lang/String;I)Landroid/content/Intent;"), jni.Value(uri), 0)
	if err != nil {
		return 0, err
	}
	defer jni.DeleteLocalRef(env, intent)
	version := jni.FindClass(env, "android/os/Build$VERSION")
	defer jni.DeleteLocalRef(env, jni.Object(version))
	api := jni.GetStaticIntField(env, version, jni.GetStaticFieldID(env, version, "SDK_INT", "I"))
	flags := jni.Value(0x08000000) // UPDATE_CURRENT; fixed action/component cannot be filled in.
	if existing {
		flags = 0x20000000
	} // NO_CREATE
	if api >= 31 {
		flags |= 0x02000000
	} // MUTABLE: Android supplies location extras.
	pc := jni.FindClass(env, "android/app/PendingIntent")
	defer jni.DeleteLocalRef(env, jni.Object(pc))
	return jni.CallStaticObjectMethod(env, pc, jni.GetStaticMethodID(env, pc, "getBroadcast", "(Landroid/content/Context;ILandroid/content/Intent;I)Landroid/app/PendingIntent;"), jni.Value(activity), 7352, jni.Value(intent), flags)
}
func cancelPhoneFixOnJVM(env jni.Env, activity, manager jni.Object) bool {
	pending, err := phoneFixPendingIntent(env, activity, true)
	if err != nil {
		return false
	}
	if pending == 0 {
		return true
	}
	defer jni.DeleteLocalRef(env, pending)
	mc := jni.GetObjectClass(env, manager)
	defer jni.DeleteLocalRef(env, jni.Object(mc))
	removeErr := jni.CallVoidMethod(env, manager, jni.GetMethodID(env, mc, "removeUpdates", "(Landroid/app/PendingIntent;)V"), jni.Value(pending))
	pc := jni.GetObjectClass(env, pending)
	defer jni.DeleteLocalRef(env, jni.Object(pc))
	cancelErr := jni.CallVoidMethod(env, pending, jni.GetMethodID(env, pc, "cancel", "()V"))
	return removeErr == nil && cancelErr == nil
}
