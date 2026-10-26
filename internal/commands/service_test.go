package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambdacontext"
)

func TestCommandOptionsReplayMismatchDoesNotEnqueueAgain(t *testing.T) {
	f := setup(t, nil)
	delay := 0
	f.r.Action = "lock"
	f.r.Options = &contract.CommandOptions{Delay: &delay}
	first := f.s.Broker(context.Background(), f.r)
	if first.State != "queued" || f.q.calls != 1 {
		t.Fatal("initial command was not queued")
	}
	delay = 2
	second := f.s.Broker(context.Background(), f.r)
	if second.State != "rejected" || second.Reason != "request-replay-mismatch" || f.q.calls != 1 || f.provider.submits != 0 {
		t.Fatal("changed options reused a durable command identity")
	}
}

func TestNewActionEligibilityMustBeCurrentAndUnambiguous(t *testing.T) {
	for _, kind := range []string{"stale", "future", "partial", "unknown", "ineligible", "expired"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, nil)
			f.r.Action = "lock-trunk"
			f.flags.value.AdditionalActions = map[string]bool{"lock-trunk": true}
			yes := true
			snapshot := contract.VehicleSnapshot{FeatureID: "vehicle-details", State: "available", FetchedAt: *f.now, Eligibility: []contract.CommandEligibility{{Action: "lock-trunk", Eligible: &yes}}}
			doc := f.s.newDoc("snapshot#vehicle-details")
			switch kind {
			case "stale":
				snapshot.FetchedAt = f.now.Add(-15*time.Minute - time.Second)
			case "future":
				snapshot.FetchedAt = f.now.Add(time.Second)
			case "partial":
				snapshot.State = "partial"
			case "unknown":
				snapshot.Eligibility[0].Eligible = nil
			case "ineligible":
				yes = false
			case "expired":
				doc.ExpiresAt = f.now.Add(-time.Second)
			}
			if err := f.docs.Commit(context.Background(), data(doc, snapshot)); err != nil {
				t.Fatal(err)
			}
			out := f.s.Broker(context.Background(), f.r)
			if out.State != "rejected" || f.q.calls != 0 || f.provider.submits != 0 {
				t.Fatal("unverified eligibility dispatched a command", kind)
			}
		})
	}
}

func TestEligibilityExpiringAfterQueuePreventsSubmission(t *testing.T) {
	f := setup(t, nil)
	f.r.Action = "lock-trunk"
	f.flags.value.AdditionalActions = map[string]bool{"lock-trunk": true}
	yes := true
	snapshot := contract.VehicleSnapshot{FeatureID: "vehicle-details", State: "available", FetchedAt: f.now.Add(-15*time.Minute + time.Second), Eligibility: []contract.CommandEligibility{{Action: "lock-trunk", Eligible: &yes}}}
	if err := f.docs.Commit(context.Background(), data(f.s.newDoc("snapshot#vehicle-details"), snapshot)); err != nil {
		t.Fatal(err)
	}
	if out := f.s.Broker(context.Background(), f.r); out.State != "queued" {
		t.Fatal("current eligibility failed")
	}
	*f.now = f.now.Add(2 * time.Second)
	if err := f.dispatch(t, "1"); err != nil {
		t.Fatal(err)
	}
	if f.provider.submits != 0 {
		t.Fatal("expired eligibility used at dispatch")
	}
	if status := f.status(); status.State != "failed" || status.Reason != "capability-stale" {
		t.Fatal("stale eligibility reason lost")
	}
}

func TestOwnerAuthorityFailureMakesNoReadOrFeatureProviderCalls(t *testing.T) {
	for _, operation := range []string{"features", "read", "cached-read"} {
		t.Run(operation, func(t *testing.T) {
			f := setup(t, nil)
			f.auth.reject = true
			f.r.Operation = operation
			f.r.FeatureID = "diagnostics"
			out := f.s.Broker(context.Background(), f.r)
			if out.State != "unconfirmed" || f.auth.calls != 1 || f.creds.loads != 0 || f.provider.readCalls != 0 || f.provider.exchanges != 0 || f.provider.refreshes != 0 || f.q.calls != 0 {
				t.Fatal("unverified owner reached vehicle data")
			}
		})
	}
}

type documents struct {
	mu           sync.Mutex
	items        map[string]auth.Document
	failState    string
	beforeCommit func([]auth.Document)
}

