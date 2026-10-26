package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReleaseReadiness(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	source, run, digest := strings.Repeat("a", 40), "12345", strings.Repeat("b", 64)
	for _, name := range []string{"complete", "missing", "failed", "pending", "stale", "future", "wrong-source", "wrong-run", "wrong-apk", "empty-evidence", "blank-evidence", "missing-evidence-hash", "missing-gate"} {
		t.Run(name, func(t *testing.T) {
			r := readinessReport{SchemaVersion: 1, SourceSHA: source, BuildRunID: run, APKSHA256: digest, VerifiedAt: now, Gates: map[string]readinessGate{}}
			for _, gate := range requiredReadinessGates {
				r.Gates[gate] = readinessGate{Status: "passed", Evidence: "artifacts/synthetic-proof.json", EvidenceSHA256: strings.Repeat("c", 64)}
			}
			switch name {
			case "failed", "pending":
				r.Gates["native-runtime"] = readinessGate{Status: name, Evidence: "artifacts/synthetic-proof.json", EvidenceSHA256: strings.Repeat("c", 64)}
			case "stale":
				r.VerifiedAt = now.Add(-7*24*time.Hour - time.Second)
			case "future":
				r.VerifiedAt = now.Add(time.Second)
			case "wrong-source":
				r.SourceSHA = strings.Repeat("c", 40)
			case "wrong-run":
				r.BuildRunID = "6789"
			case "wrong-apk":
				r.APKSHA256 = strings.Repeat("c", 64)
			case "empty-evidence":
				r.Gates["native-runtime"] = readinessGate{Status: "passed"}
			case "blank-evidence":
				r.Gates["native-runtime"] = readinessGate{Status: "passed", Evidence: "   ", EvidenceSHA256: strings.Repeat("c", 64)}
			case "missing-evidence-hash":
				r.Gates["native-runtime"] = readinessGate{Status: "passed", Evidence: "artifacts/synthetic-proof.json"}
			case "missing-gate":
				delete(r.Gates, "native-runtime")
			}
			data, _ := json.Marshal(r)
			if name == "missing" {
				data = nil
			}
			err := validateReadiness(data, source, run, digest, now)
			if (err == nil) != (name == "complete") {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}

func readinessFixture(t *testing.T, root string, now time.Time) readinessReport {
	t.Helper()
	report := readinessReport{SchemaVersion: 1, SourceSHA: strings.Repeat("a", 40), BuildRunID: "12345", APKSHA256: strings.Repeat("b", 64), VerifiedAt: now, Gates: map[string]readinessGate{}}
	for _, name := range requiredReadinessGates {
		proof := readinessProof{SchemaVersion: 1, Gate: name, Status: "passed", SourceSHA: report.SourceSHA, BuildRunID: report.BuildRunID, APKSHA256: report.APKSHA256, VerifiedAt: now, Description: "Synthetic unit-test fixture. Not device or release evidence."}
		path := "artifacts/proofs/" + name + ".json"
		report.Gates[name] = writeReadinessProof(t, root, path, proof)
	}
	return report
}

func writeReadinessProof(t *testing.T, root, path string, proof readinessProof) readinessGate {
	t.Helper()
	data, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(root, filepath.FromSlash(path))
	if err = os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(full, data, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	return readinessGate{Status: "passed", Evidence: path, EvidenceSHA256: hex.EncodeToString(hash[:])}
}

func TestReleaseReadinessRequiresTypedArtifactBoundFiles(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"complete", "missing-file", "tampered-file", "wrong-proof-gate", "failed-proof", "wrong-proof-source", "wrong-proof-run", "wrong-proof-apk", "stale-proof", "future-proof", "after-report", "empty-description", "blank-description", "oversized-file", "trailing-json", "unknown-field"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			report := readinessFixture(t, root, now)
			gate := report.Gates["native-runtime"]
			full := filepath.Join(root, filepath.FromSlash(gate.Evidence))
			data, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			var proof readinessProof
			if json.Unmarshal(data, &proof) != nil {
				t.Fatal("fixture invalid")
			}
			switch name {
			case "missing-file":
				if err = os.Remove(full); err != nil {
					t.Fatal(err)
				}
			case "tampered-file":
				if err = os.WriteFile(full, append(data, ' '), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-proof-gate":
				proof.Gate = "full-widget"
			case "failed-proof":
				proof.Status = "failed"
			case "wrong-proof-source":
				proof.SourceSHA = strings.Repeat("d", 40)
			case "wrong-proof-run":
				proof.BuildRunID = "54321"
			case "wrong-proof-apk":
				proof.APKSHA256 = strings.Repeat("d", 64)
			case "stale-proof":
				proof.VerifiedAt = now.Add(-7*24*time.Hour - time.Second)
			case "future-proof":
				proof.VerifiedAt = now.Add(time.Second)
			case "after-report":
				report.VerifiedAt = now.Add(-time.Minute)
			case "empty-description":
				proof.Description = ""
			case "blank-description":
				proof.Description = " \n\t "
			}
			if name != "missing-file" && name != "tampered-file" {
				report.Gates["native-runtime"] = writeReadinessProof(t, root, gate.Evidence, proof)
			}
			if name == "oversized-file" || name == "trailing-json" || name == "unknown-field" {
				data, err = os.ReadFile(full)
				if err != nil {
					t.Fatal(err)
				}
				switch name {
				case "oversized-file":
					data = []byte(strings.Repeat(" ", readinessFileLimit+1))
				case "trailing-json":
					data = append(data, []byte(" {}")...)
				case "unknown-field":
					data = append(data[:len(data)-1], []byte(`,"unexpected":true}`)...)
				}
				if err = os.WriteFile(full, data, 0600); err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(data)
				gate.EvidenceSHA256 = hex.EncodeToString(hash[:])
				report.Gates["native-runtime"] = gate
			}
			encoded, _ := json.Marshal(report)
			err = verifyReadinessEvidence(root, encoded, report.SourceSHA, report.BuildRunID, report.APKSHA256, now)
			if (err == nil) != (name == "complete") {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}

func TestReadinessRejectsEscapingPathsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", " ", "../outside.json", "artifacts/../outside.json", "artifacts/a/../../outside.json", "/artifacts/proof.json", `C:\artifacts\proof.json`, `artifacts\proof.json`, "artifacts/proof.json:stream.json", "artifacts/proof.txt", "artifacts//proof.json"} {
		if _, err := readReadinessArtifact(root, name); err == nil {
			t.Fatalf("unsafe path accepted: %q", name)
		}
	}
	for _, kind := range []string{"file", "directory", "root-ancestor"} {
		t.Run(kind, func(t *testing.T) {
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "proof.json"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			base := t.TempDir()
			if err := os.Mkdir(filepath.Join(base, "artifacts"), 0700); err != nil {
				t.Fatal(err)
			}
			name := "artifacts/proof.json"
			var target, link string
			switch kind {
			case "file":
				target = filepath.Join(outside, "proof.json")
				link = filepath.Join(base, "artifacts", "proof.json")
			case "directory":
				target = outside
				link = filepath.Join(base, "artifacts", "linked")
				name = "artifacts/linked/proof.json"
			case "root-ancestor":
				if err := os.MkdirAll(filepath.Join(outside, "nested", "artifacts"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(outside, "nested", "artifacts", "proof.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
				target = outside
				link = filepath.Join(base, "linked")
			}
			if err := os.Symlink(target, link); err != nil {
				if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
					t.Skip("Windows token lacks symlink privilege; the required Linux CI run exercises this case")
				}
				t.Fatal(err)
			}
			if kind == "root-ancestor" {
				base = filepath.Join(link, "nested")
			}
			if _, err := readReadinessArtifact(base, name); err == nil {
				t.Fatal("linked evidence accepted")
			}
		})
	}
}

func TestProductionReadinessVerifierRequiresActualProofFiles(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	report := readinessFixture(t, root, now)
	data, _ := json.Marshal(report)
	if err := os.WriteFile(filepath.Join(root, "artifacts", "android-readiness.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANDROID_SOURCE_SHA", report.SourceSHA)
	t.Setenv("VERIFIED_RUN_ID", report.BuildRunID)
	t.Setenv("VERIFIED_APK_SHA256", report.APKSHA256)
	t.Chdir(root)
	if err := verifyReadinessFile(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(report.Gates["native-runtime"].Evidence))); err != nil {
		t.Fatal(err)
	}
	if err := verifyReadinessFile(); err == nil {
		t.Fatal("production path accepted missing evidence")
	}
}
