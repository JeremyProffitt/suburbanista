package alexa

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

func intentFor(id string) string {
	for intent, feature := range featureIntents {
		if feature == id {
			return intent
		}
	}
	return ""
}
func TestCatalogModelPreviewParity(t *testing.T) {
	data, err := os.ReadFile("../../skill-package/interactionModels/custom/en-US.json")
	if err != nil {
		t.Fatal(err)
	}
	var model struct {
		InteractionModel struct {
			LanguageModel struct {
				Types []struct {
					Name   string `json:"name"`
					Values []struct {
						ID string `json:"id"`
					} `json:"values"`
				} `json:"types"`
			} `json:"languageModel"`
		} `json:"interactionModel"`
	}
	if json.Unmarshal(data, &model) != nil {
		t.Fatal("invalid model")
	}
	entities := map[string]bool{}
	for _, typ := range model.InteractionModel.LanguageModel.Types {
		if typ.Name == "SUBURBAN_FEATURE" {
			for _, value := range typ.Values {
				if entities[value.ID] {
					t.Fatal("duplicate preview entity")
				}
				entities[value.ID] = true
			}
		}
	}
	if len(featureIntents) != 22 || len(featurePreviews) != 22 || len(entities) != 22 {
		t.Fatal("feature catalog coverage incomplete")
	}
	for _, feature := range contract.FeatureCatalog() {
		if intentFor(feature.ID) == "" || !entities[feature.ID] || !strings.HasPrefix(featurePreviews[feature.ID], "Demo.") {
			t.Fatal("missing feature destination", feature.ID)
		}
	}
}
func TestEveryPreviewHasZeroBrokerCallsWithoutLogin(t *testing.T) {
	for _, feature := range contract.FeatureCatalog() {
		t.Run(feature.ID, func(t *testing.T) {
			b := &recordingBroker{}
			e := fixture(t)
			e.Context.System.Person = nil
			e.Request.Intent.Name = "PreviewFeatureIntent"
			e.Request.Intent.Slots = map[string]Slot{"Feature": {Value: feature.ID}}
			out, err := invoke(t, handler(b), e)
			if err != nil || len(b.Calls) != 0 || !strings.HasPrefix(out.Response.OutputSpeech.Text, "Demo.") {
				t.Fatal("preview reached live broker or lacked label")
			}
		})
	}
}
func TestAllOrdinaryActionsAndReadDestinations(t *testing.T) {
	for _, feature := range contract.FeatureCatalog() {
		if feature.Category == "ev" {
			continue
		}
		t.Run(feature.ID, func(t *testing.T) {
			b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "queued"}}
			e := fixture(t)
			e.Request.Intent.Name = intentFor(feature.ID)
			if feature.Category == "read" {
				b.Result = contract.AlexaCommandResponse{State: "succeeded", Snapshot: &contract.VehicleSnapshot{FeatureID: feature.ID, State: "unavailable", FetchedAt: fixtureTime}}
			}
			out, err := invoke(t, handler(b), e)
			if err != nil || len(b.Calls) != 1 {
				t.Fatal("feature not routed", err)
			}
			got := b.Calls[0]
			if feature.Category == "action" && (got.Operation != "submit" || got.Action != feature.Action) {
				t.Fatal("wrong action routing")
			}
			if feature.Category == "read" && (got.Operation != "read" || got.FeatureID != feature.ReadID || !strings.Contains(out.Response.OutputSpeech.Text, "unavailable")) {
				t.Fatal("wrong or misleading read routing")
			}
		})
	}
}
func TestCommandParameterValidation(t *testing.T) {
	cases := []struct {
		action string
		slots  map[string]Slot
		valid  bool
	}{
		{"start", map[string]Slot{"Temperature": {Value: "22"}}, true}, {"start", map[string]Slot{"Temperature": {Value: "22.5"}}, false},
		{"lock", map[string]Slot{"Delay": {Value: "0"}}, true}, {"lock", map[string]Slot{"Delay": {Value: "-1"}}, false}, {"lock", map[string]Slot{"Temperature": {Value: "22"}}, false},
		{"horn-and-lights", map[string]Slot{"AlertAction": {Value: "Both"}, "Duration": {Value: "1"}}, true}, {"horn-and-lights", map[string]Slot{"Duration": {Value: "0"}}, false},
		{"horn-and-lights", map[string]Slot{"AlertAction": {Value: "DoorOpen"}}, false}, {"horn-and-lights", map[string]Slot{"Override": {Value: "IgnitionOn"}}, false},
		{"flash-lights", map[string]Slot{"AlertAction": {Value: "Honk"}}, false}, {"flash-lights", map[string]Slot{"AlertAction": {Value: "Flash"}, "Delay": {Value: "2"}}, true},
		{"cancel-start", map[string]Slot{"Delay": {Value: "1"}}, false}, {"unlock", map[string]Slot{"Delay": {Value: "9999999999999999999999999"}}, false},
	}
	for _, tc := range cases {
		_, err := parseOptions(tc.action, tc.slots)
		if (err == nil) != tc.valid {
			t.Fatal("wrong parameter validation", tc.action, tc.valid)
		}
		if !tc.valid {
			b := &recordingBroker{}
			e := fixture(t)
			e.Request.Intent.Name = intentFor(tc.action)
			e.Request.Intent.Slots = tc.slots
			if _, err := invoke(t, handler(b), e); err != nil || len(b.Calls) != 0 {
				t.Fatal("invalid options reached broker")
			}
		}
	}
	for _, value := range []string{"1", "100", "80"} {
		if !chargeTargetValid(value) {
			t.Fatal("valid percent rejected")
		}
	}
	for _, value := range []string{"0", "101", "80.5", "", "lots"} {
		if chargeTargetValid(value) {
			t.Fatal("invalid percent accepted")
		}
	}
}
func TestEVLiveRequestsAreExplicitlyUnavailable(t *testing.T) {
	for _, feature := range contract.FeatureCatalog() {
		if feature.Category != "ev" {
			continue
		}
		b := &recordingBroker{}
		e := fixture(t)
		e.Request.Intent.Name = intentFor(feature.ID)
		e.Request.Intent.Slots = map[string]Slot{"Target": {Value: "80"}}
		out, err := invoke(t, handler(b), e)
		if err != nil || len(b.Calls) != 0 || !strings.Contains(out.Response.OutputSpeech.Text, "unverified") || strings.HasPrefix(out.Response.OutputSpeech.Text, "Demo.") {
			t.Fatal("EV live request fabricated availability")
		}
	}
}
func TestConversationCancelIsNotVehicleCancellation(t *testing.T) {
	for _, intent := range []string{"AMAZON.StopIntent", "AMAZON.CancelIntent"} {
		b := &recordingBroker{}
		e := fixture(t)
		e.Request.Intent.Name = intent
		out, err := invoke(t, handler(b), e)
		if err != nil || len(b.Calls) != 0 || out.Response.OutputSpeech.Text != "Goodbye." {
			t.Fatal("conversation end actuated vehicle")
		}
	}
	for _, intent := range []string{"CancelEngineIntent", "StopAlertsIntent", "StopLightsIntent"} {
		b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "queued"}}
		e := fixture(t)
		e.Request.Intent.Name = intent
		invoke(t, handler(b), e)
		if len(b.Calls) != 1 || b.Calls[0].Operation != "submit" {
			t.Fatal("explicit vehicle cancellation not routed")
		}
	}
}
func TestReadSpeechPrivacyPartialAndSourceAge(t *testing.T) {
	observed := fixtureTime.Add(-2 * time.Minute)
	for _, feature := range contract.FeatureCatalog() {
		if feature.Category != "read" {
			continue
		}
		snapshot := &contract.VehicleSnapshot{FeatureID: feature.ID, State: "partial", FetchedAt: fixtureTime, ObservedAt: &observed, Fields: []contract.SnapshotField{{Key: "private", Value: "synthetic-private-value", Label: "synthetic-secret"}}, Items: []contract.SnapshotItem{{Title: "synthetic-address"}}}
		text := snapshotSpeech(snapshot, feature.ID, fixtureTime)
		if !strings.HasPrefix(text, "Partial response.") || !strings.Contains(text, "Source observed 2 minutes ago") {
			t.Fatal("source age or partial state lost")
		}
		for _, bad := range []string{"synthetic-private-value", "synthetic-secret", "synthetic-address"} {
			if strings.Contains(text, bad) {
				t.Fatal("private field spoken")
			}
		}
		snapshot.ObservedAt = nil
		if !strings.Contains(snapshotSpeech(snapshot, feature.ID, fixtureTime), "source age is unknown") {
			t.Fatal("fetch time mislabeled as observation time")
		}
	}
}
func TestConnectionAvailabilityDoesNotClaimVehicleOnline(t *testing.T) {
	b := &recordingBroker{Result: contract.AlexaCommandResponse{State: "succeeded", Availability: []contract.FeatureAvailability{{ID: "start", Available: true}, {ID: "diagnostics", Available: true}}}}
	e := fixture(t)
	e.Request.Intent.Name = "ConnectionStatusIntent"
	out, err := invoke(t, handler(b), e)
	if err != nil || len(b.Calls) != 1 || b.Calls[0].Operation != "features" || !strings.Contains(out.Response.OutputSpeech.Text, "does not prove the vehicle is online") {
		t.Fatal("connection status invented online proof")
	}
}