func (d *documents) Load(_ context.Context, key string) (auth.Document, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	doc, ok := d.items[key]
	if !ok {
		return auth.Document{}, auth.ErrMissing
	}
	doc.Data = append([]byte(nil), doc.Data...)
	return doc, nil
}
func (d *documents) Commit(_ context.Context, docs ...auth.Document) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.beforeCommit != nil {
		d.beforeCommit(docs)
	}
	for _, doc := range docs {
		old, ok := d.items[doc.Key]
		if (ok && old.Version != doc.Version) || (!ok && doc.Version != 0) {
			return auth.ErrConflict
		}
		if strings.HasPrefix(doc.Key, "command#") && d.failState != "" {
			c, _ := decoded[command](doc)
			if c.State == d.failState {
				d.failState = ""
				return auth.ErrUnavailable
			}
		}
	}
	for _, doc := range docs {
		doc.Version++
		doc.Data = append([]byte(nil), doc.Data...)
		d.items[doc.Key] = doc
	}
	return nil
}

type flags struct{ value Flags }

func (f *flags) ReadFlags(context.Context) (Flags, error) { return f.value, nil }

type authority struct {
	calls  int
	reject bool
	now    *time.Time
}

func (a *authority) Validate(context.Context, string) (amazon.Identity, error) {
	a.calls++
	if a.reject {
		return amazon.Identity{}, errors.New("synthetic authority unavailable")
	}
	return amazon.Identity{Subject: "synthetic-owner", ValidatedAt: *a.now, ExpiresAt: a.now.Add(time.Hour)}, nil
}

type queue struct {
	body, id string
	calls    int
	fail     bool
}

func (q *queue) Send(_ context.Context, body, id string) error {
	q.calls++
	if q.fail {
		q.body, q.id = body, id // Simulate acceptance with a lost acknowledgement.
		return errors.New("synthetic queue unavailable")
	}
	q.body, q.id = body, id
	return nil
}

type credentials struct {
	record       state.Record
	loads, saves int
	busy         bool
}

func (c *credentials) Acquire(_ context.Context, holder string, now time.Time) (state.Lease, error) {
	if c.busy {
		return state.Lease{}, state.ErrConflict
	}
	return state.Lease{Holder: holder, Fence: 1, ExpiresAt: now.Add(150 * time.Second).Unix()}, nil
}
func (c *credentials) Load(context.Context, state.Lease, time.Time) (state.Record, error) {
	c.loads++
	return c.record, nil
}
func (c *credentials) Save(_ context.Context, _ state.Lease, r state.Record, _ time.Time) (state.Record, error) {
	c.saves++
	r.TokenVersion++
	c.record = r
	return r, nil
}
func (c *credentials) Release(context.Context, state.Lease) error { return nil }

type provider struct {
	now                                  *time.Time
	creds                                *credentials
	t                                    *testing.T
	submits, refreshes, exchanges, polls int
	panicSubmit                          bool
	pending                              bool
	afterExchange                        func()
	submitResult                         *onstar.CommandResult
	submitErr                            error
	allowOptions                         bool
	lastAction                           onstar.Command
	lastOptions                          onstar.CommandOptions
	readCalls                            int
}

func (p *provider) Refresh(_ context.Context, g onstar.Grant) (onstar.Grant, error) {
	p.refreshes++
	g.RefreshToken = "synthetic-rotated"
	g.AccessToken = "synthetic-new-access"
	g.ExpiresAt = p.now.Add(time.Hour).Unix()
	return g, nil
}
func (p *provider) Exchange(_ context.Context, g onstar.Grant, _ string, vin string) (onstar.GMToken, error) {
	p.exchanges++
	if p.refreshes > 0 && p.creds.record.Grant.RefreshToken != g.RefreshToken {
		p.t.Fatal("exchange before refresh persistence")
	}
	if p.afterExchange != nil {
		p.afterExchange()
	}
	return onstar.GMToken{AccessToken: "synthetic-gm", ExpiresAt: p.now.Add(time.Hour).Unix(), VehicleLinked: true, VIN: vin}, nil
}
func (p *provider) Submit(_ context.Context, _ onstar.GMToken, _ string, action onstar.Command, options onstar.CommandOptions) (onstar.CommandResult, error) {
	p.submits++
	p.lastAction = action
	p.lastOptions = options
	if action != onstar.Start && action != onstar.CancelStart && action != onstar.LockDoor && action != onstar.UnlockDoor && action != onstar.LockTrunk && action != onstar.UnlockTrunk && action != onstar.Alert && action != onstar.CancelAlert && action != onstar.FlashLights && action != onstar.StopLights {
		p.t.Fatal("unexpected action")
	}
	if !p.allowOptions && (options.CabinTemperature != nil || options.Delay != nil || options.Duration != nil || len(options.Actions) != 0) {
		p.t.Fatal("unexpected safety parameters")
	}
	if p.panicSubmit {
		panic("synthetic panic must not escape")
	}
	if p.submitResult != nil {
		return *p.submitResult, p.submitErr
	}
	return onstar.CommandResult{State: "submitted", Reason: "provider-pending", RequestID: "synthetic-provider-request", StatusURL: "https://na-mobile-api.gm.com/synthetic/status", RequestTime: *p.now}, nil
}
func (p *provider) ReadSnapshot(_ context.Context, _ onstar.GMToken, _ string, id string) (contract.VehicleSnapshot, error) {
	p.readCalls++
	observed := p.now.Add(-time.Minute)
	return contract.VehicleSnapshot{FeatureID: id, State: "available", FetchedAt: *p.now, ObservedAt: &observed, Freshness: "fresh", Fields: []contract.SnapshotField{{Key: "health-status", Label: "Health", Value: "GOOD", ObservedAt: &observed, Freshness: "fresh"}}, Items: []contract.SnapshotItem{}}, nil
}

