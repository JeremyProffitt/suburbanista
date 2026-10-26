//go:build android && widgetbind

package nativewidgets

import (
	androidapp "Java/android/app"
	"Java/android/content"
	androidos "Java/android/os"
	"Java/go/Seq"
	self "Java/nativewidgets"
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"time"

	"git.wow.st/gmp/jni"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/ownerclient"
	"golang.org/x/mobile/app"
)

const trackingStart = "ninja.jeremy.suburbanremote.TRACKING_START"
const trackingFix = "ninja.jeremy.suburbanremote.TRACKING_FIX"

// The generated service has its own Android process. It never links the Fyne
// activity runtime and receives only explicit, nonexported location deliveries.
type TrackingService struct {
	androidapp.Service
	mu      sync.Mutex
	cancel  context.CancelFunc
	working bool
}

func NewTrackingService() *TrackingService                       { return &TrackingService{} }
func (*TrackingService) OnBind(content.Intent) androidos.IBinder { return nil }
func (s *TrackingService) OnStartCommand(this self.TrackingService, intent content.Intent, flags, startID int32) int32 {
	if intent == nil || (intent.GetAction() != trackingStart && intent.GetAction() != trackingFix) {
		this.StopSelf()
		return 2
	}
	initializeContext.Do(func() { Seq.SetContext(this.GetApplicationContext()) })
	if err := trackingServiceJVM(this, func(n *native, service jni.Object) error {
		if n.intCall(service, "checkSelfPermission", "(Ljava/lang/String;)I", n.str("android.permission.ACCESS_FINE_LOCATION")) != 0 {
			return errors.New("Precise location permission required")
		}
		notice := backgroundNotification(n, service, "suburban-tracking", "Location tracking", "Location tracking is active. Open Suburbanista to manage it.", true)
		n.void(service, "startForeground", "(ILandroid/app/Notification;)V", 7301, jni.Value(notice))
		manager := n.call(service, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;", n.str("location"))
		pending := trackingPending(n, service)
		n.void(manager, "requestLocationUpdates", "(Ljava/lang/String;JFLandroid/app/PendingIntent;)V", n.str("gps"), 120000, jni.Value(math.Float32bits(15.24)), jni.Value(pending))
		return nil
	}); err != nil {
		this.StopSelf()
		return 2
	}
	s.mu.Lock()
	if s.working {
		s.mu.Unlock()
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	s.cancel = cancel
	s.working = true
	s.mu.Unlock()
	releaseWake, err := trackingWake()
	if err != nil {
		cancel()
		s.mu.Lock()
		s.cancel = nil
		s.working = false
		s.mu.Unlock()
		this.StopSelf()
		return 2
	}
	go func() {
		defer releaseWake()
		defer func() { cancel(); s.mu.Lock(); s.cancel = nil; s.working = false; s.mu.Unlock() }()
		root, err := privateRoot()
		if err != nil {
			return
		}
		vault := ownerclient.Vault{Path: filepath.Join(root, "owner-session.enc"), Crypt: crypt}
		sample, err := trackingSample()
		if err != nil {
			return
		}
		_, err = ownerclient.UploadPhone(ctx, vault, ownerclient.NewClient(), sample, time.Now())
		if errors.Is(err, ownerclient.ErrUnauthorized) {
			this.StopSelf()
		}
	}()
	return 2 // START_NOT_STICKY: reopening is required after force-stop/reboot.
}
func (s *TrackingService) OnDestroy(this self.TrackingService) {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = trackingServiceJVM(this, func(n *native, service jni.Object) error {
		manager := n.call(service, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;", n.str("location"))
		pending := trackingPending(n, service)
		n.void(manager, "removeUpdates", "(Landroid/app/PendingIntent;)V", jni.Value(pending))
		n.void(pending, "cancel", "()V")
		return nil
	})
	this.Super().OnDestroy()
}

func trackingJVM(fn func(*native, jni.Object) error) error {
	return app.RunOnJVM(func(_, env, ctx uintptr) (err error) {
		defer func() {
			if recover() != nil {
				err = errors.New("Android background operation unavailable")
			}
		}()
		n := newNative(jni.EnvFor(env))
		defer n.close()
		return fn(n, jni.Object(ctx))
	})
}

// Hold CPU only for the bounded upload, releasing promptly on every outcome.
// The platform timeout is a second bound if the Go worker cannot finish.
func trackingWake() (func(), error) {
	var lock jni.Object
	err := trackingJVM(func(n *native, ctx jni.Object) error {
		power := n.call(ctx, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;", n.str("power"))
		local := n.call(power, "newWakeLock", "(ILjava/lang/String;)Landroid/os/PowerManager$WakeLock;", 1, n.str("Suburbanista:location-upload"))
		n.void(local, "acquire", "(J)V", 25000)
		lock = jni.NewGlobalRef(n.env, local)
		if lock == 0 {
			return errors.New("Location wake unavailable")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sync.OnceFunc(func() {
		_ = trackingJVM(func(n *native, _ jni.Object) error {
			defer jni.DeleteGlobalRef(n.env, lock)
			c := n.class("android/os/PowerManager$WakeLock")
			held, e := jni.CallBooleanMethod(n.env, lock, jni.GetMethodID(n.env, c, "isHeld", "()Z"))
			if e != nil {
				return e
			}
			if held {
				n.void(lock, "release", "()V")
			}
			return nil
		})
	}), nil
}

// Keep one Application context; each Service reference is explicitly balanced.
func trackingServiceJVM(service self.TrackingService, fn func(*native, jni.Object) error) error {
	refnum := Seq.IncRef(service)
	return trackingJVM(func(n *native, context jni.Object) error {
		// RunOnJVM attaches a native thread with no application Java frame.
		// FindClass would use the system loader and cannot find APK classes.
		loader := n.call(context, "getClassLoader", "()Ljava/lang/ClassLoader;")
		seq := jni.Class(n.call(loader, "loadClass", "(Ljava/lang/String;)Ljava/lang/Class;", n.str("go.Seq")))
		defer jni.CallStaticVoidMethod(n.env, seq, jni.GetStaticMethodID(n.env, seq, "decRef", "(I)V"), jni.Value(refnum))
		ref := n.must(jni.CallStaticObjectMethod(n.env, seq, jni.GetStaticMethodID(n.env, seq, "getRef", "(I)Lgo/Seq$Ref;"), jni.Value(refnum)))
		refClass := jni.GetObjectClass(n.env, ref)
		n.keep(jni.Object(refClass))
		object := n.keep(jni.GetObjectField(n.env, ref, jni.GetFieldID(n.env, refClass, "obj", "Ljava/lang/Object;")))
		if object == 0 {
			return errors.New("Tracking service unavailable")
		}
		return fn(n, object)
	})
}

func androidAPI(n *native) int {
	c := n.class("android/os/Build$VERSION")
	return int(jni.GetStaticIntField(n.env, c, jni.GetStaticFieldID(n.env, c, "SDK_INT", "I")))
}
func trackingPending(n *native, ctx jni.Object) jni.Object {
	intent := n.static("android/content/Intent", "parseUri", "(Ljava/lang/String;I)Landroid/content/Intent;", n.str("intent:#Intent;action="+trackingFix+";component=ninja.jeremy.suburbanremote/nativewidgets.TrackingService;end"), 0)
	flags := 0x08000000
	if androidAPI(n) >= 31 {
		flags |= 0x02000000
	}
	return n.static("android/app/PendingIntent", "getService", "(Landroid/content/Context;ILandroid/content/Intent;I)Landroid/app/PendingIntent;", jni.Value(ctx), 7301, jni.Value(intent), jni.Value(flags))
}
func backgroundNotification(n *native, ctx jni.Object, channel, title, text string, ongoing bool) jni.Object {
	manager := n.call(ctx, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;", n.str("notification"))
	var builder jni.Object
	if androidAPI(n) >= 26 {
		ch := n.new("android/app/NotificationChannel", []jni.Object{jni.Object(n.class("java/lang/String")), jni.Object(n.class("java/lang/CharSequence")), n.intType()}, []jni.Object{jni.Object(n.str(channel)), jni.Object(n.str(title)), n.integer(2)})
		n.void(manager, "createNotificationChannel", "(Landroid/app/NotificationChannel;)V", jni.Value(ch))
		builder = n.new("android/app/Notification$Builder", []jni.Object{jni.Object(n.class("android/content/Context")), jni.Object(n.class("java/lang/String"))}, []jni.Object{ctx, jni.Object(n.str(channel))})
	} else {
		builder = n.new("android/app/Notification$Builder", []jni.Object{jni.Object(n.class("android/content/Context"))}, []jni.Object{ctx})
	}
	application := n.call(ctx, "getApplicationInfo", "()Landroid/content/pm/ApplicationInfo;")
	applicationClass := n.class("android/content/pm/ApplicationInfo")
	icon := jni.GetIntField(n.env, application, jni.GetFieldID(n.env, applicationClass, "icon", "I"))
	if icon == 0 {
		panic("Application notification icon unavailable")
	}
	n.call(builder, "setSmallIcon", "(I)Landroid/app/Notification$Builder;", jni.Value(icon))
	n.call(builder, "setContentTitle", "(Ljava/lang/CharSequence;)Landroid/app/Notification$Builder;", n.str(title))
	n.call(builder, "setContentText", "(Ljava/lang/CharSequence;)Landroid/app/Notification$Builder;", n.str(text))
	flag := jni.Value(0)
	if ongoing {
		flag = 1
	}
	n.call(builder, "setOngoing", "(Z)Landroid/app/Notification$Builder;", flag)
	n.call(builder, "setOnlyAlertOnce", "(Z)Landroid/app/Notification$Builder;", 1)
	intent := n.static("android/content/Intent", "parseUri", "(Ljava/lang/String;I)Landroid/content/Intent;", n.str("intent:#Intent;component=ninja.jeremy.suburbanremote/org.golang.app.GoNativeActivity;launchFlags=0x14000000;end"), 0)
	pending := n.static("android/app/PendingIntent", "getActivity", "(Landroid/content/Context;ILandroid/content/Intent;I)Landroid/app/PendingIntent;", jni.Value(ctx), 7302, jni.Value(intent), 0x0c000000)
	n.call(builder, "setContentIntent", "(Landroid/app/PendingIntent;)Landroid/app/Notification$Builder;", jni.Value(pending))
	return n.call(builder, "build", "()Landroid/app/Notification;")
}
func trackingSample() (sample ownerclient.PhoneSample, err error) {
	err = trackingJVM(func(n *native, ctx jni.Object) error {
		manager := n.call(ctx, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;", n.str("location"))
		loc := n.call(manager, "getLastKnownLocation", "(Ljava/lang/String;)Landroid/location/Location;", n.str("gps"))
		if loc == 0 {
			return errors.New("Phone fix unavailable")
		}
		c := n.class("android/location/Location")
		mock, e := jni.CallBooleanMethod(n.env, loc, jni.GetMethodID(n.env, c, "isFromMockProvider", "()Z"))
		if e != nil || mock {
			return errors.New("Phone fix unavailable")
		}
		lat, e := jni.CallDoubleMethod(n.env, loc, jni.GetMethodID(n.env, c, "getLatitude", "()D"))
		if e != nil {
			return e
		}
		lon, e := jni.CallDoubleMethod(n.env, loc, jni.GetMethodID(n.env, c, "getLongitude", "()D"))
		if e != nil {
			return e
		}
		accuracy, e := jni.CallFloatMethod(n.env, loc, jni.GetMethodID(n.env, c, "getAccuracy", "()F"))
		if e != nil {
			return e
		}
		stamp, e := jni.CallLongMethod(n.env, loc, jni.GetMethodID(n.env, c, "getTime", "()J"))
		if e != nil {
			return e
		}
		sample = ownerclient.PhoneSample{Latitude: lat, Longitude: lon, AccuracyMeters: float64(accuracy), ObservedAt: time.UnixMilli(stamp)}
		if !sample.Valid(time.Now()) {
			return errors.New("Precise phone fix unavailable")
		}
		return nil
	})
	return
}
