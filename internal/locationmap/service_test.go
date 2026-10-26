package locationmap

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
	"github.com/aws/aws-lambda-go/events"
	"math"
	"strings"
	"testing"
	"time"
)

func TestPhoneTrackingCadenceFusionAndSourcePreservation(t *testing.T) {
	f := newResolverFixture()
	ctx := context.Background()
	vehicleTime := f.at
	rawKey := f.observation(t, onstar.LocationFix{ReportedAt: &vehicleTime})
	rawBytes := string(f.docs[rawKey].Data)
	sample := PhoneSample{ObservedAt: f.at, AccuracyMeters: 5}
	first, err := RecordPhone(ctx, f.docs, "synthetic-phone-a", sample, f.at)
	if err != nil || !first.Accepted || !first.Synced {
		t.Fatal("nearby initial phone did not synchronize", err)
	}
	key, selected, err := CurrentObservation(ctx, f.docs, f.at)
	if err != nil || selected.Source != "phone" || selected.OriginRevision != first.ObservationRevision || selected.VehicleRevision != strings.TrimPrefix(rawKey, "location-observation#") || key == rawKey {
		t.Fatal("synchronized phone not selected", err)
	}
	if err = f.r.Resolve(ctx, key); err != nil {
		t.Fatal("phone observation cannot use existing resolver", err)
	}
	for _, tc := range []struct {
		name     string
		advance  time.Duration
		latitude float64
		reason   string
	}{
		{"cadence", time.Minute, metres(100).Latitude, "phone-update-too-soon"},
		{"movement", 2 * time.Minute, metres(1).Latitude, "phone-not-moved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := sample
			candidate.ObservedAt = f.at.Add(tc.advance)
			candidate.Latitude = tc.latitude
			before := len(f.docs)
			result, e := RecordPhone(ctx, f.docs, "synthetic-phone-a", candidate, candidate.ObservedAt)
			if e != nil || result.Accepted || result.Reason != tc.reason || len(f.docs) != before {
				t.Fatal("nonqualifying update persisted", e)
			}
		})
	}
	f.at = f.at.Add(2 * time.Minute)
	sample.ObservedAt, sample.Latitude = f.at, metres(100).Latitude
	moved, err := RecordPhone(ctx, f.docs, "synthetic-phone-a", sample, f.at)
	if err != nil || !moved.Accepted || !moved.Synced {
		t.Fatal("tracking stopped between vehicle observations", err)
	}
	other := sample
	other.Latitude = 0
	result, err := RecordPhone(ctx, f.docs, "synthetic-phone-b", other, f.at)
	if err != nil || !result.Accepted || result.Synced {
		t.Fatal("another phone stole synchronization from old vehicle fix", err)
	}
	_, selected, err = CurrentObservation(ctx, f.docs, f.at)
	if err != nil || selected.OriginRevision != moved.ObservationRevision {
		t.Fatal("active phone changed", err)
	}
	if string(f.docs[rawKey].Data) != rawBytes {
		t.Fatal("phone tracking rewrote raw OnStar observation")
	}
	vehicleTime = f.at
	newKey := f.observation(t, onstar.LocationFix{ReportedAt: &vehicleTime})
	key, selected, err = CurrentObservation(ctx, f.docs, f.at)
	if err != nil || key != newKey || selected.Source != "onstar" {
		t.Fatal("new separated vehicle fix did not end phone tracking", err)
	}
}

func TestPhoneTrackingRejectsBadSamplesAndFallsBackWhenStale(t *testing.T) {
	f := newResolverFixture()
	ctx := context.Background()
	at := f.at
	rawKey := f.observation(t, onstar.LocationFix{ReportedAt: &at})
	sample := PhoneSample{ObservedAt: at, AccuracyMeters: 5}
	for _, bad := range []PhoneSample{
		{Latitude: math.NaN(), ObservedAt: at},
		{ObservedAt: at.Add(61 * time.Second)},
		{ObservedAt: at.Add(-301 * time.Second)},
		{ObservedAt: at, AccuracyMeters: 31},
	} {
		if _, err := RecordPhone(ctx, f.docs, "synthetic-phone", bad, at); !errors.Is(err, auth.ErrInvalid) {
			t.Fatal("bad phone sample accepted")
		}
	}
	if _, err := RecordPhone(ctx, f.docs, "synthetic-phone", sample, at); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordPhone(ctx, f.docs, "synthetic-phone", sample, at.Add(2*time.Minute)); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("out-of-order sample accepted")
	}
	key, obs, err := CurrentObservation(ctx, f.docs, at.Add(6*time.Minute))
	if err != nil || key != rawKey || obs.Source != "onstar" {
		t.Fatal("stale phone did not fall back", err)
	}
}

func TestPhoneSynchronizesBesideVehicleBetweenScheduledPolls(t *testing.T) {
	f := newResolverFixture()
	ctx := context.Background()
	vehicleTime := f.at
	rawKey := f.observation(t, onstar.LocationFix{ReportedAt: &vehicleTime})
	rawBytes := string(f.docs[rawKey].Data)
	f.at = f.at.Add(5 * time.Minute)
	result, err := RecordPhone(ctx, f.docs, "synthetic-phone", PhoneSample{Latitude: metres(20).Latitude, ObservedAt: f.at, AccuracyMeters: 5}, f.at)
	if err != nil || !result.Accepted || !result.Synced {
		t.Fatal("fresh nearby phone could not synchronize between vehicle polls", err)
	}
	_, selected, err := CurrentObservation(ctx, f.docs, f.at)
	if err != nil || selected.Source != "phone" || selected.VehicleRevision != strings.TrimPrefix(rawKey, "location-observation#") || string(f.docs[rawKey].Data) != rawBytes {
		t.Fatal("synchronization lost source provenance", err)
	}
}

