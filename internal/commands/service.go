// Package commands owns the bounded Alexa actuation state machine. The broker
// accepts only owner-authorized intent; SQS carries no credentials or vehicle IDs.
package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

type Authority interface {
	Validate(context.Context, string) (amazon.Identity, error)
}
type Queue interface {
	Send(context.Context, string, string) error
}
type Credentials interface {
	Acquire(context.Context, string, time.Time) (state.Lease, error)
	Load(context.Context, state.Lease, time.Time) (state.Record, error)
	Save(context.Context, state.Lease, state.Record, time.Time) (state.Record, error)
	Release(context.Context, state.Lease) error
}
type Provider interface {
	Refresh(context.Context, onstar.Grant) (onstar.Grant, error)
	Exchange(context.Context, onstar.Grant, string, string) (onstar.GMToken, error)
	Submit(context.Context, onstar.GMToken, string, onstar.Command, onstar.CommandOptions) (onstar.CommandResult, error)
	CommandStatus(context.Context, onstar.GMToken, string, string, string) (onstar.CommandResult, error)
}
type Config struct {
	SkillID, Owner, DeviceID, VehicleKey, QueueARN string
	DryRunAllowed                                  bool
}
type Families interface {
	CommandFamily(context.Context, string) (string, error)
}
type Service struct {
	Config      Config
	Docs        auth.Documents
	Authority   Authority
	Queue       Queue
	Credentials Credentials
	Provider    Provider
	Flags       FlagReader
	Families    Families
	Now         func() time.Time
	Sleep       func(context.Context, time.Duration) error
	LogCommand  func(CommandDiagnostic)
}
type command struct {
	ID, Action, State, Reason, UserHash, PersonHash, PersonAccessToken string
	CreatedAt, UpdatedAt, DispatchAt                                   int64
	UserGeneration                                                     uint64
	DryRun                                                             bool
	Source, FamilyID                                                   string
	Result                                                             onstar.CommandResult
	Options                                                            contract.CommandOptions
}
type userBinding struct {
	Active      bool
	Generation  uint64
	LastEventAt int64
}
type personBinding struct {
	Owner, UserHash, PersonHash string
	UserGeneration              uint64
}
type latest struct{ ID string }
type Pointer struct {
	Kind      string `json:"kind"`
	CommandID string `json:"commandId"`
}

var commandIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ErrPersistence = errors.New("command persistence unavailable")

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *Service) sleep(ctx context.Context, d time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func hash(v string) string                            { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func data(doc auth.Document, value any) auth.Document { doc.Data, _ = json.Marshal(value); return doc }
func decoded[T any](doc auth.Document) (T, error) {
	var v T
	if json.Unmarshal(doc.Data, &v) != nil {
		return v, ErrPersistence
	}
	return v, nil
}
func (s *Service) newDoc(key string) auth.Document {
	return auth.Document{Key: key, ExpiresAt: s.now().AddDate(0, 0, 90)}
}
func (s *Service) save(ctx context.Context, doc *auth.Document, value any) error {
	if err := s.Docs.Commit(ctx, data(*doc, value)); err != nil {
		return ErrPersistence
	}
	doc.Version++
	doc.Data, _ = json.Marshal(value)
	return nil
}
func validID(v string) bool { return v != "" && len(v) <= 512 && !strings.ContainsAny(v, "\r\n\t") }
func fresh(timestamp string, now time.Time) bool {
	t, e := time.Parse(time.RFC3339Nano, timestamp)
	return e == nil && !t.After(now.Add(10*time.Second)) && now.Sub(t) <= 150*time.Second
}
func response(c command) contract.AlexaCommandResponse {
	if c.State == "accepted" {
		c.State = "unconfirmed"
		c.Reason = "queue-unconfirmed"
	}
	return contract.AlexaCommandResponse{State: c.State, Reason: c.Reason, Action: c.Action, CommandID: c.ID, UpdatedAt: c.UpdatedAt}
}
func unconfirmed(c command, reason string) contract.AlexaCommandResponse {
	return contract.AlexaCommandResponse{State: "unconfirmed", Reason: reason, Action: c.Action, CommandID: c.ID, UpdatedAt: c.UpdatedAt}
}
func uncertainRequest(r contract.AlexaCommandRequest, reason string) contract.AlexaCommandResponse {
	id := r.CommandID
	if r.Operation == "submit" {
		scope := r.SkillID
		if r.Source == "mobile" {
			scope = r.FamilyID
		}
		id = IntentID(r.Source, scope, r.RequestID)
	}
	return contract.AlexaCommandResponse{State: "unconfirmed", Reason: reason, Action: r.Action, CommandID: id}
}
func rejected(reason string) contract.AlexaCommandResponse {
	if reason == "storage-unavailable" {
		return contract.AlexaCommandResponse{State: "unconfirmed", Reason: "state-unavailable"}
	}
	return contract.AlexaCommandResponse{State: "rejected", Reason: reason}
}
func (s *Service) gated(ctx context.Context, action string, dry bool) string {
	feature, known := contract.FindFeature(action)
	if !known || feature.Category != "action" {
		return "unsupported-action"
	}
	if dry {
		if !s.Config.DryRunAllowed {
			return "dry-run-disabled"
		}
		return ""
	}
	if s.Flags == nil {
		return "controls-disabled"
	}
	flags, e := s.Flags.ReadFlags(ctx)
	if e != nil {
		return "flags-unavailable"
	}
	if reason := DisabledReason(flags, action); reason != "" {
		return reason
	}
	if action != "start" && action != "lock" && action != "unlock" {
		return s.newActionEligibility(ctx, action)
	}
	return ""
}
func IntentID(source, scope, requestID string) string {
	return hash(source + "|" + scope + "|" + requestID)
}
func (s *Service) Broker(ctx context.Context, r contract.AlexaCommandRequest) contract.AlexaCommandResponse {
	if r.Kind != "alexa-command" || !validID(r.RequestID) || !fresh(r.Timestamp, s.now()) {
		return rejected("invalid-request")
	}
	if r.Source == "mobile" {
		if s.Families == nil || r.FamilyID == "" || (r.Operation != "submit" && r.Operation != "status" && r.Operation != "read" && r.Operation != "cached-read" && r.Operation != "features") {
			return rejected("invalid-mobile-request")
		}
		token, e := s.Families.CommandFamily(ctx, r.FamilyID)
		if e != nil {
			return uncertainRequest(r, "session-revoked")
		}
		r.PersonAccessToken = token
		r.UserID = "mobile"
		r.PersonID = r.FamilyID
	} else if r.Source == "alexa" {
		if r.SkillID != s.Config.SkillID || s.Config.SkillID == "" || !validID(r.UserID) {
			return rejected("invalid-request")
		}
		if r.Operation == "event" {
			return s.event(ctx, r)
		}
	} else {
		return rejected("invalid-source")
	}
	if (r.Operation != "submit" && r.Operation != "status" && r.Operation != "read" && r.Operation != "cached-read" && r.Operation != "features") || !validID(r.PersonID) || r.PersonAccessToken == "" {
		return uncertainRequest(r, "person-link-required")
	}
	identity, e := s.Authority.Validate(ctx, r.PersonAccessToken)
	if e != nil || identity.Subject != s.Config.Owner {
		var failure *amazon.Error
		if errors.As(e, &failure) && (failure.Outcome == amazon.AuthorityUnavailable || failure.Outcome == amazon.ConfigError) {
			return uncertainRequest(r, "authority-unavailable")
		}
		return uncertainRequest(r, "owner-authority-unverified")
	}
	uh, ph := hash(r.UserID), hash(r.UserID+"|"+r.PersonID)
	ownerDoc, e := s.ownerDocument(ctx)
	if e != nil {
		return uncertainRequest(r, "owner-binding-unverified")
	}
	if r.Operation == "features" || r.Operation == "read" || r.Operation == "cached-read" {
		_, binding, e := s.user(ctx, uh, true)
		if e != nil {
			return rejected("storage-unavailable")
		}
		if !binding.Active {
			return uncertainRequest(r, "binding-revoked")
		}
		if r.Operation == "features" {
			return s.features(ctx)
		}
		return s.read(ctx, r, binding.Generation)
	}
	if r.Operation == "status" {
		return s.status(ctx, r, uh, ph)
	}
	options := contract.CommandOptions{}
	if r.Options != nil {
		options = *r.Options
	}
	if len(options.Actions) == 0 {
		options.Actions = nil
	}
	if contract.ValidateCommandOptions(r.Action, options) != nil {
		out := rejected("invalid-command-options")
		out.Action = r.Action
		return out
	}
	scope := r.SkillID
	if r.Source == "mobile" {
		scope = r.FamilyID
	}
	id := IntentID(r.Source, scope, r.RequestID)
	existing, e := s.Docs.Load(ctx, "command#"+id)
	if e == nil {
		c, err := decoded[command](existing)
		if err != nil {
			return rejected("storage-unavailable")
		}
		if c.UserHash != uh || c.PersonHash != ph || c.Action != r.Action || c.DryRun != r.DryRun {
			return rejected("request-replay-mismatch")
		}
		a, _ := json.Marshal(c.Options)
		b, _ := json.Marshal(options)
		if string(a) != string(b) {
			return rejected("request-replay-mismatch")
		}
		return s.enqueue(ctx, existing, c)
	}
	if !errors.Is(e, auth.ErrMissing) {
		return rejected("storage-unavailable")
	}
	if reason := s.gated(ctx, r.Action, r.DryRun); reason != "" {
		out := rejected(reason)
		out.Action = r.Action
		out.CommandID = id
		return out
	}
	userDoc, user, e := s.user(ctx, uh, true)
	if e != nil {
		return rejected("storage-unavailable")
	}
	if !user.Active {
		return uncertainRequest(r, "binding-revoked")
	}
	personDoc, e := s.Docs.Load(ctx, "alexa-person#"+ph)
	if errors.Is(e, auth.ErrMissing) {
		personDoc = s.newDoc("alexa-person#" + ph)
	} else if e != nil {
		return rejected("storage-unavailable")
	}
	lastDoc, e := s.Docs.Load(ctx, "alexa-latest#"+ph)
	if errors.Is(e, auth.ErrMissing) {
		lastDoc = s.newDoc("alexa-latest#" + ph)
	} else if e != nil {
		return rejected("storage-unavailable")
	}
	c := command{ID: id, Action: r.Action, State: "accepted", Reason: "enqueue-pending", UserHash: uh, PersonHash: ph, PersonAccessToken: r.PersonAccessToken, CreatedAt: s.now().Unix(), UpdatedAt: s.now().Unix(), UserGeneration: user.Generation, DryRun: r.DryRun, Source: r.Source, FamilyID: r.FamilyID, Options: options}
	doc := s.newDoc("command#" + id)
	writes := []auth.Document{data(doc, c), data(userDoc, user), data(personDoc, personBinding{s.Config.Owner, uh, ph, user.Generation}), data(lastDoc, latest{id}), ownerDoc}
	if r.Source == "mobile" {
		familyDoc, err := s.familyDocument(ctx, r.FamilyID)
		if err != nil {
			return rejected("session-revoked")
		}
		writes = append(writes, familyDoc)
	}
	if e = s.Docs.Commit(ctx, writes...); e != nil {
		return unconfirmed(c, "intent-unconfirmed")
	}
	doc.Version++
	doc.Data, _ = json.Marshal(c)
	return s.enqueue(ctx, doc, c)
}
func (s *Service) familyDocument(ctx context.Context, id string) (auth.Document, error) {
	doc, e := s.Docs.Load(ctx, "family#"+id)
	if e != nil {
		return auth.Document{}, e
	}
	var value struct{ Family auth.Family }
	if json.Unmarshal(doc.Data, &value) != nil {
		return auth.Document{}, ErrPersistence
	}
	if e = auth.CheckFamily(value.Family, s.Config.Owner, s.now()); e != nil {
		return auth.Document{}, e
	}
	return doc, nil
}

func (s *Service) ownerDocument(ctx context.Context) (auth.Document, error) {
	doc, e := s.Docs.Load(ctx, "owner-binding")
	if e != nil {
		return auth.Document{}, e
	}
	var owner string
	if doc.Version == 0 || s.Config.Owner == "" || !s.now().Before(doc.ExpiresAt) || json.Unmarshal(doc.Data, &owner) != nil || owner != s.Config.Owner {
		return auth.Document{}, errors.New("owner binding unverified")
	}
	return doc, nil
}
func (s *Service) user(ctx context.Context, uh string, initial bool) (auth.Document, userBinding, error) {
	doc, e := s.Docs.Load(ctx, "alexa-user#"+uh)
	if errors.Is(e, auth.ErrMissing) {
		return s.newDoc("alexa-user#" + uh), userBinding{Active: initial, Generation: 1}, nil
	}
	if e != nil {
		return auth.Document{}, userBinding{}, e
	}
	binding, e := decoded[userBinding](doc)
	return doc, binding, e
}
func (s *Service) event(ctx context.Context, r contract.AlexaCommandRequest) contract.AlexaCommandResponse {
	if !fresh(r.EventPublishingTime, s.now()) {
		return rejected("stale-binding-event")
	}
	active := false
	switch r.EventType {
	case "AlexaSkillEvent.SkillAccountLinked":
		active = true
	case "AlexaSkillEvent.SkillDisabled", "AlexaSkillEvent.SkillAccountUnlinked":
	default:
		return rejected("unsupported-binding-event")
	}
	doc, b, e := s.user(ctx, hash(r.UserID), false)
	if e != nil {
		return rejected("storage-unavailable")
	}
	stamp, _ := time.Parse(time.RFC3339Nano, r.EventPublishingTime)
	if stamp.UnixNano() < b.LastEventAt || (stamp.UnixNano() == b.LastEventAt && (active || !b.Active)) {
		return contract.AlexaCommandResponse{State: "succeeded", Reason: "event-already-applied"}
	}
	if b.Generation == ^uint64(0) {
		return rejected("binding-invalid")
	}
	b.Generation++
	b.Active = active
	b.LastEventAt = stamp.UnixNano()
	if s.save(ctx, &doc, b) != nil {
		return rejected("storage-unavailable")
	}
	return contract.AlexaCommandResponse{State: "succeeded", Reason: "binding-event-applied", UpdatedAt: s.now().Unix()}
}
func (s *Service) enqueue(ctx context.Context, doc auth.Document, c command) contract.AlexaCommandResponse {
	if e := s.derive(ctx, &doc, &c); e != nil {
		return unconfirmed(c, "state-unavailable")
	}
	if c.State != "accepted" {
		return response(c)
	}
	if reason := s.gated(ctx, c.Action, c.DryRun); reason != "" {
		return unconfirmed(c, reason)
	}
	if s.now().Unix()-c.CreatedAt >= 10 {
		c.State = "expired"
		c.Reason = "enqueue-window-expired"
		c.UpdatedAt = s.now().Unix()
		if s.save(ctx, &doc, c) != nil {
			return unconfirmed(c, "state-unavailable")
		}
		return response(c)
	}
	pointer, _ := json.Marshal(Pointer{"command", c.ID})
	sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	e := s.Queue.Send(sendCtx, string(pointer), c.ID)
	cancel()
	if e != nil {
		return unconfirmed(c, "queue-unconfirmed")
	}
	c.State = "queued"
	c.Reason = "queue-confirmed"
	c.UpdatedAt = s.now().Unix()
	if e = s.save(ctx, &doc, c); e != nil {
		current, loadErr := s.Docs.Load(ctx, doc.Key)
		if loadErr == nil {
			saved, decodeErr := decoded[command](current)
			if decodeErr == nil && saved.State != "accepted" {
				return response(saved)
			}
		}
		return unconfirmed(c, "queue-confirmed-state-unavailable")
	}
	return response(c)
}
func (s *Service) status(ctx context.Context, r contract.AlexaCommandRequest, uh, ph string) contract.AlexaCommandResponse {
	_, user, e := s.user(ctx, uh, false)
	if e != nil {
		return rejected("storage-unavailable")
	}
	if !user.Active {
		return uncertainRequest(r, "binding-revoked")
	}
	id := r.CommandID
	if id == "" {
		d, e := s.Docs.Load(ctx, "alexa-latest#"+ph)
		if errors.Is(e, auth.ErrMissing) {
			return contract.AlexaCommandResponse{State: "not-found"}
		}
		if e != nil {
			return rejected("storage-unavailable")
		}
		v, e := decoded[latest](d)
		if e != nil {
			return rejected("storage-unavailable")
		}
		id = v.ID
	}
	if !commandIDPattern.MatchString(id) {
		return rejected("invalid-command-id")
	}
	doc, e := s.Docs.Load(ctx, "command#"+id)
	if errors.Is(e, auth.ErrMissing) {
		return contract.AlexaCommandResponse{State: "not-found"}
	}
	if e != nil {
		return rejected("storage-unavailable")
	}
	c, e := decoded[command](doc)
	if e != nil {
		return rejected("storage-unavailable")
	}
	if c.UserHash != uh || c.PersonHash != ph || c.UserGeneration != user.Generation {
		return uncertainRequest(r, "binding-mismatch")
	}
	if s.derive(ctx, &doc, &c) != nil {
		return rejected("storage-unavailable")
	}
	return response(c)
}
func (s *Service) derive(ctx context.Context, doc *auth.Document, c *command) error {
	age := s.now().Unix() - c.CreatedAt
	changed := false
	if (c.State == "accepted" || c.State == "queued") && age >= 60 {
		c.State = "expired"
		c.Reason = "not-dispatched"
		changed = true
	}
	if (c.State == "dispatching" || c.State == "submitted") && s.now().Unix()-c.UpdatedAt >= 150 {
		if c.State == "dispatching" {
			c.Reason = "worker-lost"
		} else {
			c.Reason = "poll-abandoned"
		}
		c.State = "outcome-unknown"
		changed = true
	}
	if changed {
		c.UpdatedAt = s.now().Unix()
		return s.save(ctx, doc, *c)
	}
	return nil
}
