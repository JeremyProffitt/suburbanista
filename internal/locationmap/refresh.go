package locationmap

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const refreshDeadline = 3 * time.Minute
const refreshRetention = 24 * time.Hour

var refreshID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
var operationID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidRefreshID(id string) bool { return refreshID.MatchString(id) }

type refreshPointer struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operationId"`
}

type RefreshFamilies interface {
	CommandFamily(context.Context, string) (string, error)
}
type RefreshAuthority interface {
	Validate(context.Context, string) (amazon.Identity, error)
}
type RefreshQueue interface {
	Send(context.Context, string, string) error
}
type refreshOperation struct {
	ID, Owner, Vehicle, FamilyID, State, Reason, ObservationID string
	RequestedAt                                                time.Time
}
type refreshAlias struct{ OperationID string }
type refreshGate struct {
	OperationID string
	NotBefore   time.Time
}
type refreshWork struct {
	doc    auth.Document
	value  refreshOperation
	family auth.Document
}

type queueSender interface {
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// FIFO uses the existing location group; commands keep their independent group
// and both use the existing provider lease for serialization.
type FIFO struct {
	Client          queueSender
	URL, VehicleKey string
}

func (q FIFO) Send(ctx context.Context, body, id string) error {
	if q.Client == nil || q.URL == "" || q.VehicleKey == "" {
		return auth.ErrUnavailable
	}
	out, err := q.Client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(q.URL), MessageBody: aws.String(body), MessageGroupId: aws.String("loc-" + q.VehicleKey), MessageDeduplicationId: aws.String(id)})
	if err != nil || out == nil || aws.ToString(out.MessageId) == "" {
		return auth.ErrUnavailable
	}
	return nil
}
func aliasKey(owner, family, id string) string {
	return "location-refresh-alias#" + digest(owner+"|"+family+"|"+id)
}
func operationKey(id string) string { return "location-refresh#" + id }
func (s *Service) authorizeRefresh(ctx context.Context, family string) (auth.Document, error) {
	if s.Families == nil || s.Authority == nil || family == "" || len(family) > 512 {
		return auth.Document{}, auth.ErrHandle
	}
	// Load first; the dispatch/acceptance transaction fences any revocation or
	// refresh that changes this family while fresh owner validation is running.
	doc, err := s.Docs.Load(ctx, "family#"+family)
	if err != nil {
		return doc, auth.ErrHandle
	}
	token, err := s.Families.CommandFamily(ctx, family)
	if err != nil {
		return doc, auth.ErrHandle
	}
	identity, err := s.Authority.Validate(ctx, token)
	if err != nil {
		var failure *amazon.Error
		if errors.As(err, &failure) && (failure.Outcome == amazon.InvalidAccessToken || failure.Outcome == amazon.Revoked) {
			return doc, auth.ErrHandle
		}
		return doc, auth.ErrUnavailable
	}
	if identity.Subject != s.Owner {
		return doc, auth.ErrNotOwner
	}
	return doc, nil
}
func (s *Service) RequestRefresh(ctx context.Context, family, id string) (contract.LocationRefreshStatus, error) {
	if !refreshID.MatchString(id) {
		return contract.LocationRefreshStatus{}, auth.ErrInvalid
	}
	familyDoc, err := s.authorizeRefresh(ctx, family)
	if err != nil {
		return contract.LocationRefreshStatus{}, err
	}
	now := s.now()
	alias := auth.Document{Key: aliasKey(s.Owner, family, id), ExpiresAt: now.Add(refreshRetention)}
	if old, e := s.Docs.Load(ctx, alias.Key); e == nil {
		if !now.Before(old.ExpiresAt) {
			return contract.LocationRefreshStatus{}, auth.ErrExpired
		}
		var a refreshAlias
		if json.Unmarshal(old.Data, &a) != nil {
			return contract.LocationRefreshStatus{}, auth.ErrUnavailable
		}
		if e = s.enqueueRefresh(ctx, a.OperationID); e != nil {
			return contract.LocationRefreshStatus{}, e
		}
		return RequestStatus(ctx, s.Docs, s.Owner, family, id, now)
	} else if !errors.Is(e, auth.ErrMissing) {
		return contract.LocationRefreshStatus{}, e
	}
	gateDoc, err := s.Docs.Load(ctx, "location-refresh-active#"+digest(s.Owner+"|"+s.VehicleKey))
	if errors.Is(err, auth.ErrMissing) {
		gateDoc = auth.Document{Key: "location-refresh-active#" + digest(s.Owner+"|"+s.VehicleKey)}
	} else if err != nil {
		return contract.LocationRefreshStatus{}, err
	}
	var gate refreshGate
	if len(gateDoc.Data) > 0 && json.Unmarshal(gateDoc.Data, &gate) != nil {
		return contract.LocationRefreshStatus{}, auth.ErrUnavailable
	}
	if gate.OperationID != "" {
		current, e := loadRefresh(ctx, s.Docs, gate.OperationID, now)
		if e == nil {
			status, e := refreshStatus(ctx, s.Docs, current, s.Owner, id, now)
			if e != nil {
				return status, e
			}
			if status.State != "ready" && status.State != "failed" {
				alias = encoded(alias, refreshAlias{gate.OperationID}, alias.ExpiresAt)
				if err = s.Docs.Commit(ctx, alias, familyDoc); err != nil {
					return status, err
				}
				if err = s.enqueueRefresh(ctx, gate.OperationID); err != nil {
					return status, err
				}
				return status, nil
			}
		} else if !errors.Is(e, auth.ErrMissing) && !errors.Is(e, auth.ErrExpired) {
			return contract.LocationRefreshStatus{}, e
		}
	}
	if now.Before(gate.NotBefore) {
		return contract.LocationRefreshStatus{}, auth.ErrRate
	}
	if s.Queue == nil {
		return contract.LocationRefreshStatus{}, auth.ErrUnavailable
	}
	op := refreshOperation{ID: digest(s.Owner + "|" + family + "|" + id), Owner: s.Owner, Vehicle: s.VehicleKey, FamilyID: family, State: "queued", RequestedAt: now}
	opDoc := encoded(auth.Document{Key: operationKey(op.ID)}, op, now.Add(refreshRetention))
	alias = encoded(alias, refreshAlias{op.ID}, alias.ExpiresAt)
	gateDoc = encoded(gateDoc, refreshGate{op.ID, now.Add(time.Minute)}, now.Add(refreshRetention))
	if err = s.Docs.Commit(ctx, opDoc, alias, gateDoc, familyDoc); err != nil {
		return contract.LocationRefreshStatus{}, err
	}
	if err = s.enqueueRefresh(ctx, op.ID); err != nil {
		return contract.LocationRefreshStatus{}, err
	}
	return RequestStatus(ctx, s.Docs, s.Owner, family, id, now)
}
func (s *Service) enqueueRefresh(ctx context.Context, id string) error {
	op, err := loadRefresh(ctx, s.Docs, id, s.now())
	if err != nil {
		return err
	}
	if op.State != "queued" || !s.now().Before(op.RequestedAt.Add(refreshDeadline)) {
		return nil
	}
	if s.Queue == nil {
		return auth.ErrUnavailable
	}
	body, _ := json.Marshal(refreshPointer{"location-refresh", id})
	return s.Queue.Send(ctx, string(body), id)
}
func loadRefresh(ctx context.Context, docs auth.Documents, id string, now time.Time) (refreshOperation, error) {
	if !operationID.MatchString(id) {
		return refreshOperation{}, auth.ErrInvalid
	}
	doc, err := docs.Load(ctx, operationKey(id))
	if err != nil {
		return refreshOperation{}, err
	}
	if !now.Before(doc.ExpiresAt) {
		return refreshOperation{}, auth.ErrExpired
	}
	var op refreshOperation
	if json.Unmarshal(doc.Data, &op) != nil || op.ID != id {
		return op, auth.ErrUnavailable
	}
	return op, nil
}
func RequestStatus(ctx context.Context, docs auth.Documents, owner, family, id string, now time.Time) (contract.LocationRefreshStatus, error) {
	if !refreshID.MatchString(id) || family == "" {
		return contract.LocationRefreshStatus{}, auth.ErrInvalid
	}
	doc, err := docs.Load(ctx, aliasKey(owner, family, id))
	if err != nil {
		return contract.LocationRefreshStatus{}, err
	}
	if !now.Before(doc.ExpiresAt) {
		return contract.LocationRefreshStatus{}, auth.ErrExpired
	}
	var alias refreshAlias
	if json.Unmarshal(doc.Data, &alias) != nil {
		return contract.LocationRefreshStatus{}, auth.ErrUnavailable
	}
	op, err := loadRefresh(ctx, docs, alias.OperationID, now)
	if err != nil {
		return contract.LocationRefreshStatus{}, err
	}
	return refreshStatus(ctx, docs, op, owner, id, now)
}