func TestRemainingActionsRequireOwnerAndEligibility(t *testing.T) {
	for _, feature := range contract.FeatureCatalog() {
		if feature.Category != "action" || feature.ID == "start" || feature.ID == "lock" || feature.ID == "unlock" {
			continue
		}
		t.Run(feature.ID, func(t *testing.T) {
			f := setup(t, nil)
			f.r.Action = feature.ID
			if out := f.s.Broker(context.Background(), f.r); out.State != "rejected" || f.q.calls != 0 {
				t.Fatal("new action enabled without owner flag")
			}
			f.flags.value.AdditionalActions = map[string]bool{feature.ID: true}
			if out := f.s.Broker(context.Background(), f.r); out.State != "rejected" || f.q.calls != 0 {
				t.Fatal("new action enabled without actual eligibility")
			}
			yes := true
			snapshot := contract.VehicleSnapshot{FeatureID: "vehicle-details", State: "available", FetchedAt: *f.now, Eligibility: []contract.CommandEligibility{{Action: feature.ID, Eligible: &yes}}}
			if e := f.docs.Commit(context.Background(), data(f.s.newDoc("snapshot#vehicle-details"), snapshot)); e != nil {
				t.Fatal(e)
			}
			if feature.ID == "horn-and-lights" {
				duration := 3
				f.r.Options = &contract.CommandOptions{Duration: &duration, Actions: []string{"Honk"}}
				f.provider.allowOptions = true
			}
			if out := f.s.Broker(context.Background(), f.r); out.State != "queued" {
				t.Fatal("eligible owner-enabled action not queued", out)
			}
			if e := f.dispatch(t, "1"); e != nil {
				t.Fatal(e)
			}
			if f.provider.submits != 1 || f.provider.lastAction != onstar.Command(feature.SourceMethod) {
				t.Fatal("wrong one-submission method")
			}
			if f.r.Options != nil && (len(f.provider.lastOptions.Actions) != 1 || f.provider.lastOptions.Actions[0] != "Honk") {
				t.Fatal("options not persisted to transport")
			}
		})
	}
}

func TestReadsCacheAndRevocation(t *testing.T) {
	f := setup(t, nil)
	f.r.Operation = "read"
	f.r.FeatureID = "diagnostics"
	out := f.s.Broker(context.Background(), f.r)
	if out.State != "succeeded" || out.Snapshot == nil || out.Snapshot.FeatureID != "diagnostics" || f.provider.readCalls != 1 {
		t.Fatal("live read path failed")
	}
	*f.now = f.now.Add(20 * time.Minute)
	f.r.Timestamp = f.now.Format(time.RFC3339)
	f.r.Operation = "cached-read"
	out = f.s.Broker(context.Background(), f.r)
	if out.Snapshot == nil || out.Snapshot.Freshness != "stale" || f.provider.readCalls != 1 {
		t.Fatal("cached read fetched or forged freshness")
	}
	event := f.r
	event.Operation = "event"
	event.EventType = "AlexaSkillEvent.SkillAccountUnlinked"
	event.EventPublishingTime = event.Timestamp
	if f.s.Broker(context.Background(), event).State != "succeeded" {
		t.Fatal("unlink failed")
	}
	for _, operation := range []string{"features", "read", "cached-read"} {
		f.r.Operation = operation
		out = f.s.Broker(context.Background(), f.r)
		if out.State != "unconfirmed" || out.Reason != "binding-revoked" || f.provider.readCalls != 1 {
			t.Fatal("revoked read access permitted")
		}
	}
}