func TestResolveCurrentUsesEffectiveGeofenceObservation(t *testing.T) {
	f := newResolverFixture()
	ctx := context.Background()
	at := f.at
	rawKey := f.observation(t, onstar.LocationFix{Latitude: metres(20).Latitude, ReportedAt: &at})
	rawBytes := string(f.docs[rawKey].Data)
	useGeofences(t, GeofenceConfig{Fences: []Geofence{{Name: "Home", RadiusMeters: 30.48}, {Name: "Work", Latitude: metres(5000).Latitude, RadiusMeters: 60.96}}})
	status, err := f.r.ResolveCurrent(ctx)
	if err != nil || !status.HasMap || !status.MatchesLatest || f.renders != 1 {
		t.Fatal("stored-map request did not resolve effective location", err)
	}
	current, err := Current(ctx, f.docs, at)
	if err != nil || current.Place != "Home" || current.Fix.Latitude != 0 || current.Projection.CenterLatitude != 0 || string(f.docs[rawKey].Data) != rawBytes {
		t.Fatal("stored-map request changed raw source or used wrong center", err)
	}
}

func TestPhoneEffectiveMapBindsLatestVehicleRevision(t *testing.T) {
	f := newResolverFixture()
	ctx := context.Background()
	at := f.at
	f.observation(t, onstar.LocationFix{ReportedAt: &at})
	phone, err := RecordPhone(ctx, f.docs, "synthetic-phone", PhoneSample{ObservedAt: at, AccuracyMeters: 5}, at)
	if err != nil {
		t.Fatal(err)
	}
	firstKey, _, err := CurrentObservation(ctx, f.docs, at)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.r.Resolve(ctx, firstKey); err != nil {
		t.Fatal(err)
	}
	f.at = f.at.Add(time.Minute)
	newTime := f.at
	vehicleKey := f.observation(t, onstar.LocationFix{Latitude: metres(1).Latitude, ReportedAt: &newTime})
	secondKey, selected, err := CurrentObservation(ctx, f.docs, f.at)
	if err != nil || secondKey == firstKey || selected.OriginRevision != phone.ObservationRevision || selected.VehicleRevision != strings.TrimPrefix(vehicleKey, "location-observation#") || f.renders != 1 {
		t.Fatal("new vehicle revision lost phone provenance or rendered automatically", err)
	}
	if err = f.r.Resolve(ctx, secondKey); err != nil {
		t.Fatal(err)
	}
	current, err := Current(ctx, f.docs, f.at)
	if err != nil || current.VehicleRevision != selected.VehicleRevision || current.OriginRevision != phone.ObservationRevision || f.renders != 1 {
		t.Fatal("cached effective image lost vehicle readiness revision", err)
	}
}

func TestGeofenceDerivesCenterAndPreservesRawSource(t *testing.T) {
	f := newResolverFixture()
	ctx := context.Background()
	at := f.at
	rawKey := f.observation(t, onstar.LocationFix{Latitude: metres(10).Latitude, ReportedAt: &at})
	before := string(f.docs[rawKey].Data)
	config := GeofenceConfig{Fences: []Geofence{{Name: "Home", RadiusMeters: 30.48}, {Name: "Work", Latitude: metres(1000).Latitude, RadiusMeters: 60.96}}}
	useGeofences(t, config)
	key, obs, err := CurrentObservation(ctx, f.docs, at)
	if err != nil || key == rawKey || obs.Source != "geofence" || obs.OriginSource != "onstar" || obs.Place != "Home" || obs.Fix.Latitude != 0 || obs.Fix.ReportedAt == nil || !obs.Fix.ReportedAt.Equal(at) {
		t.Fatal("geofence center/provenance mismatch", err)
	}
	key2, _, err := CurrentObservation(ctx, f.docs, at)
	if err != nil || key2 != key || string(f.docs[rawKey].Data) != before {
		t.Fatal("derived identity unstable or raw fix rewritten", err)
	}
	work, place, err := GeofencedFix(metres(1040), config)
	if err != nil || place != "Work" || work.Latitude != metres(1000).Latitude {
		t.Fatal("work radius mismatch")
	}
	outside := metres(2000)
	fix, place, err := GeofencedFix(outside, config)
	if err != nil || place != "" || fix != outside {
		t.Fatal("outside position snapped")
	}
}

type documents map[string]auth.Document

func (d documents) Load(_ context.Context, k string) (auth.Document, error) {
	v, ok := d[k]
	if !ok {
		return v, auth.ErrMissing
	}
	return v, nil
}

type refreshFamilies struct{ revoked bool }

func (f *refreshFamilies) CommandFamily(context.Context, string) (string, error) {
	if f.revoked {
		return "", auth.ErrRevoked
	}
	return "synthetic-owner-authority", nil
}

type refreshAuthority struct {
	denied bool
	after  func()
}

func (a *refreshAuthority) Validate(context.Context, string) (amazon.Identity, error) {
	if a.after != nil {
		a.after()
	}
	if a.denied {
		return amazon.Identity{}, auth.ErrUnavailable
	}
	return amazon.Identity{Subject: "owner"}, nil
}

type refreshQueue struct {
	bodies, ids []string
	fail        bool
}