func TestUsefulReadSpeechUsesOnlyRecognizedValues(t *testing.T) {
	field := func(key, label, value, unit string) contract.SnapshotField {
		return contract.SnapshotField{Key: key, Label: label, Value: value, Unit: unit}
	}
	cases := []struct {
		id     string
		fields []contract.SnapshotField
		items  []contract.SnapshotItem
		want   []string
	}{
		{"diagnostics", []contract.SnapshotField{field("health-status", "", "GOOD", "")}, []contract.SnapshotItem{{Title: "Vehicle health", Fields: []contract.SnapshotField{field("diagnostic-value", "Fuel Level", "75", "%"), field("diagnostic-value", "Oil Life", "42", "percent"), field("diagnostic-value", "Odometer", "12345", "mi"), field("diagnostic-value", "Battery Voltage", "12.6", "V")}}}, []string{"Reported health: good", "fuel level 75 percent", "oil life 42 percent", "odometer 12345 miles"}},
		{"garage", []contract.SnapshotField{field("year", "", "2027", ""), field("make", "", "CHEVROLET", ""), field("model", "", "SUBURBAN", "")}, nil, []string{"2027 Chevrolet Suburban"}},
		{"onstar-plan", nil, []contract.SnapshotItem{{Title: "never speak this title", Fields: []contract.SnapshotField{field("plan-status", "", "ACTIVE", ""), field("expiry-date", "", "2027-10-24", "")}}}, []string{"plan is active", "October 24, 2027"}},
		{"warranty", []contract.SnapshotField{field("warranty-count", "", "25", "")}, []contract.SnapshotItem{{Fields: []contract.SnapshotField{field("warranty-status", "", "ACTIVE", ""), field("expiration-date", "", "2029-01-02", ""), field("end-mileage", "", "60000", "miles")}}}, []string{"25 warranty entries", "warranty is active", "January 2, 2029", "60000 miles"}},
		{"recalls", []contract.SnapshotField{field("recall-count", "", "24", "")}, []contract.SnapshotItem{{Title: "bounded item count"}}, []string{"24 recall entries"}},
		{"sxm-subscription", []contract.SnapshotField{field("sxm-status", "", "ACTIVE", ""), field("suspended", "", "false", "")}, []contract.SnapshotItem{{Fields: []contract.SnapshotField{field("end-date", "", "2027-03-04", "")}}}, []string{"status: active", "not suspended", "March 4, 2027"}},
	}
	for _, tc := range cases {
		snapshot := &contract.VehicleSnapshot{FeatureID: tc.id, State: "partial", Fields: tc.fields, Items: tc.items, FetchedAt: fixtureTime}
		text := snapshotSpeech(snapshot, tc.id, fixtureTime)
		for _, want := range tc.want {
			if !strings.Contains(text, want) {
				t.Fatal("useful read value missing", tc.id, want, text)
			}
		}
		if !strings.HasPrefix(text, "Partial response.") || !strings.Contains(text, "source age is unknown") {
			t.Fatal("freshness or partial status lost")
		}
		if tc.id == "diagnostics" && strings.Contains(text, "12.6") {
			t.Fatal("more than three measurements spoken")
		}
	}
	for _, tc := range []struct{ label, value, unit, want string }{{"Front Left Tire Pressure", "36", "psi", "front left tire pressure 36 psi"}, {"Battery Voltage", "12.6", "V", "battery 12.6 volts"}} {
		got, ok := spokenMeasurement(tc.label, tc.value, tc.unit)
		if !ok || got != tc.want {
			t.Fatal("recognized measurement unavailable")
		}
	}
}

