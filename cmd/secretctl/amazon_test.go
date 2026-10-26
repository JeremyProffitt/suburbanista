package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func syntheticAuthorizeURL() string {
	q := url.Values{"response_type": {"code"}, "redirect_uri": {askRedirect}, "scope": {askScopes}, "state": {"synthetic-state"}, "client_id": {"synthetic-client"}}
	return "https://www.amazon.com/ap/oa?" + q.Encode()
}
func TestASKOutputCapture(t *testing.T) {
	capture := &askOutput{url: make(chan string, 1)}
	u := syntheticAuthorizeURL()
	// Pinned ASK2.30.7 emits this prefix and pretty-printed token JSON.
	for _, piece := range []string{"Paste the following url to your browser:\n    ", u[:40], u[40:], "\n"} {
		if _, err := capture.Write([]byte(piece)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case got := <-capture.url:
		if got != u {
			t.Fatal("wrong URL")
		}
	default:
		t.Fatal("complete URL not emitted")
	}
	fake := amazonTokens{Access: "synthetic-access", Refresh: "synthetic-refresh", Type: "bearer", Expires: 3600}
	data, _ := json.MarshalIndent(fake, "", "  ")
	capture.Write(append([]byte("\x1b[2KPlease enter the Authorization Code: synthetic-code\nThe LWA tokens result:\n"), data...))
	got, err := parseASKTokens(capture.data)
	if err != nil || got != fake {
		t.Fatal("token result not captured")
	}
	for _, bad := range []string{"unexpected output", "The LWA tokens result: {}", "The LWA tokens result: {broken"} {
		if _, err := parseASKTokens([]byte(bad)); err == nil {
			t.Fatal("malformed token result accepted")
		}
	}
	for _, bad := range []string{strings.Replace(u, "www.amazon.com", "evil.invalid", 1), u + "&access_token=private", strings.Replace(u, "response_type=code", "response_type=token", 1), u + "&state=other"} {
		if validAuthorizeURL(bad) {
			t.Fatal("untrusted authorization URL accepted")
		}
	}
}
func TestASKEnvironmentIsolation(t *testing.T) {
	t.Setenv("ASK_LWA_TOKEN_HOST", "https://untrusted.invalid")
	t.Setenv("NODE_OPTIONS", "--require unsafe")
	t.Setenv("ASK_REFRESH_TOKEN", "synthetic-private")
	t.Setenv("AWS_ACCESS_KEY_ID", "synthetic-private")
	env := askEnvironment("isolated-profile")
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"untrusted.invalid", "NODE_OPTIONS=", "ASK_REFRESH_TOKEN=", "AWS_ACCESS_KEY_ID="} {
		if strings.Contains(joined, forbidden) {
			t.Fatal("inherited sensitive override")
		}
	}
	for _, required := range []string{"HOME=isolated-profile", "USERPROFILE=isolated-profile", "ASK_SHARE_USAGE=false", "ASK_SKIP_NEW_VERSION_REMINDER=true"} {
		if !strings.Contains(joined, required) {
			t.Fatal("isolation setting absent")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAmazonVendorLookup(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.amazonalexa.com/v1/vendors" || r.Header.Get("Authorization") != "Bearer "+"synthetic-access" {
			t.Fatal("wrong vendor request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"vendors":[{"id":"vendor-one","name":"Synthetic Vendor","roles":["ROLE_DEVELOPER"]}]}`)), Header: http.Header{}}, nil
	})}
	vendors, err := fetchVendors(client, "synthetic-access")
	if err != nil || len(vendors) != 1 {
		t.Fatal("vendor decode failed")
	}
	var output bytes.Buffer
	selected, err := selectVendor(vendors, &output, func(string) ([]byte, error) { t.Fatal("single vendor prompted"); return nil, nil })
	if err != nil || selected != "vendor-one" {
		t.Fatal("single vendor not selected")
	}
	vendors = append(vendors, amazonVendor{ID: "vendor-two", Name: "Second", Roles: []string{"ROLE_ADMINISTRATOR"}})
	selected, err = selectVendor(vendors, &output, func(string) ([]byte, error) { return []byte("vendor-two"), nil })
	if err != nil || selected != "vendor-two" {
		t.Fatal("explicit vendor selection failed")
	}
	if _, err = selectVendor(vendors, &output, func(string) ([]byte, error) { return []byte("wrong"), nil }); err == nil {
		t.Fatal("guessed vendor accepted")
	}
	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret-bearing-error") })
	_, err = fetchVendors(client, "synthetic-access")
	if err == nil || strings.Contains(err.Error(), "secret-bearing-error") {
		t.Fatal("unsafe HTTP error")
	}
}
func TestAmazonInstallStdin(t *testing.T) {
	values := map[string]map[string]string{"secret": {}, "variable": {}}
	run := func(args []string, input []byte) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "synthetic-private") {
			t.Fatal("secret in argv")
		}
		if args[1] == "list" {
			var rows []metadata
			for name := range values[args[0]] {
				rows = append(rows, metadata{Name: name, UpdatedAt: "2026-10-22T00:00:00Z"})
			}
			return json.Marshal(rows)
		}
		values[args[0]][args[2]] = string(input)
		return nil, nil
	}
	var output bytes.Buffer
	if err := installAmazon(run, "synthetic-private", "vendor-one", &output); err != nil {
		t.Fatal(err)
	}
	if values["secret"]["ASK_REFRESH_TOKEN"] != "synthetic-private" || values["variable"]["ASK_VENDOR_ID"] != "vendor-one" || strings.Contains(output.String(), "synthetic-private") {
		t.Fatal("installation contract violated")
	}
	if err := installAmazon(run, "replacement", "vendor-two", &output); err == nil {
		t.Fatal("existing credentials overwritten")
	}
}
