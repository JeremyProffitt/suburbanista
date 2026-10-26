package telemetryhistory

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

type documents struct {
	sync.Mutex
	rows                      map[string]auth.Document
	writes, failAt, failAfter int
}

func (d *documents) Load(_ context.Context, key string) (auth.Document, error) {
	d.Lock()
	defer d.Unlock()
	v, ok := d.rows[key]
	if !ok {
		return v, auth.ErrMissing
	}
	return v, nil
}
func (d *documents) Commit(_ context.Context, values ...auth.Document) error {
	d.Lock()
	defer d.Unlock()
	d.writes++
	if d.writes == d.failAt {
		return auth.ErrUnavailable
	}
	if len(values) > 25 {
		return errors.New("transaction limit")
	}
	for _, v := range values {
		if d.rows[v.Key].Version != v.Version {
			return auth.ErrConflict
		}
		if len(v.Data)*4/3+1024 > 400*1024 {
			return errors.New("item limit")
		}
	}
	for _, v := range values {
		v.Version++
		d.rows[v.Key] = v
	}
	if d.writes == d.failAfter {
		return auth.ErrUnavailable
	}
	return nil
}

type credentials struct {
	record                          state.Record
	acquired, released, saves, busy int
}

func (c *credentials) Acquire(context.Context, string, time.Time) (state.Lease, error) {
	c.acquired++
	if c.busy > 0 {
		c.busy--
		return state.Lease{}, state.ErrConflict
	}
	return state.Lease{Fence: 1}, nil
}
func (c *credentials) Load(context.Context, state.Lease, time.Time) (state.Record, error) {
	return c.record, nil
}
func (c *credentials) Save(_ context.Context, _ state.Lease, r state.Record, _ time.Time) (state.Record, error) {
	c.saves++
	if r.TokenVersion != c.record.TokenVersion {
		return state.Record{}, state.ErrConflict
	}
	r.Bootstrap.RefreshToken = r.Grant.RefreshToken
	r.Bootstrap.RefreshObtainedAt = r.Grant.RefreshObtainedAt
	r.Bootstrap.B2CSubject = r.Grant.Subject
	r.Bootstrap.ProviderState = r.ProviderState
	r.TokenVersion++
	c.record = r
	return r, nil
}
func (c *credentials) Release(context.Context, state.Lease) error { c.released++; return nil }

type provider struct {
	reads, refreshes, exchanges int
	health                      onstar.HealthStatus
	err, errorRefresh           error
	beforeRead                  func()
	now                         func() time.Time
}

func (p *provider) Refresh(_ context.Context, g onstar.Grant) (onstar.Grant, error) {
	p.refreshes++
	if p.errorRefresh != nil {
		return onstar.Grant{}, p.errorRefresh
	}
	g.AccessToken = "synthetic-new"
	g.ExpiresAt = p.now().Add(time.Hour).Unix()
	return g, p.errorRefresh
}
func (p *provider) Exchange(_ context.Context, _ onstar.Grant, _ string, vin string) (onstar.GMToken, error) {
	p.exchanges++
	return onstar.GMToken{AccessToken: "synthetic-gm", ExpiresAt: p.now().Add(time.Hour).Unix(), VIN: vin, VehicleLinked: true}, nil
}
func (p *provider) Diagnostics(context.Context, onstar.GMToken, string) (onstar.HealthStatus, error) {
	p.reads++
	if p.beforeRead != nil {
		p.beforeRead()
	}
	return p.health, p.err
}

type fixture struct {
	service  *Service
	docs     *documents
	creds    *credentials
	provider *provider
	now      time.Time
	paused   bool
}

