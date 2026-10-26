package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type inputExecutor func(context.Context, []string, []byte) execution

type linkingShape struct {
	BodyType        string `json:"bodyType"`
	Object          bool   `json:"object"`
	Empty           bool   `json:"empty"`
	TypeField       bool   `json:"typeField"`
	ClientIDField   bool   `json:"clientIdField"`
	RequestWrapper  bool   `json:"requestWrapper"`
	ResponseWrapper bool   `json:"responseWrapper"`
	ClientWrapper   bool   `json:"clientWrapper"`
	InfoWrapper     bool   `json:"infoWrapper"`
}
type linkingInspection struct {
	SkillID                              string           `json:"skillId"`
	Stage                                string           `json:"stage"`
	ReadOnly                             bool             `json:"readOnly"`
	HTTPStatus                           int              `json:"httpStatus"`
	Shape                                linkingShape     `json:"shape"`
	Metadata                             linkingReadiness `json:"metadata"`
	HTTPBasicVerified                    bool             `json:"httpBasicVerified"`
	RequireLinkOnEnableReadbackAvailable bool             `json:"requireLinkOnEnableReadbackAvailable"`
	RequireLinkOnEnableVerified          bool             `json:"requireLinkOnEnableVerified"`
}

func inspectLinking(ctx context.Context, identity result, token, clientID string, run executor) (linkingInspection, error) {
	out := linkingInspection{SkillID: identity.SkillID, Stage: "development", ReadOnly: true}
	if identity.SkillID != suburbanSkill || identity.Created || !identity.VendorMatchedReference || !identity.ManifestVerified || !lwaClientID.MatchString(clientID) {
		return out, errors.New("link inspection requires verified existing skill and canonical client")
	}
	response, err := invoke(ctx, run, token, "get-account-linking-info", "--skill-id", identity.SkillID, "--stage", "development")
	out.HTTPStatus = response.Status
	if err != nil {
		return out, err
	}
	out.Shape.BodyType = "other"
	trimmed := bytes.TrimSpace(response.Body)
	if len(trimmed) > 0 {
		switch trimmed[0] {
		case '{':
			out.Shape.BodyType = "object"
		case '[':
			out.Shape.BodyType = "array"
		case '"':
			out.Shape.BodyType = "string"
		case 'n':
			if string(trimmed) == "null" {
				out.Shape.BodyType = "null"
			}
		}
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(response.Body, &root) == nil && root != nil {
		out.Shape.Object = true
		out.Shape.Empty = len(root) == 0
		_, out.Shape.TypeField = root["type"]
		_, out.Shape.ClientIDField = root["clientId"]
		_, out.Shape.RequestWrapper = root["accountLinkingRequest"]
		_, out.Shape.ResponseWrapper = root["accountLinkingResponse"]
		_, out.Shape.ClientWrapper = root["accountLinkingClient"]
		_, out.Shape.InfoWrapper = root["accountLinkingInfo"]
		out.Metadata, err = linkingMetadata(response.Body, clientID)
		var properties struct {
			Scheme string `json:"accessTokenScheme"`
			Skip   *bool  `json:"skipOnEnablement"`
		}
		normalized, normalizeErr := linkingReadbackObject(response.Body)
		if normalizeErr != nil {
			return out, normalizeErr
		}
		if json.Unmarshal(normalized, &properties) == nil {
			out.HTTPBasicVerified = properties.Scheme == "HTTP_BASIC"
			out.RequireLinkOnEnableReadbackAvailable = properties.Skip != nil
			out.RequireLinkOnEnableVerified = properties.Skip != nil && !*properties.Skip
		}
	}
	return out, err
}
func writeLinkInspection(out linkingInspection) error {
	if os.MkdirAll("artifacts", 0700) != nil {
		return errors.New("cannot create inspection directory")
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return errors.New("cannot encode inspection metadata")
	}
	if os.WriteFile("artifacts/alexa-linking-inspection.json", data, 0600) != nil {
		return errors.New("cannot write inspection metadata")
	}
	return nil
}

type linkConfigurationReport struct {
	SkillID                              string           `json:"skillId"`
	Stage                                string           `json:"stage"`
	SharedSettingsVerified               bool             `json:"sharedSettingsVerified"`
	HTTPBasicVerified                    bool             `json:"httpBasicVerified"`
	RequireLinkOnEnableSubmitted         bool             `json:"requireLinkOnEnableSubmitted"`
	RequireLinkOnEnableReadbackAvailable bool             `json:"requireLinkOnEnableReadbackAvailable"`
	OwnerOAuthRequired                   bool             `json:"ownerOAuthRequired"`
	AlexaRedirectURLsAvailable           bool             `json:"alexaRedirectUrlsAvailable"`
	Metadata                             linkingReadiness `json:"metadata"`
}

var lwaClientID = regexp.MustCompile(`^amzn1\.application-oa2-client\.[a-f0-9]{32}$`)

// configureLinking is a separate one-time secret consumer. It cannot run through
// normal voice deployment and refuses to make even a read call without approval.
func configureLinking(ctx context.Context, identity result, token, clientID, secret string, approved bool, run inputExecutor) (linkConfigurationReport, error) {
	report := linkConfigurationReport{SkillID: identity.SkillID, Stage: "development", OwnerOAuthRequired: true}
	if !approved {
		return report, errors.New("owner approval for shared LWA secret configuration is required")
	}
	if identity.SkillID != suburbanSkill || identity.Created || !identity.VendorMatchedReference || !identity.ManifestVerified || !lwaClientID.MatchString(clientID) || secret == "" || token == "" || run == nil {
		return report, errors.New("link configuration requires verified existing skill and canonical LWA inputs")
	}
	payload, err := json.Marshal(map[string]any{"accountLinkingRequest": map[string]any{"type": "AUTH_CODE", "authorizationUrl": "https://www.amazon.com/ap/oa", "accessTokenUrl": "https://api.amazon.com/auth/o2/token", "clientId": clientID, "clientSecret": secret, "accessTokenScheme": "HTTP_BASIC", "scopes": []string{"profile:user_id"}, "skipOnEnablement": false}})
	if err != nil {
		return report, errors.New("cannot prepare account linking request")
	}
	defer clear(payload)
	// Pinned ASK 2.30.7's file: loader reads the supplied path directly with
	// fs.readFileSync. /dev/stdin is the child pipe; no private file is created.
	submit := func(ctx context.Context, args []string) execution {
		for _, arg := range args {
			if containsPrivate([]byte(arg), token, secret) {
				return execution{PrivateOutput: true}
			}
		}
		out := run(ctx, args, payload)
		if containsPrivate(out.Out, token, secret) || containsPrivate(out.Err, token, secret) {
			return execution{PrivateOutput: true}
		}
		return out
	}
	updated, err := invoke(ctx, submit, token, "update-account-linking-info", "--skill-id", identity.SkillID, "--stage", "development", "--account-linking-request", "file:/dev/stdin")
	if err != nil {
		return report, errors.New("account linking update outcome unverified; no retry")
	}
	if updated.Status != 204 {
		return report, errors.New("account linking update not acknowledged")
	}
	report.RequireLinkOnEnableSubmitted = true
	read := func(ctx context.Context, args []string) execution {
		out := run(ctx, args, nil)
		if containsPrivate(out.Out, token, secret) || containsPrivate(out.Err, token, secret) {
			return execution{PrivateOutput: true}
		}
		return out
	}
	checked, err := invoke(ctx, read, token, "get-account-linking-info", "--skill-id", identity.SkillID, "--stage", "development")
	if err != nil {
		return report, errors.New("account linking readback failed")
	}
	report.Metadata, err = linkingMetadata(checked.Body, clientID)
	if err != nil {
		return report, err
	}
	var extra struct {
		Scheme string `json:"accessTokenScheme"`
		Skip   *bool  `json:"skipOnEnablement"`
	}
	normalized, err := linkingReadbackObject(checked.Body)
	if err != nil {
		return report, err
	}
	if json.Unmarshal(normalized, &extra) != nil {
		return report, errors.New("invalid account linking readback")
	}
	report.HTTPBasicVerified = extra.Scheme == "HTTP_BASIC"
	report.RequireLinkOnEnableReadbackAvailable = extra.Skip != nil
	if extra.Skip != nil && *extra.Skip {
		return report, errors.New("account linking enablement requirement mismatch")
	}
	if !report.Metadata.Ready || !report.HTTPBasicVerified {
		return report, errors.New("account linking canonical settings mismatch")
	}
	report.SharedSettingsVerified = true
	// SMAPI redirectUrls is for app-to-app redirects, not the Alexa-generated
	// callback URLs that must be allowed in the LWA security profile.
	return report, nil
}
func writeLinkConfigurationReport(report linkConfigurationReport) error {
	if os.MkdirAll("artifacts", 0700) != nil {
		return errors.New("cannot create linking report directory")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.New("cannot encode linking report")
	}
	if os.WriteFile("artifacts/alexa-linking.json", data, 0600) != nil {
		return errors.New("cannot write linking report")
	}
	return nil
}

// invokeInput provides stdin without ever putting the input into argv or files.
func invokeInput(ctx context.Context, args []string, input []byte, private []string) execution {
	command := exec.CommandContext(ctx, "ask", args...)
	command.Env = []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "LWA_CLIENT_SECRET=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Stdin = bytes.NewReader(input)
	var out, stderr bytes.Buffer
	command.Stdout = &out
	command.Stderr = &stderr
	err := command.Run()
	if containsPrivate(out.Bytes(), private...) || containsPrivate(stderr.Bytes(), private...) {
		return execution{PrivateOutput: true, Failure: errors.New("private output withheld")}
	}
	return execution{Out: out.Bytes(), Err: stderr.Bytes(), Failure: err}
}
