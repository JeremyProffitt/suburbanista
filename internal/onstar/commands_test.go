package onstar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	synthetic "github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
)

func linkedToken() GMToken {
	return GMToken{AccessToken: "synthetic-command-token", ExpiresAt: 1800003600, VehicleLinked: true, VIN: synthetic.VIN}
}
func commandFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/command-" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCommandWireBodiesAndSingleSubmission(t *testing.T) {
	for _, tc := range []struct {
		command    Command
		path, body string
	}{
		{Start, "start", `{}`}, {CancelStart, "cancelStart", `{}`},
		{LockDoor, "lock", `{"lockDoorRequest":{"delay":0}}`}, {UnlockDoor, "unlock", `{"unlockDoorRequest":{"delay":0}}`},
		{LockTrunk, "lockTrunk", `{"lockTrunkRequest":{"delay":0}}`}, {UnlockTrunk, "unlockTrunk", `{"unlockTrunkRequest":{"delay":0}}`},
		{Alert, "alert", `{"action":["Honk","Flash"],"delay":0,"duration":1}`}, {FlashLights, "alert", `{"action":["Flash"],"delay":0,"duration":1}`},
		{CancelAlert, "cancelAlert", `{}`}, {StopLights, "cancelAlert", `{}`},
	} {
		t.Run(string(tc.command), func(t *testing.T) {
			calls := 0
			c := fakeClient(func(req *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(req.Body)
				var got, want any
				if json.Unmarshal(body, &got) != nil || json.Unmarshal([]byte(tc.body), &want) != nil || !reflect.DeepEqual(got, want) {
					t.Fatal("source-derived command body differs")
				}
				if req.Method != http.MethodPost || req.URL.String() != APIBase+"/veh/cmd/v3/"+tc.path+"/"+synthetic.VIN {
					t.Fatal("wrong fixed-v3 endpoint")
				}
				if req.Header.Get("Content-Type") != "application/json; charset=UTF-8" || req.Header.Get("Authorization") != "Bearer "+"synthetic-command-token" {
					t.Fatal("wrong command headers")
				}
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) > 15*time.Second {
					t.Fatal("missing command deadline")
				}
				if strings.Contains(string(body), "override") {
					t.Fatal("safety override included")
				}
				return response(string(commandFixture(t, "pending")), 200), nil
			})
			result, err := c.Submit(context.Background(), linkedToken(), synthetic.VIN, tc.command, CommandOptions{})
			if err != nil || result.State != "submitted" || calls != 1 {
				t.Fatal("acceptance mapped incorrectly")
			}
		})
	}
}

func TestCommandFailuresNeverFallbackOrRetry(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500} {
		calls := 0
		c := fakeClient(func(*http.Request) (*http.Response, error) {
			calls++
			return response(`{"requestId":"synthetic-request","message":"synthetic-private-detail"}`, status), nil
		})
		result, err := c.Submit(context.Background(), linkedToken(), synthetic.VIN, Start, CommandOptions{})
		want := "failed"
		if status == 500 {
			want = "outcome-unknown"
		}
		if err == nil || calls != 1 || result.State != want || strings.Contains(err.Error(), "private-detail") {
			t.Fatal("failure retried, leaked or misclassified")
		}
	}
	c := fakeClient(func(*http.Request) (*http.Response, error) { return response(`{}`, 200), nil })
	result, err := c.Submit(context.Background(), linkedToken(), synthetic.VIN, Start, CommandOptions{})
	if err == nil || result.State != "outcome-unknown" {
		t.Fatal("HTTP200 without terminal evidence reported success")
	}
}

