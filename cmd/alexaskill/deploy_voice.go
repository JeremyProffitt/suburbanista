package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"
)

var ownedAlexaARN = regexp.MustCompile(`^arn:aws:lambda:us-east-1:759775734231:function:suburban-remote-AlexaFunction-[A-Za-z0-9]+$`)

const voiceModelPath = "skill-package/interactionModels/custom/en-US.json"
const personalization = "alexa::person_id:read"

var voiceEvents = []string{"SKILL_DISABLED", "SKILL_ACCOUNT_LINKED", "SKILL_ACCOUNT_UNLINKED"}

type linkingReadiness struct {
	Configured            bool     `json:"configured"`
	SharedClientMatch     bool     `json:"sharedClientMatch"`
	AuthorizationCode     bool     `json:"authorizationCode"`
	AuthorizationURLMatch bool     `json:"authorizationUrlMatch"`
	TokenURLMatch         bool     `json:"tokenUrlMatch"`
	ScopeMatch            bool     `json:"scopeMatch"`
	Ready                 bool     `json:"ready"`
	Missing               []string `json:"missing"`
}
type voiceReport struct {
	SkillID                         string           `json:"skillId"`
	Stage                           string           `json:"stage"`
	EndpointVerified                bool             `json:"endpointVerified"`
	ManifestVerified                bool             `json:"manifestVerified"`
	ModelVerified                   bool             `json:"modelVerified"`
	DevelopmentEnabled              bool             `json:"developmentEnabled"`
	OwnerVoiceAuthorizationVerified bool             `json:"ownerVoiceAuthorizationVerified"`
	Invocation                      string           `json:"invocation"`
	AccountLinking                  linkingReadiness `json:"accountLinking"`
}

