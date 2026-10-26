package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
)

type memoryStore struct {
	record   state.Record
	failSave int
	writes   int
	held     bool
	events   *[]string
}

func (s *memoryStore) Acquire(context.Context, string, time.Time) (state.Lease, error) {
	if s.held {
		return state.Lease{}, errors.New("held")
	}
	s.held = true
	return state.Lease{Fence: 1}, nil
}
func (s *memoryStore) Load(context.Context, state.Lease, time.Time) (state.Record, error) {
	return s.record, nil
}
func (s *memoryStore) Save(_ context.Context, _ state.Lease, r state.Record, _ time.Time) (state.Record, error) {
	s.writes++
	*s.events = append(*s.events, "save")
	if s.writes == s.failSave {
		return state.Record{}, errors.New("write failed")
	}
	r.TokenVersion++
	s.record = r
	return r, nil
}
func (s *memoryStore) Release(context.Context, state.Lease) error { s.held = false; return nil }

type provider struct {
	events        *[]string
	err           error
	exchangeErr   error
	wake          bool
	refreshInputs []string
}

func (p *provider) Refresh(_ context.Context, grant onstar.Grant) (onstar.Grant, error) {
	p.refreshInputs = append(p.refreshInputs, grant.RefreshToken)
	*p.events = append(*p.events, "refresh")
	return onstar.Grant{RefreshToken: "replacement"}, p.err
}
func (p *provider) Exchange(context.Context, onstar.Grant, string, string) (onstar.GMToken, error) {
	*p.events = append(*p.events, "exchange")
	return onstar.GMToken{}, p.exchangeErr
}
func (p *provider) Vehicles(context.Context, onstar.GMToken) ([]onstar.Vehicle, error) {
	*p.events = append(*p.events, "vehicles")
	return []onstar.Vehicle{{VIN: testdata.VIN}}, nil
}
func (p *provider) Location(_ context.Context, _ onstar.GMToken, _ string, wake bool) (onstar.LocationStatus, error) {
	p.wake = wake
	*p.events = append(*p.events, "location")
	return onstar.LocationStatus{}, nil
}
func setup(t *testing.T) (*Worker, *memoryStore, *provider, context.Context) {
	t.Helper()
	events := []string{}
	now := time.Now()
	bootstrap, err := onstar.NewBootstrap(onstar.Grant{RefreshToken: "original", Subject: "synthetic", RefreshObtainedAt: now.Unix()}, "device", testdata.VIN, 1, "ready")
	if err != nil {
		t.Fatal(err)
	}
	s := &memoryStore{record: state.Record{Bootstrap: bootstrap, Grant: onstar.Grant{RefreshToken: "original"}, TokenVersion: 1}, events: &events}
	p := &provider{events: &events}
	w := &Worker{Store: s, Provider: p, DeviceID: "device", EnrollmentVersion: 1, DeployNonce: "nonce", Now: func() time.Time { return now }}
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(120*time.Second))
	t.Cleanup(cancel)
	return w, s, p, ctx
}
func request() Request { return Request{Live: true, RunID: "run-1", DeployNonce: "nonce"} }
func TestRefreshPersistenceOrderAndReplay(t *testing.T) {
	w, s, p, ctx := setup(t)
	result := w.Run(ctx, request(), "request-1", true)
	if result.Status != "passed" || p.wake {
		t.Fatal("read-only proof failed")
	}
	if result.TokenVersionBefore != 1 || result.TokenVersionAfter != 4 || result.EnrollmentVersion != 1 {
		t.Fatal("persisted versions not reported")
	}
	want := []string{"save", "refresh", "save", "exchange", "vehicles", "location", "save"}
	if !reflect.DeepEqual(*s.events, want) {
		t.Fatalf("operation order: %v", *s.events)
	}
	result = w.Run(ctx, request(), "request-2", true)
	if result.Reason != "proof-budget-exhausted" || len(*s.events) != len(want) {
		t.Fatal("proof replay called provider")
	}
	r := request()
	r.RunID = "run-2"
	result = w.Run(ctx, r, "request-3", true)
	if result.Status != "passed" || result.TokenVersionBefore != 4 || result.TokenVersionAfter != 7 || result.EnrollmentVersion != 1 || !reflect.DeepEqual(p.refreshInputs, []string{"original", "replacement"}) {
		t.Fatal("next invocation did not load persisted grant and versions")
	}
}

func TestEarlierProofReplayAfterAnotherRun(t *testing.T) {
	w, s, _, ctx := setup(t)
	first := request()
	if got := w.Run(ctx, first, "request-a", true); got.Status != "passed" {
		t.Fatal("first proof failed")
	}
	second := first
	second.RunID = "run-2"
	if got := w.Run(ctx, second, "request-b", true); got.Status != "passed" {
		t.Fatal("second proof failed")
	}
	writes, events := s.writes, len(*s.events)
	if got := w.Run(ctx, first, "request-c", true); got.Reason != "proof-budget-exhausted" || s.writes != writes || len(*s.events) != events {
		t.Fatal("earlier replay performed work")
	}
	if len(s.record.ProofRunIDs) != 2 || s.record.ProofAttempts != 2 {
		t.Fatal("proof history or budget changed")
	}
}

