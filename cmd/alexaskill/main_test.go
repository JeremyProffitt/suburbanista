package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const firstID = "amzn1.ask.skill.11111111-1111-4111-8111-111111111111"
const secondID = "amzn1.ask.skill.22222222-2222-4222-8222-222222222222"

var manifest = []byte(`{"manifest":{"publishingInformation":{"locales":{"en-US":{"name":"Suburban Remote"}}},"apis":{"custom":{}}}}`)

func config() configuration {
	return configuration{Token: "synthetic-management-token", Vendor: "synthetic-vendor", Reference: referenceSkill, Wait: func(context.Context) error { return nil }}
}
func envelope(status int, body string) execution {
	data, _ := json.Marshal(struct {
		Body   json.RawMessage `json:"body"`
		Status int             `json:"statusCode"`
	}{json.RawMessage(body), status})
	return execution{Out: data}
}
func list(ids ...string) execution {
	type item struct {
		ID    string            `json:"skillId"`
		Names map[string]string `json:"nameByLocale"`
	}
	entries := []item{}
	for _, id := range ids {
		name := skillName
		if id == referenceSkill {
			name = "Automatica"
		}
		entries = append(entries, item{id, map[string]string{"en-US": name}})
	}
	data, _ := json.Marshal(struct {
		Skills    []item `json:"skills"`
		Truncated bool   `json:"isTruncated"`
	}{Skills: entries})
	return envelope(200, string(data))
}

type scripted struct {
	t         *testing.T
	responses []execution
	calls     [][]string
}