func TestReadRechecksBindingAfterRefresh(t *testing.T) {
	f := setup(t, nil)
	f.creds.record.GMExpiresAt = 0
	f.creds.record.Grant.ExpiresAt = 0
	f.r.Operation = "read"
	f.r.FeatureID = "diagnostics"
	f.provider.afterExchange = func() {
		event := f.r
		event.Operation = "event"
		event.EventType = "AlexaSkillEvent.SkillDisabled"
		event.EventPublishingTime = event.Timestamp
		_ = f.s.Broker(context.Background(), event)
	}
	out := f.s.Broker(context.Background(), f.r)
	if out.State != "unconfirmed" || f.provider.readCalls != 0 {
		t.Fatal("binding change during refresh exposed vehicle snapshot")
	}
}

func TestFeatureMetadataAndEVNeverCallProvider(t *testing.T) {
	f := setup(t, nil)
	f.r.Operation = "features"
	out := f.s.Broker(context.Background(), f.r)
	if out.State != "succeeded" || len(out.Availability) != 22 || f.creds.loads != 0 || f.provider.readCalls != 0 {
		t.Fatal("catalog caused provider access")
	}
	f.r.Operation = "read"
	f.r.FeatureID = "ev-charging-metrics"
	out = f.s.Broker(context.Background(), f.r)
	if out.Snapshot == nil || out.Snapshot.State != "unavailable" || f.creds.loads != 0 || f.provider.readCalls != 0 {
		t.Fatal("unimplemented EV path pretended to run")
	}
	f.r.FeatureID = "../owner-binding"
	if f.s.Broker(context.Background(), f.r).State != "rejected" {
		t.Fatal("feature injection accepted")
	}
}
func (p *provider) CommandStatus(context.Context, onstar.GMToken, string, string, string) (onstar.CommandResult, error) {
	p.polls++
	if p.pending {
		return onstar.CommandResult{State: "submitted"}, nil
	}
	return onstar.CommandResult{State: "succeeded", Reason: "provider-confirmed"}, nil
}

type fixture struct {
	s        *Service
	docs     *documents
	flags    *flags
	auth     *authority
	q        *queue
	creds    *credentials
	provider *provider
	now      *time.Time
	r        contract.AlexaCommandRequest
}