func TestLegacyProofIDPreserved(t *testing.T) {
	w, s, _, ctx := setup(t)
	s.record.ProofRunID = "old-run"
	s.record.ProofAttempts = 1
	if got := w.Run(ctx, request(), "request-new", true); got.Status != "passed" {
		t.Fatal("legacy record failed")
	}
	writes, events := s.writes, len(*s.events)
	r := request()
	r.RunID = "old-run"
	if got := w.Run(ctx, r, "request-old", true); got.Reason != "proof-budget-exhausted" || s.writes != writes || len(*s.events) != events {
		t.Fatal("legacy proof ID lost")
	}
}
func TestCrashBeforeRefreshPersistenceStopsExchange(t *testing.T) {
	w, s, _, ctx := setup(t)
	s.failSave = 2
	result := w.Run(ctx, request(), "request", true)
	if result.Reason != "refresh-persistence-failed" || !reflect.DeepEqual(*s.events, []string{"save", "refresh", "save"}) {
		t.Fatal("exchange ran after failed persistence")
	}
	if s.record.Grant.RefreshToken != "original" || s.record.ProofAttempts != 1 {
		t.Fatal("crash state invalid")
	}
}
func TestLeaseAndCooldownPreventProviderRequests(t *testing.T) {
	w, s, p, ctx := setup(t)
	s.held = true
	if result := w.Run(ctx, request(), "request", true); result.Reason != "lease-unavailable" {
		t.Fatal("lease bypassed")
	}
	s.held = false
	p.err = &onstar.Error{Kind: "access-denied", Status: 403, AuthEndpoint: true}
	if result := w.Run(ctx, request(), "request", true); result.Reason != "access-denied" {
		t.Fatal("classification failed")
	}
	if s.record.CooldownUntil != w.Now().Add(6*time.Hour).Unix() || s.record.Grant.RefreshToken != "original" {
		t.Fatal("cooldown or grant persistence failed")
	}
	before := len(*s.events)
	r := request()
	r.RunID = "run-2"
	if result := w.Run(ctx, r, "request-2", true); result.Reason != "provider-cooldown" || len(*s.events) != before {
		t.Fatal("cooldown bypassed")
	}
}

func TestWarmProofDoesNotAcquireLeaseOrSpendBudget(t *testing.T) {
	w, s, _, ctx := setup(t)
	// A held lease would return lease-unavailable if warm dispatch reached storage.
	s.held = true
	got := w.Run(ctx, request(), "warm-request", false)
	if got.Status != "blocked" || got.Reason != "warm-proof-disallowed" || s.writes != 0 || len(*s.events) != 0 || s.record.ProofAttempts != 0 {
		t.Fatal("warm proof reached storage or provider")
	}
}
func TestProofRequiresLiveBindingAndBudget(t *testing.T) {
	for _, kind := range []string{"live", "nonce", "device", "version", "budget", "revoked", "disconnected", "vehicle-not-linked"} {
		t.Run(kind, func(t *testing.T) {
			w, s, _, ctx := setup(t)
			r := request()
			switch kind {
			case "live":
				r.Live = false
			case "nonce":
				r.DeployNonce = "wrong"
			case "device":
				w.DeviceID = "wrong"
			case "version":
				w.EnrollmentVersion = 2
			case "budget":
				s.record.ProofAttempts = 3
			case "revoked":
				s.record.ProviderState = "reauth-required"
			case "disconnected", "vehicle-not-linked":
				s.record.ProviderState = kind
			}
			if got := w.Run(ctx, r, "request", true); got.Status != "blocked" || len(*s.events) != 0 {
				t.Fatal("provider gate bypassed")
			}
		})
	}
}

func TestVehicleLinkFailureRetainsRotatedGrant(t *testing.T) {
	w, s, p, ctx := setup(t)
	p.exchangeErr = &onstar.Error{Kind: "vehicle-not-linked"}
	got := w.Run(ctx, request(), "request", true)
	if got.Reason != "vehicle-not-linked" || s.record.ProviderState != "vehicle-not-linked" || s.record.Grant.RefreshToken != "replacement" {
		t.Fatal("linkage failure discarded rotated credential")
	}
	if got.TokenVersionBefore != 1 || got.TokenVersionAfter != 4 || got.TokenVersionAfter != s.record.TokenVersion {
		t.Fatal("failure-state persisted version not reported")
	}
	if !reflect.DeepEqual(*s.events, []string{"save", "refresh", "save", "exchange", "save"}) {
		t.Fatal("read continued without linkage")
	}
}

func TestFailedFinalWriteReportsLastPersistedVersion(t *testing.T) {
	w, s, _, ctx := setup(t)
	s.failSave = 3
	got := w.Run(ctx, request(), "request", true)
	if got.Status != "failed" || got.Reason != "proof-state-write-failed" || got.TokenVersionBefore != 1 || got.TokenVersionAfter != 3 || got.TokenVersionAfter != s.record.TokenVersion {
		t.Fatal("uncommitted final version reported")
	}
}

func TestRevocationAndFailedBudgetAreDurable(t *testing.T) {
	w, s, p, ctx := setup(t)
	p.err = &onstar.Error{Kind: "reauth-required", Status: 400, AuthEndpoint: true}
	got := w.Run(ctx, request(), "request", true)
	if got.Reason != "reauth-required" || s.record.ProviderState != "reauth-required" || s.record.CooldownUntil != 0 || s.record.Grant.RefreshToken != "original" {
		t.Fatal("revocation classification or preservation failed")
	}
	w, s, _, ctx = setup(t)
	s.failSave = 1
	if got = w.Run(ctx, request(), "request", true); got.Reason != "proof-budget-write-failed" || !reflect.DeepEqual(*s.events, []string{"save"}) {
		t.Fatal("provider ran without durable budget")
	}
}
