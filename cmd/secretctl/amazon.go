package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const askVersion = "2.30.7"
const askScopes = "alexa::ask:skills:readwrite alexa::ask:models:readwrite alexa::ask:skills:test"
const askRedirect = "https://ask-cli-static-content.s3-us-west-2.amazonaws.com/html/ask-cli-no-browser.html"

type amazonTokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Type    string `json:"token_type"`
	Expires int    `json:"expires_in"`
}
type amazonVendor struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}
type hiddenPrompt func(string) ([]byte, error)

func runAmazon(args []string, output io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: secretctl amazon")
	}
	secrets, err := inventory(runGH, "secret")
	if err != nil {
		return err
	}
	if _, exists := secrets["ASK_REFRESH_TOKEN"]; exists {
		return errors.New("ASK_REFRESH_TOKEN already exists; preserved without a new authorization")
	}
	variables, err := inventory(runGH, "variable")
	if err != nil {
		return err
	}
	if _, exists := variables["ASK_VENDOR_ID"]; exists {
		return errors.New("ASK_VENDOR_ID already exists; preserve it and complete attended recovery before a new authorization")
	}
	cli, err := findASK()
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "Repository: "+repository)
	fmt.Fprintln(output, "Privacy: https://suburban.jeremy.ninja/privacy")
	fmt.Fprintln(output, "Website: https://suburban.jeremy.ninja")
	fmt.Fprintln(output, "Web/mobile return URL: https://suburban.jeremy.ninja/auth/lwa/callback")
	fmt.Fprintln(output, "This authorizes ASK management only. Amazon login and consent stay in your browser.")
	prompt := func(message string) ([]byte, error) {
		fmt.Fprint(output, message)
		value, err := readAmazonInput()
		fmt.Fprintln(output)
		if err == nil {
			fmt.Fprintf(output, "Input length: %d characters. This confirms nonempty entry, not provider validity.\n", len(value))
		}
		return value, err
	}
	tokens, err := captureASK(cli, output, prompt)
	if err != nil {
		return err
	}
	defer func() { tokens.Access = ""; tokens.Refresh = "" }()
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	vendors, err := fetchVendors(client, tokens.Access)
	if err != nil {
		return err
	}
	vendor, err := selectVendor(vendors, output, prompt)
	if err != nil {
		return err
	}
	return installAmazon(runGH, tokens.Refresh, vendor, output)
}

func findASK() (string, error) {
	entry, err := exec.LookPath("ask")
	if err != nil {
		return "", errors.New("install ASK CLI 2.30.7 before authorization")
	}
	root := filepath.Join(filepath.Dir(entry), "node_modules", "ask-cli")
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", errors.New("cannot locate installed ASK package metadata")
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Version != askVersion {
		return "", errors.New("ASK CLI must be exactly version 2.30.7")
	}
	cli := filepath.Join(root, "dist", "bin", "ask-util.js")
	if _, err := os.Stat(cli); err != nil {
		return "", errors.New("ASK CLI util entrypoint unavailable")
	}
	return cli, nil
}

// The installed ASK command reads AppConfig even for token generation. Isolate its
// non-secret empty configuration; never access the owner's real ASK profile.
func askEnvironment(home string) []string {
	var env []string
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ASK_") || strings.HasPrefix(upper, "AWS_") || strings.HasPrefix(upper, "NODE_") || upper == "HOME" || upper == "USERPROFILE" || upper == "FORCE_COLOR" {
			continue
		}
		env = append(env, value)
	}
	return append(env, "HOME="+home, "USERPROFILE="+home, "ASK_SHARE_USAGE=false", "ASK_SKIP_NEW_VERSION_REMINDER=true", "FORCE_COLOR=0")
}

type askOutput struct {
	mu        sync.Mutex
	data      []byte
	url       chan string
	published bool
}

var authURLPattern = regexp.MustCompile(`https://www\.amazon\.com/ap/oa\?[^\s]+`)