func setup(t *testing.T, store auth.Documents) *fixture {
	t.Helper()
	now := time.Date(2040, 1, 2, 3, 4, 5, 0, time.UTC)
	docs := &documents{items: map[string]auth.Document{}}
	if store == nil {
		store = docs
	}
	if e := store.Commit(context.Background(), data(auth.Document{Key: "owner-binding", ExpiresAt: now.AddDate(100, 0, 0)}, "synthetic-owner")); e != nil {
		t.Fatal(e)
	}
	a := &authority{now: &now}
	q := &queue{}
	f := &flags{Flags{ActuationEnabled: true, ProviderPaused: false, StartSupported: true, LockSupported: true, UnlockSupported: true}}
	grant := onstar.Grant{RefreshToken: "synthetic-refresh", Subject: testdata.Subject, AccessToken: "synthetic-b2c", ExpiresAt: now.Add(time.Hour).Unix(), RefreshObtainedAt: now.Unix()}
	b, e := onstar.NewBootstrap(grant, testdata.DeviceID, testdata.VIN, 1, "ready")
	if e != nil {
		t.Fatal(e)
	}
	creds := &credentials{record: state.Record{Bootstrap: b, Grant: grant, ProviderState: "ready", GMToken: "synthetic-gm", GMExpiresAt: now.Add(time.Hour).Unix(), TokenVersion: 1}}
	p := &provider{now: &now, creds: creds, t: t}
	s := &Service{Config: Config{SkillID: "synthetic-skill", Owner: "synthetic-owner", DeviceID: testdata.DeviceID, VehicleKey: "synthetic-vehicle", QueueARN: "synthetic-queue"}, Docs: store, Authority: a, Queue: q, Credentials: creds, Provider: p, Flags: f, Now: func() time.Time { return now }, Sleep: func(ctx context.Context, d time.Duration) error {
		now = now.Add(d)
		if deadline, ok := ctx.Deadline(); ok && !now.Before(deadline) {
			return context.DeadlineExceeded
		}
		return nil
	}}
	r := contract.AlexaCommandRequest{Kind: "alexa-command", Source: "alexa", Operation: "submit", Action: "start", SkillID: s.Config.SkillID, RequestID: "synthetic-request", UserID: "synthetic-user", PersonID: "synthetic-person", PersonAccessToken: "synthetic-person-token", Timestamp: now.Format(time.RFC3339)}
	return &fixture{s, docs, f, a, q, creds, p, &now, r}
}
func (f *fixture) message(count string) events.SQSMessage {
	return events.SQSMessage{MessageId: "synthetic-message", Body: f.q.body, EventSource: "aws:sqs", EventSourceARN: f.s.Config.QueueARN, Attributes: map[string]string{"MessageGroupId": "cmd-" + f.s.Config.VehicleKey, "ApproximateReceiveCount": count}}
}
func (f *fixture) dispatch(t *testing.T, count string) error {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), f.now.Add(120*time.Second))
	defer cancel()
	return f.s.Dispatch(ctx, f.message(count))
}
func (f *fixture) status() contract.AlexaCommandResponse {
	r := f.r
	r.Operation = "status"
	r.Timestamp = f.now.Format(time.RFC3339)
	return f.s.Broker(context.Background(), r)
}
func commandConformance(t *testing.T, store auth.Documents) {
	t.Helper()
	f := setup(t, store)
	var diagnostics []CommandDiagnostic
	f.s.LogCommand = func(entry CommandDiagnostic) { diagnostics = append(diagnostics, entry) }
	ctx := context.Background()
	first := f.s.Broker(ctx, f.r)
	if first.State != "queued" || f.q.calls != 1 {
		t.Fatal("intent not queued", first)
	}
	if strings.Contains(f.q.body, f.r.PersonAccessToken) || strings.Contains(f.q.body, testdata.VIN) {
		t.Fatal("private queue payload")
	}
	again := f.s.Broker(ctx, f.r)
	if again.CommandID != first.CommandID || f.q.calls != 1 {
		t.Fatal("duplicate queued twice")
	}
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	if f.provider.submits != 1 || f.auth.calls < 3 || f.status().State != "succeeded" {
		t.Fatal("real fixture dispatch not verified")
	}
	if len(diagnostics) != 2 || diagnostics[0].Operation != "submit" || diagnostics[1].Operation != "status" {
		t.Fatal("provider attempts missing structured diagnostics")
	}
	if e := f.dispatch(t, "2"); e != nil || f.provider.submits != 1 {
		t.Fatal("redelivery resubmitted")
	}
}
func TestCommandConformance(t *testing.T) { commandConformance(t, nil) }
func TestQueueAmbiguityUsesSameIntent(t *testing.T) {
	f := setup(t, nil)
	f.q.fail = true
	if out := f.s.Broker(context.Background(), f.r); out.State != "unconfirmed" || out.Reason != "queue-unconfirmed" {
		t.Fatal("unconfirmed queue claimed acceptance")
	}
	f.q.fail = false
	out := f.s.Broker(context.Background(), f.r)
	if out.State != "queued" || len(f.docs.items) != 5 {
		t.Fatal("retry did not recover same intent")
	}
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	if e := f.dispatch(t, "2"); e != nil || f.provider.submits != 1 {
		t.Fatal("ambiguous queue retry repeated actuation")
	}
}