func (q *refreshQueue) Send(_ context.Context, body, id string) error {
	q.bodies = append(q.bodies, body)
	q.ids = append(q.ids, id)
	if q.fail {
		return auth.ErrUnavailable
	}
	return nil
}
func manualSetup(t *testing.T) (*Service, *provider, documents, *refreshQueue, *refreshFamilies, *refreshAuthority) {
	s, p, _, docs, _, _ := setup(t)
	q := &refreshQueue{}
	f := &refreshFamilies{}
	a := &refreshAuthority{}
	s.Queue = q
	s.Families = f
	s.Authority = a
	docs["family#synthetic-family"] = auth.Document{Key: "family#synthetic-family", Version: 1, ExpiresAt: s.now().Add(24 * time.Hour), Data: json.RawMessage(`{"synthetic":"family"}`)}
	return s, p, docs, q, f, a
}
func manualMessage(body string) events.SQSMessage {
	return events.SQSMessage{EventSource: "aws:sqs", EventSourceARN: "queue", Attributes: map[string]string{"MessageGroupId": "loc-truck"}, Body: body}
}
func TestOwnerRefreshDeduplicatesCoalescesAndTracksSameImage(t *testing.T) {
	s, p, docs, q, _, _ := manualSetup(t)
	ctx := context.Background()
	id := "synthetic-refresh-one"
	first, err := s.RequestRefresh(ctx, "synthetic-family", id)
	if err != nil || first.State != "queued" || first.PollAfterSeconds != 10 {
		t.Fatal("request not queued", err)
	}
	again, err := s.RequestRefresh(ctx, "synthetic-family", id)
	if err != nil || again.OperationID != first.OperationID {
		t.Fatal("retry changed identity")
	}
	other, err := s.RequestRefresh(ctx, "synthetic-family", "synthetic-refresh-two")
	if err != nil || other.OperationID != first.OperationID {
		t.Fatal("tap did not coalesce", err)
	}
	for _, body := range q.bodies {
		if strings.Contains(body, "synthetic-family") || strings.Contains(body, "synthetic-owner-authority") {
			t.Fatal("queue leaked session scope")
		}
		if err = s.HandleRefresh(ctx, manualMessage(body)); err != nil {
			t.Fatal(err)
		}
	}
	if p.calls != 1 || len(p.wakes) != 1 || !p.wakes[0] {
		t.Fatal("duplicate wake", p.calls)
	}
	status, err := RequestStatus(ctx, docs, s.Owner, "synthetic-family", id, s.now())
	if err != nil || status.State != "resolving" || status.ObservationRevision == "" {
		t.Fatal("missing observation status", err)
	}
	before := p.calls
	for i := 0; i < 10; i++ {
		if _, err = RequestStatus(ctx, docs, s.Owner, "synthetic-family", id, s.now()); err != nil {
			t.Fatal(err)
		}
	}
	if p.calls != before {
		t.Fatal("status contacted provider")
	}
	op, _ := loadRefresh(ctx, docs, first.OperationID, s.now())
	doc := docs[op.ObservationID]
	var observation Observation
	json.Unmarshal(doc.Data, &observation)
	observation.Status = "ready"
	observation.Map = &CurrentMap{ObservationRevision: observation.Revision, ImageID: strings.Repeat("a", 32), ImageVersion: "1", GeneratedAt: s.now(), ExpiresAt: s.now().Add(CacheLifetime), Fix: observation.Fix, ReceivedAt: observation.ReceivedAt}
	if err = docs.Commit(ctx, encoded(doc, observation, doc.ExpiresAt)); err != nil {
		t.Fatal(err)
	}
	status, err = RequestStatus(ctx, docs, s.Owner, "synthetic-family", "synthetic-refresh-two", s.now())
	if err != nil || status.State != "ready" || status.ImageID != strings.Repeat("a", 32) || status.ObservationRevision != observation.Revision || status.ReceivedAt.IsZero() {
		t.Fatal("matching ready view absent", status, err)
	}
	if _, err = s.RequestRefresh(ctx, "synthetic-family", "synthetic-refresh-three"); !errors.Is(err, auth.ErrRate) {
		t.Fatal("rapid new wake not bounded", err)
	}
}
func TestOwnerRefreshGuardsFailuresAndExpiry(t *testing.T) {
	for _, kind := range []string{"revoked", "paused", "cooldown", "busy", "provider-error", "missing-fix", "queue-ambiguity", "timeout", "family-conflict"} {
		t.Run(kind, func(t *testing.T) {
			s, p, docs, q, f, a := manualSetup(t)
			ctx := context.Background()
			id := "synthetic-refresh-guard"
			if kind == "family-conflict" {
				a.after = func() { d := docs["family#synthetic-family"]; d.Version++; docs[d.Key] = d }
				if _, err := s.RequestRefresh(ctx, "synthetic-family", id); !errors.Is(err, auth.ErrConflict) || len(q.bodies) != 0 {
					t.Fatal("family race accepted", err)
				}
				return
			}
			if kind == "queue-ambiguity" {
				q.fail = true
			}
			out, err := s.RequestRefresh(ctx, "synthetic-family", id)
			if kind == "queue-ambiguity" {
				if err == nil {
					t.Fatal("ambiguous send claimed success")
				}
				q.fail = false
				out, err = s.RequestRefresh(ctx, "synthetic-family", id)
			}
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "revoked":
				f.revoked = true
			case "paused":
				s.RequestPaused = func(context.Context) (bool, error) { return true, nil }
			case "cooldown":
				s.Credentials.(*credentials).record.CooldownUntil = s.now().Add(time.Hour).Unix()
			case "busy":
				s.Credentials.(*credentials).held = true
			case "provider-error":
				p.problem = errors.New("synthetic provider failure")
			case "missing-fix":
				p.pending = true
			case "timeout":
				at := s.now().Add(refreshDeadline)
				s.Now = func() time.Time { return at }
			}
			if err = s.HandleRefresh(ctx, manualMessage(q.bodies[0])); err != nil {
				t.Fatal(err)
			}
			status, err := RequestStatus(ctx, docs, s.Owner, "synthetic-family", id, s.now())
			if err != nil {
				t.Fatal(err)
			}
			if kind == "queue-ambiguity" {
				if status.State != "resolving" || p.calls != 1 {
					t.Fatal("stable retry failed")
				}
				if err = s.HandleRefresh(ctx, manualMessage(q.bodies[len(q.bodies)-1])); err != nil || p.calls != 1 {
					t.Fatal("redelivery repeated wake")
				}
				return
			}
			if status.State != "failed" {
				t.Fatal("failure not terminal", kind, status.State, out.OperationID)
			}
			if kind != "provider-error" && kind != "missing-fix" && p.calls != 0 {
				t.Fatal("guard contacted provider")
			}
		})
	}
}