func (s *scripted) run(_ context.Context, args []string) execution {
	s.t.Helper()
	s.calls = append(s.calls, append([]string{}, args...))
	if len(s.responses) == 0 {
		s.t.Fatal("unexpected additional ASK call")
	}
	out := s.responses[0]
	s.responses = s.responses[1:]
	return out
}
func TestReferenceMismatchAndDuplicateRefuseCreate(t *testing.T) {
	for _, response := range []execution{list(firstID), list(referenceSkill, firstID, secondID)} {
		s := &scripted{t: t, responses: []execution{response}}
		if _, err := resolve(context.Background(), config(), manifest, s.run); err == nil || len(s.calls) != 1 {
			t.Fatal("unsafe vendor inventory accepted")
		}
	}
}
func TestExistingSkillAndStagesAreDeduplicated(t *testing.T) {
	for _, configured := range []string{"", firstID} {
		s := &scripted{t: t, responses: []execution{list(referenceSkill, firstID, firstID), envelope(200, string(manifest))}}
		c := config()
		c.Configured = configured
		out, err := resolve(context.Background(), c, manifest, s.run)
		if err != nil || out.Created || out.SkillID != firstID || !out.ManifestVerified || len(s.calls) != 2 {
			t.Fatal("existing skill not reused", err)
		}
		if s.calls[1][1] != "get-skill-manifest" {
			t.Fatal("existing skill mutated")
		}
	}
}
func TestPaginationUsesNextToken(t *testing.T) {
	s := &scripted{t: t, responses: []execution{envelope(200, `{"skills":[],"isTruncated":true,"nextToken":"synthetic-page"}`), list(referenceSkill, firstID), envelope(200, string(manifest))}}
	if _, err := resolve(context.Background(), config(), manifest, s.run); err != nil {
		t.Fatal(err)
	}
	want := []string{"smapi", "list-skills-for-vendor", "--max-results", "50", "--next-token", "synthetic-page", "--profile", profile, "--full-response"}
	if !reflect.DeepEqual(s.calls[1], want) {
		t.Fatal("wrong page CLI arguments")
	}
}
func TestCreatePersistsBeforeBoundedReadback(t *testing.T) {
	s := &scripted{t: t, responses: []execution{list(referenceSkill), envelope(202, `{"skillId":"`+firstID+`"}`), envelope(404, `{}`), envelope(200, string(manifest))}}
	c := config()
	persisted := false
	c.Persist = func(out result) error {
		if len(s.calls) != 2 || out.ManifestVerified || !out.Created || out.SkillID != firstID {
			t.Fatal("receipt not persisted before readback")
		}
		persisted = true
		return nil
	}
	c.Wait = func(context.Context) error {
		if !persisted {
			t.Fatal("readback ran before receipt")
		}
		return nil
	}
	out, err := resolve(context.Background(), c, manifest, s.run)
	if err != nil || !out.Created || !out.ManifestVerified {
		t.Fatal("creation verification failed", err)
	}
	if s.calls[1][1] != "create-skill-for-vendor" {
		t.Fatal("create not invoked")
	}
	for _, arg := range s.calls[1] {
		if strings.Contains(arg, c.Token) {
			t.Fatal("token in argv")
		}
	}
}
func TestCreateFailureNeverRetriesOrRecreates(t *testing.T) {
	s := &scripted{t: t, responses: []execution{list(referenceSkill), {Failure: errors.New("synthetic failure"), Err: []byte("provider unavailable")}}}
	if _, err := resolve(context.Background(), config(), manifest, s.run); err == nil || len(s.calls) != 2 {
		t.Fatal("ambiguous create retried")
	}
}
func TestPendingReadbackRetainsIdentityAndBoundsAttempts(t *testing.T) {
	responses := []execution{list(referenceSkill), envelope(202, `{"skillId":"`+firstID+`"}`)}
	for i := 0; i < 6; i++ {
		responses = append(responses, envelope(404, `{}`))
	}
	s := &scripted{t: t, responses: responses}
	out, err := resolve(context.Background(), config(), manifest, s.run)
	if err == nil || out.SkillID != firstID || !out.Created || out.ManifestVerified || len(s.calls) != 8 {
		t.Fatal("pending identity lost or retry limit ignored")
	}
}
func TestReadbackAndConfiguredIdentityFailClosed(t *testing.T) {
	c := config()
	c.Configured = secondID
	s := &scripted{t: t, responses: []execution{list(referenceSkill, firstID)}}
	if _, err := resolve(context.Background(), c, manifest, s.run); err == nil || len(s.calls) != 1 {
		t.Fatal("unlisted configured skill accepted")
	}
	s = &scripted{t: t, responses: []execution{list(referenceSkill, firstID), envelope(200, strings.Replace(string(manifest), "Suburban Remote", "Other", 1))}}
	if _, err := resolve(context.Background(), config(), manifest, s.run); err == nil {
		t.Fatal("wrong readback manifest accepted")
	}
}
func TestRawFailureAndEncodedSecretNeverEscape(t *testing.T) {
	c := config()
	for _, form := range encoded(c.Token) {
		s := &scripted{t: t, responses: []execution{{Failure: errors.New("failure"), Err: []byte("invalid_grant " + form)}}}
		_, err := resolve(context.Background(), c, manifest, s.run)
		if err == nil || strings.Contains(err.Error(), form) || err.Error() != "ASK output contained private value; withheld" {
			t.Fatal("private child output not withheld")
		}
	}
	for text, want := range map[string]string{"invalid_grant details": "ASK authentication invalid-grant", "unauthorized_client details": "ASK authentication unauthorized-client", "status 401": "ASK authentication unauthorized", "status 403": "ASK authorization denied", "ValidationError detail": "SMAPI validation rejected", "unknown option --x": "ASK CLI invocation rejected"} {
		if got := safeFailure([]byte(text)).Error(); got != want {
			t.Fatal("wrong safe failure label", got)
		}
	}
}
func TestWorkflowGuard(t *testing.T) {
	env := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "JeremyProffittOrg/Suburban-Remote", "GITHUB_REF": "refs/heads/main", "GITHUB_EVENT_NAME": "workflow_dispatch"}
	if err := guard(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	for key := range env {
		copy := map[string]string{}
		for k, v := range env {
			copy[k] = v
		}
		copy[key] = "incorrect"
		if guard(func(k string) string { return copy[k] }) == nil {
			t.Fatal("workflow trust guard bypassed")
		}
	}
}

func TestTemporaryAWSOutputGuard(t *testing.T) {
	values := []string{"synthetic-ASK-token", "synthetic-temporary-AWS-key", "synthetic-session-token"}
	for _, value := range values {
		for _, form := range encoded(value) {
			if !containsPrivate([]byte("failure "+form), values...) {
				t.Fatal("temporary credential output accepted")
			}
		}
	}
	if containsPrivate([]byte("safe"), values...) {
		t.Fatal("safe output rejected")
	}
}
