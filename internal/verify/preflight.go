package verify

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

var PreflightRequired = []string{"prerequisite-evidence", "operator-decisions"}
var PrerequisiteNames = []string{
	"repository", "github-secret-access", "aws-account-role", "certificate-domain-region",
	"amazon-developer-access", "gm-entitlement", "vehicle-owner-garage", "target-handset",
	"signing-key-recovery", "alexa-device-account", "ses-identity", "go-toolchain",
	"sam-toolchain", "gh-toolchain", "ask-toolchain", "android-toolchain",
	"predeploy-checks", "attended-consent", "noninteractive-session", "network", "session-duration",
}
var DecisionNames = []string{"slot-wake-policy", "four-hour-update-check", "alexa-invocation-name", "alexa-device-account", "failed-create-stack-delete", "daily-digest", "cost-center-tag", "officepc-local-actions"}

type Prerequisite struct {
	Name               string `json:"name"`
	Status             string `json:"status"`
	Source             string `json:"source"`
	Observed           string `json:"observed"`
	Date               string `json:"date"`
	Attended           bool   `json:"attended"`
	Owner              string `json:"owner"`
	DependentMilestone string `json:"dependentMilestone"`
}
type Decision struct {
	Name   string `json:"name"`
	Answer string `json:"answer"`
	Date   string `json:"date"`
	Owner  string `json:"owner"`
}
type PreflightInput struct {
	Prerequisites []Prerequisite `json:"prerequisites"`
	Decisions     []Decision     `json:"decisions"`
}

func dated(value string, now time.Time) bool {
	t, err := time.Parse("2006-01-02", value)
	return err == nil && !t.After(now)
}
func nonempty(values ...string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return false
		}
	}
	return true
}

// Preflight evaluates evidence completeness. A documented blocker is not a passed live proof.
func Preflight(data []byte, path, commit, runID string, now time.Time) Report {
	r := Report{SchemaVersion: "1", Mode: "preflight", Commit: commit, RunID: runID, GeneratedAt: now.UTC()}
	hash := sha256.Sum256(data)
	evidence := []Evidence{{Path: path, SHA256: fmt.Sprintf("%x", hash)}}
	var input PreflightInput
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	parseErr := decoder.Decode(&input)
	if parseErr == nil {
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			parseErr = fmt.Errorf("trailing input")
		}
	}
	prereqs := map[string]Prerequisite{}
	duplicates := false
	for _, row := range input.Prerequisites {
		if _, ok := prereqs[row.Name]; ok {
			duplicates = true
		}
		prereqs[row.Name] = row
	}
	validRows := parseErr == nil && !duplicates && len(prereqs) == len(PrerequisiteNames)
	for _, name := range PrerequisiteNames {
		row, exists := prereqs[name]
		valid := exists && (row.Status == "pass" || row.Status == "blocked") && nonempty(row.Source, row.Observed) && dated(row.Date, now)
		if row.Attended || row.Status == "blocked" {
			valid = valid && row.Owner == "Jeremy" && nonempty(row.DependentMilestone)
		}
		status := row.Status
		if !valid {
			status = "fail"
			validRows = false
		}
		r.Checks = append(r.Checks, Check{Name: "prerequisite/" + name, Status: status, Measured: false, Expected: "dated source and non-secret observation; attended or blocked steps name owner and dependent milestone", Actual: row.Observed, Evidence: evidence, Notes: row.Source + "; " + row.Date + "; owner=" + row.Owner + "; dependent=" + row.DependentMilestone})
	}
	decisions := map[string]Decision{}
	duplicates = false
	for _, d := range input.Decisions {
		if _, ok := decisions[d.Name]; ok {
			duplicates = true
		}
		decisions[d.Name] = d
	}
	validDecisions := parseErr == nil && !duplicates && len(decisions) == len(DecisionNames)
	for _, name := range DecisionNames {
		d, exists := decisions[name]
		if !exists || !nonempty(d.Answer) || d.Owner != "Jeremy" || !dated(d.Date, now) {
			validDecisions = false
		}
	}
	for _, result := range []struct {
		name  string
		valid bool
	}{{"prerequisite-evidence", validRows}, {"operator-decisions", validDecisions}} {
		status := "fail"
		actual := "evidence is incomplete or invalid"
		if result.valid {
			status = "pass"
			actual = "all required evidence rows validated"
		}
		r.Checks = append(r.Checks, Check{Name: result.name, Required: true, Status: status, Measured: true, Expected: "complete dated evidence", Actual: actual, Evidence: evidence, Notes: "Measures input completeness only; prerequisite claims are not independently verified by this command."})
	}
	return r
}
