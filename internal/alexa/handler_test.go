package alexa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

const syntheticSkill = "amzn1.ask.skill.00000000-0000-4000-8000-000000000001"

var fixtureTime = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

type recordingBroker struct {
	Calls  []contract.AlexaCommandRequest
	Result contract.AlexaCommandResponse
	Err    error
	Delay  time.Duration
}

func (b *recordingBroker) Call(ctx context.Context, r contract.AlexaCommandRequest) (contract.AlexaCommandResponse, error) {
	b.Calls = append(b.Calls, r)
	if b.Delay > 0 {
		timer := time.NewTimer(b.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return contract.AlexaCommandResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
	return b.Result, b.Err
}
func fixture(t *testing.T) Envelope {
	t.Helper()
	data, err := os.ReadFile("testdata/envelopes/IntentRequest_LockIntent_synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func invoke(t *testing.T, h Handler, e Envelope) (Response, error) {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return h.Handle(context.Background(), data)
}
func handler(b Broker) Handler {
	return Handler{SkillID: syntheticSkill, Broker: b, Now: func() time.Time { return fixtureTime }}
}
func TestAllCommandsSubmitWithoutPIN(t *testing.T) {
	for intent, action := range map[string]string{"StartIntent": "start", "LockIntent": "lock", "UnlockIntent": "unlock"} {
		t.Run(intent, func(t *testing.T) {
			b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "queued"}}
			e := fixture(t)
			e.Request.Intent.Name = intent
			out, err := invoke(t, handler(b), e)
			if err != nil || len(b.Calls) != 1 {
				t.Fatal("command not forwarded", err)
			}
			got := b.Calls[0]
			if got.Source != "alexa" || got.Operation != "submit" || got.Action != action || got.PersonAccessToken != "synthetic-account-token" || got.PersonID != e.Context.System.User.ID || got.RequestID != e.Request.ID || got.DryRun {
				t.Fatal("wrong command identity")
			}
			data, _ := json.Marshal(out)
			for _, forbidden := range []string{"Connections.", "PIN", "synthetic-account-token", "synthetic-person-linked-token", "amzn1.ask.person"} {
				if strings.Contains(string(data), forbidden) {
					t.Fatal("response contains unwanted identity or confirmation")
				}
			}
			if strings.Contains(out.Response.OutputSpeech.Text, "confirmed") {
				t.Fatal("queue falsely reported vehicle success")
			}
			want := map[string]string{"lock": "Lock command sent.", "unlock": "Unlock command sent.", "start": "Start command sent."}[action]
			if out.Response.OutputSpeech.Text != want || out.Response.ShouldEndSession == nil || !*out.Response.ShouldEndSession {
				t.Fatal("command acknowledgement is not concise")
			}
		})
	}
}
func TestWrongSkillStaleFutureAndSessionMismatch(t *testing.T) {
	for _, kind := range []string{"skill", "session", "stale", "future", "request-id"} {
		t.Run(kind, func(t *testing.T) {
			b := &recordingBroker{}
			e := fixture(t)
			switch kind {
			case "skill":
				e.Context.System.Application.ID = "other"
			case "session":
				e.Session.Application.ID = "other"
			case "stale":
				e.Request.Timestamp = fixtureTime.Add(-151 * time.Second).Format(time.RFC3339)
			case "future":
				e.Request.Timestamp = fixtureTime.Add(31 * time.Second).Format(time.RFC3339)
			case "request-id":
				e.Request.ID = "bad\nrequest"
			}
			if _, err := invoke(t, handler(b), e); err == nil || len(b.Calls) != 0 {
				t.Fatal("invalid request reached worker")
			}
		})
	}
}

func TestCommandSentRequiresConfirmedSubmission(t *testing.T) {
	for state, confirmed := range map[string]bool{"queued": true, "dispatching": true, "submitted": true, "succeeded": true, "accepted": false, "unconfirmed": false, "failed": false, "rejected": false, "outcome-unknown": false, "expired": false} {
		t.Run(state, func(t *testing.T) {
			b := &recordingBroker{Result: contract.AlexaCommandResponse{State: state, Action: "lock"}}
			e := fixture(t)
			out, err := invoke(t, handler(b), e)
			if err != nil || len(b.Calls) != 1 || (out.Response.OutputSpeech.Text == "Lock command sent.") != confirmed {
				t.Fatal("submission acknowledgement does not match confirmed delivery")
			}
			e.Request.Intent.Name = "CommandStatusIntent"
			out, err = invoke(t, handler(b), e)
			if err != nil || len(b.Calls) != 2 || strings.Contains(out.Response.OutputSpeech.Text, "command sent.") {
				t.Fatal("command status lost its actual outcome")
			}
		})
	}
}
func TestLinkedAccountDoesNotRequirePerson(t *testing.T) {
	for _, intent := range []string{"LockIntent", "UnlockIntent"} {
		t.Run(intent, func(t *testing.T) {
			e := fixture(t)
			e.Request.Intent.Name = intent
			e.Context.System.Person = nil
			raw, _ := json.Marshal(e)
			var data map[string]any
			json.Unmarshal(raw, &data)
			system := data["context"].(map[string]any)["System"].(map[string]any)
			system["user"].(map[string]any)["accessToken"] = "synthetic-account-token"
			raw, _ = json.Marshal(data)
			b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "queued"}}
			out, err := handler(b).Handle(context.Background(), raw)
			if err != nil || len(b.Calls) != 1 || !strings.Contains(out.Response.OutputSpeech.Text, "command sent.") || out.Response.Card != nil {
				t.Fatal("linked account required a voice profile")
			}
			if got := b.Calls[0]; got.PersonID != e.Context.System.User.ID || got.PersonAccessToken != "synthetic-account-token" {
				t.Fatal("shared account identity not sent for owner validation")
			}
		})
	}
}