func TestPostEnqueueWriteFailureIsUnconfirmed(t *testing.T) {
	f := setup(t, nil)
	f.docs.failState = "queued"
	out := f.s.Broker(context.Background(), f.r)
	if out.State != "unconfirmed" || out.CommandID == "" {
		t.Fatal("confirmed-send persistence failure incorrectly rejected")
	}
	if f.status().State != "unconfirmed" {
		t.Fatal("unconfirmed intent claimed queued")
	}
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	again := f.s.Broker(context.Background(), f.r)
	if again.State != "succeeded" || f.provider.submits != 1 {
		t.Fatal("uncertain queue recovery lost outcome")
	}
}
func TestDisableUnlinkAndWrongSourceNeverSubmit(t *testing.T) {
	for _, which := range []string{"flags", "unlink", "authority", "queue-source"} {
		t.Run(which, func(t *testing.T) {
			f := setup(t, nil)
			if f.s.Broker(context.Background(), f.r).State != "queued" {
				t.Fatal("fixture queue")
			}
			switch which {
			case "flags":
				f.flags.value.ActuationEnabled = false
			case "unlink":
				r := f.r
				r.Operation = "event"
				r.EventType = "AlexaSkillEvent.SkillAccountUnlinked"
				r.EventPublishingTime = r.Timestamp
				if f.s.Broker(context.Background(), r).State != "succeeded" {
					t.Fatal("event not persisted")
				}
			case "authority":
				f.auth.reject = true
			case "queue-source":
				f.s.Config.QueueARN = "wrong-queue"
			}
			ctx, cancel := context.WithDeadline(context.Background(), f.now.Add(120*time.Second))
			defer cancel()
			msg := f.message("1")
			if which == "queue-source" {
				msg.EventSourceARN = "synthetic-queue"
			}
			if e := f.s.Dispatch(ctx, msg); e != nil {
				t.Fatal(e)
			}
			if f.provider.submits != 0 {
				t.Fatal("unauthorized actuation")
			}
		})
	}
}
func TestSubmissionCrashAndPersistenceFailureNeverResend(t *testing.T) {
	for _, mode := range []string{"panic", "write-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t, nil)
			_ = f.s.Broker(context.Background(), f.r)
			if mode == "panic" {
				f.provider.panicSubmit = true
			} else {
				f.docs.failState = "submitted"
			}
			e := f.dispatch(t, "1")
			if mode == "write-failure" && !errors.Is(e, ErrPersistence) {
				t.Fatal("persistence failure acknowledged")
			}
			if mode == "panic" && e != nil {
				t.Fatal("panic blocked group")
			}
			if e = f.dispatch(t, "2"); e != nil {
				t.Fatal(e)
			}
			if f.provider.submits != 1 || f.status().State != "outcome-unknown" {
				t.Fatal("uncertain actuation was repeated")
			}
		})
	}
}
func TestRefreshPersistenceAndBoundedPoll(t *testing.T) {
	f := setup(t, nil)
	f.creds.record.GMExpiresAt = 0
	f.creds.record.Grant.ExpiresAt = 0
	f.provider.pending = true
	_ = f.s.Broker(context.Background(), f.r)
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	if f.provider.refreshes != 1 || f.creds.saves < 2 || f.provider.submits != 1 || f.provider.polls > 45 || f.status().State != "outcome-unknown" {
		t.Fatal("refresh/poll bounds failed")
	}
}
func TestDryRunMakesNoProviderRequest(t *testing.T) {
	f := setup(t, nil)
	f.flags.value = Flags{ProviderPaused: true}
	f.s.Config.DryRunAllowed = true
	f.r.DryRun = true
	if f.s.Broker(context.Background(), f.r).State != "queued" {
		t.Fatal("dry run not queued")
	}
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	if f.status().State != "dry-run" || f.creds.loads != 0 || f.provider.submits != 0 || f.provider.refreshes != 0 || f.provider.exchanges != 0 {
		t.Fatal("dry run called provider")
	}
}
func TestExpiryAndLeaseContentionAcknowledge(t *testing.T) {
	f := setup(t, nil)
	_ = f.s.Broker(context.Background(), f.r)
	f.creds.busy = true
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	if f.status().State != "expired" || f.provider.submits != 0 {
		t.Fatal("busy vehicle not expired")
	}
}

func TestMobileFamilyRevocationStopsQueuedCommand(t *testing.T) {
	f := setup(t, nil)
	ctx := context.Background()
	family, _, e := auth.NewFamily("synthetic-owner", "synthetic-owner", "synthetic-owner", "synthetic-phone", *f.now)
	if e != nil {
		t.Fatal(e)
	}
	value := struct {
		Family      auth.Family
		Tokens      amazon.Tokens
		ValidatedAt time.Time
	}{family, amazon.Tokens{AccessToken: "synthetic-mobile-access", RefreshToken: "synthetic-mobile-refresh", ExpiresAt: f.now.Add(time.Hour)}, *f.now}
	encoded, _ := json.Marshal(value)
	doc := auth.Document{Key: "family#" + family.ID, ExpiresAt: family.Deadline, Data: encoded}
	if e = f.docs.Commit(ctx, doc); e != nil {
		t.Fatal(e)
	}
	f.s.Families = &auth.MobileService{Store: f.docs, Owner: "synthetic-owner", Now: func() time.Time { return *f.now }}
	f.r.Source = "mobile"
	f.r.FamilyID = family.ID
	f.r.PersonID = ""
	f.r.UserID = ""
	f.r.PersonAccessToken = ""
	f.r.SkillID = ""
	accepted := f.s.Broker(ctx, f.r)
	if accepted.State != "queued" {
		t.Fatal("mobile intent not queued", accepted)
	}
	doc, e = f.docs.Load(ctx, doc.Key)
	if e != nil {
		t.Fatal(e)
	}
	value.Family.Revoked = true
	doc.Data, _ = json.Marshal(value)
	if e = f.docs.Commit(ctx, doc); e != nil {
		t.Fatal(e)
	}
	if e = f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	if f.provider.submits != 0 {
		t.Fatal("revoked mobile session dispatched")
	}
}