// CurrentStatus is a read-only observation receipt, including when image
// resolution has not completed. It never substitutes receipt time for source time.
func CurrentStatus(ctx context.Context, docs auth.Documents, owner string, now time.Time) (contract.LocationRefreshStatus, error) {
	out := contract.LocationRefreshStatus{State: "failed", Reason: "not-yet-observed", PollAfterSeconds: 10}
	doc, err := docs.Load(ctx, "location-latest")
	if errors.Is(err, auth.ErrMissing) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if !now.Before(doc.ExpiresAt) {
		return out, auth.ErrExpired
	}
	var value latest
	if json.Unmarshal(doc.Data, &value) != nil {
		return out, auth.ErrUnavailable
	}
	out.ReceivedAt = value.PolledAt
	if value.Fix != nil && value.Fix.ReportedAt != nil {
		out.ObservedAt = *value.Fix.ReportedAt
	}
	if value.ObservationID == "" {
		out.Reason = "map-unavailable"
		return out, nil
	}
	doc, err = docs.Load(ctx, value.ObservationID)
	if err != nil {
		return out, err
	}
	if !now.Before(doc.ExpiresAt) {
		return out, auth.ErrExpired
	}
	var observation Observation
	if json.Unmarshal(doc.Data, &observation) != nil {
		return out, auth.ErrUnavailable
	}
	if observation.Owner != owner {
		return out, auth.ErrNotOwner
	}
	applyObservationStatus(&out, observation, now)
	return out, nil
}