func object(raw []byte) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return nil, errors.New("invalid voice deployment object")
	}
	return out, nil
}
func setJSON(target map[string]json.RawMessage, key string, value any) {
	target[key], _ = json.Marshal(value)
}
func mergeVoiceManifest(data []byte, arn string) ([]byte, error) {
	if !ownedAlexaARN.MatchString(arn) || checkManifest(data, false) != nil {
		return nil, errors.New("voice endpoint or skill identity invalid")
	}
	root, err := object(data)
	if err != nil {
		return nil, err
	}
	manifest, err := object(root["manifest"])
	if err != nil {
		return nil, err
	}
	apis, err := object(manifest["apis"])
	if err != nil {
		return nil, err
	}
	custom, err := object(apis["custom"])
	if err != nil {
		return nil, err
	}
	// Region overrides would silently bypass the default endpoint. Existing ones
	// must already match the owned function; this operation does not migrate regions.
	if err = checkRegionEndpoints(custom, arn); err != nil {
		return nil, err
	}
	setJSON(custom, "endpoint", map[string]string{"uri": arn})
	setJSON(apis, "custom", custom)
	setJSON(manifest, "apis", apis)
	permissions := []map[string]json.RawMessage{}
	if raw, ok := manifest["permissions"]; ok && json.Unmarshal(raw, &permissions) != nil {
		return nil, errors.New("invalid manifest permissions")
	}
	accountPermissions := []map[string]json.RawMessage{}
	for _, permission := range permissions {
		var name string
		json.Unmarshal(permission["name"], &name)
		if name != personalization {
			accountPermissions = append(accountPermissions, permission)
		}
	}
	setJSON(manifest, "permissions", accountPermissions)
	events := map[string]json.RawMessage{}
	if raw, ok := manifest["events"]; ok {
		events, err = object(raw)
		if err != nil {
			return nil, err
		}
	}
	if err = checkRegionEndpoints(events, arn); err != nil {
		return nil, err
	}
	setJSON(events, "endpoint", map[string]string{"uri": arn})
	subscriptions := []map[string]json.RawMessage{}
	if raw, ok := events["subscriptions"]; ok && json.Unmarshal(raw, &subscriptions) != nil {
		return nil, errors.New("invalid event subscriptions")
	}
	seen := map[string]bool{}
	for _, subscription := range subscriptions {
		var name string
		json.Unmarshal(subscription["eventName"], &name)
		seen[name] = true
	}
	for _, name := range voiceEvents {
		if !seen[name] {
			subscription := map[string]json.RawMessage{}
			setJSON(subscription, "eventName", name)
			subscriptions = append(subscriptions, subscription)
		}
	}
	setJSON(events, "subscriptions", subscriptions)
	setJSON(manifest, "events", events)
	setJSON(root, "manifest", manifest)
	return json.Marshal(root)
}
func checkRegionEndpoints(container map[string]json.RawMessage, arn string) error {
	raw, ok := container["regions"]
	if !ok {
		return nil
	}
	var regions map[string]struct {
		Endpoint struct {
			URI string `json:"uri"`
		} `json:"endpoint"`
	}
	if json.Unmarshal(raw, &regions) != nil {
		return errors.New("invalid regional endpoints")
	}
	for _, region := range regions {
		if region.Endpoint.URI != "" && region.Endpoint.URI != arn {
			return errors.New("regional endpoint would bypass owned function")
		}
	}
	return nil
}
func modelContent(data []byte) (json.RawMessage, error) {
	root, err := object(data)
	if err != nil {
		return nil, err
	}
	model, err := object(root["interactionModel"])
	if err != nil {
		return nil, err
	}
	language, err := object(model["languageModel"])
	if err != nil {
		return nil, err
	}
	var invocation string
	if json.Unmarshal(language["invocationName"], &invocation) != nil || invocation != "suburban" {
		return nil, errors.New("voice model invocation must be suburban")
	}
	return root["interactionModel"], nil
}
func voiceWait(ctx context.Context) error {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func awaitVoiceStatus(ctx context.Context, token, id, resource string, run executor, wait func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if wait == nil {
		wait = voiceWait
	}
	for attempt := 0; attempt < 40; attempt++ {
		out, err := invoke(ctx, run, token, "get-skill-status", "--skill-id", id, "--resource", resource)
		if err != nil {
			return err
		}
		var status struct {
			Manifest struct {
				Last struct {
					Status string `json:"status"`
				} `json:"lastUpdateRequest"`
			} `json:"manifest"`
			Models map[string]struct {
				Last struct {
					Status string `json:"status"`
				} `json:"lastUpdateRequest"`
			} `json:"interactionModel"`
		}
		if json.Unmarshal(out.Body, &status) != nil {
			return errors.New("voice build status invalid")
		}
		state := status.Manifest.Last.Status
		if resource == "interactionModel" {
			state = status.Models["en-US"].Last.Status
		}
		switch state {
		case "SUCCEEDED":
			return nil
		case "FAILED":
			return errors.New("voice " + resource + " build rejected by Amazon")
		case "IN_PROGRESS":
		default:
			return errors.New("voice build status unrecognized")
		}
		if attempt == 39 {
			return errors.New("voice build timed out")
		}
		if err = wait(ctx); err != nil {
			return errors.New("voice build wait canceled")
		}
	}
	return errors.New("voice build timed out")
}

// The live service wraps GET settings in accountLinkingResponse, whereas the
// pinned SDK schema documents a bare object. Accept these two observed/documented
// shapes only, and reject mixed or nested wrappers rather than guessing.
func linkingReadbackObject(data []byte) ([]byte, error) {
	root, err := object(data)
	if err != nil {
		return nil, errors.New("account linking metadata invalid")
	}
	wrapped, ok := root["accountLinkingResponse"]
	if !ok {
		return data, nil
	}
	for _, key := range []string{"type", "clientId", "authorizationUrl", "accessTokenUrl", "accountLinkingRequest", "accountLinkingClient", "accountLinkingInfo"} {
		if _, exists := root[key]; exists {
			return nil, errors.New("ambiguous account linking metadata")
		}
	}
	inner, err := object(wrapped)
	if err != nil {
		return nil, errors.New("account linking response wrapper invalid")
	}
	if _, nested := inner["accountLinkingResponse"]; nested {
		return nil, errors.New("nested account linking response wrapper")
	}
	return wrapped, nil
}
func linkingMetadata(data []byte, clientID string) (linkingReadiness, error) {
	out := linkingReadiness{Missing: []string{}}
	data, err := linkingReadbackObject(data)
	if err != nil {
		return out, err
	}
	var link struct {
		Type             string   `json:"type"`
		ClientID         string   `json:"clientId"`
		AuthorizationURL string   `json:"authorizationUrl"`
		TokenURL         string   `json:"accessTokenUrl"`
		Scopes           []string `json:"scopes"`
	}
	if json.Unmarshal(data, &link) != nil {
		return out, errors.New("account linking metadata invalid")
	}
	out.Configured = link.ClientID != "" && link.Type != ""
	out.SharedClientMatch = clientID != "" && link.ClientID == clientID
	out.AuthorizationCode = link.Type == "AUTH_CODE"
	out.AuthorizationURLMatch = link.AuthorizationURL == "https://www.amazon.com/ap/oa"
	out.TokenURLMatch = link.TokenURL == "https://api.amazon.com/auth/o2/token"
	for _, scope := range link.Scopes {
		if scope == "profile:user_id" {
			out.ScopeMatch = true
		}
	}
	for _, check := range []struct {
		name string
		pass bool
	}{{"configured", out.Configured}, {"shared-client", out.SharedClientMatch}, {"authorization-code", out.AuthorizationCode}, {"amazon-authorization-url", out.AuthorizationURLMatch}, {"amazon-token-url", out.TokenURLMatch}, {"owner-profile-scope", out.ScopeMatch}} {
		if !check.pass {
			out.Missing = append(out.Missing, check.name)
		}
	}
	out.Ready = len(out.Missing) == 0
	return out, nil
}
func deployVoice(ctx context.Context, identity result, token, arn, clientID string, model []byte, run executor, write manifestWriter, wait func(context.Context) error) (voiceReport, error) {
	report := voiceReport{SkillID: identity.SkillID, Stage: "development", Invocation: "suburban"}
	if identity.SkillID != suburbanSkill || identity.Created || !identity.VendorMatchedReference || !identity.ManifestVerified || !ownedAlexaARN.MatchString(arn) || strings.TrimSpace(clientID) == "" || write == nil {
		return report, errors.New("voice deployment requires verified existing skill and owned endpoint")
	}
	expectedModel, err := modelContent(model)
	if err != nil {
		return report, err
	}
	before, err := invoke(ctx, run, token, "get-skill-manifest", "--skill-id", identity.SkillID, "--stage", "development")
	if err != nil {
		return report, err
	}
	wanted, err := mergeVoiceManifest(before.Body, arn)
	if err != nil {
		return report, err
	}
	if !sameJSON(wanted, before.Body) {
		path, cleanup, err := write(wanted)
		if err != nil || cleanup == nil {
			return report, errors.New("cannot prepare voice manifest")
		}
		defer cleanup()
		if _, err = invoke(ctx, run, token, "update-skill-manifest", "--skill-id", identity.SkillID, "--stage", "development", "--manifest", "file:"+path); err != nil {
			return report, errors.New("voice manifest update outcome unverified; no retry")
		}
		if err = awaitVoiceStatus(ctx, token, identity.SkillID, "manifest", run, wait); err != nil {
			return report, err
		}
	}
	after, err := invoke(ctx, run, token, "get-skill-manifest", "--skill-id", identity.SkillID, "--stage", "development")
	if err != nil {
		return report, err
	}
	if !sameJSON(wanted, after.Body) {
		return report, errors.New("voice manifest readback differs from authorized update")
	}
	report.ManifestVerified = true
	report.EndpointVerified = true
	if _, err = invoke(ctx, run, token, "set-interaction-model", "--skill-id", identity.SkillID, "--stage", "development", "--locale", "en-US", "--interaction-model", "file:"+voiceModelPath); err != nil {
		return report, errors.New("voice model update outcome unverified; no retry")
	}
	if err = awaitVoiceStatus(ctx, token, identity.SkillID, "interactionModel", run, wait); err != nil {
		return report, err
	}
	actual, err := invoke(ctx, run, token, "get-interaction-model", "--skill-id", identity.SkillID, "--stage", "development", "--locale", "en-US")
	if err != nil {
		return report, err
	}
	actualModel, err := modelContent(actual.Body)
	if err != nil || !sameJSON(expectedModel, actualModel) {
		return report, errors.New("voice model readback mismatch")
	}
	report.ModelVerified = true
	enabled, err := invoke(ctx, run, token, "set-skill-enablement", "--skill-id", identity.SkillID, "--stage", "development")
	if err != nil || enabled.Status != 204 {
		return report, errors.New("development skill enablement unverified; no retry")
	}
	status, err := invoke(ctx, run, token, "get-skill-enablement-status", "--skill-id", identity.SkillID, "--stage", "development")
	if err != nil || status.Status != 204 {
		return report, errors.New("development skill enablement readback failed")
	}
	report.DevelopmentEnabled = true
	link, err := invoke(ctx, run, token, "get-account-linking-info", "--skill-id", identity.SkillID, "--stage", "development")
	if errors.Is(err, errPending) {
		report.AccountLinking = linkingReadiness{Missing: []string{"configured"}}
		return report, nil
	}
	if err != nil {
		return report, err
	}
	report.AccountLinking, err = linkingMetadata(link.Body, clientID)
	return report, err
}
func writeVoiceReport(report voiceReport) error {
	if err := os.MkdirAll("artifacts", 0700); err != nil {
		return errors.New("cannot create voice report directory")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.New("cannot encode voice report")
	}
	if os.WriteFile("artifacts/alexa-voice.json", data, 0600) != nil {
		return errors.New("cannot write voice report")
	}
	return nil
}
