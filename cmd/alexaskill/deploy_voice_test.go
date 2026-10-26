package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const syntheticAlexaARN = "arn:aws:lambda:us-east-1:759775734231:function:suburban-remote-AlexaFunction-Synthetic123"

var syntheticModel = []byte(`{"interactionModel":{"languageModel":{"invocationName":"suburban","intents":[{"name":"StartIntent","samples":["start"]}],"types":[]}}}`)

func noWait(context.Context) error { return nil }
func TestVoiceMergePreservesIconsAndUnrelatedFields(t *testing.T) {
	withPersonalization := []byte(strings.Replace(string(richManifest), `"permissions":[`, `"permissions":[{"name":"`+personalization+`"},`, 1))
	merged, err := mergeVoiceManifest(withPersonalization, syntheticAlexaARN)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	json.Unmarshal(richManifest, &before)
	json.Unmarshal(merged, &after)
	a, _ := object(before["manifest"])
	b, _ := object(after["manifest"])
	for _, key := range []string{"publishingInformation", "privacyAndCompliance", "unknownFutureField"} {
		if !sameJSON(a[key], b[key]) {
			t.Fatal("unrelated manifest data changed", key)
		}
	}
	if !strings.Contains(string(merged), "synthetic-permission") || strings.Contains(string(merged), personalization) || !strings.Contains(string(merged), "https://example.invalid/old.png") || !strings.Contains(string(merged), "9007199254740993") {
		t.Fatal("existing metadata lost")
	}
	for _, event := range voiceEvents {
		if !strings.Contains(string(merged), event) {
			t.Fatal("missing bindingevent")
		}
	}
	again, err := mergeVoiceManifest(merged, syntheticAlexaARN)
	if err != nil || !sameJSON(again, merged) {
		t.Fatal("merge is not idempotent")
	}
	for _, bad := range []string{strings.Replace(syntheticAlexaARN, "759775734231", "123456789012", 1), strings.Replace(syntheticAlexaARN, "us-east-1", "us-west-2", 1), strings.Replace(syntheticAlexaARN, "AlexaFunction", "WorkerFunction", 1)} {
		if _, err = mergeVoiceManifest(richManifest, bad); err == nil {
			t.Fatal("unowned endpoint accepted")
		}
	}
}
func TestVoiceDeployOneUpdateAndReadback(t *testing.T) {
	merged, _ := mergeVoiceManifest(richManifest, syntheticAlexaARN)
	script := &scripted{t: t, responses: []execution{envelope(200, string(richManifest)), envelope(202, `{}`), envelope(200, `{"manifest":{"lastUpdateRequest":{"status":"SUCCEEDED"}}}`), envelope(200, string(merged)), envelope(202, `{}`), envelope(200, `{"interactionModel":{"en-US":{"lastUpdateRequest":{"status":"IN_PROGRESS"}}}}`), envelope(200, `{"interactionModel":{"en-US":{"lastUpdateRequest":{"status":"SUCCEEDED"}}}}`), envelope(200, string(syntheticModel)), envelope(204, `{}`), envelope(204, `{}`), envelope(200, `{"type":"AUTH_CODE","clientId":"synthetic-shared-client","authorizationUrl":"https://www.amazon.com/ap/oa","accessTokenUrl":"https://api.amazon.com/auth/o2/token","scopes":["profile:user_id"],"clientSecret":"must-not-appear-in-report"}`)}}
	cleaned := false
	writer := func(data []byte) (string, func(), error) {
		if !sameJSON(data, merged) {
			t.Fatal("wrong manifest submitted")
		}
		return "synthetic-voice.json", func() { cleaned = true }, nil
	}
	report, err := deployVoice(context.Background(), iconIdentity(), "synthetic-management-token", syntheticAlexaARN, "synthetic-shared-client", syntheticModel, script.run, writer, noWait)
	if err != nil || !report.ModelVerified || !report.ManifestVerified || !report.AccountLinking.Ready || !report.DevelopmentEnabled || report.OwnerVoiceAuthorizationVerified || !cleaned {
		t.Fatal("voice deployment failed", err)
	}
	updates, models := 0, 0
	for _, call := range script.calls {
		switch call[1] {
		case "create-skill-for-vendor":
			t.Fatal("voice deployment created skill")
		case "update-skill-manifest":
			updates++
		case "set-interaction-model":
			models++
			if !strings.Contains(strings.Join(call, " "), "--interaction-model file:"+voiceModelPath) {
				t.Fatal("wrong model CLI flag")
			}
		}
	}
	if updates != 1 || models != 1 {
		t.Fatal("mutation repeated")
	}
	if script.calls[8][1] != "set-skill-enablement" || script.calls[9][1] != "get-skill-enablement-status" {
		t.Fatal("development enablement did not follow model verification")
	}
	for _, call := range script.calls[8:10] {
		if !strings.Contains(strings.Join(call, " "), "--stage development") {
			t.Fatal("enablement stage was not development")
		}
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "must-not-appear") || strings.Contains(string(data), "clientSecret") {
		t.Fatal("linking secret leaked")
	}
}
func TestDevelopmentEnablementReadbackFailureDoesNotClaimReady(t *testing.T) {
	merged, _ := mergeVoiceManifest(richManifest, syntheticAlexaARN)
	script := &scripted{t: t, responses: []execution{envelope(200, string(merged)), envelope(200, string(merged)), envelope(202, `{}`), envelope(200, `{"interactionModel":{"en-US":{"lastUpdateRequest":{"status":"SUCCEEDED"}}}}`), envelope(200, string(syntheticModel)), envelope(204, `{}`), envelope(404, `{}`)}}
	writer := func([]byte) (string, func(), error) {
		t.Fatal("unchanged manifest should not be rewritten")
		return "", nil, nil
	}
	report, err := deployVoice(context.Background(), iconIdentity(), "synthetic-token", syntheticAlexaARN, "synthetic-client", syntheticModel, script.run, writer, noWait)
	if err == nil || report.DevelopmentEnabled || !report.ModelVerified || len(script.calls) != 7 {
		t.Fatal("enablement readback failed open")
	}
}
func TestVoiceWrongIdentityModelAndReadbackFailClosed(t *testing.T) {
	writer := func([]byte) (string, func(), error) { return "synthetic.json", func() {}, nil }
	for _, kind := range []string{"reference", "created", "id", "model"} {
		identity := iconIdentity()
		model := syntheticModel
		switch kind {
		case "reference":
			identity.VendorMatchedReference = false
		case "created":
			identity.Created = true
		case "id":
			identity.SkillID = firstID
		case "model":
			model = []byte(strings.Replace(string(model), "suburban", "other", 1))
		}
		script := &scripted{t: t}
		if _, err := deployVoice(context.Background(), identity, "synthetic-management-token", syntheticAlexaARN, "synthetic-client", model, script.run, writer, noWait); err == nil || len(script.calls) != 0 {
			t.Fatal("invalid voice deployment reached provider")
		}
	}
	script := &scripted{t: t, responses: []execution{envelope(200, string(richManifest)), envelope(202, `{}`), envelope(200, `{"manifest":{"lastUpdateRequest":{"status":"SUCCEEDED"}}}`), envelope(200, string(richManifest))}}
	if _, err := deployVoice(context.Background(), iconIdentity(), "synthetic-management-token", syntheticAlexaARN, "synthetic-client", syntheticModel, script.run, writer, noWait); err == nil || len(script.calls) != 4 {
		t.Fatal("wrong manifest readback accepted")
	}
}
func TestVoiceStatusFailureIsBoundedAndDoesNotEcho(t *testing.T) {
	script := &scripted{t: t, responses: []execution{envelope(200, `{"interactionModel":{"en-US":{"lastUpdateRequest":{"status":"FAILED","errors":[{"message":"private-body"}]}}}}`)}}
	err := awaitVoiceStatus(context.Background(), "synthetic-token", suburbanSkill, "interactionModel", script.run, noWait)
	if err == nil || strings.Contains(err.Error(), "private-body") || len(script.calls) != 1 {
		t.Fatal("build failure leaked or retried")
	}
	script = &scripted{t: t, responses: []execution{{Failure: errors.New("failure"), Err: []byte("synthetic-token")}}}
	if err := awaitVoiceStatus(context.Background(), "synthetic-token", suburbanSkill, "manifest", script.run, noWait); err == nil || strings.Contains(err.Error(), "synthetic-token") {
		t.Fatal("secret output leaked")
	}
}
func TestAccountLinkingReadinessNeverTreatsMismatchAsReady(t *testing.T) {
	report, err := linkingMetadata([]byte(`{"type":"AUTH_CODE","clientId":"different","clientSecret":"never-output"}`), "expected")
	if err != nil || report.Ready || report.SharedClientMatch || !report.Configured {
		t.Fatal("link readiness mismatch mishandled")
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "never-output") {
		t.Fatal("secret in readiness")
	}
}