func TestOwnerRefreshRevalidatesImmediatelyBeforeWake(t *testing.T) {
	s, p, _, q, _, a := manualSetup(t)
	calls := 0
	a.after = func() {
		calls++
		if calls == 3 {
			a.denied = true
		}
	}
	if _, err := s.RequestRefresh(context.Background(), "synthetic-family", "synthetic-late-revocation"); err != nil {
		t.Fatal(err)
	}
	if err := s.HandleRefresh(context.Background(), manualMessage(q.bodies[0])); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 || calls != 3 {
		t.Fatal("late owner guard did not prevent wake")
	}
}
func (d documents) QueryDocument(ctx context.Context, k string) (auth.Document, error) {
	return d.Load(ctx, k)
}
func (d documents) Commit(_ context.Context, docs ...auth.Document) error {
	for _, v := range docs {
		if d[v.Key].Version != v.Version {
			return auth.ErrConflict
		}
	}
	for _, v := range docs {
		v.Version++
		d[v.Key] = v
	}
	return nil
}

type credentials struct {
	record state.Record
	held   bool
}

type temporarilyBusyCredentials struct {
	*credentials
	remaining, attempts int
}

func (c *temporarilyBusyCredentials) Acquire(ctx context.Context, holder string, at time.Time) (state.Lease, error) {
	c.attempts++
	if c.remaining > 0 {
		c.remaining--
		return state.Lease{}, state.ErrConflict
	}
	return c.credentials.Acquire(ctx, holder, at)
}

func TestOwnerRefreshJoinsScheduledObservationAndDefersLeaseConflict(t *testing.T) {
	ctx := context.Background()
	t.Run("scheduled-in-flight", func(t *testing.T) {
		s, p, docs, q, _, _ := manualSetup(t)
		id := "synthetic-join-schedule"
		out, err := s.RequestRefresh(ctx, "synthetic-family", id)
		if err != nil {
			t.Fatal(err)
		}
		at := s.now().Add(time.Second)
		s.Now = func() time.Time { return at }
		if err = s.Handle(ctx, message(at)); err != nil {
			t.Fatal(err)
		}
		if err = s.HandleRefresh(ctx, manualMessage(q.bodies[0])); err != nil {
			t.Fatal(err)
		}
		if p.calls != 1 {
			t.Fatal("manual refresh repeated scheduled wake")
		}
		op, err := loadRefresh(ctx, docs, out.OperationID, s.now())
		if err != nil || op.ObservationID == "" || op.State != "resolving" {
			t.Fatal("did not join scheduled observation", err)
		}
	})
	t.Run("command-lease", func(t *testing.T) {
		s, p, _, q, _, _ := manualSetup(t)
		c := &temporarilyBusyCredentials{credentials: s.Credentials.(*credentials), remaining: 2}
		s.Credentials = c
		if _, err := s.RequestRefresh(ctx, "synthetic-family", "synthetic-busy-provider"); err != nil {
			t.Fatal(err)
		}
		if err := s.HandleRefresh(ctx, manualMessage(q.bodies[0])); err != nil {
			t.Fatal(err)
		}
		if c.attempts != 3 || p.calls != 1 {
			t.Fatal("bounded lease defer did not collect once")
		}
	})
}

func TestOwnerRefreshMetadataAndTerminalResolution(t *testing.T) {
	s, _, docs, q, _, _ := manualSetup(t)
	ctx := context.Background()
	id := "synthetic-metadata-id"
	out, err := s.RequestRefresh(ctx, "synthetic-family", id)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.HandleRefresh(ctx, manualMessage(q.bodies[0])); err != nil {
		t.Fatal(err)
	}
	op, _ := loadRefresh(ctx, docs, out.OperationID, s.now())
	doc := docs[op.ObservationID]
	var observation Observation
	json.Unmarshal(doc.Data, &observation)
	current, err := CurrentStatus(ctx, docs, s.Owner, s.now())
	if err != nil || current.ObservationRevision != observation.Revision || current.State != "resolving" || current.ReceivedAt.IsZero() {
		t.Fatal("current pending timestamp missing", err)
	}
	observation.Status = "failed"
	observation.Reason = "map-source-http-400"
	if err = docs.Commit(ctx, encoded(doc, observation, doc.ExpiresAt)); err != nil {
		t.Fatal(err)
	}
	failed, err := RequestStatus(ctx, docs, s.Owner, "synthetic-family", id, s.now())
	if err != nil || failed.State != "failed" || failed.Reason != "map-source-http-400" {
		t.Fatal("resolver failure not terminal", err)
	}
	doc = docs[op.ObservationID]
	observation.Status = "ready"
	observation.Reason = ""
	observation.Map = &CurrentMap{ImageID: strings.Repeat("b", 32), ImageVersion: "1", GeneratedAt: s.now(), ExpiresAt: s.now().Add(CacheLifetime)}
	docs.Commit(ctx, encoded(doc, observation, doc.ExpiresAt))
	latestDoc := docs["location-latest"]
	var currentPointer latest
	json.Unmarshal(latestDoc.Data, &currentPointer)
	currentPointer.ObservationID = "newer-observation"
	docs.Commit(ctx, encoded(latestDoc, currentPointer, latestDoc.ExpiresAt))
	superseded, err := RequestStatus(ctx, docs, s.Owner, "synthetic-family", id, s.now())
	if err != nil || superseded.State != "failed" || superseded.Reason != "refresh-superseded" || superseded.ImageID != "" {
		t.Fatal("old map claimed as current", err)
	}
	if _, err = RequestStatus(ctx, docs, s.Owner, "different-family", id, s.now()); !errors.Is(err, auth.ErrMissing) {
		t.Fatal("request receipt escaped family scope")
	}
	if _, err = RequestStatus(ctx, docs, s.Owner, "synthetic-family", id, s.now().Add(refreshRetention)); !errors.Is(err, auth.ErrExpired) {
		t.Fatal("logical request expiry ignored")
	}
}

func (s *credentials) Acquire(context.Context, string, time.Time) (state.Lease, error) {
	if s.held {
		return state.Lease{}, errors.New("held")
	}
	s.held = true
	return state.Lease{}, nil
}
func (s *credentials) Load(context.Context, state.Lease, time.Time) (state.Record, error) {
	return s.record, nil
}
func (s *credentials) Save(_ context.Context, _ state.Lease, r state.Record, _ time.Time) (state.Record, error) {
	s.record = r
	return r, nil
}
func (s *credentials) Release(context.Context, state.Lease) error { s.held = false; return nil }

