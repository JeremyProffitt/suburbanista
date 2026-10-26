package mobile

import (
	"context"
	"sync"
	"time"
)

const phoneFixTimeout = 20 * time.Second
const phoneFixRetryInterval = 15 * time.Minute

// One coordinator belongs to one Screen. Opening starts a foreground lifetime;
// closing cancels it. Native cleanup completes before the single-flight slot is
// released, including across a rapid background/foreground transition.
type foregroundLocation struct {
	sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	busy       bool
	next       time.Time
	working    bool
	workCancel context.CancelFunc
}

func (f *foregroundLocation) open() {
	f.close()
	f.Lock()
	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.next = time.Time{}
	f.Unlock()
}
func (f *foregroundLocation) close() {
	f.Lock()
	cancel := f.cancel
	f.ctx = nil
	f.cancel = nil
	f.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (f *foregroundLocation) context(parent context.Context) (context.Context, context.Context, func(), bool) {
	f.Lock()
	session := f.ctx
	if session == nil || session.Err() != nil || parent.Err() != nil || f.working {
		f.Unlock()
		return nil, nil, nil, false
	}
	// The foreground is the direct parent, so backgrounding cancels the work
	// synchronously. Optional caller cancellation is combined separately.
	ctx, cancel := context.WithCancel(session)
	f.working = true
	f.workCancel = cancel
	f.Unlock()
	stop := context.AfterFunc(parent, cancel)
	if parent.Err() != nil {
		cancel()
	}
	return ctx, session, sync.OnceFunc(func() {
		stop()
		cancel()
		f.Lock()
		f.working = false
		f.workCancel = nil
		f.Unlock()
	}), true
}
func (f *foregroundLocation) cancelWork() {
	f.Lock()
	cancel := f.workCancel
	f.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (f *foregroundLocation) begin(session context.Context, now time.Time, explicit bool) (func(), bool) {
	f.Lock()
	if session == nil || session != f.ctx || session.Err() != nil || f.busy || (!explicit && now.Before(f.next)) {
		f.Unlock()
		return nil, false
	}
	f.busy = true
	f.next = now.Add(phoneFixRetryInterval)
	f.Unlock()
	return sync.OnceFunc(func() { f.Lock(); f.busy = false; f.Unlock() }), true
}

type phoneLocationSource struct {
	read  func(time.Time) (PhoneFix, string)
	start func(context.Context) (func(), string)
}

// acquirePhoneLocation is the foreground cache/refresh boundary. The platform
// adapter owns the OS request; coordinates never leave this stack except for
// the existing rounded weather request.
func acquirePhoneLocation(ctx context.Context, source phoneLocationSource, now func() time.Time, wait func(context.Context) bool) (PhoneFix, string) {
	ctx, cancel := context.WithTimeout(ctx, phoneFixTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return PhoneFix{}, "Weather paused"
	}
	fix, state := source.read(now())
	if ctx.Err() != nil {
		return PhoneFix{}, "Weather paused"
	}
	if state == "ready" || !refreshablePhoneState(state) {
		return fix, state
	}
	if ctx.Err() != nil {
		return PhoneFix{}, "Weather paused"
	}
	cleanup, startState := source.start(ctx)
	if cleanup == nil {
		if startState != "" {
			state = startState
		}
		return PhoneFix{}, state
	}
	cleanup = sync.OnceFunc(cleanup)
	stopOnCancel := context.AfterFunc(ctx, cleanup)
	defer func() { stopOnCancel(); cleanup() }()
	for attempt := 0; attempt < 40; attempt++ {
		if !wait(ctx) {
			break
		}
		if ctx.Err() != nil {
			break
		}
		fix, state = source.read(now())
		if ctx.Err() != nil {
			return PhoneFix{}, "Weather paused"
		}
		if state == "ready" || !refreshablePhoneState(state) {
			return fix, state
		}
	}
	if ctx.Err() == context.Canceled {
		return PhoneFix{}, "Weather paused"
	}
	return PhoneFix{}, state
}

func refreshablePhoneState(state string) bool {
	return state == "Phone location needs an update" || state == "Phone location unavailable" || state == "Phone location is too imprecise"
}
func waitForPhoneFix(ctx context.Context) bool {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func phoneFixState(f PhoneFix, now time.Time) string {
	if !f.Allowed {
		return "Location permission needed"
	}
	if f.AccuracyMeters < 0 || f.AccuracyMeters > 10000 {
		return "Phone location is too imprecise"
	}
	if f.ObservedAt.IsZero() {
		return "Phone location unavailable"
	}
	if f.ObservedAt.After(now.Add(time.Minute)) {
		return "Phone location time is invalid"
	}
	if now.Sub(f.ObservedAt) > 30*time.Minute {
		return "Phone location needs an update"
	}
	if !f.usable(now) {
		return "Phone location unavailable"
	}
	return "ready"
}
