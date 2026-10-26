package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const lwaCallback = "https://suburban.jeremy.ninja/auth/lwa/callback"
const lwaConsole = "https://developer.amazon.com/loginwithamazon/console/site/lwa/overview.html"

func runOwnerID(args []string, output io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: secretctl owner-id")
	}
	value, err := runGH([]string{"variable", "get", "LWA_CLIENT_ID", "--repo", repository}, nil)
	if err != nil {
		return errors.New("configure LWA_CLIENT_ID with secretctl lwa first")
	}
	prompt := func(label string) ([]byte, error) {
		fmt.Fprint(output, label)
		v, e := readAmazonInput()
		fmt.Fprintln(output)
		return v, e
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	owner, err := proveLWAOwner(client, strings.TrimSpace(string(value)), nil, output, prompt)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, owner)
	return nil
}

func runLWA(args []string, output io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: secretctl lwa")
	}
	prompt := func(label string) ([]byte, error) {
		fmt.Fprint(output, label)
		value, err := readAmazonInput()
		fmt.Fprintln(output)
		if err == nil {
			fmt.Fprintf(output, "Input length: %d characters; length does not prove provider validity.\n", len(value))
		}
		return value, err
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return setupLWA(runGH, prompt, func(id string, secret []byte) (string, error) {
		return proveLWAOwner(client, id, secret, output, prompt)
	}, output)
}

func setupLWA(run runCommand, prompt hiddenPrompt, prove func(string, []byte) (string, error), output io.Writer) error {
	secrets, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	variables, err := inventory(run, "variable")
	if err != nil {
		return err
	}
	readID := func(name, title string) (string, error) {
		if _, exists := variables[name]; exists {
			value, e := run([]string{"variable", "get", name, "--repo", repository}, nil)
			return strings.TrimSpace(string(value)), e
		}
		fmt.Fprintln(output, "Create or select the "+title+" security profile at:")
		fmt.Fprintln(output, lwaConsole)
		fmt.Fprintln(output, "Use Create a New Security Profile, then Manage > Web Settings > Edit.")
		fmt.Fprintln(output, "Consent Privacy Notice URL: https://suburban.jeremy.ninja/privacy")
		fmt.Fprintln(output, "Allowed Origin: https://suburban.jeremy.ninja")
		fmt.Fprintln(output, "Allowed Return URL: "+lwaCallback)
		fmt.Fprintln(output, "Save, then copy the Client ID. Add the console-supplied Alexa return URLs to this same profile later.")
		value, e := prompt(name + " (empty skips setup): ")
		defer clear(value)
		if e != nil {
			return "", e
		}
		return strings.TrimSpace(string(value)), nil
	}
	fmt.Fprintln(output, "Repository: "+repository+"\nStage shared-profile: one profile for web, mobile, and Alexa")
	webID, err := readID("LWA_CLIENT_ID", "Suburban Remote")
	if err != nil {
		return err
	}
	if webID == "" {
		fmt.Fprintln(output, "Setup skipped; no values changed.")
		return nil
	}
	var secret []byte
	if _, exists := secrets["LWA_CLIENT_SECRET"]; !exists {
		fmt.Fprintln(output, "In that profile's Web Settings, reveal the Client Secret. This helper stores it as LWA_CLIENT_SECRET; use the same secret locally in Alexa account-linking settings later.")
		secret, err = prompt("LWA_CLIENT_SECRET (hidden; empty skips setup): ")
		defer clear(secret)
		if err != nil {
			return err
		}
		if len(secret) == 0 {
			fmt.Fprintln(output, "Setup skipped; no values changed.")
			return nil
		}
	}
	fmt.Fprintln(output, "Stage owner-proof: sign in once as the owner with the shared profile")
	webOwner, err := prove(webID, secret)
	if err != nil {
		return err
	}
	return installLWA(run, webID, webOwner, secret, output)
}

func newLWARequest(clientID string) (string, string, string, error) {
	if !strings.HasPrefix(clientID, "amzn1.application-oa2-client.") || len(clientID) > 512 {
		return "", "", "", errors.New("invalid Amazon Client ID")
	}
	var verifierBytes, stateBytes [32]byte
	if _, err := rand.Read(verifierBytes[:]); err != nil {
		return "", "", "", err
	}
	if _, err := rand.Read(stateBytes[:]); err != nil {
		return "", "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes[:])
	state := base64.RawURLEncoding.EncodeToString(stateBytes[:])
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {clientID}, "scope": {"profile:user_id"}, "response_type": {"code"}, "redirect_uri": {lwaCallback}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	return "https://www.amazon.com/ap/oa?" + q.Encode(), verifier, state, nil
}

func lwaRedirectCode(value, expectedState string, now, expires time.Time) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != "suburban.jeremy.ninja" || u.Path != "/auth/lwa/callback" || u.User != nil || u.Fragment != "" || !now.Before(expires) {
		return "", errors.New("invalid or expired return URL")
	}
	q := u.Query()
	if len(q["state"]) != 1 || len(q["code"]) != 1 || q.Get("error") != "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(expectedState)) != 1 {
		return "", errors.New("return URL did not match this sign-in")
	}
	code := q.Get("code")
	if code == "" || len(code) > 4096 {
		return "", errors.New("invalid authorization code")
	}
	return code, nil
}

