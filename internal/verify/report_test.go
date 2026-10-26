package verify

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validReport() Report {
	return Report{SchemaVersion: "1", Mode: "test", Commit: strings.Repeat("a", 40), RunID: "local-test", GeneratedAt: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), Checks: []Check{{Name: "proof", Required: true, Status: "pass", Measured: true}}}
}
func TestRequiredEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Report)
	}{
		{"missing", func(r *Report) { r.Checks = nil }},
		{"declared-only", func(r *Report) { r.Checks[0].Measured = false }},
		{"blocked", func(r *Report) { r.Checks[0].Status = "blocked" }},
		{"downgraded", func(r *Report) { r.Checks[0].Required = false }},
		{"duplicate", func(r *Report) { r.Checks = append(r.Checks, r.Checks[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validReport()
			tc.mutate(&r)
			if r.Validate([]string{"proof"}) == nil {
				t.Fatal("invalid evidence passed")
			}
		})
	}
}
func TestWriteValidAndFailedReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	r := validReport()
	if digest, err := r.Write(path, []string{"proof"}, nil); err != nil || len(digest) != 64 {
		t.Fatalf("valid report failed: %v", err)
	}
	r.Checks[0].Status = "fail"
	if digest, err := r.Write(path, []string{"proof"}, nil); err == nil || digest == "" {
		t.Fatal("failed evidence must write a failing report")
	}
}
func TestCanaryNeverWritten(t *testing.T) {
	canary := "private-test-canary-value"
	for _, value := range []string{canary, base64.StdEncoding.EncodeToString([]byte(canary))} {
		path := filepath.Join(t.TempDir(), "report.json")
		r := validReport()
		r.Checks[0].Notes = value
		if _, err := r.Write(path, []string{"proof"}, []string{canary}); err == nil {
			t.Fatal("leak accepted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("sensitive report written")
		}
	}
	r := validReport()
	escaped := "private\"canary\\value\nline"
	r.Checks[0].Notes = escaped
	if _, err := r.Write(filepath.Join(t.TempDir(), "report.json"), []string{"proof"}, []string{escaped}); err == nil {
		t.Fatal("JSON escaped canary accepted")
	}
}
func TestRedactionPatterns(t *testing.T) {
	for _, value := range []string{"eyJ" + strings.Repeat("a", 12) + ".payload", "Bearer " + "sensitive", "1GNS" + "KJKL0RR123456", "code=" + strings.Repeat("x", 20), `{"refresh_token":"sensitive"}`, `latitude=` + `37.12345`, `{"longitude":` + `-83.654321}`} {
		if RedactCheck([]byte(value), nil) == nil {
			t.Fatal("sensitive pattern accepted")
		}
	}
}

func TestSourcePrivacy(t *testing.T) {
	for _, number := range []string{"+45.0", ".5", "-0.5", "1e2", "1", "0.1e-3"} {
		if CheckSourcePrivacy([]byte("latitude="+number), nil) == nil {
			t.Fatal("coordinate form accepted")
		}
	}
	for _, value := range []string{"eyJ" + strings.Repeat("a", 12) + ".payload", "Bearer " + "sensitive", "1GNS" + "KJKL0RR999999", "code=" + strings.Repeat("x", 20), `latitude=` + `37.12345`, `{"longitude":` + `-83.654321}`} {
		if CheckSourcePrivacy([]byte(value), nil) == nil {
			t.Fatal("sensitive source passed")
		}
	}
	if err := CheckSourcePrivacy([]byte(`{"vin":"1GNSKJKL0RR123456","latitude":0,"longitude":0.0}`), nil); err != nil {
		t.Fatal(err)
	}
	canary := "synthetic-scan-canary"
	if CheckSourcePrivacy([]byte(base64.StdEncoding.EncodeToString([]byte(canary))), []string{canary}) == nil {
		t.Fatal("encoded canary passed")
	}
	dir := t.TempDir()
	if _, err := ScanSourcePrivacy([]string{dir}, nil); err == nil {
		t.Fatal("empty scan passed")
	}
	if err := os.WriteFile(filepath.Join(dir, "safe.json"), []byte(`{"status":"pending"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := ScanSourcePrivacy([]string{dir}, nil); err != nil || n != 1 {
		t.Fatal("safe scan failed", err)
	}
	if _, err := ScanSourcePrivacy([]string{filepath.Join(dir, "missing")}, nil); err == nil {
		t.Fatal("unreadable scan passed")
	}
}

func TestReportRejectsEscapedCredentialNotes(t *testing.T) {
	for _, note := range []string{`{"refresh_token":"synthetic-private"}`, `{"refresh\u005ftoken":"synthetic-private"}`, `{"lat\u0069tude":37}`, "latitude=" + "1", "longitude=" + ".5"} {
		r := validReport()
		r.Checks[0].Notes = note
		path := filepath.Join(t.TempDir(), "report.json")
		if _, err := r.Write(path, []string{"proof"}, nil); err == nil {
			t.Fatal("sensitive note accepted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("sensitive report written")
		}
	}
}
