// Package locationmap polls the enrolled owner's location on an explicit schedule.
package locationmap

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/internal/worker"
	"github.com/aws/aws-lambda-go/events"
	"time"
)

type Service struct {
	Credentials                                        worker.Store
	Provider                                           worker.Provider
	Docs                                               auth.Documents
	Owner, DeviceID, VehicleKey, QueueARN, ScheduleARN string
	Paused                                             func(context.Context) (bool, error)
	RequestPaused                                      func(context.Context) (bool, error)
	Families                                           RefreshFamilies
	Authority                                          RefreshAuthority
	Queue                                              RefreshQueue
	Now                                                func() time.Time
	Sleep                                              func(context.Context, time.Duration) error
}
type latest struct {
	Fix           *onstar.LocationFix `json:"fix,omitempty"`
	PolledAt      time.Time           `json:"polledAt"`
	Reason        string              `json:"reason,omitempty"`
	ObservationID string              `json:"observationId,omitempty"`
}

// Status is an IAM-only operational receipt. Coordinates never leave the
// encrypted document through this response.
type Status struct {
	HasFix        bool   `json:"hasFix"`
	HasMap        bool   `json:"hasMap"`
	MatchesLatest bool   `json:"matchesLatest"`
	PolledAt      int64  `json:"polledAt"`
	MapAt         int64  `json:"mapAt"`
	Reason        string `json:"reason"`
	ImageID       string `json:"imageId,omitempty"`
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	doc, e := s.Docs.Load(ctx, "location-latest")
	if errors.Is(e, auth.ErrMissing) {
		return Status{Reason: "not-yet-polled"}, nil
	}
	if e != nil {
		return Status{}, errors.New("location-status-unavailable")
	}
	var value latest
	if json.Unmarshal(doc.Data, &value) != nil {
		return Status{}, errors.New("location-status-invalid")
	}
	unix := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.Unix()
	}
	status := Status{HasFix: value.Fix != nil, PolledAt: unix(value.PolledAt), Reason: value.Reason}
	if current, err := Current(ctx, s.Docs, s.now()); err == nil {
		status.HasMap = true
		status.MatchesLatest = true
		status.MapAt = unix(current.GeneratedAt)
		status.ImageID = current.ImageID
	} else if value.ObservationID != "" {
		observationDoc, loadErr := s.Docs.Load(ctx, value.ObservationID)
		var observation Observation
		if loadErr == nil && json.Unmarshal(observationDoc.Data, &observation) == nil && status.Reason == "" {
			if observation.Status == "failed" {
				status.Reason = observation.Reason
			} else if observation.Status == "pending" {
				status.Reason = "map-pending"
			} else {
				status.Reason = "map-expired"
			}
		}
	}
	return status, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *Service) wait(ctx context.Context) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, 6*time.Second)
	}
	t := time.NewTimer(6 * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (s *Service) save(ctx context.Context, doc auth.Document, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	doc.Data = b
	doc.ExpiresAt = s.now().AddDate(0, 0, 90)
	return s.Docs.Commit(ctx, doc)
}

func (s *Service) Handle(ctx context.Context, message events.SQSMessage) error {
	var input struct{ Kind, VehicleKey, ScheduleARN, ScheduledAt string }
	if message.EventSource != "aws:sqs" || message.EventSourceARN != s.QueueARN || message.Attributes["MessageGroupId"] != "loc-"+s.VehicleKey || json.Unmarshal([]byte(message.Body), &input) != nil || input.Kind != "location" || input.VehicleKey != s.VehicleKey || input.ScheduleARN != s.ScheduleARN {
		return nil
	}
	at, e := time.Parse(time.RFC3339, input.ScheduledAt)
	now := s.now()
	if e != nil || at.After(now.Add(time.Minute)) || now.Sub(at) > 10*time.Minute {
		return nil
	}
	slot := "location-slot#" + at.UTC().Format("20060102T1504")
	_, e = s.collect(ctx, slot, nil)
	return e
}

func (s *Service) collect(ctx context.Context, slot string, work *refreshWork) (string, error) {
	now := s.now()
	var e error
	if _, e = s.Docs.Load(ctx, slot); e == nil {
		return "", nil
	} else if !errors.Is(e, auth.ErrMissing) {
		return "", e
	}
	pausedCheck := s.Paused
	if work != nil && s.RequestPaused != nil {
		pausedCheck = s.RequestPaused
	}
	if pausedCheck == nil {
		return "", errors.New("location-pause-check-unavailable")
	}
	paused, e := pausedCheck(ctx)
	if e != nil {
		return "", e
	}
	if paused {
		return "", s.failRefresh(ctx, work, "provider-paused")
	}
	lease, e := s.Credentials.Acquire(ctx, slot, now)
	if work != nil {
		// Do not burn FIFO redeliveries while a command owns the provider lease.
		// Same-group scheduled collection is serialized and adopted above.
		for attempt := 0; errors.Is(e, state.ErrConflict) && attempt < 30; attempt++ {
			if s.Sleep != nil {
				if err := s.Sleep(ctx, time.Second); err != nil {
					break
				}
			} else {
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return "", ctx.Err()
				case <-timer.C:
				}
			}
			if !s.now().Before(work.value.RequestedAt.Add(refreshDeadline)) {
				return "", s.failRefresh(ctx, work, "refresh-timeout")
			}
			lease, e = s.Credentials.Acquire(ctx, slot, s.now())
		}
	}
	if e != nil {
		if work != nil {
			return "", s.failRefresh(ctx, work, "vehicle-busy")
		}
		return "", e
	}
	now = s.now()
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = s.Credentials.Release(release, lease)
	}()
	providerCtx := ctx
	if work != nil {
		deadline := time.Now().Add(90 * time.Second)
		if outer, ok := ctx.Deadline(); ok && outer.Add(-20*time.Second).Before(deadline) {
			deadline = outer.Add(-20 * time.Second)
		}
		var cancel context.CancelFunc
		providerCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	record, e := s.Credentials.Load(ctx, lease, now)
	if e != nil {
		return "", e
	}
	if record.Bootstrap.ValidateDevice(s.DeviceID) != nil || record.ProviderState != "ready" || record.CooldownUntil > now.Unix() {
		return "", s.failRefresh(ctx, work, "provider-not-ready")
	}
	if work != nil && providerCtx.Err() != nil {
		return "", s.failRefresh(ctx, work, "refresh-timeout")
	}
	// Claim before any provider call: SQS redelivery cannot repeat the GPS wake.
	claim := encoded(auth.Document{Key: slot}, map[string]string{"state": "started"}, now.Add(90*24*time.Hour))
	claims := []auth.Document{claim}
	if work != nil {
		work.family, e = s.authorizeRefresh(ctx, work.value.FamilyID)
		if e != nil {
			return "", s.failRefresh(ctx, work, "owner-authority-unavailable")
		}
		work.value.State = "collecting"
		claims = append(claims, encoded(work.doc, work.value, work.doc.ExpiresAt), work.family)
	}
	if e = s.Docs.Commit(ctx, claims...); e != nil {
		return "", e
	}
	if work != nil {
		work.doc.Version++
	}
	doc, e := s.Docs.Load(ctx, "location-latest")
	if errors.Is(e, auth.ErrMissing) {
		doc = auth.Document{Key: "location-latest"}
	} else if e != nil {
		return "", e
	}
	value := latest{}
	if len(doc.Data) > 0 && json.Unmarshal(doc.Data, &value) != nil {
		return "", errors.New("location-state-invalid")
	}
	fail := func(reason string) error {
		value.Reason = reason
		writes := []auth.Document{encoded(doc, value, s.now().Add(90*24*time.Hour))}
		if work != nil {
			work.value.State = "failed"
			work.value.Reason = reason
			writes = append(writes, encoded(work.doc, work.value, work.doc.ExpiresAt))
		}
		return s.Docs.Commit(ctx, writes...)
	}
	providerFail := func(cause error) error {
		reason := "provider-unavailable"
		var problem *onstar.Error
		if errors.As(cause, &problem) {
			reason = problem.Kind
			if problem.Kind == "reauth-required" || problem.Kind == "account-mismatch" {
				record.ProviderState = "reauth-required"
			}
			if problem.Kind == "rate-limited" {
				record.CooldownUntil = now.Add(time.Duration(max(problem.RetryAfterSeconds, 1800)) * time.Second).Unix()
				record.CooldownReason = "location-rate-limited"
			}
			if problem.AuthEndpoint && (problem.Status == 401 || problem.Status == 403) {
				record.CooldownUntil = now.Add(6 * time.Hour).Unix()
				record.CooldownReason = "auth-endpoint-denied"
			}
			if _, e = s.Credentials.Save(ctx, lease, record, s.now()); e != nil {
				return e
			}
		}
		return fail(reason)
	}
	token := onstar.GMToken{AccessToken: record.GMToken, ExpiresAt: record.GMExpiresAt, VIN: record.Bootstrap.VIN, VehicleLinked: true}
	if token.AccessToken == "" || token.ExpiresAt <= now.Add(30*time.Second).Unix() {
		if record.Grant.AccessToken == "" || record.Grant.ExpiresAt <= now.Add(30*time.Second).Unix() {
			record.Grant, e = s.Provider.Refresh(providerCtx, record.Grant)
			if e != nil {
				return "", providerFail(e)
			}
			record, e = s.Credentials.Save(ctx, lease, record, s.now())
			if e != nil {
				return "", e
			}
		}
		token, e = s.Provider.Exchange(providerCtx, record.Grant, s.DeviceID, record.Bootstrap.VIN)
		if e != nil {
			return "", providerFail(e)
		}
		record.GMToken = token.AccessToken
		record.GMExpiresAt = token.ExpiresAt
		record, e = s.Credentials.Save(ctx, lease, record, s.now())
		if e != nil {
			return "", e
		}
	}
	if work != nil {
		// Token refresh/exchange can take time. Revalidate the durable family and
		// pause flag again at dispatch, with a conditional family write as fence.
		guard, err := s.authorizeRefresh(ctx, work.value.FamilyID)
		if err != nil {
			return "", fail("owner-authority-unavailable")
		}
		paused, err := pausedCheck(ctx)
		if err != nil {
			return "", err
		}
		if paused {
			return "", fail("provider-paused")
		}
		if err = s.Docs.Commit(ctx, guard, encoded(work.doc, work.value, work.doc.ExpiresAt)); err != nil {
			return "", err
		}
		work.doc.Version++
	}
	loc, e := s.Provider.Location(providerCtx, token, record.Bootstrap.VIN, true)
	if e != nil {
		return "", providerFail(e)
	}
	for i := 0; i < 2 && loc.Pending; i++ {
		if s.wait(providerCtx) != nil {
			return "", fail("location-pending")
		}
		loc, e = s.Provider.Location(providerCtx, token, record.Bootstrap.VIN, false)
		if e != nil {
			return "", providerFail(e)
		}
	}
	if loc.Pending || !loc.FixAvailable || loc.Fix == nil {
		return "", fail("location-unavailable")
	}
	if !validFix(*loc.Fix) {
		return "", fail("location-unavailable")
	}
	receivedAt := s.now()
	identity := digest(s.Owner + "|" + s.VehicleKey + "|onstar|" + slot)
	observationKey := "location-observation#" + identity
	observation := Observation{Owner: s.Owner, Vehicle: s.VehicleKey, Source: "onstar", SourceID: identity, Revision: identity, ReceivedAt: receivedAt, Fix: *loc.Fix, Status: "pending"}
	expiry := s.now().Add(90 * 24 * time.Hour)
	// Receipt time is not source observation time. Keep every accepted source
	// observation in history, but never promote an unknown or older source time
	// over a position with a known newer timestamp.
	retainCurrent := false
	if value.Fix != nil && value.Fix.ReportedAt != nil {
		if loc.Fix.ReportedAt == nil {
			retainCurrent = true
			value.Reason = "location-observation-time-unknown"
		} else if loc.Fix.ReportedAt.Before(*value.Fix.ReportedAt) {
			retainCurrent = true
			value.Reason = "location-older-observation"
		}
	}
	if !retainCurrent {
		value.Fix = loc.Fix
		value.PolledAt = receivedAt
		value.Reason = ""
		value.ObservationID = observationKey
	}
	writes := []auth.Document{encoded(auth.Document{Key: observationKey}, observation, expiry), encoded(doc, value, expiry)}
	if work != nil {
		work.value.State = "resolving"
		work.value.ObservationID = observationKey
		work.value.Reason = ""
		writes = append(writes, encoded(work.doc, work.value, work.doc.ExpiresAt))
	}
	return observationKey, s.Docs.Commit(ctx, writes...)
}