func TestVehicleStatusIntentReadsDiagnosticsWithoutSubmitting(t *testing.T) {
	e := fixture(t)
	e.Context.System.Person = nil
	e.Request.Intent.Name = "VehicleStatusIntent"
	at := fixtureTime.Add(-time.Hour)
	b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "succeeded", Snapshot: &contract.VehicleSnapshot{FeatureID: "diagnostics", State: "available", Items: []contract.SnapshotItem{{Fields: []contract.SnapshotField{
		{Key: "diagnostic-value", SourceName: "FUEL_LEVEL", Value: "50", Unit: "%", ObservedAt: &at},
		{Key: "diagnostic-value", SourceName: "FUEL_RANGE", Value: "160.9344", Unit: "KM", ObservedAt: &at},
		{Key: "diagnostic-value", SourceName: "ODO_READ", Value: "16093.44", Unit: "KM", ObservedAt: &at},
	}}}}}}
	out, err := invoke(t, handler(b), e)
	if err != nil || len(b.Calls) != 1 || b.Calls[0].Operation != "read" || b.Calls[0].FeatureID != "diagnostics" || b.Calls[0].Action != "" {
		t.Fatal("vehicle status did not use the read-only diagnostics path")
	}
	for _, want := range []string{"Fuel 50 percent", "Estimated range 100 miles", "Odometer 10000 miles", "reported 1 hour ago"} {
		if !strings.Contains(out.Response.OutputSpeech.Text, want) {
			t.Fatal("missing status reading", want)
		}
	}
	b.Result.Snapshot = nil
	out, err = invoke(t, handler(b), e)
	if err != nil || out.Response.OutputSpeech.Text != "Vehicle status is unavailable right now." {
		t.Fatal("missing status data reported as command success")
	}
}
func TestLinkingGuidanceAndPrivateDiagnostics(t *testing.T) {
	for _, kind := range []string{"unlinked", "person-unlinked", "linked", "invalid-user", "blank-token", "oversize-token", "broker-failure"} {
		t.Run(kind, func(t *testing.T) {
			e := fixture(t)
			e.Request.Intent.Name = "ConnectionStatusIntent"
			b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "succeeded"}}
			switch kind {
			case "unlinked":
				e.Context.System.User.AccessToken = ""
			case "person-unlinked":
				e.Context.System.User.AccessToken = "synthetic-account-token"
				e.Context.System.Person.AccessToken = ""
			case "blank-token":
				e.Context.System.User.AccessToken = " \t"
			case "oversize-token":
				e.Context.System.User.AccessToken = strings.Repeat("x", 16385)
			case "invalid-user":
				e.Context.System.User.ID = ""
				e.Session.User.ID = ""
			case "broker-failure":
				b.Result = contract.AlexaCommandResponse{State: "rejected", Reason: "synthetic-private-reason"}
			}
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(previous)
			out, err := invoke(t, handler(b), e)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unlinked", "blank-token", "oversize-token":
				if len(b.Calls) != 0 || out.Response.Card == nil || out.Response.Card.Type != "LinkAccount" {
					t.Fatal("unlinked account did not receive link card")
				}
			case "invalid-user":
				if len(b.Calls) != 0 || !strings.Contains(out.Response.OutputSpeech.Text, "valid account identity") {
					t.Fatal("invalid identity accepted")
				}
			default:
				if len(b.Calls) != 1 || b.Calls[0].Operation != "features" || b.Calls[0].PersonAccessToken != "synthetic-account-token" || b.Calls[0].PersonID != e.Context.System.User.ID {
					t.Fatal("linked account did not reach connection status")
				}
			}
			for _, private := range []string{"synthetic-account-token", "synthetic-person-linked-token", "synthetic-private-reason", "amzn1.ask.person", "amzn1.ask.account"} {
				if strings.Contains(logs.String(), private) {
					t.Fatal("diagnostics exposed private input")
				}
			}
			if !strings.Contains(logs.String(), "alexa_auth operation=features") {
				t.Fatal("connection diagnostics missing")
			}
		})
	}
}
func TestAccountStillRequiresOwnerAuthority(t *testing.T) {
	e := fixture(t)
	e.Context.System.Person = nil
	e.Request.Intent.Name = "ConnectionStatusIntent"
	b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "rejected", Reason: "owner-authority-unverified"}}
	out, err := invoke(t, handler(b), e)
	if err != nil || len(b.Calls) != 1 || !strings.Contains(out.Response.OutputSpeech.Text, "could not verify your owner account") {
		t.Fatal("account linking bypassed owner verification")
	}
}
func TestReplayKeepsDurableRequestIdentity(t *testing.T) {
	b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "queued"}}
	e := fixture(t)
	h := handler(b)
	invoke(t, h, e)
	b.Result = contract.AlexaCommandResponse{State: "rejected", Reason: "replay"}
	out, err := invoke(t, h, e)
	if err != nil || len(b.Calls) != 2 || b.Calls[0].RequestID != b.Calls[1].RequestID || !strings.Contains(out.Response.OutputSpeech.Text, "already handled") {
		t.Fatal("replay identity lost")
	}
}
func TestBindingEventsNeedFreshPublishingTimeButNoToken(t *testing.T) {
	for _, kind := range []string{"SkillDisabled", "SkillAccountLinked", "SkillAccountUnlinked"} {
		t.Run(kind, func(t *testing.T) {
			b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "succeeded"}}
			e := fixture(t)
			e.Session = nil
			e.Request.Type = "AlexaSkillEvent." + kind
			e.Request.EventPublishingTime = e.Request.Timestamp
			e.Context.System.Person = nil
			out, err := invoke(t, handler(b), e)
			if err != nil || out.Response.OutputSpeech != nil || len(b.Calls) != 1 {
				t.Fatal("event not persisted")
			}
			if got := b.Calls[0]; got.PersonAccessToken != "" || got.PersonID != "" || got.Operation != "event" || got.EventPublishingTime != e.Request.EventPublishingTime {
				t.Fatal("event identity malformed")
			}
			e.Request.EventPublishingTime = fixtureTime.Add(-151 * time.Second).Format(time.RFC3339)
			if _, err = invoke(t, handler(b), e); err == nil || len(b.Calls) != 1 {
				t.Fatal("stale event accepted")
			}
		})
	}
	b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "rejected", Reason: "storage-unavailable"}}
	e := fixture(t)
	e.Request.Type = "AlexaSkillEvent.SkillDisabled"
	e.Request.EventPublishingTime = e.Request.Timestamp
	if _, err := invoke(t, handler(b), e); err == nil {
		t.Fatal("event persistence failure acknowledged")
	}
}
func TestStatusReportsOnlyRealBoundedState(t *testing.T) {
	e := fixture(t)
	e.Request.Intent.Name = "CommandStatusIntent"
	b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "succeeded", Action: "lock", UpdatedAt: fixtureTime.Unix() - 20}}
	out, err := invoke(t, handler(b), e)
	if err != nil || b.Calls[0].Operation != "status" || !strings.Contains(out.Response.OutputSpeech.Text, "vehicle confirmed the lock") || !strings.Contains(out.Response.OutputSpeech.Text, "20 seconds") {
		t.Fatal("status did not reflect durable result")
	}
	b.Result = contract.AlexaCommandResponse{State: "outcome-unknown", Reason: "synthetic-secret"}
	out, _ = invoke(t, handler(b), e)
	if strings.Contains(out.Response.OutputSpeech.Text, "synthetic-secret") || !strings.Contains(out.Response.OutputSpeech.Text, "do not know") {
		t.Fatal("unknown result misreported")
	}
}
func TestResponseDeadline(t *testing.T) {
	b := &recordingBroker{Delay: 5 * time.Second}
	start := time.Now()
	out, err := invoke(t, handler(b), fixture(t))
	if err != nil || time.Since(start) >= 5*time.Second || !strings.Contains(out.Response.OutputSpeech.Text, "could not confirm") {
		t.Fatal("voice request exceeded its budget")
	}
}
func TestLambdaDeadlineReservesRenderingTime(t *testing.T) {
	b := &recordingBroker{Delay: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	raw, _ := json.Marshal(fixture(t))
	start := time.Now()
	handler(b).Handle(ctx, raw)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("parent Lambda deadline was not reserved")
	}
}