func TestReadSpeechRejectsPrivateOrUnrecognizedValues(t *testing.T) {
	for _, tc := range []struct{ label, value, unit string }{{"Fuel Level", "https://private.invalid/value", "%"}, {"Fuel Level", strings.Repeat("1", 17), "%"}, {"Fuel Level", "101", "%"}, {"Fuel Level", "NaN", "%"}, {"Odometer", "9999999", "miles"}, {"Latitude", "37.42", "degrees"}, {"Owner token", "42", "%"}, {"Battery Voltage", "12.6", "private-name"}, {"Front Left Tire Pressure", "-1", "psi"}} {
		if _, ok := spokenMeasurement(tc.label, tc.value, tc.unit); ok {
			t.Fatal("unsafe measurement accepted")
		}
	}
	snapshot := &contract.VehicleSnapshot{FeatureID: "onstar-plan", State: "available", Items: []contract.SnapshotItem{{Title: "synthetic-private-title", Fields: []contract.SnapshotField{{Key: "plan-status", Value: "synthetic-private-token"}, {Key: "expiry-date", Value: "2027-01-02 private-address"}, {Key: "unknown", Value: "private-value"}}}}}
	text := snapshotSpeech(snapshot, "onstar-plan", fixtureTime)
	for _, bad := range []string{"synthetic-private", "private-address", "private-value"} {
		if strings.Contains(text, bad) {
			t.Fatal("private provider data spoken")
		}
	}
	if !strings.Contains(text, "No recognized plan status") {
		t.Fatal("missing safe-data fallback")
	}
	if safeDate("2027-02-30") != "" || safeStatus("active secret") != "" {
		t.Fatal("malformed date or status accepted")
	}
	snapshot = &contract.VehicleSnapshot{FeatureID: "recalls", State: "available", Items: []contract.SnapshotItem{{Title: "sample"}}}
	if strings.Contains(snapshotSpeech(snapshot, "recalls", fixtureTime), "1 recall entries") {
		t.Fatal("bounded items mistaken for actual total")
	}
}

