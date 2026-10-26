// alexaskill resolves or creates the owner's development-only custom skill.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const referenceSkill = "amzn1.ask.skill.ecfcf936-be07-41dd-b244-eba82b8e23a7"
const profile = "__ENVIRONMENT_ASK_PROFILE__"
const skillName = "Suburbanista"
const legacySkillName = "Suburban Remote"

var skillID = regexp.MustCompile(`^amzn1\.ask\.skill\.[a-f0-9-]{36}$`)

type execution struct {
	Out, Err      []byte
	Failure       error
	PrivateOutput bool
}
type executor func(context.Context, []string) execution
type configuration struct {
	Token, Vendor, Reference, Configured string
	Persist                              func(result) error
	Wait                                 func(context.Context) error
}
type result struct {
	SkillID                string `json:"skillId"`
	VendorMatchedReference bool   `json:"vendorMatchedReference"`
	Created                bool   `json:"created"`
	ManifestVerified       bool   `json:"manifestVerified"`
	IconsVerified          bool   `json:"iconsVerified,omitempty"`
	SmallIconURI           string `json:"smallIconUri,omitempty"`
	LargeIconURI           string `json:"largeIconUri,omitempty"`
}
type response struct {
	Body   json.RawMessage `json:"body"`
	Status int             `json:"statusCode"`
}
type listing struct {
	Skills []struct {
		ID    string            `json:"skillId"`
		Stage string            `json:"stage"`
		Names map[string]string `json:"nameByLocale"`
	} `json:"skills"`
	Truncated bool   `json:"isTruncated"`
	Next      string `json:"nextToken"`
}

func encoded(value string) []string {
	j, _ := json.Marshal(value)
	return []string{value, base64.StdEncoding.EncodeToString([]byte(value)), string(j[1 : len(j)-1]), url.QueryEscape(value)}
}
func containsPrivate(data []byte, values ...string) bool {
	for _, value := range values {
		if value == "" {
			continue
		}
		for _, form := range encoded(value) {
			if bytes.Contains(data, []byte(form)) {
				return true
			}
		}
	}
	return false
}
func mask(w io.Writer, value string) {
	if value == "" {
		return
	}
	for _, form := range encoded(value) {
		for _, line := range strings.Split(form, "\n") {
			if line != "" {
				fmt.Fprintf(w, "::add-mask::%s\n", strings.NewReplacer("%", "%25", "\r", "%0D").Replace(line))
			}
		}
	}
}

var errPending = errors.New("SMAPI skill creation pending")