type provider struct {
	calls   int
	wakes   []bool
	pending bool
	problem error
	fix     onstar.LocationFix
}

func (p *provider) Refresh(context.Context, onstar.Grant) (onstar.Grant, error) {
	return onstar.Grant{}, errors.New("unexpected refresh")
}
func (p *provider) Exchange(context.Context, onstar.Grant, string, string) (onstar.GMToken, error) {
	return onstar.GMToken{}, errors.New("unexpected exchange")
}
func (p *provider) Vehicles(context.Context, onstar.GMToken) ([]onstar.Vehicle, error) {
	return nil, errors.New("unexpected garage")
}
func (p *provider) Location(_ context.Context, _ onstar.GMToken, _ string, wake bool) (onstar.LocationStatus, error) {
	p.calls++
	p.wakes = append(p.wakes, wake)
	if p.problem != nil {
		return onstar.LocationStatus{}, p.problem
	}
	if p.pending {
		return onstar.LocationStatus{Pending: true}, nil
	}
	return onstar.LocationStatus{FixAvailable: true, Fix: &p.fix}, nil
}
func setup(t *testing.T) (*Service, *provider, *credentials, documents, *int, *int) {
	t.Helper()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	b, e := onstar.NewBootstrap(onstar.Grant{RefreshToken: "synthetic", Subject: "synthetic", RefreshObtainedAt: now.Unix()}, "device", testdata.VIN, 1, "ready")
	if e != nil {
		t.Fatal(e)
	}
	p := &provider{}
	creds := &credentials{record: state.Record{Bootstrap: b, ProviderState: "ready", GMToken: "synthetic", GMExpiresAt: now.Add(24 * time.Hour).Unix()}}
	docs := documents{}
	renders, publishes := 0, 0
	s := &Service{Credentials: creds, Provider: p, Docs: docs, Owner: "owner", DeviceID: "device", VehicleKey: "truck", QueueARN: "queue", ScheduleARN: "schedule", Paused: func(context.Context) (bool, error) { return false, nil }, Now: func() time.Time { return now }, Sleep: func(context.Context, time.Duration) error { return nil }}
	return s, p, creds, docs, &renders, &publishes
}
func message(at time.Time) events.SQSMessage {
	body, _ := json.Marshal(map[string]string{"kind": "location", "vehicleKey": "truck", "scheduleArn": "schedule", "scheduledAt": at.Format(time.RFC3339)})
	return events.SQSMessage{EventSource: "aws:sqs", EventSourceARN: "queue", Attributes: map[string]string{"MessageGroupId": "loc-truck"}, Body: string(body)}
}
func TestChangedCoordinatesOnlyAndDuplicateDelivery(t *testing.T) {
	s, p, _, docs, renders, publishes := setup(t)
	ctx := context.Background()
	first := message(s.now())
	if e := s.Handle(ctx, first); e != nil {
		t.Fatal(e)
	}
	if e := s.Handle(ctx, first); e != nil {
		t.Fatal(e)
	}
	if p.calls != 1 || *renders != 0 || *publishes != 0 {
		t.Fatal("duplicate performed work")
	}
	now := s.now().Add(30 * time.Minute)
	s.Now = func() time.Time { return now }
	if e := s.Handle(ctx, message(now)); e != nil {
		t.Fatal(e)
	}
	if p.calls != 2 || *renders != 0 || *publishes != 0 {
		t.Fatal("unchanged coordinates regenerated map")
	}
	p.fix.Longitude = float64(1)
	now = now.Add(30 * time.Minute)
	if e := s.Handle(ctx, message(now)); e != nil {
		t.Fatal(e)
	}
	if *renders != 0 || *publishes != 0 {
		t.Fatal("collector rendered a map")
	}
	if len(docs) != 7 {
		t.Fatal("missing GPS history/slots/latest")
	}
}
func TestPendingBoundPauseAndRateLimit(t *testing.T) {
	s, p, c, _, renders, _ := setup(t)
	p.pending = true
	if e := s.Handle(context.Background(), message(s.now())); e != nil {
		t.Fatal(e)
	}
	if p.calls != 3 || len(p.wakes) != 3 || !p.wakes[0] || p.wakes[1] || p.wakes[2] || *renders != 0 {
		t.Fatal("pending loop repeated wake or fabricated map")
	}
	s, p, c, _, _, _ = setup(t)
	p.problem = &onstar.Error{Kind: "rate-limited", Status: 429, RetryAfterSeconds: 60}
	if e := s.Handle(context.Background(), message(s.now())); e != nil {
		t.Fatal(e)
	}
	if c.record.CooldownUntil != s.now().Add(30*time.Minute).Unix() {
		t.Fatal("rate limit not persisted")
	}
	s, p, _, _, _, _ = setup(t)
	s.Paused = func(context.Context) (bool, error) { return true, nil }
	if e := s.Handle(context.Background(), message(s.now())); e != nil || p.calls != 0 {
		t.Fatal("pause ignored")
	}
}
func TestRejectUntrustedSchedule(t *testing.T) {
	s, p, _, _, _, _ := setup(t)
	m := message(s.now())
	m.Attributes["MessageGroupId"] = "cmd-truck"
	if e := s.Handle(context.Background(), m); e != nil || p.calls != 0 {
		t.Fatal("wrong group accepted")
	}
	m = message(s.now().Add(-time.Hour))
	if e := s.Handle(context.Background(), m); e != nil || p.calls != 0 {
		t.Fatal("stale schedule accepted")
	}
}