func (a *askOutput) Write(data []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.data)+len(data) > 1024*1024 {
		return 0, errors.New("ASK output exceeds limit")
	}
	a.data = append(a.data, data...)
	// Publish only complete lines: a pipe read can split a URL at any byte.
	if !a.published {
		if end := bytes.LastIndexByte(a.data, '\n'); end >= 0 {
			candidate := authURLPattern.Find(a.data[:end])
			if len(candidate) > 0 {
				value := string(candidate)
				if validAuthorizeURL(value) {
					a.url <- value
					a.published = true
				}
			}
		}
	}
	return len(data), nil
}
func validAuthorizeURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != "www.amazon.com" || u.Path != "/ap/oa" || u.User != nil || u.Fragment != "" {
		return false
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("redirect_uri") != askRedirect || q.Get("scope") != askScopes || q.Get("state") == "" || q.Get("client_id") == "" {
		return false
	}
	allowed := map[string]bool{"response_type": true, "redirect_uri": true, "scope": true, "state": true, "client_id": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return true
}
func parseASKTokens(data []byte) (amazonTokens, error) {
	var result amazonTokens
	marker := []byte("The LWA tokens result:")
	index := bytes.LastIndex(data, marker)
	if index < 0 {
		return result, errors.New("ASK did not return a token result")
	}
	part := bytes.TrimSpace(data[index+len(marker):])
	decoder := json.NewDecoder(bytes.NewReader(part))
	if decoder.Decode(&result) != nil || result.Access == "" || result.Refresh == "" || !strings.EqualFold(result.Type, "bearer") || result.Expires <= 0 {
		return amazonTokens{}, errors.New("ASK returned an invalid token result")
	}
	return result, nil
}

func captureASK(cli string, output io.Writer, prompt hiddenPrompt) (amazonTokens, error) {
	var empty amazonTokens
	home, err := os.MkdirTemp("", "suburban-ask-")
	if err != nil {
		return empty, errors.New("cannot create isolated ASK configuration")
	}
	// MkdirTemp returns a child of the OS temp directory. Verify the boundary before deletion.
	rel, err := filepath.Rel(os.TempDir(), home)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return empty, errors.New("invalid temporary configuration boundary")
	}
	defer os.RemoveAll(home)
	if err = os.Mkdir(filepath.Join(home, ".ask"), 0700); err != nil {
		return empty, errors.New("cannot create ASK configuration directory")
	}
	if err = os.WriteFile(filepath.Join(home, ".ask", "cli_config"), []byte(`{"profiles":{},"share_usage":false}`), 0600); err != nil {
		return empty, errors.New("cannot initialize ASK configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", cli, "generate-lwa-tokens", "--no-browser", "--scopes", askScopes)
	hideProcess(cmd)
	cmd.Env = askEnvironment(home)
	capture := &askOutput{url: make(chan string, 1)}
	defer func() { capture.mu.Lock(); defer capture.mu.Unlock(); clear(capture.data) }()
	cmd.Stdout = capture
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		return empty, errors.New("cannot connect ASK input")
	}
	defer input.Close()
	if err = cmd.Start(); err != nil {
		return empty, errors.New("cannot start ASK CLI")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var authorizeURL string
	select {
	case authorizeURL = <-capture.url:
	case <-done:
		return empty, errors.New("ASK stopped before authorization; no raw output was shown")
	case <-ctx.Done():
		<-done
		return empty, errors.New("ASK authorization timed out")
	}
	fmt.Fprintln(output, "Open this URL yourself in your browser, sign in, and allow the requested access:")
	fmt.Fprintln(output, authorizeURL)
	code, err := prompt("Paste the returned authorization code (hidden; empty cancels): ")
	if err != nil || len(bytes.TrimSpace(code)) == 0 {
		clear(code)
		cancel()
		<-done
		return empty, errors.New("authorization cancelled; no GitHub write")
	}
	code = bytes.TrimSpace(code)
	if bytes.ContainsAny(code, "\r\n") {
		clear(code)
		cancel()
		<-done
		return empty, errors.New("authorization code must be one line")
	}
	_, err = input.Write(append(code, '\n'))
	clear(code)
	if err != nil {
		cancel()
		<-done
		return empty, errors.New("cannot deliver authorization code")
	}
	if err = <-done; err != nil {
		return empty, errors.New("Amazon authorization failed; no raw output was shown")
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return parseASKTokens(capture.data)
}

// Vendor Management v1 accepts the already-issued access token. No ASK client
// credentials are extracted, and no token refresh occurs outside ASK CLI.
func fetchVendors(client *http.Client, access string) ([]amazonVendor, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.amazonalexa.com/v1/vendors", nil)
	if err != nil {
		return nil, errors.New("cannot prepare vendor lookup")
	}
	req.Header.Set("Authorization", "Bearer "+access)
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Amazon vendor lookup failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Amazon vendor lookup returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Vendors []amazonVendor `json:"vendors"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&result) != nil {
		return nil, errors.New("invalid vendor response")
	}
	return result.Vendors, nil
}
func selectVendor(vendors []amazonVendor, output io.Writer, prompt hiddenPrompt) (string, error) {
	var eligible []amazonVendor
	for _, v := range vendors {
		for _, role := range v.Roles {
			if role == "ROLE_ADMINISTRATOR" || role == "ROLE_DEVELOPER" {
				if v.ID != "" {
					eligible = append(eligible, v)
				}
				break
			}
		}
	}
	if len(eligible) == 0 {
		return "", errors.New("Amazon returned no authorized developer vendor")
	}
	if len(eligible) == 1 {
		fmt.Fprintf(output, "Amazon vendor: %s (%s)\n", eligible[0].Name, eligible[0].ID)
		return eligible[0].ID, nil
	}
	for _, v := range eligible {
		fmt.Fprintf(output, "Amazon vendor: %s (%s)\n", v.Name, v.ID)
	}
	choice, err := prompt("Enter the exact vendor ID to use (empty cancels): ")
	defer clear(choice)
	if err != nil {
		return "", errors.New("vendor selection cancelled")
	}
	for _, v := range eligible {
		if string(bytes.TrimSpace(choice)) == v.ID {
			return v.ID, nil
		}
	}
	return "", errors.New("vendor selection did not match; no GitHub write")
}
func installAmazon(run runCommand, refresh, vendor string, output io.Writer) error {
	// Recheck immediately before mutation, preserving values added during consent.
	secrets, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	variables, err := inventory(run, "variable")
	if err != nil {
		return err
	}
	if _, exists := secrets["ASK_REFRESH_TOKEN"]; exists {
		return errors.New("ASK_REFRESH_TOKEN appeared during consent; preserved")
	}
	if _, exists := variables["ASK_VENDOR_ID"]; exists {
		return errors.New("ASK_VENDOR_ID appeared during consent; preserved")
	}
	value := []byte(refresh)
	_, err = run([]string{"secret", "set", "ASK_REFRESH_TOKEN", "--repo", repository}, value)
	clear(value)
	if err != nil {
		return errors.New("ASK_REFRESH_TOKEN write failed; no value was logged")
	}
	if _, err = run([]string{"variable", "set", "ASK_VENDOR_ID", "--repo", repository}, []byte(vendor)); err != nil {
		return errors.New("ASK refresh token installed; vendor variable write failed; preserve the token and set ASK_VENDOR_ID from the displayed vendor")
	}
	secrets, err = inventory(run, "secret")
	if err != nil {
		return err
	}
	variables, err = inventory(run, "variable")
	if err != nil {
		return err
	}
	if secrets["ASK_REFRESH_TOKEN"].UpdatedAt == "" || variables["ASK_VENDOR_ID"].UpdatedAt == "" {
		return errors.New("Amazon credential metadata verification failed")
	}
	fmt.Fprintln(output, "ASK_REFRESH_TOKEN and ASK_VENDOR_ID installed; metadata verified. No app login or Alexa setup completion is implied.")
	return nil
}