func TestCommandResultFixtures(t *testing.T) {
	for _, tc := range []struct{ name, state string }{{"pending", "submitted"}, {"complete", "succeeded"}, {"failed", "failed"}} {
		result, err := parseCommandResult(commandFixture(t, tc.name))
		if err != nil || result.State != tc.state || strings.Contains(result.Reason, "private") {
			t.Fatal("wrong result mapping")
		}
	}
	for _, body := range []string{`{}`, `{"status":"new-unknown-state"}`, `{"status":"IN_PROGRESS"}`, `{"status":"success","url":"https://evil.invalid/status"}`, `{"status":"success","commandResponse":{"status":"success"}}`, `{"commandResponse":{"commandResponse":{"status":"success"}}}`} {
		result, err := parseCommandResult([]byte(body))
		if err == nil || result.State == "succeeded" {
			t.Fatal("invalid provider evidence accepted")
		}
	}
}

func TestCommandDiagnosticReasonMatchesParseFailure(t *testing.T) {
	for _, test := range []struct{ body, reason string }{
		{`{"requestId":"synthetic-request","requestTime":"2026-10-24T01:00:00Z","status":"IN_PROGRESS","url":"https://untrusted.invalid/status"}`, "invalid-status-url"},
		{`{"requestId":"synthetic-request","requestTime":"invalid","status":"IN_PROGRESS","url":"/status"}`, "invalid-command-time"},
		{`{"requestId":"synthetic-request","requestTime":"2026-10-24T01:00:00Z","status":"synthetic-unknown"}`, "unknown-command-status"},
	} {
		result, err := parseCommandResult([]byte(test.body))
		if err == nil || result.State != "outcome-unknown" || result.Reason != test.reason {
			t.Errorf("safe reason=%q want=%q", result.Reason, test.reason)
		}
	}
}

func TestCommandDiagnosticURLClassesDoNotChangeAcceptance(t *testing.T) {
	for _, tc := range []struct{ url, class string }{{"", "missing"}, {"http://na-mobile-api.gm.com/status", "scheme"}, {"https://other.invalid/status", "host"}, {"https://na-mobile-api.gm.com:443/status", "valid"}, {"https://na-mobile-api.gm.com:8443/status", "port"}, {"https://synthetic-private@na-mobile-api.gm.com/status", "userinfo"}, {"/status#synthetic-private", "fragment"}, {"/", "root"}, {"%zz", "invalid"}, {"/status", "valid"}} {
		_, class, err := commandStatusReference(tc.url)
		if class != tc.class || (err == nil) != (tc.class == "valid") {
			t.Fatal("URL classification changed validation")
		}
	}
	result, err := parseCommandResult([]byte(`{"requestId":"synthetic-request","requestTime":"2026-10-24T01:00:00Z","status":"synthetic-private-status","url":"https://synthetic-private-host.invalid/private"}`))
	if err == nil || result.Reason != "invalid-status-url" || result.URLClass != "host" || result.ProviderStatusClass != "unknown" {
		t.Fatal("safe diagnostic classes not retained")
	}
}

func TestCommandStatusAcceptsOnlyDefaultHTTPSPort(t *testing.T) {
	const reference = "https://na-mobile-api.gm.com:443/status/synthetic-request?x=1&x=2&encoded=a%2Fb"
	const canonical = "https://na-mobile-api.gm.com/status/synthetic-request?x=1&x=2&encoded=a%2Fb"
	body, _ := json.Marshal(map[string]string{"requestId": "synthetic-request", "requestTime": "2026-10-24T01:00:00Z", "status": "IN_PROGRESS", "url": reference})
	posts, gets := 0, 0
	c := fakeClient(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			posts++
			return response(string(body), 200), nil
		}
		gets++
		if req.URL.String() != canonical {
			t.Fatal("canonical status path or query changed")
		}
		return response(string(commandFixture(t, "complete")), 200), nil
	})
	submitted, err := c.Submit(context.Background(), linkedToken(), synthetic.VIN, LockDoor, CommandOptions{})
	if err != nil || submitted.State != "submitted" || submitted.StatusURL != canonical {
		t.Fatalf("default HTTPS port rejected: state=%s reason=%s", submitted.State, submitted.Reason)
	}
	completed, err := c.CommandStatus(context.Background(), linkedToken(), synthetic.VIN, submitted.RequestID, submitted.StatusURL)
	if err != nil || completed.State != "succeeded" || posts != 1 || gets != 1 {
		t.Fatal("canonical status polling failed")
	}
	for _, bad := range []string{"https://na-mobile-api.gm.com:8443/status", "https://na-mobile-api.gm.com:80/status", "http://na-mobile-api.gm.com:443/status", "https://other.invalid:443/status", "https://private@na-mobile-api.gm.com:443/status", "https://na-mobile-api.gm.com:443/status#fragment"} {
		if _, err := commandStatusURL(bad); err == nil {
			t.Fatal("non-equivalent status origin accepted")
		}
	}
}