func TestCollectorPreservesSourceTimeAndNormalizesObservation(t *testing.T) {
	s, p, _, docs, _, _ := setup(t)
	ctx := context.Background()
	observed := s.now().Add(-time.Hour)
	p.fix.ReportedAt = &observed
	if err := s.Handle(ctx, message(s.now())); err != nil {
		t.Fatal(err)
	}
	var value latest
	json.Unmarshal(docs["location-latest"].Data, &value)
	var observation Observation
	json.Unmarshal(docs[value.ObservationID].Data, &observation)
	if observation.Source != "onstar" || observation.SourceID == "" || observation.Owner != "owner" || observation.Vehicle != "truck" || observation.Status != "pending" || observation.Fix.ReportedAt == nil || !observation.Fix.ReportedAt.Equal(observed) || !observation.ReceivedAt.Equal(s.now()) {
		t.Fatal("observation provenance missing")
	}
	if docs[value.ObservationID].ExpiresAt.Sub(s.now()) != 90*24*time.Hour {
		t.Fatal("history expiry changed")
	}
	status, err := s.Status(ctx)
	if err != nil || !status.HasFix || status.HasMap {
		t.Fatal("collector claimed image")
	}
	later := s.now().Add(30 * time.Minute)
	s.Now = func() time.Time { return later }
	p.problem = &onstar.Error{Kind: "rate-limited", Status: 429}
	if err = s.Handle(ctx, message(later)); err != nil {
		t.Fatal(err)
	}
	next, _ := s.Status(ctx)
	if next.PolledAt != status.PolledAt {
		t.Fatal("failed poll replaced successful observation time")
	}
}

func TestCollectorRetainsKnownCurrentForUnknownAndOlderSourceTimes(t *testing.T) {
	for _, kind := range []string{"unknown", "older", "equal", "newer"} {
		t.Run(kind, func(t *testing.T) {
			s, p, _, docs, _, _ := setup(t)
			ctx := context.Background()
			known := s.now().Add(-time.Minute)
			p.fix.ReportedAt = &known
			if err := s.Handle(ctx, message(s.now())); err != nil {
				t.Fatal(err)
			}
			var first latest
			json.Unmarshal(docs["location-latest"].Data, &first)
			later := s.now().Add(30 * time.Minute)
			s.Now = func() time.Time { return later }
			p.fix.Latitude = metres(100).Latitude
			sourceTime := known
			switch kind {
			case "unknown":
				p.fix.ReportedAt = nil
			case "older":
				sourceTime = known.Add(-time.Minute)
				p.fix.ReportedAt = &sourceTime
			case "equal":
				p.fix.ReportedAt = &sourceTime
			case "newer":
				sourceTime = known.Add(time.Minute)
				p.fix.ReportedAt = &sourceTime
			}
			if err := s.Handle(ctx, message(later)); err != nil {
				t.Fatal(err)
			}
			var current latest
			json.Unmarshal(docs["location-latest"].Data, &current)
			retain := kind == "unknown" || kind == "older"
			if (current.ObservationID == first.ObservationID) != retain {
				t.Fatal("source freshness promotion wrong")
			}
			if retain && (current.Fix.ReportedAt == nil || !current.Fix.ReportedAt.Equal(known) || current.Fix.Latitude != first.Fix.Latitude || !current.PolledAt.Equal(first.PolledAt) || current.Reason == "") {
				t.Fatal("known position replaced or freshness reason lost")
			}
			count := 0
			for key, doc := range docs {
				if len(key) >= len("location-observation#") && key[:len("location-observation#")] == "location-observation#" {
					count++
					var o Observation
					if json.Unmarshal(doc.Data, &o) != nil {
						t.Fatal("invalid history")
					}
					if o.ReceivedAt.Equal(later) && kind == "unknown" && o.Fix.ReportedAt != nil {
						t.Fatal("unknown source time fabricated")
					}
				}
			}
			if count != 2 || p.calls != 2 {
				t.Fatal("accepted stale/unknown observation was discarded")
			}
		})
	}
}

type memoryObjects map[string][]byte

func (m memoryObjects) Put(_ context.Context, id string, data []byte) error {
	if _, ok := m[id]; ok {
		return errors.New("immutable")
	}
	m[id] = append([]byte(nil), data...)
	return nil
}
func (m memoryObjects) Get(_ context.Context, id string) ([]byte, error) {
	data, ok := m[id]
	if !ok {
		return nil, auth.ErrMissing
	}
	return data, nil
}

type resolverFixture struct {
	r       *Resolver
	docs    documents
	objects memoryObjects
	at      time.Time
	renders int
	serial  int
}