func proveLWAOwner(client *http.Client, clientID string, secret []byte, output io.Writer, prompt hiddenPrompt) (string, error) {
	authorize, verifier, state, err := newLWARequest(clientID)
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(10 * time.Minute)
	fmt.Fprintln(output, "Open this URL yourself and sign in as the vehicle owner:")
	fmt.Fprintln(output, authorize)
	fmt.Fprintln(output, "The baseline page will say Unrecognized login state. Copy its entire address into the hidden prompt below. That rejection is expected for this local setup flow.")
	returned, err := prompt("Full returned address (hidden; empty cancels): ")
	defer clear(returned)
	if err != nil || len(returned) == 0 {
		return "", errors.New("owner proof cancelled; no values changed")
	}
	code, err := lwaRedirectCode(strings.TrimSpace(string(returned)), state, time.Now(), expires)
	if err != nil {
		return "", err
	}
	owner, err := exchangeLWAOwner(client, clientID, secret, code, verifier)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(output, "Owner proof passed; tokens discarded.")
	return owner, nil
}

func exchangeLWAOwner(client *http.Client, clientID string, secret []byte, code, verifier string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {lwaCallback}}
	if len(secret) > 0 {
		form.Set("client_secret", string(secret))
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.amazon.com/auth/o2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tokens amazonTokens
	if err := lwaJSON(client, req, &tokens); err != nil {
		return "", err
	}
	if tokens.Access == "" || tokens.Expires <= 0 || !strings.EqualFold(tokens.Type, "bearer") {
		return "", errors.New("invalid Amazon token response")
	}
	if len(secret) > 0 && tokens.Refresh == "" {
		return "", errors.New("Amazon did not confirm the confidential client grant; no values changed")
	}
	// Amazon documents tokeninfo as a server-side query. Its URL is never printed,
	// persisted, or included in errors; browser-visible URLs contain no tokens.
	req, _ = http.NewRequest(http.MethodGet, "https://api.amazon.com/auth/o2/tokeninfo?"+url.Values{"access_token": {tokens.Access}}.Encode(), nil)
	var info struct {
		Audience string `json:"aud"`
	}
	if err := lwaJSON(client, req, &info); err != nil {
		return "", err
	}
	if info.Audience != clientID {
		return "", errors.New("Amazon token audience mismatch")
	}
	req, _ = http.NewRequest(http.MethodGet, "https://api.amazon.com/user/profile", nil)
	req.Header.Set("Authorization", "Bearer "+tokens.Access)
	var profile struct {
		UserID string `json:"user_id"`
	}
	if err := lwaJSON(client, req, &profile); err != nil {
		return "", err
	}
	if !strings.HasPrefix(profile.UserID, "amzn1.account.") || len(profile.UserID) > 512 {
		return "", errors.New("invalid Amazon owner ID")
	}
	return profile.UserID, nil
}

func lwaJSON(client *http.Client, req *http.Request, result any) error {
	response, err := client.Do(req)
	if err != nil {
		return errors.New("Amazon verification request failed; no request details were logged")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&failure)
		label := "provider-error"
		switch failure.Code {
		case "invalid_client", "invalid_grant", "unauthorized_client", "invalid_request", "invalid_token", "ServerError":
			label = failure.Code
		}
		return fmt.Errorf("Amazon verification returned HTTP %d (%s); no values changed", response.StatusCode, label)
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(result) != nil {
		return errors.New("invalid Amazon verification response")
	}
	return nil
}

func installLWA(run runCommand, clientID, owner string, secret []byte, output io.Writer) error {
	secrets, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	variables, err := inventory(run, "variable")
	if err != nil {
		return err
	}
	items := []struct{ name, value string }{{"LWA_CLIENT_ID", clientID}, {"OWNER_AMAZON_USER_ID", owner}}
	for _, item := range items {
		if _, exists := variables[item.name]; exists {
			value, e := run([]string{"variable", "get", item.name, "--repo", repository}, nil)
			if e != nil {
				return e
			}
			if strings.TrimSpace(string(value)) != item.value {
				return errors.New("existing " + item.name + " differs; no replacement allowed")
			}
		}
	}
	if _, exists := secrets["LWA_CLIENT_SECRET"]; !exists {
		if len(secret) == 0 {
			return errors.New("LWA_CLIENT_SECRET is missing")
		}
		if _, err := run([]string{"secret", "set", "LWA_CLIENT_SECRET", "--repo", repository}, secret); err != nil {
			return errors.New("LWA client secret write failed")
		}
	}
	for _, item := range items {
		if _, exists := variables[item.name]; !exists {
			if _, err := run([]string{"variable", "set", item.name, "--repo", repository}, []byte(item.value)); err != nil {
				return errors.New("variable write failed; existing credentials preserved")
			}
		}
	}
	secrets, err = inventory(run, "secret")
	if err != nil {
		return err
	}
	variables, err = inventory(run, "variable")
	if err != nil {
		return err
	}
	if secrets["LWA_CLIENT_SECRET"].UpdatedAt == "" {
		return errors.New("LWA secret metadata verification failed")
	}
	for _, item := range items {
		if variables[item.name].UpdatedAt == "" {
			return errors.New("LWA variable metadata verification failed")
		}
	}
	fmt.Fprintln(output, "Shared LWA client secret, client ID, and owner ID installed or preserved; metadata verified. Use this same profile for Alexa console linking.")
	return nil
}
