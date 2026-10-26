package verify

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/worker"
)

var OnStarRequired = []string{"lambda-invocation", "deployment-version", "deployment-nonce", "proof-run-id", "lambda-request-id", "lambda-log-stream", "lambda-cold-start", "provider-proof-status", "encrypted-credential-read", "b2c-refresh", "refresh-persisted-before-exchange", "gm-exchange", "garage-membership", "cached-location-response"}
var onStarProviderChecks = append([]string(nil), OnStarRequired[8:]...)

func init() {
	OnStarRequired = append(OnStarRequired, "enrollment-version", "token-version-before", "token-version-after")
}

var requestIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// Custom Lambda log groups include the function name before the version marker.
var logStreamPattern = regexp.MustCompile(`^\d{4}/\d{2}/\d{2}/(?:[A-Za-z0-9_-]{1,64})?\[[$A-Za-z0-9._-]+\][a-f0-9]{32}$`)
var proofIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var initDurationPattern = regexp.MustCompile(`(?:^|\s)Init Duration:\s+([0-9]+(?:\.[0-9]+)?) ms(?:\s|$)`)

// OnStarInvocation consumes sensitive transport buffers without retaining them.
// Only allowlisted identifiers and measured scalar results enter the report.
func OnStarInvocation(payload, logs []byte, invokeOK bool, commit, runID, nonce string, now time.Time) Report {
	r := Report{SchemaVersion: "1", Mode: "deployed-onstar-invocation", Commit: commit, RunID: runID, GeneratedAt: now.UTC(), Checks: []Check{}}
	add := func(name string, passed bool, actual string) {
		status := "fail"
		if passed {
			status = "pass"
		}
		r.Checks = append(r.Checks, Check{Name: name, Required: true, Status: status, Measured: true, Expected: "verified deployed invocation", Actual: actual})
	}
	var result worker.Result
	decoded := len(payload) <= 64*1024 && json.Unmarshal(payload, &result) == nil
	add("lambda-invocation", invokeOK && decoded, "response decoded without function error")
	add("deployment-version", decoded && shaPattern.MatchString(commit) && result.Version == commit, "exact expected commit")
	validNonce := decoded && shaPattern.MatchString(nonce) && nonce == commit && result.DeployNonce == nonce
	nonceValue := "invalid"
	if validNonce {
		nonceValue = nonce
	}
	add("deployment-nonce", validNonce, nonceValue)
	add("proof-run-id", decoded && proofIDPattern.MatchString(runID) && result.RunID == runID, "exact requested run")
	requestValid := decoded && requestIDPattern.MatchString(result.RequestID)
	requestValue := "invalid"
	if requestValid {
		requestValue = result.RequestID
	}
	add("lambda-request-id", requestValid, requestValue)
	streamValid := decoded && logStreamPattern.MatchString(result.LogStream)
	streamValue := "invalid"
	if streamValid {
		streamValue = result.LogStream
	}
	add("lambda-log-stream", streamValid, streamValue)
	reportLines := 0
	matched := false
	initMS := float64(0)
	if len(logs) <= 64*1024 {
		for _, line := range strings.Split(string(logs), "\n") {
			if !strings.HasPrefix(line, "REPORT RequestId: ") {
				continue
			}
			reportLines++
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[2] != result.RequestID {
				continue
			}
			parts := initDurationPattern.FindStringSubmatch(line)
			if len(parts) != 2 {
				continue
			}
			value, e := strconv.ParseFloat(parts[1], 64)
			if e == nil && value > 0 {
				matched = true
				initMS = value
			}
		}
	}
	cold := requestValid && reportLines == 1 && matched && result.ColdStart
	coldActual := "cold start not verified"
	if cold {
		coldActual = "Init Duration milliseconds=" + strconv.FormatFloat(initMS, 'f', -1, 64)
	}
	add("lambda-cold-start", cold, coldActual)
	proofStatus := "invalid worker response"
	if decoded {
		switch result.Status {
		case "passed":
			proofStatus = "passed"
		case "blocked", "failed":
			proofStatus = result.Status + "; reason=unrecognized"
			switch result.Reason {
			case "invalid-proof-request", "warm-proof-disallowed", "insufficient-deadline", "lease-unavailable", "credential-load-failed", "credential-binding-mismatch", "provider-cooldown", "reauth-required", "vehicle-not-linked", "proof-budget-exhausted", "proof-budget-write-failed", "provider-unavailable", "access-denied", "rate-limited", "deadline", "invalid-location-response", "account-mismatch", "failure-state-write-failed", "refresh-persistence-failed", "proof-state-write-failed":
				proofStatus = result.Status + "; reason=" + result.Reason
			}
		}
	}
	add("provider-proof-status", decoded && result.Status == "passed", proofStatus)
	counts := map[string]int{}
	checks := map[string]worker.Check{}
	for _, c := range result.Checks {
		counts[c.Name]++
		checks[c.Name] = c
	}
	for _, name := range onStarProviderChecks {
		c, ok := checks[name]
		passed := decoded && ok && counts[name] == 1 && c.Passed && c.Milliseconds >= 0 && c.Milliseconds <= 120000
		actual := "missing or failed measurement"
		if passed {
			actual = "passed; milliseconds=" + strconv.FormatInt(c.Milliseconds, 10)
		}
		add(name, passed, actual)
	}
	add("enrollment-version", decoded && result.EnrollmentVersion > 0, strconv.FormatInt(result.EnrollmentVersion, 10))
	add("token-version-before", decoded && result.TokenVersionBefore > 0, strconv.FormatUint(result.TokenVersionBefore, 10))
	add("token-version-after", decoded && result.TokenVersionAfter > result.TokenVersionBefore, strconv.FormatUint(result.TokenVersionAfter, 10))
	return r
}

