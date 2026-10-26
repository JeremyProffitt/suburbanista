package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
)

func TestCallbackLocationBinding(t *testing.T) {
	state := strings.Repeat("s", 43)
	callback := onstar.RedirectURI + "?" + url.Values{"state": {state}, "code": {"synthetic-code"}}.Encode()
	got, err := callbackLocation(callback, state)
	if err != nil || got != callback {
		t.Fatal("valid callback rejected")
	}
	for _, bad := range []string{strings.Replace(callback, state, "wrong", 1), callback + "&state=duplicate", callback + "#fragment", strings.Replace(callback, "//auth", "//evil", 1), strings.Replace(callback, "//auth?", "//auth/path?", 1)} {
		if _, err := callbackLocation(bad, state); err == nil {
			t.Fatal("unbound callback accepted")
		}
	}
	if got, err := callbackLocation("https://custlogin.gm.com/intermediate", state); err != nil || got != "" {
		t.Fatal("intermediate navigation captured")
	}
	root := strings.Replace(callback, "//auth?", "//auth/?", 1)
	if got, err := callbackLocation(root, state); err != nil || got != root {
		t.Fatal("literal callback root slash rejected")
	}
	for _, path := range []string{"/other", "//", "/./", "/%2e", "/%2F", "/.."} {
		if _, err := callbackLocation(strings.Replace(callback, "//auth?", "//auth"+path+"?", 1), state); err == nil {
			t.Fatal("non-root callback path accepted")
		}
	}
	if _, err := callbackLocation(root, "wrong"); err == nil {
		t.Fatal("root slash bypassed state validation")
	}
}

func TestBrowserDiagnosticsDisabled(t *testing.T) {
	for _, name := range []string{"DEBUGP", "DEBUG", "PWDEBUG", "NODE_OPTIONS", "NODE_DEBUG", "NODE_DEBUG_NATIVE", "SSLKEYLOGFILE", "CHROME_LOG_FILE"} {
		t.Setenv(name, "synthetic-debug-enabled")
	}
	options, err := privateBrowserOptions()
	if err != nil || options.Verbose || options.Stdout == nil || options.Stderr == nil || options.Logger == nil {
		t.Fatal("private options missing")
	}
	for _, name := range []string{"DEBUGP", "DEBUG", "PWDEBUG", "NODE_OPTIONS", "NODE_DEBUG", "NODE_DEBUG_NATIVE", "SSLKEYLOGFILE", "CHROME_LOG_FILE"} {
		if os.Getenv(name) != "" {
			t.Fatal("diagnostic environment retained")
		}
	}
}

func TestCallbackFailureReasonsNeverExposeValues(t *testing.T) {
	state := strings.Repeat("s", 43)
	for _, tc := range []struct {
		query  url.Values
		reason string
	}{
		{url.Values{"state": {"wrong-private-state"}, "code": {"synthetic-code"}}, "state-mismatch"},
		{url.Values{"state": {state}, "code": {strings.Repeat("x", 4097)}}, "code-exceeds-size-limit"},
		{url.Values{"state": {state}, "error": {"invalid_request"}, "error_description": {"AADB2C90006 synthetic-private-account"}}, "invalid_request, AADB2C90006"},
		{url.Values{"state": {state}, "error": {"synthetic-private-error"}, "error_description": {"synthetic-private-account"}}, "provider-error"},
	} {
		got, err := callbackLocation(onstar.RedirectURI+"?"+tc.query.Encode(), state)
		if got != "" || err == nil || !strings.Contains(err.Error(), tc.reason) {
			t.Fatal("safe reason missing")
		}
		for _, private := range []string{state, "synthetic-code", "wrong-private-state", "synthetic-private-account", "synthetic-private-error"} {
			if strings.Contains(err.Error(), private) {
				t.Fatal("private callback value escaped")
			}
		}
	}
}

// Opt-in browser proof uses only localhost and invented values. Normal CI does
// not install browsers or reach GM. No screenshot, trace or HAR is produced.
func TestPlaywrightCallbackCapture(t *testing.T) {
	if os.Getenv("ONSTAR_BROWSER_TEST") != "1" {
		t.Skip("set ONSTAR_BROWSER_TEST=1 after installing pinned browser")
	}
	for _, scenario := range []string{"redirect", "root-redirect", "wrong-state", "denied", "html-denied", "timeout", "closed"} {
		t.Run(scenario, func(t *testing.T) {
			state := strings.Repeat("s", 43)
			callbackState := state
			if scenario == "wrong-state" {
				callbackState = "wrong"
			}
			callback := onstar.RedirectURI + "?" + url.Values{"state": {callbackState}, "code": {"synthetic-code"}}.Encode()
			if scenario == "root-redirect" {
				callback = strings.Replace(callback, "//auth?", "//auth/?", 1)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "redirect", "root-redirect", "wrong-state":
					w.Header().Set("Location", callback)
					w.WriteHeader(302)
				case "denied":
					w.WriteHeader(403)
				case "html-denied":
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte("<title>Access Denied</title>"))
				default:
					_, _ = w.Write([]byte("synthetic sign-in page"))
				}
			}))
			defer server.Close()
			b, err := openEnrollmentBrowser(true)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			limit := 5 * time.Second
			if scenario == "timeout" {
				limit = 500 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			if scenario == "closed" {
				go func() { time.Sleep(200 * time.Millisecond); _ = b.page.Close() }()
			}
			got, err := b.Capture(ctx, server.URL+"/start?"+url.Values{"state": {state}}.Encode(), server.URL)
			if scenario == "redirect" || scenario == "root-redirect" {
				if err != nil || string(got) != callback {
					t.Fatal("redirect not captured privately", err)
				}
				return
			}
			if err == nil || len(got) != 0 {
				t.Fatal("failed flow accepted")
			}
			if strings.Contains(err.Error(), "synthetic-code") || strings.Contains(err.Error(), state) {
				t.Fatal("private callback leaked in error")
			}
			if scenario == "denied" || scenario == "html-denied" {
				var failure *onstar.Error
				if !errors.As(err, &failure) || !failure.AuthEndpoint || failure.Kind != "access-denied" {
					t.Fatal("denial not passed to cooldown guard")
				}
			}
		})
	}
}