func applyObservationStatus(out *contract.LocationRefreshStatus, observation Observation, now time.Time) {
	out.ObservationRevision = observation.Revision
	out.ReceivedAt = observation.ReceivedAt
	if observation.Fix.ReportedAt != nil {
		out.ObservedAt = *observation.Fix.ReportedAt
	} else {
		out.ObservedAt = time.Time{}
	}
	out.State = "resolving"
	out.Reason = ""
	if observation.Status == "failed" {
		out.State = "failed"
		out.Reason = observation.Reason
		return
	}
	if observation.Status == "ready" && observation.Map != nil {
		if !now.Before(observation.Map.ExpiresAt) {
			out.State = "failed"
			out.Reason = "map-expired"
			return
		}
		out.State = "ready"
		out.ImageID = observation.Map.ImageID
		out.ImageVersion = observation.Map.ImageVersion
		out.ImageGeneratedAt = observation.Map.GeneratedAt
	}
}
func refreshStatus(ctx context.Context, docs auth.Documents, op refreshOperation, owner, id string, now time.Time) (contract.LocationRefreshStatus, error) {
	if op.Owner != owner {
		return contract.LocationRefreshStatus{}, auth.ErrNotOwner
	}
	out := contract.LocationRefreshStatus{RequestID: id, OperationID: op.ID, State: op.State, Reason: op.Reason, RequestedAt: op.RequestedAt, PollAfterSeconds: 10}
	if op.State == "failed" {
		return out, nil
	}
	if op.ObservationID != "" {
		doc, err := docs.Load(ctx, op.ObservationID)
		if err != nil {
			return out, err
		}
		if !now.Before(doc.ExpiresAt) {
			return out, auth.ErrExpired
		}
		var observation Observation
		if json.Unmarshal(doc.Data, &observation) != nil {
			return out, auth.ErrUnavailable
		}
		if observation.Owner != owner || observation.Vehicle != op.Vehicle {
			return out, auth.ErrNotOwner
		}
		applyObservationStatus(&out, observation, now)
		if out.State == "failed" {
			return out, nil
		}
		if out.State == "ready" {
			latestDoc, err := docs.Load(ctx, "location-latest")
			if err != nil {
				return out, err
			}
			if !now.Before(latestDoc.ExpiresAt) {
				return out, auth.ErrExpired
			}
			var current latest
			if json.Unmarshal(latestDoc.Data, &current) != nil {
				return out, auth.ErrUnavailable
			}
			if current.ObservationID != op.ObservationID {
				out.State = "failed"
				out.Reason = "refresh-superseded"
				out.ImageID = ""
				out.ImageVersion = ""
				out.ImageGeneratedAt = time.Time{}
				return out, nil
			}
			return out, nil
		}
	}
	if !now.Before(op.RequestedAt.Add(refreshDeadline)) {
		out.State = "failed"
		out.Reason = "refresh-timeout"
	}
	return out, nil
}
func (s *Service) HandleRefresh(ctx context.Context, message events.SQSMessage) error {
	var pointer refreshPointer
	if message.EventSource != "aws:sqs" || message.EventSourceARN != s.QueueARN || message.Attributes["MessageGroupId"] != "loc-"+s.VehicleKey || json.Unmarshal([]byte(message.Body), &pointer) != nil || pointer.Kind != "location-refresh" || !operationID.MatchString(pointer.OperationID) {
		return nil
	}
	doc, err := s.Docs.Load(ctx, operationKey(pointer.OperationID))
	if errors.Is(err, auth.ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	var op refreshOperation
	if json.Unmarshal(doc.Data, &op) != nil || op.ID != pointer.OperationID || op.Owner != s.Owner || op.Vehicle != s.VehicleKey {
		return auth.ErrInvalid
	}
	if op.State != "queued" {
		return nil
	}
	work := &refreshWork{doc: doc, value: op}
	if !s.now().Before(op.RequestedAt.Add(refreshDeadline)) {
		return s.failRefresh(ctx, work, "refresh-timeout")
	}
	if adopted, err := s.adoptRecentObservation(ctx, work); err != nil || adopted {
		return err
	}
	_, err = s.collect(ctx, "location-manual-slot#"+op.ID, work)
	return err
}

// FIFO serialization lets a tap join the scheduled collection already in
// progress. Only an observation actually received after this request qualifies.
func (s *Service) adoptRecentObservation(ctx context.Context, work *refreshWork) (bool, error) {
	doc, err := s.Docs.Load(ctx, "location-latest")
	if errors.Is(err, auth.ErrMissing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var value latest
	if json.Unmarshal(doc.Data, &value) != nil {
		return false, auth.ErrUnavailable
	}
	if value.ObservationID == "" || value.PolledAt.Before(work.value.RequestedAt) || !s.now().Before(doc.ExpiresAt) {
		return false, nil
	}
	odoc, err := s.Docs.Load(ctx, value.ObservationID)
	if err != nil {
		return false, err
	}
	var observation Observation
	if json.Unmarshal(odoc.Data, &observation) != nil {
		return false, auth.ErrUnavailable
	}
	if observation.Owner != s.Owner || observation.Vehicle != s.VehicleKey || observation.ReceivedAt.Before(work.value.RequestedAt) || observation.ReceivedAt.After(s.now().Add(time.Minute)) || !s.now().Before(odoc.ExpiresAt) || (observation.Status != "pending" && observation.Status != "ready") {
		return false, nil
	}
	if observation.Status == "ready" && (observation.Map == nil || !s.now().Before(observation.Map.ExpiresAt)) {
		return false, nil
	}
	guard, err := s.authorizeRefresh(ctx, work.value.FamilyID)
	if err != nil {
		return true, s.failRefresh(ctx, work, "owner-authority-unavailable")
	}
	work.value.State = "resolving"
	work.value.ObservationID = value.ObservationID
	return true, s.Docs.Commit(ctx, encoded(work.doc, work.value, work.doc.ExpiresAt), guard)
}
func (s *Service) failRefresh(ctx context.Context, work *refreshWork, reason string) error {
	if work == nil {
		return nil
	}
	work.value.State = "failed"
	work.value.Reason = reason
	return s.Docs.Commit(ctx, encoded(work.doc, work.value, work.doc.ExpiresAt))
}