func safeFailure(data []byte) error {
	text := strings.ToLower(string(data))
	for _, entry := range []struct{ match, label string }{{"invalid_grant", "ASK authentication invalid-grant"}, {"unauthorized_client", "ASK authentication unauthorized-client"}, {"invalid_client", "ASK authentication invalid-client"}, {"401", "ASK authentication unauthorized"}, {"403", "ASK authorization denied"}, {"validation", "SMAPI validation rejected"}, {"400", "SMAPI request rejected"}, {"unknown option", "ASK CLI invocation rejected"}, {"command not found", "ASK CLI unavailable"}} {
		if strings.Contains(text, entry.match) {
			return errors.New(entry.label)
		}
	}
	if strings.Contains(text, "404") || strings.Contains(text, "303") {
		return errPending
	}
	return errors.New("ASK operation failed; raw output withheld")
}
func invoke(ctx context.Context, run executor, token string, args ...string) (response, error) {
	args = append([]string{"smapi"}, args...)
	args = append(args, "--profile", profile, "--full-response")
	for _, arg := range args {
		for _, form := range encoded(token) {
			if form != "" && strings.Contains(arg, form) {
				return response{}, errors.New("private value in ASK argument")
			}
		}
	}
	call := run(ctx, args)
	if call.PrivateOutput {
		return response{}, errors.New("ASK output contained private value; withheld")
	}
	combined := append(bytes.Clone(call.Out), call.Err...)
	for _, form := range encoded(token) {
		if form != "" && bytes.Contains(combined, []byte(form)) {
			return response{}, errors.New("ASK output contained private value; withheld")
		}
	}
	if call.Failure != nil {
		return response{}, safeFailure(combined)
	}
	var out response
	if len(call.Out) > 4*1024*1024 || json.Unmarshal(call.Out, &out) != nil || out.Status == 0 {
		return response{}, errors.New("invalid ASK response envelope")
	}
	if out.Status < 200 || out.Status >= 300 {
		return out, safeFailure(append([]byte(fmt.Sprintf("status %d ", out.Status)), out.Body...))
	}
	return out, nil
}
func checkManifest(data []byte, emptyCustom bool) error {
	var envelope struct {
		Manifest struct {
			Publishing struct {
				Locales map[string]struct {
					Name string `json:"name"`
				} `json:"locales"`
			} `json:"publishingInformation"`
			APIs map[string]json.RawMessage `json:"apis"`
		} `json:"manifest"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return errors.New("skill manifest name mismatch")
	}
	name := envelope.Manifest.Publishing.Locales["en-US"].Name
	if name != skillName && name != legacySkillName {
		return errors.New("skill manifest name mismatch")
	}
	custom, ok := envelope.Manifest.APIs["custom"]
	if !ok || len(envelope.Manifest.APIs) != 1 {
		return errors.New("skill manifest is not custom-only")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(custom, &fields) != nil || fields == nil {
		return errors.New("skill custom API invalid")
	}
	if emptyCustom && len(fields) != 0 {
		return errors.New("new skill must have an empty custom API")
	}
	return nil
}
func resolve(ctx context.Context, c configuration, manifest []byte, run executor) (result, error) {
	var out result
	if c.Token == "" || strings.HasPrefix(strings.TrimSpace(c.Token), "{") || c.Vendor == "" || c.Reference != referenceSkill || (c.Configured != "" && !skillID.MatchString(c.Configured)) {
		return out, errors.New("invalid Alexa configuration")
	}
	if err := checkManifest(manifest, true); err != nil {
		return out, err
	}
	foundReference := false
	named := map[string]bool{}
	listed := map[string]bool{}
	seenPages := map[string]bool{}
	next := ""
	for page := 0; page < 100; page++ {
		args := []string{"list-skills-for-vendor", "--max-results", "50"}
		if next != "" {
			args = append(args, "--next-token", next)
		}
		response, err := invoke(ctx, run, c.Token, args...)
		if err != nil {
			return out, err
		}
		var list listing
		if json.Unmarshal(response.Body, &list) != nil || list.Skills == nil {
			return out, errors.New("invalid vendor skill list")
		}
		for _, skill := range list.Skills {
			if !skillID.MatchString(skill.ID) {
				return out, errors.New("invalid listed skill identity")
			}
			listed[skill.ID] = true
			if skill.ID == c.Reference {
				foundReference = true
			}
			if skill.Names["en-US"] == skillName || skill.Names["en-US"] == legacySkillName {
				named[skill.ID] = true
			}
		}
		if !list.Truncated {
			break
		}
		if list.Next == "" || len(list.Next) > 2047 || seenPages[list.Next] || page == 99 {
			return out, errors.New("invalid or excessive skill pagination")
		}
		seenPages[list.Next] = true
		next = list.Next
	}
	if !foundReference {
		return out, errors.New("vendor does not contain the confirmed reference skill")
	}
	out.VendorMatchedReference = true
	if len(named) > 1 {
		return out, errors.New("multiple Suburban Remote skills; creation refused")
	}
	if c.Configured != "" {
		if !listed[c.Configured] || !named[c.Configured] {
			return out, errors.New("configured skill is not the named vendor skill")
		}
		out.SkillID = c.Configured
	} else {
		for id := range named {
			out.SkillID = id
		}
	}
	if out.SkillID == "" {
		created, err := invoke(ctx, run, c.Token, "create-skill-for-vendor", "--manifest", "file:skill-package/skill.json")
		if err != nil {
			return out, fmt.Errorf("create outcome unverified; no retry: %w", err)
		}
		var body struct {
			ID string `json:"skillId"`
		}
		if json.Unmarshal(created.Body, &body) != nil || !skillID.MatchString(body.ID) {
			return out, errors.New("create outcome has no valid skill identity; no retry")
		}
		out.SkillID = body.ID
		out.Created = true
	}
	if c.Persist != nil {
		if err := c.Persist(out); err != nil {
			return out, errors.New("cannot persist skill identity; no retry")
		}
	}
	var readback response
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		readback, err = invoke(ctx, run, c.Token, "get-skill-manifest", "--skill-id", out.SkillID, "--stage", "development")
		if err == nil || !out.Created || !errors.Is(err, errPending) || attempt == 5 {
			break
		}
		wait := c.Wait
		if wait == nil {
			wait = func(ctx context.Context) error {
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					return nil
				}
			}
		}
		if waitErr := wait(ctx); waitErr != nil {
			return out, errors.New("skill readback wait canceled; identity retained")
		}
	}
	if err != nil {
		return out, fmt.Errorf("skill readback failed; existing identity retained: %w", err)
	}
	if err = checkManifest(readback.Body, false); err != nil {
		return out, err
	}
	out.ManifestVerified = true
	return out, nil
}
func guard(env func(string) string) error {
	if env("GITHUB_ACTIONS") != "true" || env("GITHUB_REPOSITORY") != "JeremyProffittOrg/Suburban-Remote" || env("GITHUB_REF") != "refs/heads/main" || env("GITHUB_EVENT_NAME") != "workflow_dispatch" {
		return errors.New("skill setup requires this repository's dispatched main workflow")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if err := guard(os.Getenv); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return errors.New("skill setup requires the Linux CI runner")
	}
	if os.Getenv("ALEXA_ACTION") == "configure-linking" && os.Getenv("OWNER_APPROVED_LINK_CONFIGURATION") != "true" {
		return errors.New("owner approval for shared LWA secret configuration is required")
	}
	token := os.Getenv("ASK_REFRESH_TOKEN")
	private := []string{}
	for _, name := range []string{"ASK_REFRESH_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		value := os.Getenv(name)
		if value == "" {
			return fmt.Errorf("missing %s", name)
		}
		private = append(private, value)
		mask(os.Stdout, value)
	}
	if os.Getenv("ALEXA_ACTION") == "configure-linking" {
		value := os.Getenv("LWA_CLIENT_SECRET")
		if value == "" {
			return errors.New("missing LWA_CLIENT_SECRET")
		}
		private = append(private, value)
		mask(os.Stdout, value)
	}
	for _, name := range []string{"ASK_CLIENT_ID", "ASK_CLIENT_SECRET", "ASK_LWA_CLIENT_ID", "ASK_LWA_CLIENT_SECRET", "ASK_LWA_CLIENT_CONFIRMATION"} {
		if os.Getenv(name) != "" {
			return errors.New("ASK client overrides are not permitted")
		}
	}
	budget := 4 * time.Minute
	if os.Getenv("ALEXA_ACTION") == "deploy-voice" {
		budget = 22 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	execute := func(ctx context.Context, args []string) execution {
		return invokeInput(ctx, args, nil, private)
	}
	version := execute(ctx, []string{"--version"})
	if version.Failure != nil || strings.TrimSpace(string(version.Out)) != "2.30.7" {
		return errors.New("ASK CLI 2.30.7 required")
	}
	manifest, err := os.ReadFile("skill-package/skill.json")
	if err != nil {
		return errors.New("skill-package/skill.json unavailable")
	}
	action := os.Getenv("ALEXA_ACTION")
	if action == "" {
		action = "resolve"
	}
	if action != "resolve" && action != "update-icons" && action != "deploy-voice" && action != "configure-linking" && action != "inspect-linking" {
		return errors.New("unknown Alexa action")
	}
	if action == "update-icons" && os.Getenv("ALEXA_SKILL_ID") != suburbanSkill {
		return errors.New("icon update requires the existing Suburban skill identity")
	}
	if action == "deploy-voice" && os.Getenv("ALEXA_SKILL_ID") != suburbanSkill {
		return errors.New("voice deployment requires the existing Suburban skill identity")
	}
	if action == "configure-linking" && os.Getenv("ALEXA_SKILL_ID") != suburbanSkill {
		return errors.New("link configuration requires the existing Suburban skill identity")
	}
	if action == "inspect-linking" && os.Getenv("ALEXA_SKILL_ID") != suburbanSkill {
		return errors.New("link inspection requires the existing Suburban skill identity")
	}
	persist := func(result result) error {
		if err = os.MkdirAll("artifacts", 0700); err != nil {
			return errors.New("cannot create skill report directory")
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		if err = os.WriteFile("artifacts/alexa-skill.json", data, 0600); err != nil {
			return errors.New("cannot write skill report")
		}
		if path := os.Getenv("GITHUB_OUTPUT"); path != "" {
			output, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return errors.New("cannot write workflow skill identity")
			}
			_, err = fmt.Fprintln(output, "skill_id="+result.SkillID)
			output.Close()
			if err != nil {
				return errors.New("cannot write workflow skill identity")
			}
		}
		return nil
	}
	result, resolveErr := resolve(ctx, configuration{Token: token, Vendor: os.Getenv("ASK_VENDOR_ID"), Reference: os.Getenv("REFERENCE_SKILL_ID"), Configured: os.Getenv("ALEXA_SKILL_ID"), Persist: persist}, manifest, execute)
	if resolveErr != nil {
		return resolveErr
	}
	if action == "update-icons" {
		client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if err = verifyPublishedIcons(ctx, client); err != nil {
			return err
		}
		result, err = updateIcons(ctx, result, token, execute, writeIconManifest, nil)
		if err != nil {
			return err
		}
	}
	if action == "deploy-voice" {
		model, readErr := os.ReadFile(voiceModelPath)
		if readErr != nil {
			return errors.New("voice interaction model unavailable")
		}
		report, deployErr := deployVoice(ctx, result, token, os.Getenv("ALEXA_FUNCTION_ARN"), os.Getenv("LWA_CLIENT_ID"), model, execute, writeIconManifest, nil)
		if err = writeVoiceReport(report); err != nil {
			return err
		}
		if deployErr != nil {
			return deployErr
		}
	}
	if action == "configure-linking" {
		report, configureErr := configureLinking(ctx, result, token, os.Getenv("LWA_CLIENT_ID"), os.Getenv("LWA_CLIENT_SECRET"), os.Getenv("OWNER_APPROVED_LINK_CONFIGURATION") == "true", func(ctx context.Context, args []string, input []byte) execution {
			return invokeInput(ctx, args, input, private)
		})
		if err = writeLinkConfigurationReport(report); err != nil {
			return err
		}
		if configureErr != nil {
			return configureErr
		}
	}
	if action == "inspect-linking" {
		report, inspectErr := inspectLinking(ctx, result, token, os.Getenv("LWA_CLIENT_ID"), execute)
		if err = writeLinkInspection(report); err != nil {
			return err
		}
		if inspectErr != nil {
			return inspectErr
		}
	}
	if err = persist(result); err != nil {
		return err
	}
	fmt.Println("Alexa development skill identity and manifest verified")
	return nil
}
