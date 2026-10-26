//go:build android && widgetbind

package nativewidgets

import (
	"Java/android/app/job"
	"Java/go/Seq"
	self "Java/nativewidgets"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"git.wow.st/gmp/jni"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/ownerclient"
)

const updateJobID = 7303
const updateScheduleAction = "ninja.jeremy.suburbanremote.UPDATE_SCHEDULE"
const updateCancelAction = "ninja.jeremy.suburbanremote.UPDATE_CANCEL"

type UpdateJobService struct {
	job.JobService
	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewUpdateJobService() *UpdateJobService { return &UpdateJobService{} }
func (s *UpdateJobService) OnStartJob(this self.UpdateJobService, params job.JobParameters) bool {
	initializeContext.Do(func() { Seq.SetContext(this.GetApplicationContext()) })
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = cancel
	s.mu.Unlock()
	go func() {
		defer func() { cancel(); this.JobFinished(params, false) }()
		root, err := privateRoot()
		if err != nil || !updatesEnabled(root) {
			return
		}
		vault := ownerclient.Vault{Path: filepath.Join(root, "owner-session.enc"), Crypt: crypt}
		client := ownerclient.NewClient()
		var session ownerclient.Session
		err = vault.WithLock(ctx, func() error {
			saved, e := vault.Load()
			if e != nil {
				return e
			}
			if saved.Session.RefreshHandle == "" {
				return ownerclient.ErrUnauthorized
			}
			if time.Until(saved.Session.AccessExpiresAt) < time.Minute {
				saved.Session, e = client.Refresh(ctx, saved.Session)
				if e != nil {
					return e
				}
				if e = vault.Save(saved); e != nil {
					return e
				}
			}
			session = saved.Session
			return nil
		})
		if err != nil || !updatesEnabled(root) {
			return
		}
		var data json.RawMessage
		if client.Request(ctx, "GET", "/api/releases/latest", session.AccessHandle, nil, &data) != nil {
			return
		}
		var key string
		var code int64
		var api int
		if trackingJVM(func(n *native, c jni.Object) error {
			pkg := n.call(c, "getPackageName", "()Ljava/lang/String;")
			pm := n.call(c, "getPackageManager", "()Landroid/content/pm/PackageManager;")
			info := n.call(pm, "getPackageInfo", "(Ljava/lang/String;I)Landroid/content/pm/PackageInfo;", jni.Value(pkg), 0)
			ic := n.class("android/content/pm/PackageInfo")
			code = int64(jni.GetIntField(n.env, info, jni.GetFieldID(n.env, ic, "versionCode", "I")))
			application := n.call(pm, "getApplicationInfo", "(Ljava/lang/String;I)Landroid/content/pm/ApplicationInfo;", jni.Value(pkg), 128)
			ac := n.class("android/content/pm/ApplicationInfo")
			bundle := n.keep(jni.GetObjectField(n.env, application, jni.GetFieldID(n.env, ac, "metaData", "Landroid/os/Bundle;")))
			if bundle == 0 {
				return errors.New("Update trust unavailable")
			}
			value := n.call(bundle, "getString", "(Ljava/lang/String;)Ljava/lang/String;", n.str("suburban.updatePublicKey"))
			if value == 0 {
				return errors.New("Update trust unavailable")
			}
			key = jni.GoString(n.env, jni.String(value))
			api = androidAPI(n)
			return nil
		}) != nil {
			return
		}
		_, newer, err := ownerclient.VerifyUpdate(data, key, code, api, time.Now())
		if err != nil || !newer || ctx.Err() != nil || !updatesEnabled(root) {
			return
		}
		_ = trackingJVM(func(n *native, c jni.Object) error {
			if ctx.Err() != nil || !updatesEnabled(root) {
				return context.Canceled
			}
			manager := n.call(c, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;", n.str("notification"))
			notice := backgroundNotification(n, c, "suburban-updates", "Suburbanista update", "A verified update is available. Tap to download and install.", false)
			n.void(manager, "notify", "(ILandroid/app/Notification;)V", updateJobID, jni.Value(notice))
			return nil
		})
	}()
	return true
}
func (s *UpdateJobService) OnStopJob(params job.JobParameters) bool {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return false
}

func updatesEnabled(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "background-updates-enabled"))
	return err == nil && string(data) == "enabled"
}

// The explicit receiver action comes only from this nonexported package.
// Reopening does not reset an already registered six-hour job.
func scheduleBackgroundUpdates(enable bool) error {
	root, err := privateRoot()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	value := "disabled"
	if enable {
		value = "enabled"
	}
	if err = os.WriteFile(filepath.Join(root, "background-updates-enabled"), []byte(value), 0600); err != nil {
		return err
	}
	return trackingJVM(func(n *native, ctx jni.Object) error {
		scheduler := n.call(ctx, "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;", n.str("jobscheduler"))
		if !enable {
			n.void(scheduler, "cancel", "(I)V", updateJobID)
			return nil
		}
		pending := n.call(scheduler, "getAllPendingJobs", "()Ljava/util/List;")
		for i, count := 0, n.intCall(pending, "size", "()I"); i < count; i++ {
			info := n.call(pending, "get", "(I)Ljava/lang/Object;", jni.Value(i))
			if n.intCall(info, "getId", "()I") == updateJobID {
				return nil
			}
		}
		component := n.new("android/content/ComponentName", []jni.Object{jni.Object(n.class("java/lang/String")), jni.Object(n.class("java/lang/String"))}, []jni.Object{jni.Object(n.str("ninja.jeremy.suburbanremote")), jni.Object(n.str("nativewidgets.UpdateJobService"))})
		builder := n.new("android/app/job/JobInfo$Builder", []jni.Object{n.intType(), jni.Object(n.class("android/content/ComponentName"))}, []jni.Object{n.integer(updateJobID), component})
		n.call(builder, "setPeriodic", "(J)Landroid/app/job/JobInfo$Builder;", 21600000)
		n.call(builder, "setRequiredNetworkType", "(I)Landroid/app/job/JobInfo$Builder;", 1)
		info := n.call(builder, "build", "()Landroid/app/job/JobInfo;")
		if n.intCall(scheduler, "schedule", "(Landroid/app/job/JobInfo;)I", jni.Value(info)) != 1 {
			return errors.New("Update scheduling unavailable")
		}
		return nil
	})
}
