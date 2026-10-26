package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

const syntheticClientID = "amzn1.application-oa2-client.00000000000000000000000000000001"
const syntheticClientSecret = "synthetic-private-client-secret+&quoted\""

func linkingReadback() execution {
	return envelope(200, `{"type":"AUTH_CODE","clientId":"`+syntheticClientID+`","authorizationUrl":"https://www.amazon.com/ap/oa","accessTokenUrl":"https://api.amazon.com/auth/o2/token","accessTokenScheme":"HTTP_BASIC","scopes":["profile:user_id"]}`)
}
func TestLinkConfigurationApprovalAndIdentityRequired(t *testing.T) {
	for _, kind := range []string{"approval", "skill", "reference", "profile", "secret"} {
		identity := iconIdentity()
		approved := true
		client, secret := syntheticClientID, syntheticClientSecret
		switch kind {
		case "approval":
			approved = false
		case "skill":
			identity.SkillID = firstID
		case "reference":
			identity.VendorMatchedReference = false
		case "profile":
			client = "other-profile"
		case "secret":
			secret = ""
		}
		calls := 0
		_, err := configureLinking(context.Background(), identity, "synthetic-token", client, secret, approved, func(context.Context, []string, []byte) execution { calls++; return execution{} })
		if err == nil || calls != 0 {
			t.Fatal("unauthorized configuration reached API", kind)
		}
	}
}
func TestLinkConfigurationSecretUsesStdinOnly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	previous := os.Stdout
	reader, writer, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stdout = writer
	defer func() { os.Stdout = previous; writer.Close(); reader.Close() }()
	calls := 0
	run := func(_ context.Context, args []string, input []byte) execution {
		calls++
		joined := strings.Join(args, " ")
		for _, form := range encoded(syntheticClientSecret) {
			if strings.Contains(joined, form) {
				t.Fatal("private value on argv")
			}
		}
		if calls == 1 {
			if !strings.Contains(joined, "update-account-linking-info") || !strings.Contains(joined, "--account-linking-request file:/dev/stdin") || !strings.Contains(joined, "--stage development") {
				t.Fatal("wrong private request route")
			}
			var request struct {
				Payload struct {
					ClientID string   `json:"clientId"`
					Secret   string   `json:"clientSecret"`
					Scheme   string   `json:"accessTokenScheme"`
					Skip     bool     `json:"skipOnEnablement"`
					Type     string   `json:"type"`
					Scopes   []string `json:"scopes"`
				} `json:"accountLinkingRequest"`
			}
			if json.Unmarshal(input, &request) != nil || request.Payload.ClientID != syntheticClientID || request.Payload.Secret != syntheticClientSecret || request.Payload.Scheme != "HTTP_BASIC" || request.Payload.Skip || request.Payload.Type != "AUTH_CODE" || len(request.Payload.Scopes) != 1 || request.Payload.Scopes[0] != "profile:user_id" {
				t.Fatal("canonical linking payload invalid")
			}
			return envelope(204, `{}`)
		}
		if calls != 2 || len(input) != 0 || !strings.Contains(joined, "get-account-linking-info") {
			t.Fatal("readback used private input or extra request")
		}
		return linkingReadback()
	}
	report, err := configureLinking(context.Background(), iconIdentity(), "synthetic-token", syntheticClientID, syntheticClientSecret, true, run)
	writer.Close()
	os.Stdout = previous
	printed, readErr := io.ReadAll(reader)
	if readErr != nil || containsPrivate(printed, syntheticClientSecret, "synthetic-token") {
		t.Fatal("private output escaped")
	}
	files, fileErr := os.ReadDir(dir)
	if fileErr != nil || len(files) != 0 {
		t.Fatal("configuration wrote private temporary files")
	}
	if err != nil || calls != 2 || !report.SharedSettingsVerified || !report.HTTPBasicVerified || !report.RequireLinkOnEnableSubmitted || report.RequireLinkOnEnableReadbackAvailable || !report.OwnerOAuthRequired || report.AlexaRedirectURLsAvailable {
		t.Fatal("linking metadata misreported", err)
	}
	data, _ := json.Marshal(report)
	if containsPrivate(data, syntheticClientSecret, "synthetic-token") || strings.Contains(string(data), "clientSecret") {
		t.Fatal("secret in report")
	}
}
func TestLinkConfigurationReadbackAndLeakFailClosed(t *testing.T) {
	for _, kind := range []string{"readback", "raw-leak", "encoded-leak", "failed-write"} {
		calls := 0
		run := func(_ context.Context, _ []string, _ []byte) execution {
			calls++
			if calls == 1 {
				switch kind {
				case "raw-leak":
					return execution{Out: []byte(syntheticClientSecret)}
				case "encoded-leak":
					return execution{Err: []byte(encoded(syntheticClientSecret)[1])}
				case "failed-write":
					return execution{Failure: errors.New("synthetic failure")}
				}
				return envelope(204, `{}`)
			}
			return envelope(200, `{"type":"AUTH_CODE","clientId":"wrong"}`)
		}
		report, err := configureLinking(context.Background(), iconIdentity(), "synthetic-token", syntheticClientID, syntheticClientSecret, true, run)
		if err == nil || report.SharedSettingsVerified || containsPrivate([]byte(err.Error()), syntheticClientSecret) {
			t.Fatal("linking failure leaked or verified")
		}
		if kind != "readback" && calls != 1 {
			t.Fatal("ambiguous write retried")
		}
	}
}