func fixtureFor(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Date(2026, 10, 25, 15, 5, 0, 0, time.UTC)}
	grant := onstar.Grant{Subject: "synthetic", RefreshToken: "synthetic-refresh", AccessToken: "synthetic-access", RefreshObtainedAt: f.now.Unix(), ExpiresAt: f.now.Add(time.Hour).Unix()}
	bootstrap, err := onstar.NewBootstrap(grant, "synthetic-device", testdata.VIN, 1, "ready")
	if err != nil {
		t.Fatal(err)
	}
	f.docs = &documents{rows: map[string]auth.Document{}}
	f.creds = &credentials{record: state.Record{Bootstrap: bootstrap, Grant: grant, GMToken: "synthetic-gm", GMExpiresAt: f.now.Add(time.Hour).Unix(), ProviderState: "ready", TokenVersion: 1}}
	f.provider = &provider{now: func() time.Time { return f.now }}
	f.service = &Service{Docs: f.docs, Credentials: f.creds, Provider: f.provider, Owner: "synthetic-owner", Vehicle: "synthetic-vehicle", DeviceID: "synthetic-device", ScheduleARN: "synthetic-schedule", Now: func() time.Time { return f.now }, Wait: func(context.Context) bool { return true }, Paused: func(context.Context) (bool, error) { return f.paused, nil }}
	stamp := f.now.Add(-2 * time.Hour).Format(time.RFC3339)
	f.provider.health = onstar.HealthStatus{HealthElement: onstar.HealthElement{Status: "GOOD", ObservedAt: stamp}}
	for i := 0; i < 82; i++ {
		name := fmt.Sprintf("SYNTHETIC_%d", i)
		unit := "synthetic"
		if i == 0 {
			name = "FUEL_LEVEL"
			unit = "%"
		}
		if i == 1 {
			name = "ODO_READ"
			unit = "KM"
		}
		value := fmt.Sprint(i + 1)
		f.provider.health.Diagnostics = append(f.provider.health.Diagnostics, onstar.HealthDiagnostic{HealthElement: onstar.HealthElement{Name: name, Value: &value, Unit: unit, ObservedAt: stamp}})
	}
	return f
}
func (f *fixture) request() Request {
	return Request{Kind: "hourly-diagnostics", VehicleKey: f.service.Vehicle, ScheduleARN: f.service.ScheduleARN, ScheduledAt: f.now}
}
func (f *fixture) history(t *testing.T) onstar.HealthStatus {
	t.Helper()
	key := f.service.key(f.now.Truncate(time.Hour))
	doc, err := f.docs.Load(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var r record
	if json.Unmarshal(doc.Data, &r) != nil {
		t.Fatal("history manifest")
	}
	var zipped bytes.Buffer
	for i := 0; i < r.Parts; i++ {
		part, e := f.docs.Load(context.Background(), fmt.Sprintf("%s#part-%02d", key, i))
		var body struct {
			Data []byte `json:"data"`
		}
		if e != nil || json.Unmarshal(part.Data, &body) != nil {
			t.Fatal("history part")
		}
		zipped.Write(body.Data)
		if part.ExpiresAt.Year() != 9999 {
			t.Fatal("history expires")
		}
	}
	reader, e := gzip.NewReader(&zipped)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	raw, e := io.ReadAll(reader)
	if e != nil {
		t.Fatal(e)
	}
	var health onstar.HealthStatus
	if json.Unmarshal(raw, &health) != nil {
		t.Fatal("history health data")
	}
	return health
}
func TestHourlyReadArchivesAllReadingsOnceAndUpdatesDisplay(t *testing.T) {
	f := fixtureFor(t)
	ctx := context.Background()
	f.provider.beforeRead = func() {
		doc, e := f.docs.Load(ctx, f.service.key(f.now.Truncate(time.Hour)))
		var r record
		if e != nil || json.Unmarshal(doc.Data, &r) != nil || r.State != "dispatched" || r.DataCalls != 1 {
			t.Fatal("data read preceded durable dispatch fence")
		}
	}
	got, err := f.service.Collect(ctx, f.request())
	if err != nil || got.State != "collected" || !got.ArchiveVerified || got.Readings != 82 || !got.HasFuel || !got.HasOdometer || f.provider.reads != 1 || f.provider.refreshes != 0 || f.creds.released != 1 {
		t.Fatal("collection result invalid", got, err)
	}
	if !reflect.DeepEqual(f.history(t), f.provider.health) {
		t.Fatal("full readings, source timestamps or units were changed")
	}
	cache, _ := f.docs.Load(ctx, "snapshot#diagnostics")
	var snapshot contract.VehicleSnapshot
	if json.Unmarshal(cache.Data, &snapshot) != nil || snapshot.FeatureID != "diagnostics" || snapshot.ObservedAt == nil || !snapshot.ObservedAt.Equal(f.now.Add(-2*time.Hour)) || snapshot.FetchedAt != f.now || snapshot.State != "partial" {
		t.Fatal("bounded display cache or source timestamps invalid")
	}
	if got.CollectedAt != f.now || got.Hour != f.now.Truncate(time.Hour) {
		t.Fatal("collection clock differs from schedule/source clocks")
	}
	for i := 0; i < 3; i++ {
		replay, e := f.service.Collect(ctx, f.request())
		if e != nil || !replay.ArchiveVerified || f.provider.reads != 1 {
			t.Fatal("hour replay called provider", e)
		}
	}
	latest, e := f.service.Latest(ctx)
	if e != nil || !latest.ArchiveVerified || latest.Readings != 82 {
		t.Fatal("safe latest receipt", e)
	}
	f.now = f.now.Add(time.Hour)
	if _, e = f.service.Collect(ctx, f.request()); e != nil || f.provider.reads != 2 {
		t.Fatal("next hour did not collect", e)
	}
}
func TestArchiveLargerThanOneDynamoItemIsComplete(t *testing.T) {
	f := fixtureFor(t)
	random := make([]byte, 200*1024)
	_, _ = rand.New(rand.NewSource(1)).Read(random)
	large := hex.EncodeToString(random)
	f.provider.health.Diagnostics[2].Value = &large
	r, err := f.service.Collect(context.Background(), f.request())
	if err != nil || !r.ArchiveVerified {
		t.Fatal("large full archive failed", err)
	}
	if !reflect.DeepEqual(f.history(t), f.provider.health) {
		t.Fatal("large archive truncated")
	}
	doc, _ := f.docs.Load(context.Background(), f.service.key(f.now.Truncate(time.Hour)))
	var saved record
	_ = json.Unmarshal(doc.Data, &saved)
	if saved.Parts < 2 {
		t.Fatal("oversized item was not split")
	}
}

func TestConcurrentHourAndMissingReadings(t *testing.T) {
	f := fixtureFor(t)
	var pending sync.WaitGroup
	for i := 0; i < 12; i++ {
		pending.Add(1)
		go func() { defer pending.Done(); _, _ = f.service.Collect(context.Background(), f.request()) }()
	}
	pending.Wait()
	if f.provider.reads != 1 {
		t.Fatal("concurrent hour repeated diagnostics")
	}
	if receipt, err := f.service.Latest(context.Background()); err != nil || !receipt.ArchiveVerified {
		t.Fatal("concurrent collection did not persist", err)
	}
	f = fixtureFor(t)
	f.provider.health.Diagnostics = nil
	receipt, err := f.service.Collect(context.Background(), f.request())
	if err != nil || receipt.State != "unavailable" || receipt.Readings != 0 || receipt.HasFuel || receipt.HasOdometer || !receipt.ArchiveVerified {
		t.Fatal("missing values were fabricated", err)
	}
}

func TestReceiptRequiresUsableFuelAndOdometer(t *testing.T) {
	for _, tc := range []struct {
		name, value, unit string
		index             int
	}{
		{"empty-fuel", "", "%", 0}, {"unknown-fuel", "unknown", "%", 0}, {"negative-fuel", "-1", "%", 0}, {"nan-fuel", "NaN", "%", 0}, {"infinite-fuel", "+Inf", "%", 0}, {"fuel-over100", "101", "%", 0}, {"fuel-unit", "50", "GAL", 0},
		{"empty-odometer", "", "KM", 1}, {"unknown-odometer", "unknown", "KM", 1}, {"negative-odometer", "-1", "KM", 1}, {"nan-odometer", "NaN", "KM", 1}, {"infinite-odometer", "+Inf", "KM", 1}, {"odometer-unit", "50", "L", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixtureFor(t)
			f.provider.health.Diagnostics[tc.index].Value = &tc.value
			f.provider.health.Diagnostics[tc.index].Unit = tc.unit
			got, err := f.service.Collect(context.Background(), f.request())
			if err != nil || !got.ArchiveVerified {
				t.Fatal("raw archive rejected", err)
			}
			if tc.index == 0 && got.HasFuel || tc.index == 1 && got.HasOdometer {
				t.Fatal("unusable metric claimed usable")
			}
			if !reflect.DeepEqual(f.history(t), f.provider.health) {
				t.Fatal("raw metric discarded")
			}
		})
	}
	for _, unit := range []string{"KM", "MI"} {
		f := fixtureFor(t)
		zero := "0"
		f.provider.health.Diagnostics[0].Value = &zero
		f.provider.health.Diagnostics[1].Value = &zero
		f.provider.health.Diagnostics[1].Unit = unit
		r, e := f.service.Collect(context.Background(), f.request())
		if e != nil || !r.HasFuel || !r.HasOdometer {
			t.Fatal("valid zero metric rejected")
		}
	}
}
func TestDispatchAndArchiveFailureNeverRepeatDataRead(t *testing.T) {
	for _, stage := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(stage), func(t *testing.T) {
			f := fixtureFor(t)
			f.docs.failAt = stage
			_, _ = f.service.Collect(context.Background(), f.request())
			if stage < 3 && f.provider.reads != 0 {
				t.Fatal("read before durable fence")
			}
			if stage == 3 && f.provider.reads != 1 {
				t.Fatal("expected one read")
			}
			if stage > 1 {
				_, _ = f.service.Collect(context.Background(), f.request())
				if f.provider.reads > 1 {
					t.Fatal("failed invocation repeated data read")
				}
			}
		})
	}
	f := fixtureFor(t)
	f.docs.failAfter = 3
	r, e := f.service.Collect(context.Background(), f.request())
	if e != nil || !r.ArchiveVerified || f.provider.reads != 1 {
		t.Fatal("uncertain commit was not verified without another read", e)
	}
	f = fixtureFor(t)
	f.provider.err = &onstar.Error{Kind: "deadline"}
	r, e = f.service.Collect(context.Background(), f.request())
	if e != nil || r.State != "failed" || r.DataCalls != 1 {
		t.Fatal("provider failure not recorded", e)
	}
	_, _ = f.service.Collect(context.Background(), f.request())
	if f.provider.reads != 1 {
		t.Fatal("provider failure retried")
	}
}
func TestPauseCooldownBindingAndContentionDoNotRead(t *testing.T) {
	for _, kind := range []string{"paused", "cooldown", "binding", "busy", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			f := fixtureFor(t)
			ctx := context.Background()
			switch kind {
			case "paused":
				f.paused = true
			case "cooldown":
				f.creds.record.CooldownUntil = f.now.Add(time.Hour).Unix()
			case "binding":
				f.service.DeviceID = "different"
			case "busy":
				f.creds.busy = 20
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, _ = f.service.Collect(ctx, f.request())
			if f.provider.reads+f.provider.refreshes+f.provider.exchanges != 0 {
				t.Fatal("blocked collector called provider")
			}
		})
	}
	f := fixtureFor(t)
	f.creds.busy = 2
	r, e := f.service.Collect(context.Background(), f.request())
	if e != nil || !r.ArchiveVerified || f.provider.reads != 1 || f.creds.acquired != 3 {
		t.Fatal("bounded contention recovery", e)
	}
}
func TestRefreshPersistenceAndAuthCooldown(t *testing.T) {
	f := fixtureFor(t)
	f.creds.record.GMExpiresAt = 0
	f.creds.record.Grant.ExpiresAt = 0
	f.provider.beforeRead = func() {
		if f.creds.saves != 2 || f.creds.record.GMExpiresAt <= f.now.Unix() {
			t.Fatal("credentials not persisted before diagnostics")
		}
	}
	if _, e := f.service.Collect(context.Background(), f.request()); e != nil || f.provider.reads != 1 || f.provider.refreshes != 1 || f.provider.exchanges != 1 {
		t.Fatal("refresh collection", e)
	}
	f = fixtureFor(t)
	f.creds.record.GMExpiresAt = 0
	f.creds.record.Grant.ExpiresAt = 0
	f.provider.errorRefresh = &onstar.Error{Kind: "access-denied", AuthEndpoint: true, Status: 403}
	_, _ = f.service.Collect(context.Background(), f.request())
	if f.provider.reads != 0 || f.creds.record.CooldownUntil != f.now.Add(6*time.Hour).Unix() {
		t.Fatal("auth denial did not preserve cooldown")
	}
}

