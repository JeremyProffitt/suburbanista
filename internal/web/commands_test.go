package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/commands"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

type sessionDocuments struct{ items map[string]auth.Document }

func (d sessionDocuments) Load(_ context.Context, key string) (auth.Document, error) {
	value, ok := d.items[key]
	if !ok {
		return auth.Document{}, auth.ErrMissing
	}
	return value, nil
}
func (d sessionDocuments) Commit(context.Context, ...auth.Document) error { return auth.ErrConflict }

type brokerStub struct {
	calls []contract.AlexaCommandRequest
}

func (b *brokerStub) Command(_ context.Context, r contract.AlexaCommandRequest) (contract.AlexaCommandResponse, error) {
	b.calls = append(b.calls, r)
	if r.Operation == "features" {
		return contract.AlexaCommandResponse{State: "available", Availability: []contract.FeatureAvailability{{ID: "start", Available: true, ProofState: "owner-reported"}}}, nil
	}
	if r.Operation == "read" || r.Operation == "cached-read" {
		return contract.AlexaCommandResponse{State: "available", Snapshot: &contract.VehicleSnapshot{FeatureID: r.FeatureID, State: "partial", Reason: "observation-time-unavailable", FetchedAt: time.Now().UTC()}}, nil
	}
	return contract.AlexaCommandResponse{State: "queued", Action: r.Action, CommandID: "synthetic-command", UpdatedAt: 1800000000}, nil
}
func TestMobileCommandRouteUsesServerFamilyAndToken(t *testing.T) {
	now := time.Now().UTC()
	family, issued, e := auth.NewFamily("synthetic-owner", "synthetic-owner", "synthetic-owner", "synthetic-phone", now)
	if e != nil {
		t.Fatal(e)
	}
	fdata, _ := json.Marshal(struct {
		Family      auth.Family
		Tokens      amazon.Tokens
		ValidatedAt time.Time
	}{family, amazon.Tokens{AccessToken: "synthetic-server-token"}, now})
	adata, _ := json.Marshal(struct {
		FamilyID  string
		ExpiresAt time.Time
	}{family.ID, issued.AccessExpiresAt})
	// Session indexes contain a hash of the opaque handle, never its plaintext.
	hash := sha256.Sum256([]byte(issued.AccessHandle))
	digest := hex.EncodeToString(hash[:])
	docs := sessionDocuments{map[string]auth.Document{"family#" + family.ID: {Key: "family#" + family.ID, Version: 1, ExpiresAt: family.Deadline, Data: fdata}, "access#" + digest: {Key: "access#" + digest, Version: 1, ExpiresAt: issued.AccessExpiresAt, Data: adata}}}
	mobile := &auth.MobileService{Store: docs, Owner: "synthetic-owner", Now: func() time.Time { return now }}
	broker := &brokerStub{}
	adapter, e := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: mobile, Provider: &state.Store{}, Commands: broker})
	if e != nil {
		t.Fatal(e)
	}
	event := request(t)
	event.RawPath = "/api/commands"
	event.RawQueryString = ""
	event.RequestContext.HTTP.Method = "POST"
	event.Headers = map[string]string{"content-type": "application/json", "authorization": "Bearer " + issued.AccessHandle}
	event.Body = `{"action":"lock","requestId":"synthetic-mobile-intent"}`
	out, e := adapter.Handle(context.Background(), event)
	if e != nil || out.StatusCode != 202 || len(broker.calls) != 1 {
		t.Fatal("command route failed", out.StatusCode, e)
	}
	sent := broker.calls[0]
	if sent.Source != "mobile" || sent.FamilyID != family.ID || sent.PersonAccessToken != "synthetic-server-token" || sent.Action != "lock" || sent.RequestID != "synthetic-mobile-intent" || sent.DryRun {
		t.Fatal("wrong server authority forwarded")
	}
	if strings.Contains(out.Body, "synthetic-server-token") || strings.Contains(out.Body, family.ID) {
		t.Fatal("private authority returned")
	}
	event.RequestContext.HTTP.Method = "GET"
	event.RawPath = "/api/commands/status"
	event.RawQueryString = "requestId=synthetic-mobile-intent"
	event.Body = ""
	out, e = adapter.Handle(context.Background(), event)
	if e != nil || out.StatusCode != 202 || broker.calls[1].CommandID != commands.IntentID("mobile", family.ID, "synthetic-mobile-intent") {
		t.Fatal("status lookup not bound to original intent")
	}
	event.RequestContext.HTTP.Method = "POST"
	event.RawPath = "/api/commands"
	event.RawQueryString = ""
	event.Body = `{"action":"lock","requestId":"synthetic-mobile-intent","familyId":"attacker"}`
	out, _ = adapter.Handle(context.Background(), event)
	if out.StatusCode != 400 || len(broker.calls) != 2 {
		t.Fatal("client supplied family accepted")
	}
	// Complete feature and read routes use exactly the same server authority.
	event.RequestContext.HTTP.Method = "GET"
	event.RawPath = "/api/features"
	event.Body = ""
	out, _ = adapter.Handle(context.Background(), event)
	var features contract.FeaturesResponse
	if out.StatusCode != 200 || json.Unmarshal([]byte(out.Body), &features) != nil || len(features.Features) != 22 || len(features.Availability) != 1 || broker.calls[2].Operation != "features" {
		t.Fatal("feature catalog route")
	}
	event.RequestContext.HTTP.Method = "POST"
	event.RawPath = "/api/vehicle/read"
	event.Body = `{"featureId":"diagnostics","requestId":"synthetic-read"}`
	out, _ = adapter.Handle(context.Background(), event)
	if out.StatusCode != 200 || broker.calls[3].Operation != "read" || broker.calls[3].FeatureID != "diagnostics" || broker.calls[3].FamilyID != family.ID {
		t.Fatal("read authority route")
	}
	event.RequestContext.HTTP.Method = "GET"
	event.RawPath = "/api/vehicle/read/status"
	event.RawQueryString = "featureId=diagnostics"
	event.Body = ""
	out, _ = adapter.Handle(context.Background(), event)
	if out.StatusCode != 200 || broker.calls[4].Operation != "cached-read" {
		t.Fatal("cached reads must not issue a fresh provider read")
	}
	event.RawQueryString = "featureId=start"
	out, _ = adapter.Handle(context.Background(), event)
	if out.StatusCode != 400 || len(broker.calls) != 5 {
		t.Fatal("action accepted as read")
	}
	event.RawPath = "/api/features"
	event.RawQueryString = ""
	delete(event.Headers, "authorization")
	out, _ = adapter.Handle(context.Background(), event)
	if out.StatusCode != 401 || len(broker.calls) != 5 {
		t.Fatal("feature route missing owner authentication")
	}
}
