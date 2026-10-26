package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/verify"
)

func TestPreflightFixtures(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{{"good", 0}, {"bad", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			output := filepath.Join(t.TempDir(), "report.json")
			code := run([]string{"preflight", "--input", "testdata/preflight/" + tc.name + ".json", "--report", output, "--commit", strings.Repeat("a", 40), "--run-id", "fixture"}, &stdout, &stderr)
			if code != tc.want {
				t.Fatalf("exit %d, want %d: %s", code, tc.want, &stderr)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var report verify.Report
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			if report.SchemaVersion != "1" || report.Mode != "preflight" {
				t.Fatal("invalid envelope")
			}
		})
	}
}

func TestTemplateFixtures(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{{"good", 0}, {"bad", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"template", "--input", "testdata/template/" + tc.name + ".json", "--report", filepath.Join(t.TempDir(), "report.json"), "--commit", strings.Repeat("a", 40)}, &stdout, &stderr)
			if code != tc.want {
				t.Fatalf("exit %d want %d: %s", code, tc.want, stderr.String())
			}
		})
	}
}

func TestRedactionCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	for _, tc := range []struct {
		body string
		want int
	}{{`{"status":"safe"}`, 0}, {"Bearer " + "synthetic-private", 1}} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		if got := run([]string{"redaction", "--paths", dir}, &out, &stderr); got != tc.want {
			t.Fatalf("exit=%d want=%d", got, tc.want)
		}
		if strings.Contains(out.String()+stderr.String(), tc.body) {
			t.Fatal("scanner echoed input")
		}
	}
}
func TestMissingInputWritesFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	output := filepath.Join(t.TempDir(), "report.json")
	code := run([]string{"preflight", "--input", filepath.Join(t.TempDir(), "missing.json"), "--report", output, "--commit", strings.Repeat("a", 40)}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("missing input passed")
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal("failed report absent", err)
	}
}
func TestSensitiveInputNeverWritten(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.json")
	output := filepath.Join(dir, "report.json")
	if err := os.WriteFile(input, []byte(`{"notes":"Bearer `+`private-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if run([]string{"preflight", "--input", input, "--report", output}, &stdout, &stderr) == 0 {
		t.Fatal("leak passed")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("report exists")
	}
	if strings.Contains(stderr.String(), "private-value") {
		t.Fatal("secret in error")
	}
}

func TestPreflightRejectsIncompleteEvidence(t *testing.T) {
	data, err := os.ReadFile("testdata/preflight/good.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*verify.PreflightInput)
	}{
		{"missing-row", func(in *verify.PreflightInput) { in.Prerequisites = in.Prerequisites[1:] }},
		{"undated", func(in *verify.PreflightInput) { in.Prerequisites[0].Date = "today" }},
		{"missing-source", func(in *verify.PreflightInput) { in.Prerequisites[0].Source = "" }},
		{"missing-result", func(in *verify.PreflightInput) { in.Prerequisites[0].Observed = "" }},
		{"missing-owner", func(in *verify.PreflightInput) { in.Prerequisites[0].Owner = "" }},
		{"missing-dependent", func(in *verify.PreflightInput) { in.Prerequisites[0].DependentMilestone = "" }},
		{"unanswered-decision", func(in *verify.PreflightInput) { in.Decisions[0].Answer = "" }},
		{"duplicate-row", func(in *verify.PreflightInput) { in.Prerequisites = append(in.Prerequisites, in.Prerequisites[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input verify.PreflightInput
			if err := json.Unmarshal(data, &input); err != nil {
				t.Fatal(err)
			}
			tc.change(&input)
			changed, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			report := verify.Preflight(changed, "fixture", strings.Repeat("a", 40), "fixture", time.Now())
			if report.Validate(verify.PreflightRequired) == nil {
				t.Fatal("invalid evidence passed")
			}
		})
	}
	data = append(data, []byte(` {"unexpected":true}`)...)
	report := verify.Preflight(data, "fixture", strings.Repeat("a", 40), "fixture", time.Now())
	if report.Validate(verify.PreflightRequired) == nil {
		t.Fatal("trailing JSON passed")
	}
}