func TestDiagnosticsFailureAfterExchangePersistsCurrentCredentialVersion(t *testing.T) {
	for _, problem := range []*onstar.Error{{Kind: "rate-limited", RetryAfterSeconds: 7200}, {Kind: "reauth-required", Status: 401, AuthEndpoint: true}} {
		t.Run(problem.Kind, func(t *testing.T) {
			f := fixtureFor(t)
			f.creds.record.GMExpiresAt = 0
			f.creds.record.Grant.ExpiresAt = 0
			f.provider.err = problem
			receipt, err := f.service.Collect(context.Background(), f.request())
			if err != nil || receipt.State != "failed" || receipt.Reason != problem.Kind || f.creds.record.TokenVersion != 4 {
				t.Fatal("post-exchange failure state was not persisted", receipt.State, err)
			}
			want := f.now.Add(2 * time.Hour).Unix()
			if problem.AuthEndpoint {
				want = f.now.Add(6 * time.Hour).Unix()
				if f.creds.record.ProviderState != "reauth-required" {
					t.Fatal("reauthorization state missing")
				}
			}
			if f.creds.record.CooldownUntil != want {
				t.Fatal("post-exchange cooldown missing")
			}
			_, _ = f.service.Collect(context.Background(), f.request())
			if f.provider.reads != 1 {
				t.Fatal("diagnostics failure repeated")
			}
		})
	}
}

