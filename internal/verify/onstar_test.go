package verify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/worker"
)

func syntheticInvocation() (worker.Result, string) {
	r := worker.Result{Status: "passed", RunID: "synthetic-run", RequestID: "11111111-2222-3333-4444-555555555555", LogStream: "2026/10/23/[$LATEST]aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DeployNonce: strings.Repeat("a", 40), Version: strings.Repeat("a", 40), ColdStart: true}
	r.EnrollmentVersion = 1
	r.TokenVersionBefore = 1
	r.TokenVersionAfter = 4
	for _, name := range onStarProviderChecks {
		r.Checks = append(r.Checks, worker.Check{Name: name, Passed: true, Milliseconds: 1})
	}
	return r, "START RequestId: " + r.RequestID + " Version: $LATEST\nREPORT RequestId: " + r.RequestID + "\tDuration: 300.25 ms\tBilled Duration: 400 ms\tInit Duration: 80.15 ms\n"
}
func TestOnStarInvocationColdProof(t *testing.T) {
	for _, stream := range []string{"2026/10/23/[$LATEST]aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "2026/10/23/suburban-remote-WorkerFunction-synthetic[$LATEST]aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		r, logs := syntheticInvocation()
		r.LogStream = stream
		payload, _ := json.Marshal(r)
		report := OnStarInvocation(payload, []byte(logs), true, r.Version, r.RunID, r.DeployNonce, time.Unix(1000, 0))
		if e := report.Validate(OnStarRequired); e != nil {
			t.Fatal(e)
		}
	}
}
func TestOnStarInvocationRejectsUnprovedResults(t *testing.T) {
	cases := []struct {
		name  string
		alter func(*worker.Result, *string)
	}{
		{"warm", func(r *worker.Result, l *string) { r.ColdStart = false }},
		{"missing-report", func(r *worker.Result, l *string) { *l = "synthetic init marker only" }},
		{"warm-report", func(r *worker.Result, l *string) { *l = strings.ReplaceAll(*l, "\tInit Duration: 80.15 ms", "") }},
		{"different-request", func(r *worker.Result, l *string) {
			*l = strings.ReplaceAll(*l, r.RequestID, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
		}},
		{"duplicate-report", func(r *worker.Result, l *string) { *l += *l }},
		{"version-mismatch", func(r *worker.Result, l *string) { r.Version = strings.Repeat("b", 40) }},
		{"nonce-mismatch", func(r *worker.Result, l *string) { r.DeployNonce = "different" }},
		{"false-marker", func(r *worker.Result, l *string) { r.Checks[0].Passed = false }},
		{"duplicate-marker", func(r *worker.Result, l *string) { r.Checks = append(r.Checks, r.Checks[0]) }},
		{"failed-status", func(r *worker.Result, l *string) { r.Status = "blocked" }},
		{"unsafe-stream", func(r *worker.Result, l *string) { r.LogStream = "synthetic-secret-value" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, logs := syntheticInvocation()
			tc.alter(&r, &logs)
			payload, _ := json.Marshal(r)
			report := OnStarInvocation(payload, []byte(logs), true, strings.Repeat("a", 40), "synthetic-run", strings.Repeat("a", 40), time.Unix(1000, 0))
			if report.Validate(OnStarRequired) == nil {
				t.Fatal("unproved response accepted")
			}
		})
	}
}
func TestOnStarInvocationDoesNotRetainRawTransport(t *testing.T) {
	r, logs := syntheticInvocation()
	r.Reason = "synthetic-private-response"
	logs += "synthetic-private-log\n"
	payload, _ := json.Marshal(r)
	report := OnStarInvocation(payload, []byte(logs), false, r.Version, r.RunID, r.DeployNonce, time.Unix(1000, 0))
	encoded, _ := json.Marshal(report)
	for _, value := range []string{r.Reason, "synthetic-private-log", "START RequestId:"} {
		if strings.Contains(string(encoded), value) {
			t.Fatal("raw transport escaped")
		}
	}
	if RedactCheck(encoded, nil) != nil {
		t.Fatal("safe report failed privacy check")
	}
	if report.Validate(OnStarRequired) == nil {
		t.Fatal("failed invocation accepted")
	}
}

func TestOnStarInvocationFailureReasonAllowlist(t *testing.T) {
	for _, reason := range []string{"credential-load-failed", "provider-cooldown", "synthetic-private-response"} {
		r, logs := syntheticInvocation()
		r.Status = "blocked"
		r.Reason = reason
		payload, _ := json.Marshal(r)
		report := OnStarInvocation(payload, []byte(logs), true, r.Version, r.RunID, r.DeployNonce, time.Unix(1000, 0))
		for _, check := range report.Checks {
			if check.Name != "provider-proof-status" {
				continue
			}
			want := "blocked; reason=" + reason
			if reason == "synthetic-private-response" {
				want = "blocked; reason=unrecognized"
			}
			if check.Actual != want || check.Status != "fail" {
				t.Fatal("failure diagnosis is not safely classified")
			}
		}
	}
}

func TestOnStarPairRequiresDistinctColdStartsAndPersistedVersion(t *testing.T) {
	r, logs := syntheticInvocation()
	payload, _ := json.Marshal(r)
	first := OnStarInvocation(payload, []byte(logs), true, r.Version, r.RunID, r.DeployNonce, time.Unix(1000, 0))
	r.RunID = "synthetic-second"
	r.Version = strings.Repeat("b", 40)
	r.DeployNonce = r.Version
	r.RequestID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	r.LogStream = "2026/10/23/[$LATEST]bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	r.TokenVersionBefore = 4
	r.TokenVersionAfter = 7
	logs = "REPORT RequestId: " + r.RequestID + "\tDuration: 300 ms\tInit Duration: 90 ms\n"
	payload, _ = json.Marshal(r)
	second := OnStarInvocation(payload, []byte(logs), true, r.Version, r.RunID, r.DeployNonce, time.Unix(1100, 0))
	if e := OnStarPair(first, second, r.Version, "synthetic-pair", time.Unix(1200, 0)).Validate(OnStarPairRequired); e != nil {
		t.Fatal(e)
	}
	if OnStarPair(first, first, r.Version, "synthetic-pair", time.Unix(1200, 0)).Validate(OnStarPairRequired) == nil {
		t.Fatal("same environment accepted twice")
	}
	if OnStarPair(first, second, first.Commit, "synthetic-pair", time.Unix(1200, 0)).Validate(OnStarPairRequired) == nil {
		t.Fatal("aggregate accepted wrong final commit")
	}
	badNonce := second
	badNonce.Checks = append([]Check(nil), second.Checks...)
	for i := range badNonce.Checks {
		if badNonce.Checks[i].Name == "deployment-nonce" {
			badNonce.Checks[i].Actual = first.Commit
		}
	}
	if OnStarPair(first, badNonce, r.Version, "synthetic-pair", time.Unix(1200, 0)).Validate(OnStarPairRequired) == nil {
		t.Fatal("receipt nonce did not bind its commit")
	}
	for i := range second.Checks {
		if second.Checks[i].Name == "token-version-before" {
			second.Checks[i].Actual = "2"
		}
	}
	if OnStarPair(first, second, r.Version, "synthetic-pair", time.Unix(1200, 0)).Validate(OnStarPairRequired) == nil {
		t.Fatal("lost refresh rotation accepted")
	}
}