func TestCommandStatusRejectsUntrustedReferencesAndMismatches(t *testing.T) {
	calls := 0
	c := fakeClient(func(*http.Request) (*http.Response, error) {
		calls++
		return response(string(commandFixture(t, "complete")), 200), nil
	})
	for _, endpoint := range []string{"https://evil.invalid/status", "http://na-mobile-api.gm.com/status", "https://na-mobile-api.gm.com@evil.invalid/status", "//evil.invalid/status", "https://na-mobile-api.gm.com/status#fragment"} {
		if _, err := c.CommandStatus(context.Background(), linkedToken(), synthetic.VIN, "synthetic-request", endpoint); err == nil {
			t.Fatal("untrusted poll URL accepted")
		}
	}
	if calls != 0 {
		t.Fatal("untrusted URL made a request")
	}
	if result, err := c.CommandStatus(context.Background(), linkedToken(), synthetic.VIN, "different-request", APIBase+"/status"); err == nil || result.State != "outcome-unknown" {
		t.Fatal("wrong operation status accepted")
	}
}

func TestCommandOptionsFailBeforeSend(t *testing.T) {
	c := fakeClient(func(*http.Request) (*http.Response, error) { t.Fatal("invalid command sent"); return nil, nil })
	minus := -1
	temperature := 21
	for _, tc := range []struct {
		command Command
		options CommandOptions
	}{{"unknown", CommandOptions{}}, {LockDoor, CommandOptions{CabinTemperature: &temperature}}, {Alert, CommandOptions{Delay: &minus}}, {Alert, CommandOptions{Actions: []string{"DoorOpen"}}}, {Start, CommandOptions{Duration: &minus}}} {
		if _, err := c.Submit(context.Background(), linkedToken(), synthetic.VIN, tc.command, tc.options); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	if _, err := c.Submit(context.Background(), linkedToken(), synthetic.OtherVIN, Start, CommandOptions{}); err == nil {
		t.Fatal("wrong vehicle allowed")
	}
}

func TestCommandHTTPFixtureReplay(t *testing.T) {
	posts, gets := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			posts++
			_, _ = w.Write(commandFixture(t, "pending"))
		case http.MethodGet:
			gets++
			_, _ = w.Write(commandFixture(t, "complete"))
		default:
			t.Error("unexpected request method")
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	// Only this test transport rewrites the fixed provider origin to localhost.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	c := fakeClient(func(req *http.Request) (*http.Response, error) {
		copy := req.Clone(req.Context())
		u := *req.URL
		u.Scheme = target.Scheme
		u.Host = target.Host
		copy.URL = &u
		return transport.RoundTrip(copy)
	})
	submitted, err := c.Submit(context.Background(), linkedToken(), synthetic.VIN, LockDoor, CommandOptions{})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := c.CommandStatus(context.Background(), linkedToken(), synthetic.VIN, submitted.RequestID, submitted.StatusURL)
	if err != nil || completed.State != "succeeded" || posts != 1 || gets != 1 {
		t.Fatal("fixture replay failed")
	}
}