func TestRefreshFailurePreservesRetainedGrantAndEnrollment(t *testing.T) {
	f := fixtureFor(t)
	f.creds.record.GMExpiresAt = 0
	f.creds.record.Grant.ExpiresAt = 0
	originalGrant, originalBootstrap := f.creds.record.Grant, f.creds.record.Bootstrap
	f.provider.errorRefresh = &onstar.Error{Kind: "reauth-required", AuthEndpoint: true, Status: 401}
	receipt, err := f.service.Collect(context.Background(), f.request())
	if err != nil || receipt.Reason != "reauth-required" || f.provider.reads != 0 {
		t.Fatal("refresh failure result invalid", err)
	}
	if !reflect.DeepEqual(f.creds.record.Grant, originalGrant) {
		t.Fatal("failed refresh erased retained grant")
	}
	originalBootstrap.ProviderState = "reauth-required"
	if !reflect.DeepEqual(f.creds.record.Bootstrap, originalBootstrap) {
		t.Fatal("failed refresh erased enrollment binding")
	}
	if f.creds.record.CooldownUntil != f.now.Add(6*time.Hour).Unix() {
		t.Fatal("refresh failure did not retain cooldown")
	}
}
func TestInvalidEventAndArchiveTamperingFailClosed(t *testing.T) {
	for _, mutate := range []func(*Request){func(r *Request) { r.Kind = "other" }, func(r *Request) { r.ScheduleARN = "other" }, func(r *Request) { r.VehicleKey = "other" }, func(r *Request) { r.ScheduledAt = r.ScheduledAt.Add(2 * time.Minute) }, func(r *Request) { r.ScheduledAt = r.ScheduledAt.Add(-3 * time.Hour) }} {
		f := fixtureFor(t)
		r := f.request()
		mutate(&r)
		if _, e := f.service.Collect(context.Background(), r); e == nil || f.docs.writes != 0 || f.provider.reads != 0 {
			t.Fatal("invalid event accepted")
		}
	}
	f := fixtureFor(t)
	_, _ = f.service.Collect(context.Background(), f.request())
	key := f.service.key(f.now.Truncate(time.Hour)) + "#part-00"
	part := f.docs.rows[key]
	part.Data = []byte(`{"data":"AA=="}`)
	f.docs.rows[key] = part
	if _, e := f.service.Latest(context.Background()); e == nil {
		t.Fatal("corrupt history verified")
	}
}