func newResolverFixture() *resolverFixture {
	f := &resolverFixture{docs: documents{}, objects: memoryObjects{}, at: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	f.r = &Resolver{Docs: f.docs, Objects: f.objects, Owner: "owner", Vehicle: "truck", Config: DefaultRenderConfig(), Now: func() time.Time { return f.at }, Render: func(context.Context, onstar.LocationFix, time.Time) ([]byte, error) {
		f.renders++
		return []byte("synthetic-map"), nil
	}}
	return f
}
func (f *resolverFixture) observation(t *testing.T, fix onstar.LocationFix) string {
	t.Helper()
	f.serial++
	id := digest(time.Duration(f.serial).String())
	key := "location-observation#" + id
	o := Observation{Owner: "owner", Vehicle: "truck", Source: "onstar", SourceID: id, Revision: id, ReceivedAt: f.at, Fix: fix, Status: "pending"}
	if err := f.docs.Commit(context.Background(), encoded(auth.Document{Key: key}, o, f.at.Add(90*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	doc := f.docs["location-latest"]
	doc.Key = "location-latest"
	if err := f.docs.Commit(context.Background(), encoded(doc, latest{Fix: &fix, PolledAt: f.at, ObservationID: key}, f.at.Add(90*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	return key
}
func (f *resolverFixture) resolve(t *testing.T, fix onstar.LocationFix) CurrentMap {
	t.Helper()
	key := f.observation(t, fix)
	if err := f.r.Resolve(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	m, err := Current(context.Background(), f.docs, f.at)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func metres(n float64) onstar.LocationFix {
	return onstar.LocationFix{Latitude: n / earthRadius * 180 / math.Pi}
}
func TestLocationCacheRevisitBoundariesAndFixedCentre(t *testing.T) {
	for _, feet := range []float64{49, 50, 51} {
		t.Run(time.Duration(feet).String(), func(t *testing.T) {
			f := newResolverFixture()
			first := f.resolve(t, metres(0))
			f.at = f.at.Add(time.Minute)
			next := f.resolve(t, metres(feet*.3048))
			wantHit := feet <= 50
			if next.CacheHit != wantHit || (next.ImageID == first.ImageID) != wantHit {
				t.Fatal("radius mismatch")
			}
			if wantHit && (next.Projection.CenterLatitude != 0 || f.renders != 1) {
				t.Fatal("cache hit moved centre or rendered")
			}
		})
	}
	f := newResolverFixture()
	a := f.resolve(t, metres(0))
	f.at = f.at.Add(time.Minute)
	f.resolve(t, metres(100))
	f.at = f.at.Add(time.Minute)
	again := f.resolve(t, metres(0))
	if again.ImageID != a.ImageID || !again.CacheHit || f.renders != 2 {
		t.Fatal("A B A cache miss")
	}
	f.resolve(t, metres(14))
	drift := f.resolve(t, metres(28))
	if drift.ImageID == a.ImageID {
		t.Fatal("successive hits drifted centre")
	}
}
func TestCacheAdjacentCellNearestAndIdentity(t *testing.T) {
	f := newResolverFixture()
	a := f.resolve(t, metres(-5))
	f.at = f.at.Add(time.Minute)
	b := f.resolve(t, metres(16))
	near := f.resolve(t, metres(10))
	if near.ImageID != b.ImageID || near.ImageID == a.ImageID || !near.CacheHit {
		t.Fatal("nearest adjacent cell not selected")
	}
	tie := f.resolve(t, metres(5.5))
	if tie.ImageID != b.ImageID {
		t.Fatal("equal-distance match did not choose newest generation")
	}
	for _, change := range []func(*RenderConfig){func(c *RenderConfig) { c.Style = "Standard" }, func(c *RenderConfig) { c.Width = 900 }, func(c *RenderConfig) { c.Version += "-next" }} {
		change(&f.r.Config)
		before := f.renders
		m := f.resolve(t, metres(10))
		if m.CacheHit || f.renders != before+1 {
			t.Fatal("incompatible render reused")
		}
	}
}
func TestCacheFixedExpiryAndMissingObject(t *testing.T) {
	f := newResolverFixture()
	first := f.resolve(t, metres(0))
	f.at = f.at.Add(29 * 24 * time.Hour)
	hit := f.resolve(t, metres(0))
	if !hit.ExpiresAt.Equal(first.ExpiresAt) || !hit.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatal("hit extended expiry")
	}
	f.at = first.ExpiresAt
	if _, err := Current(context.Background(), f.docs, f.at); !errors.Is(err, auth.ErrMissing) {
		t.Fatal("current served expired cache")
	}
	next := f.resolve(t, metres(0))
	if next.CacheHit || next.ImageID == first.ImageID || f.renders != 2 {
		t.Fatal("undeleted expired metadata reused")
	}
	delete(f.objects, next.ImageID)
	replacement := f.resolve(t, metres(0))
	if replacement.CacheHit || replacement.ImageID == next.ImageID {
		t.Fatal("missing object reused")
	}
	_, index, err := f.r.index(context.Background(), f.r.cellKeys(metres(0))[4])
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range index.Entries {
		if id == next.ImageID {
			t.Fatal("purged object retained in bounded index")
		}
	}
}

func TestReadyMapRegeneratesForNewRenderProfile(t *testing.T) {
	f := newResolverFixture()
	key := f.observation(t, metres(0))
	if err := f.r.Resolve(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	first, err := Current(context.Background(), f.docs, f.at)
	if err != nil || first.ImageBytes <= 0 || len(first.ImageSHA256) != 64 || first.NoticeRows != satelliteNoticeRows {
		t.Fatal("image integrity metadata missing", err)
	}
	f.r.Config.Version += "-next"
	if err = f.r.Resolve(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	second, err := Current(context.Background(), f.docs, f.at)
	if err != nil || second.ImageID == first.ImageID || second.RenderVersion != f.r.Config.Version || f.renders != 2 {
		t.Fatal("ready map bypassed new render profile", err)
	}
}
func TestResolverDuplicateConcurrentMissAndOldCompletion(t *testing.T) {
	f := newResolverFixture()
	key := f.observation(t, metres(0))
	var newer string
	f.r.Render = func(context.Context, onstar.LocationFix, time.Time) ([]byte, error) {
		f.renders++
		newer = f.observation(t, metres(100))
		if err := f.r.Resolve(context.Background(), newer); err == nil || err.Error() != "map-generation-busy" {
			t.Fatal("concurrent generation not fenced")
		}
		return []byte("synthetic-map"), nil
	}
	if err := f.r.Resolve(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := Current(context.Background(), f.docs, f.at); !errors.Is(err, auth.ErrMissing) {
		t.Fatal("old completion replaced current")
	}
	if err := f.r.Resolve(context.Background(), key); err != nil || f.renders != 1 {
		t.Fatal("duplicate regenerated")
	}
	f.r.Render = func(context.Context, onstar.LocationFix, time.Time) ([]byte, error) {
		f.renders++
		return []byte("synthetic-map"), nil
	}
	if err := f.r.Resolve(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	if _, err := Current(context.Background(), f.docs, f.at); err != nil {
		t.Fatal(err)
	}
}
func TestStreamFilterPartialFailuresAndFailurePrivacy(t *testing.T) {
	f := newResolverFixture()
	key := f.observation(t, metres(0))
	f.r.Render = func(context.Context, onstar.LocationFix, time.Time) ([]byte, error) {
		return nil, errors.New("unsafe upstream detail")
	}
	if err := RequestMap(context.Background(), f.docs, f.at); err != nil {
		t.Fatal(err)
	}
	demandKey := ""
	for k := range f.docs {
		if strings.HasPrefix(k, "map-request#") {
			demandKey = k
		}
	}
	record := events.DynamoDBEventRecord{EventSource: "aws:dynamodb", EventName: "INSERT", Change: events.DynamoDBStreamRecord{SequenceNumber: "1", Keys: map[string]events.DynamoDBAttributeValue{"pk": events.NewStringAttribute("auth#" + demandKey)}}}
	response, err := f.r.Handle(context.Background(), events.DynamoDBEvent{Records: []events.DynamoDBEventRecord{record}})
	if err != nil || len(response.BatchItemFailures) != 1 || response.BatchItemFailures[0].ItemIdentifier != "1" {
		t.Fatal("failed record not retained")
	}
	var o Observation
	json.Unmarshal(f.docs[key].Data, &o)
	if o.Reason != "map-render-unavailable" || o.Status != "failed" || o.Map != nil {
		t.Fatal("unsafe failure or false ready")
	}
	for _, kind := range []string{"MODIFY", "REMOVE"} {
		record.EventName = kind
		response, err = f.r.Handle(context.Background(), events.DynamoDBEvent{Records: []events.DynamoDBEventRecord{record}})
		if err != nil || len(response.BatchItemFailures) != 0 {
			t.Fatal("resolver update or TTL caused recursion")
		}
	}
	record.EventName = "INSERT"
	record.Change.Keys["pk"] = events.NewStringAttribute("auth#map-image#synthetic")
	response, _ = f.r.Handle(context.Background(), events.DynamoDBEvent{Records: []events.DynamoDBEventRecord{record}})
	if len(response.BatchItemFailures) != 0 {
		t.Fatal("cache event accepted")
	}
}

func TestMapDemandOnlyRendersRequestedImages(t *testing.T) {
	f := newResolverFixture()
	key := f.observation(t, metres(0))
	ctx := context.Background()
	record := func(key string) events.DynamoDBEvent {
		return events.DynamoDBEvent{Records: []events.DynamoDBEventRecord{{EventSource: "aws:dynamodb", EventName: "INSERT", Change: events.DynamoDBStreamRecord{SequenceNumber: "1", Keys: map[string]events.DynamoDBAttributeValue{"pk": events.NewStringAttribute("auth#" + key)}}}}}
	}
	if _, err := f.r.Handle(ctx, record(key)); err != nil || f.renders != 0 {
		t.Fatal("observation rendered without a map request")
	}
	for i := 0; i < 3; i++ {
		if err := RequestMap(ctx, f.docs, f.at); err != nil {
			t.Fatal(err)
		}
	}
	var requests []string
	for k := range f.docs {
		if strings.HasPrefix(k, "map-request#") {
			requests = append(requests, k)
		}
	}
	if len(requests) != 1 {
		t.Fatal("map reads were not coalesced")
	}
	for i := 0; i < 2; i++ {
		if result, err := f.r.Handle(ctx, record(requests[0])); err != nil || len(result.BatchItemFailures) != 0 {
			t.Fatal("demand failed")
		}
	}
	if f.renders != 1 {
		t.Fatal("duplicate demand rerendered")
	}
	f.at = f.at.Add(2 * time.Minute)
	f.observation(t, metres(5))
	if err := RequestMap(ctx, f.docs, f.at); err != nil {
		t.Fatal(err)
	}
	for k := range f.docs {
		if strings.HasPrefix(k, "map-request#") {
			f.r.Handle(ctx, record(k))
		}
	}
	current, err := Current(ctx, f.docs, f.at)
	if err != nil || !current.CacheHit || f.renders != 1 {
		t.Fatal("requested cached location rendered again")
	}
}

func TestCacheWrapsAntimeridianAndRejectsOffImagePin(t *testing.T) {
	f := newResolverFixture()
	const dateline = 180 - 4e-5
	a := f.resolve(t, onstar.LocationFix{Longitude: dateline})
	b := f.resolve(t, onstar.LocationFix{Longitude: -dateline})
	if b.ImageID != a.ImageID || !b.CacheHit {
		t.Fatal("longitude wrap missed neighbouring cell")
	}
	f = newResolverFixture()
	f.r.Config.Width = 64
	f.r.Config.Height = 64
	a = f.resolve(t, metres(0))
	b = f.resolve(t, metres(14))
	if b.ImageID == a.ImageID || b.CacheHit {
		t.Fatal("off-image marker accepted")
	}
}

func TestExpiredGenerationCannotPublishOverNewOwner(t *testing.T) {
	f := newResolverFixture()
	oldKey := f.observation(t, metres(0))
	var newKey string
	f.r.Render = func(context.Context, onstar.LocationFix, time.Time) ([]byte, error) {
		f.at = f.at.Add(3 * time.Minute)
		newKey = f.observation(t, metres(100))
		f.r.Render = func(context.Context, onstar.LocationFix, time.Time) ([]byte, error) { return []byte("new-map"), nil }
		if err := f.r.Resolve(context.Background(), newKey); err != nil {
			t.Fatal(err)
		}
		return []byte("old-map"), nil
	}
	if err := f.r.Resolve(context.Background(), oldKey); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("expired generation published", err)
	}
	m, err := Current(context.Background(), f.docs, f.at)
	if err != nil || m.Fix.Latitude != metres(100).Latitude {
		t.Fatal("new owner result lost")
	}
}

func TestResolverRetainsSafeRendererReasonAndSuppressesPrivateErrors(t *testing.T) {
	private := "Bearer" + " " + strings.Repeat("synthetic", 8)
	for _, tc := range []struct{ message, want string }{
		{"map-source-http-400", "map-source-http-400"},
		{"map-source-timeout", "map-source-timeout"},
		{private, "map-render-unavailable"},
		{"map-source-http-400: " + private, "map-render-unavailable"},
		{"map-source-http-400\n" + private, "map-render-unavailable"},
	} {
		f := newResolverFixture()
		key := f.observation(t, metres(0))
		f.r.Render = func(context.Context, onstar.LocationFix, time.Time) ([]byte, error) {
			return nil, errors.New(tc.message)
		}
		err := f.r.Resolve(context.Background(), key)
		var observation Observation
		if json.Unmarshal(f.docs[key].Data, &observation) != nil || err == nil || err.Error() != tc.want || observation.Reason != tc.want || observation.Status != "failed" {
			t.Fatal("renderer reason classification failed")
		}
		if strings.Contains(string(f.docs[key].Data), private) {
			t.Fatal("private renderer error persisted")
		}
		if len(f.objects) != 0 {
			t.Fatal("failed renderer published an image")
		}
	}
}