var OnStarPairRequired = []string{"first-invocation", "second-invocation", "distinct-cold-starts", "durable-refresh-continuity"}

func OnStarPair(first, second Report, commit, runID string, now time.Time) Report {
	r := Report{SchemaVersion: "1", Mode: "deployed-onstar-cold-starts", Commit: commit, RunID: runID, GeneratedAt: now.UTC(), Checks: []Check{}}
	add := func(name string, ok bool) {
		status := "fail"
		if ok {
			status = "pass"
		}
		r.Checks = append(r.Checks, Check{Name: name, Required: true, Measured: true, Status: status, Expected: "two measured cold invocations with durable refresh continuity", Actual: status})
	}
	value := func(input Report, name string) string {
		for _, c := range input.Checks {
			if c.Name == name {
				return c.Actual
			}
		}
		return ""
	}
	valid := func(input Report) bool {
		return input.Mode == "deployed-onstar-invocation" && shaPattern.MatchString(input.Commit) && value(input, "deployment-nonce") == input.Commit && input.Validate(OnStarRequired) == nil
	}
	a, b := valid(first), valid(second) && second.Commit == commit
	add("first-invocation", a)
	add("second-invocation", b)
	distinct := a && b && first.RunID != second.RunID && first.Commit != second.Commit
	for _, name := range []string{"lambda-request-id", "lambda-log-stream", "deployment-nonce"} {
		distinct = distinct && value(first, name) != "" && value(first, name) != value(second, name)
	}
	add("distinct-cold-starts", distinct)
	after, e1 := strconv.ParseUint(value(first, "token-version-after"), 10, 64)
	before, e2 := strconv.ParseUint(value(second, "token-version-before"), 10, 64)
	add("durable-refresh-continuity", a && b && e1 == nil && e2 == nil && after > 0 && before >= after && value(first, "enrollment-version") == value(second, "enrollment-version"))
	r.Checks = append(r.Checks,
		Check{Name: "location-schema", Required: false, Measured: false, Status: "blocked", Expected: "live validated coordinates and fix timestamp", Actual: "not established by cached status retrieval"},
		Check{Name: "four-slot-evidence", Required: false, Measured: false, Status: "blocked", Expected: "four scheduled slots with verified evidence", Actual: "not established by two read-only cold invocations"})
	return r
}