func TestInspectLinkingIsReadOnlyAndRevealsOnlyKnownShape(t *testing.T) {
	for _, test := range []struct {
		body           string
		ready, wrapper bool
	}{
		{`{"type":"AUTH_CODE","clientId":"` + syntheticClientID + `","authorizationUrl":"https://www.amazon.com/ap/oa","accessTokenUrl":"https://api.amazon.com/auth/o2/token","accessTokenScheme":"HTTP_BASIC","scopes":["profile:user_id"],"skipOnEnablement":false}`, true, false},
		{`{"accountLinkingResponse":{"clientSecret":"synthetic-do-not-report","clientId":"synthetic-other-profile"},"synthetic-private-key":"synthetic-private-value"}`, false, true},
	} {
		calls := 0
		run := func(_ context.Context, args []string) execution {
			calls++
			if args[1] != "get-account-linking-info" {
				t.Fatal("inspection attempted mutation")
			}
			return envelope(200, test.body)
		}
		report, err := inspectLinking(context.Background(), iconIdentity(), "synthetic-management-token", syntheticClientID, run)
		if err != nil || calls != 1 || !report.ReadOnly || report.HTTPStatus != 200 || report.Metadata.Ready != test.ready || report.Shape.ResponseWrapper != test.wrapper || report.Shape.BodyType != "object" {
			t.Fatal("inspection metadata incorrect", err)
		}
		data, _ := json.Marshal(report)
		for _, private := range []string{"synthetic-do-not-report", "synthetic-other-profile", "synthetic-private-key", "synthetic-private-value", syntheticClientID} {
			if strings.Contains(string(data), private) {
				t.Fatal("inspection leaked provider values")
			}
		}
	}
}
func TestInspectLinkingRejectsUnverifiedIdentityBeforeGET(t *testing.T) {
	identity := iconIdentity()
	identity.VendorMatchedReference = false
	calls := 0
	_, err := inspectLinking(context.Background(), identity, "synthetic-token", syntheticClientID, func(context.Context, []string) execution { calls++; return execution{} })
	if err == nil || calls != 0 {
		t.Fatal("unverified inspection reached provider")
	}
}

func TestActualWrappedLinkReadbackUsesCanonicalFields(t *testing.T) {
	// Runtime GET receipt40087817445 proves the accountLinkingResponse wrapper.
	// Values here are synthetic; no provider body or credential is retained.
	bare := `{"type":"AUTH_CODE","clientId":"` + syntheticClientID + `","authorizationUrl":"https://www.amazon.com/ap/oa","accessTokenUrl":"https://api.amazon.com/auth/o2/token","accessTokenScheme":"HTTP_BASIC","scopes":["profile:user_id"],"skipOnEnablement":false}`
	calls := 0
	report, err := inspectLinking(context.Background(), iconIdentity(), "synthetic-management-token", syntheticClientID, func(_ context.Context, args []string) execution {
		calls++
		if args[1] != "get-account-linking-info" {
			t.Fatal("readback attempted mutation")
		}
		return envelope(200, `{"accountLinkingResponse":`+bare+`}`)
	})
	if err != nil || calls != 1 || !report.Shape.ResponseWrapper || !report.Metadata.Ready || !report.HTTPBasicVerified || !report.RequireLinkOnEnableReadbackAvailable || !report.RequireLinkOnEnableVerified {
		t.Fatal("actual wrapped readback was not verified", err)
	}
}
func TestLinkReadbackRejectsAmbiguousAndInvalidWrappers(t *testing.T) {
	for _, raw := range []string{`{"accountLinkingResponse":null}`, `{"type":"AUTH_CODE","accountLinkingResponse":{}}`, `{"accountLinkingResponse":{"accountLinkingResponse":{}}}`} {
		if _, err := linkingReadbackObject([]byte(raw)); err == nil {
			t.Fatal("ambiguous wrapper accepted")
		}
	}
}