func TestReconciliationAuthorityLossStaysUnknown(t *testing.T) {
	f := setup(t, nil)
	_ = f.s.Broker(context.Background(), f.r)
	f.provider.pending = true
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	f.auth.reject = true
	if e := f.dispatch(t, "2"); e != nil {
		t.Fatal(e)
	}
	doc, e := f.docs.Load(context.Background(), "command#"+f.q.id)
	if e != nil {
		t.Fatal(e)
	}
	c, _ := decoded[command](doc)
	if c.State != "outcome-unknown" || f.provider.submits != 1 {
		t.Fatal("unknown action mislabeled failure or resubmitted")
	}
}

func TestAuthorityRevokedDuringRefreshNeverSubmits(t *testing.T) {
	f := setup(t, nil)
	f.creds.record.GMExpiresAt = 0
	f.creds.record.Grant.ExpiresAt = 0
	_ = f.s.Broker(context.Background(), f.r)
	f.provider.afterExchange = func() { f.auth.reject = true }
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	if f.provider.submits != 0 || f.provider.refreshes != 1 {
		t.Fatal("late authority revocation not enforced")
	}
}

func TestReplayAuthorizationUncertaintyPreservesIntent(t *testing.T) {
	f := setup(t, nil)
	first := f.s.Broker(context.Background(), f.r)
	if first.State != "queued" {
		t.Fatal("fixture queue")
	}
	f.auth.reject = true
	again := f.s.Broker(context.Background(), f.r)
	if again.State != "unconfirmed" || again.Action != first.Action || again.CommandID != first.CommandID || f.q.calls != 1 {
		t.Fatal("replay authority failure falsely rejected prior intent")
	}
}

func TestDurableOwnerBindingRequiredAtAcceptAndDispatch(t *testing.T) {
	for _, change := range []string{"delete", "different-owner", "malformed"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t, nil)
			ctx := context.Background()
			if f.s.Broker(ctx, f.r).State != "queued" {
				t.Fatal("fixture intent failed")
			}
			f.docs.mu.Lock()
			switch change {
			case "delete":
				delete(f.docs.items, "owner-binding")
			default:
				doc := f.docs.items["owner-binding"]
				doc.Version++
				if change == "malformed" {
					doc.Data = []byte(`{"invalid":true}`)
				} else {
					doc.Data = []byte(`"another-owner"`)
				}
				f.docs.items[doc.Key] = doc
			}
			f.docs.mu.Unlock()
			if e := f.dispatch(t, "1"); e != nil {
				t.Fatal(e)
			}
			if f.provider.submits != 0 || f.provider.refreshes != 0 || f.provider.exchanges != 0 || f.creds.loads != 0 {
				t.Fatal("unverified owner reached GM credentials or provider")
			}
			f.r.RequestID = "synthetic-new-request"
			out := f.s.Broker(ctx, f.r)
			if out.State != "unconfirmed" || out.Reason != "owner-binding-unverified" || f.q.calls != 1 {
				t.Fatal("owner binding bypassed at accept")
			}
			f.docs.mu.Lock()
			_, exists := f.docs.items["owner-binding"]
			f.docs.mu.Unlock()
			if change == "delete" && exists {
				t.Fatal("owner binding recreated by command service")
			}
		})
	}
}

func TestDurableOwnerChangeDuringRefreshStopsSubmit(t *testing.T) {
	f := setup(t, nil)
	f.creds.record.GMExpiresAt = 0
	f.creds.record.Grant.ExpiresAt = 0
	_ = f.s.Broker(context.Background(), f.r)
	f.provider.afterExchange = func() { f.docs.mu.Lock(); delete(f.docs.items, "owner-binding"); f.docs.mu.Unlock() }
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal(e)
	}
	if f.provider.submits != 0 {
		t.Fatal("owner binding not reread immediately before dispatch")
	}
}

func TestDispatchCASIncludesDurableOwnerBinding(t *testing.T) {
	f := setup(t, nil)
	_ = f.s.Broker(context.Background(), f.r)
	f.docs.beforeCommit = func(writes []auth.Document) {
		for _, doc := range writes {
			if strings.HasPrefix(doc.Key, "command#") {
				c, _ := decoded[command](doc)
				if c.State == "dispatching" {
					delete(f.docs.items, "owner-binding")
					f.docs.beforeCommit = nil
					return
				}
			}
		}
	}
	if e := f.dispatch(t, "1"); e != nil {
		t.Fatal("normal owner change blocked the queue", e)
	}
	if f.provider.submits != 0 {
		t.Fatal("owner deletion raced past dispatch CAS")
	}
	doc, e := f.docs.Load(context.Background(), "command#"+f.q.id)
	if e != nil {
		t.Fatal(e)
	}
	c, _ := decoded[command](doc)
	if c.State != "failed" || c.Reason != "dispatch-state-changed" {
		t.Fatal("conditional change not durably handled")
	}
}

