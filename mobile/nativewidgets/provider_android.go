//go:build android && widgetbind

// Package nativewidgets is bound by unmodified gomobile. Its only Java code is
// the generated Android subclass; it does not link the Fyne activity runtime.
package nativewidgets

import (
	"Java/android/appwidget"
	"Java/android/content"
	"Java/go/Seq"
	self "Java/nativewidgets"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"git.wow.st/gmp/jni"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/keystore"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/ownerclient"
	"golang.org/x/mobile/app"
)

type CompactProvider struct{ appwidget.AppWidgetProvider }
type FullProvider struct{ appwidget.AppWidgetProvider }

func NewCompactProvider() *CompactProvider { return &CompactProvider{} }
func NewFullProvider() *FullProvider       { return &FullProvider{} }

func (p *CompactProvider) OnReceive(this self.CompactProvider, c content.Context, intent content.Intent) {
	action := intent.GetAction()
	if action == updateScheduleAction || action == updateCancelAction {
		initializeContext.Do(func() { Seq.SetContext(c.GetApplicationContext()) })
		updateScheduleMu.Lock()
		pending := this.GoAsync()
		go func() {
			defer updateScheduleMu.Unlock()
			defer pending.Finish()
			_ = scheduleBackgroundUpdates(action == updateScheduleAction)
		}()
		return
	}
	if locationFixAcknowledgment(action) {
		return
	}
	if !acceptedAction(action) {
		return
	}
	initializeContext.Do(func() { Seq.SetContext(c.GetApplicationContext()) })
	pending := this.GoAsync()
	go func() { defer pending.Finish(); deliver(action) }()
}
func (p *FullProvider) OnReceive(this self.FullProvider, c content.Context, intent content.Intent) {
	action := intent.GetAction()
	if locationFixAcknowledgment(action) {
		return
	}
	if !acceptedAction(action) {
		return
	}
	initializeContext.Do(func() { Seq.SetContext(c.GetApplicationContext()) })
	pending := this.GoAsync()
	go func() { defer pending.Finish(); deliver(action) }()
}

var initializeContext sync.Once
var updateScheduleMu sync.Mutex

func deliver(action string) {
	// Broadcast receivers have a bounded execution window. No scheduled retries
	// or provider/GPS reads are issued here.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	root, err := privateRoot()
	if err != nil {
		return
	}
	vault := ownerclient.Vault{Path: filepath.Join(root, "owner-session.enc"), Crypt: crypt}
	client := ownerclient.NewClient()
	message := ""
	if _, _, navigation := appearanceNavigation(action); navigation {
		now := time.Now()
		local := now
		available := false
		_ = app.RunOnJVM(func(_, env, _ uintptr) error {
			offset, ok := appearancecore.DeviceCalendarOffset(jni.EnvFor(env), now)
			local, available = appearancecore.CalendarAt(now, offset, ok)
			return nil
		})
		if available {
			if e := navigateAppearance(ctx, root, action, local); e != nil {
				message = e.Error()
			}
		} else {
			message = "Local date unavailable · open app"
		}
	}
	if _, control := commandFor(action); control {
		result, e := dispatchControl(ctx, vault, client, action)
		if e != nil {
			message = e.Error()
		} else {
			message = commandLabel(result.State)
		}
	}
	state := readState(ctx, root, vault, client, needsNetwork(action))
	if message != "" {
		state.status = message
	}
	_ = app.RunOnJVM(func(_, env, context uintptr) error {
		return render(jni.EnvFor(env), jni.Object(context), state)
	})
}

func crypt(input []byte, encrypt bool) (output []byte, err error) {
	err = app.RunOnJVM(func(_, env, _ uintptr) error {
		var e error
		output, e = keystore.Crypt(jni.EnvFor(env), input, encrypt)
		return e
	})
	return
}
func privateRoot() (root string, err error) {
	err = app.RunOnJVM(func(_, env, context uintptr) (nativeErr error) {
		defer func() {
			if recover() != nil {
				nativeErr = errors.New("Private storage unavailable")
			}
		}()
		n := newNative(jni.EnvFor(env))
		defer n.close()
		file := n.call(jni.Object(context), "getFilesDir", "()Ljava/io/File;")
		path := n.call(file, "getAbsolutePath", "()Ljava/lang/String;")
		root = filepath.Join(jni.GoString(n.env, jni.String(path)), "fyne")
		if root == "" {
			return errors.New("Private storage unavailable")
		}
		return nil
	})
	return
}