func TestHealthSpeechKeepsReadingAgeSeparateFromReportAge(t *testing.T) {
	reportAt := fixtureTime.Add(-time.Minute)
	readingAt := fixtureTime.Add(-2 * time.Hour)
	snapshot := &contract.VehicleSnapshot{FeatureID: "diagnostics", State: "available", ObservedAt: &reportAt, Items: []contract.SnapshotItem{{Fields: []contract.SnapshotField{{Key: "diagnostic-value", Label: "Fuel level", Value: "75", Unit: "%", ObservedAt: &readingAt}}}}}
	text := snapshotSpeech(snapshot, "diagnostics", fixtureTime)
	if !strings.Contains(text, "reading was observed 120 minutes ago") || !strings.Contains(text, "Source observed 1 minute ago") {
		t.Fatal("individual reading age was replaced by report age")
	}
	snapshot.Items[0].Fields[0].ObservedAt = nil
	if !strings.Contains(snapshotSpeech(snapshot, "diagnostics", fixtureTime), "reading's source age is unknown") {
		t.Fatal("individual reading age invented")
	}
}

func TestVehicleStatusUsesVerifiedMeasurementsAndTheirAge(t *testing.T) {
	old := fixtureTime.Add(-18 * time.Hour)
	recent := fixtureTime.Add(-2 * time.Minute)
	future := fixtureTime.Add(time.Minute)
	field := func(name, value, unit string, at *time.Time) contract.SnapshotField {
		return contract.SnapshotField{Key: "diagnostic-value", SourceName: name, Label: "private-provider-label", Value: value, Unit: unit, ObservedAt: at}
	}
	tests := []struct {
		name   string
		fields []contract.SnapshotField
		want   []string
		absent []string
	}{
		{"kilometers", []contract.SnapshotField{field("FUEL_LEVEL", "44.7", "%", &old), field("FUEL_RANGE", "160.9344000", "KM", &old), field("ODO_READ", "16093.440000", "KM", &old)}, []string{"Fuel 44.7 percent, reported 18 hours ago.", "Estimated range 100 miles, reported 18 hours ago.", "Odometer 10000 miles, reported 18 hours ago."}, []string{"unavailable", "private"}},
		{"zero", []contract.SnapshotField{field("FUEL_LEVEL", "0", "%", &recent), field("FUEL_RANGE_MI", "0", "MI", &recent), field("ODO_READ_MI", "0", "MI", &recent)}, []string{"Fuel 0 percent", "Estimated range 0 miles", "Odometer 0 miles", "reported 2 minutes ago"}, []string{"unavailable"}},
		{"missing-times", []contract.SnapshotField{field("FUEL_LEVEL", "50", "%", nil), field("FUEL_RANGE", "160.9344", "KM", &future)}, []string{"Fuel 50 percent, source age unknown.", "Estimated range 100 miles, source age unknown.", "Odometer unavailable."}, []string{"reported", "live"}},
		{"no-inference", []contract.SnapshotField{field("FUEL_REMAINING", "50", "L", &recent), field("TRIP_A_ODO", "100", "KM", &recent), field("DOOR_LOCK_STATE", "LOCKED", "", &recent), field("ENGINE_RUNNING", "true", "", &recent), field("ENGINE_TYPE", "RUNNING", "", &recent)}, []string{"Fuel unavailable.", "Estimated range unavailable.", "Odometer unavailable."}, []string{"locked", "unlocked", "running", "stopped", "50", "100"}},
		{"wrong-units", []contract.SnapshotField{field("FUEL_LEVEL", "50", "L", &recent), field("FUEL_RANGE", "100", "%", &recent), field("ODO_READ", "200", "MI", &recent)}, []string{"Fuel unavailable.", "Estimated range unavailable.", "Odometer unavailable."}, []string{"50", "100", "200"}},
		{"newest-then-native", []contract.SnapshotField{field("FUEL_RANGE_MI", "99", "MI", &old), field("FUEL_RANGE", "160.9344", "KM", &recent), field("ODO_READ", "16093.44", "KM", &recent), field("ODO_READ_MI", "10001", "MI", &recent)}, []string{"Estimated range 100 miles, reported 2 minutes ago.", "Odometer 10001 miles, reported 2 minutes ago."}, []string{"18 hours", "range 99"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := &contract.VehicleSnapshot{FeatureID: "diagnostics", State: "partial", FetchedAt: fixtureTime, ObservedAt: &fixtureTime, Items: []contract.SnapshotItem{{Fields: test.fields}}}
			text := vehicleStatusSpeech(snapshot, fixtureTime)
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %q", want, text)
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(strings.ToLower(text), absent) {
					t.Fatalf("unexpected %q in %q", absent, text)
				}
			}
		})
	}
	for _, invalid := range []string{"NaN", "+Inf", "-1", "101", "private-value"} {
		snapshot := &contract.VehicleSnapshot{FeatureID: "diagnostics", State: "available", Fields: []contract.SnapshotField{field("FUEL_LEVEL", invalid, "%", &recent)}}
		if !strings.Contains(vehicleStatusSpeech(snapshot, fixtureTime), "Fuel unavailable.") {
			t.Fatal("invalid fuel reading accepted")
		}
	}
	for _, snapshot := range []*contract.VehicleSnapshot{nil, {FeatureID: "diagnostics", State: "unavailable"}, {FeatureID: "warranty", State: "available", Fields: []contract.SnapshotField{field("ODO_READ_MI", "60000", "MI", &recent)}}} {
		if vehicleStatusSpeech(snapshot, fixtureTime) != "Vehicle status is unavailable right now." {
			t.Fatal("unavailable or unrelated source reported as vehicle status")
		}
	}
}