func TestDispatchConflictAcknowledgesExistingOutcome(t *testing.T) {
	for _, state := range []string{"dispatching", "submitted", "outcome-unknown", "succeeded"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t, nil)
			_ = f.s.Broker(context.Background(), f.r)
			f.docs.beforeCommit = func(writes []auth.Document) {
				for _, doc := range writes {
					if strings.HasPrefix(doc.Key, "command#") {
						c, _ := decoded[command](doc)
						if c.State == "dispatching" {
							existing := f.docs.items[doc.Key]
							current, _ := decoded[command](existing)
							current.State = state
							existing.Version++
							existing.Data, _ = json.Marshal(current)
							f.docs.items[doc.Key] = existing
							f.docs.beforeCommit = nil
							return
						}
					}
				}
			}
			if e := f.dispatch(t, "1"); e != nil || f.provider.submits != 0 {
				t.Fatal("existing outcome retried or blocked", e)
			}
		})
	}
}

func TestSubmitDiagnosticPreservesSafeReasonAndOneLog(t *testing.T) {
	f := setup(t, nil)
	f.provider.submitResult = &onstar.CommandResult{State: "outcome-unknown", Reason: "invalid-status-url", RequestID: "synthetic-private-request", URLClass: "host", ProviderStatusClass: "pending", HTTPStatusClass: "2xx"}
	f.provider.submitErr = &onstar.Error{Kind: "invalid-status-url"}
	var logs []CommandDiagnostic
	f.s.LogCommand = func(entry CommandDiagnostic) { logs = append(logs, entry) }
	_ = f.s.Broker(context.Background(), f.r)
	if err := f.dispatch(t, "1"); err != nil {
		t.Fatal(err)
	}
	doc, e := f.docs.Load(context.Background(), "command#"+f.q.id)
	if e != nil {
		t.Fatal(e)
	}
	c, _ := decoded[command](doc)
	if c.Reason != "invalid-status-url" || c.Result.Reason != "invalid-status-url" || c.State != "outcome-unknown" || f.provider.submits != 1 || len(logs) != 1 {
		t.Fatal("safe parser cause hidden or command repeated")
	}
	if logs[0].ErrorKind != "invalid-status-url" || logs[0].HTTPStatusClass != "2xx" || logs[0].URLClass != "host" || !logs[0].HasProviderID || logs[0].HasStatusURL {
		t.Fatal("diagnostic classification missing")
	}
}

func TestCommandDiagnosticNeverLogsPrivateValues(t *testing.T) {
	f := setup(t, nil)
	canary := "SYNTHETIC_PRIVATE_DIAGNOSTIC_CANARY"
	var recorded CommandDiagnostic
	f.s.LogCommand = func(entry CommandDiagnostic) { recorded = entry }
	result := onstar.CommandResult{State: canary, Reason: canary, RequestID: canary, StatusURL: "https://private.invalid/" + canary, URLClass: canary, ProviderStatusClass: canary, HTTPStatusClass: canary}
	ctx := lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{AwsRequestID: "11111111-2222-3333-4444-555555555555"})
	f.s.observe(ctx, "submit", hash("synthetic-command"), result, &onstar.Error{Kind: canary, Status: 403}, *f.now)
	encoded, _ := json.Marshal(recorded)
	for _, private := range []string{canary, "private.invalid", testdata.VIN, "https://"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("diagnostic leaked private value")
		}
	}
	if recorded.ErrorKind != "unclassified" || recorded.HTTPStatusClass != "4xx" || recorded.Reason != "provider-outcome-unclassified" || !recorded.HasProviderID || !recorded.HasStatusURL {
		t.Fatal("diagnostic did not retain safe facts")
	}
	var fields map[string]any
	if json.Unmarshal(encoded, &fields) != nil {
		t.Fatal("diagnostic JSON invalid")
	}
	for _, key := range []string{"ts", "level", "svc", "attempt", "requestId", "op", "outcome", "durationMs"} {
		if _, ok := fields[key]; !ok {
			t.Fatal("planned diagnostic field missing", key)
		}
	}
	for _, key := range []string{"operation", "state", "durationMilliseconds"} {
		if _, ok := fields[key]; ok {
			t.Fatal("unplanned diagnostic alias", key)
		}
	}
	if recorded.Service != "worker" || recorded.Level != "WARN" || recorded.Attempt != 1 || recorded.RequestID == "" {
		t.Fatal("runtime diagnostic metadata invalid")
	}
}