// The corpus is synthetic, based on Amazon's request/event envelope documentation.
// It is not evidence of a live device invocation or person-level account linking.
func TestEnvelopeContract(t *testing.T) {
	files, err := filepath.Glob("testdata/envelopes/*.json")
	if err != nil || len(files) < 14 {
		t.Fatal("envelope corpus incomplete")
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = decode(data); err != nil {
			t.Fatal(file, err)
		}
	}
	if _, err = decode([]byte(`{"unknown":true}`)); err == nil {
		t.Fatal("unmodeled top-level field accepted")
	}
}
func TestInteractionModel(t *testing.T) {
	data, err := os.ReadFile("../../skill-package/interactionModels/custom/en-US.json")
	if err != nil {
		t.Fatal(err)
	}
	var model struct {
		InteractionModel struct {
			LanguageModel struct {
				Invocation string `json:"invocationName"`
				Intents    []struct {
					Name    string   `json:"name"`
					Samples []string `json:"samples"`
				} `json:"intents"`
			} `json:"languageModel"`
		} `json:"interactionModel"`
	}
	if json.Unmarshal(data, &model) != nil || model.InteractionModel.LanguageModel.Invocation != "suburban" {
		t.Fatal("wrong invocation model")
	}
	names := map[string]bool{}
	for _, intent := range model.InteractionModel.LanguageModel.Intents {
		if names[intent.Name] {
			t.Fatal("duplicate model intent")
		}
		names[intent.Name] = true
	}
	for _, name := range []string{"StartIntent", "LockIntent", "UnlockIntent", "VehicleStatusIntent", "CommandStatusIntent", "AMAZON.HelpIntent", "AMAZON.StopIntent", "AMAZON.CancelIntent", "AMAZON.NavigateHomeIntent", "AMAZON.FallbackIntent"} {
		if !names[name] {
			t.Fatal("missing modeled intent", name)
		}
	}
	for name, id := range featureIntents {
		if !names[name] {
			t.Fatal("catalog intent missing", name)
		}
		if _, ok := contract.FindFeature(id); !ok {
			t.Fatal("intent has no catalog row", id)
		}
	}
	if len(names) != len(featureIntents)+10 {
		t.Fatal("unhandled model intent")
	}
}
func TestBrokerFailureDoesNotClaimSubmission(t *testing.T) {
	b := &recordingBroker{Err: errors.New("synthetic-sensitive-body")}
	out, err := invoke(t, handler(b), fixture(t))
	if err != nil || strings.Contains(out.Response.OutputSpeech.Text, "synthetic") || strings.Contains(out.Response.OutputSpeech.Text, "is queued") {
		t.Fatal("broker failure misreported")
	}
}
func TestUnconfirmedQueueNeverClaimsNotSentOrSuccess(t *testing.T) {
	text := commandSpeech(contract.AlexaCommandResponse{State: "unconfirmed", Reason: "queue-unconfirmed"}, false, fixtureTime)
	if text != "I could not confirm whether the request was queued. Ask for command status before trying again." {
		t.Fatal("ambiguous queue speech changed")
	}
	for _, bad := range []string{"not sent", "completed", "confirmed by the vehicle", "is queued"} {
		if strings.Contains(text, bad) {
			t.Fatal("ambiguous queue misreported")
		}
	}
}
func FuzzAlexaEnvelope(f *testing.F) {
	data, _ := os.ReadFile("testdata/envelopes/IntentRequest_LockIntent_synthetic.json")
	f.Add(data)
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		b := &recordingBroker{}
		h := handler(b)
		h.Handle(context.Background(), raw)
		if e, err := decode(raw); err == nil && e.Context.System.Application.ID != syntheticSkill && len(b.Calls) > 0 {
			t.Fatal("wrong skill reached broker")
		}
	})
}
