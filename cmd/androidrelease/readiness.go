package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Evidence is deliberately separate from signing: a successfully built APK is
// not evidence that the owner acceptance gates passed.
type readinessReport struct {
	SchemaVersion int                      `json:"schemaVersion"`
	SourceSHA     string                   `json:"sourceSha"`
	BuildRunID    string                   `json:"buildRunId"`
	APKSHA256     string                   `json:"apkSha256"`
	VerifiedAt    time.Time                `json:"verifiedAt"`
	Gates         map[string]readinessGate `json:"gates"`
}
type readinessGate struct {
	Status         string `json:"status"`
	Evidence       string `json:"evidence"`
	EvidenceSHA256 string `json:"evidenceSha256"`
}

// Each gate points to a separately retained typed proof. A summary assertion
// cannot substitute for the evidence file, its bytes, or its artifact identity.
type readinessProof struct {
	SchemaVersion int       `json:"schemaVersion"`
	Gate          string    `json:"gate"`
	Status        string    `json:"status"`
	SourceSHA     string    `json:"sourceSha"`
	BuildRunID    string    `json:"buildRunId"`
	APKSHA256     string    `json:"apkSha256"`
	VerifiedAt    time.Time `json:"verifiedAt"`
	Description   string    `json:"description"`
}

const readinessFileLimit = 64 * 1024

var readinessDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

var requiredReadinessGates = []string{
	"visual-parity", "compact-widget", "full-widget", "session-continuity",
	"unknown-command-safety", "private-real-map", "map-key-scope-revocation",
	"non-ev-scope", "truthful-source-time", "preview-separation",
	"startup-permissions", "native-runtime", "backdrop-artwork", "map-interaction",
}

func validateReadiness(data []byte, source, run, digest string, now time.Time) error {
	invalid := errors.New("release readiness evidence missing, stale, incomplete or mismatched")
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(source) || !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(run) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(digest) {
		return invalid
	}
	var report readinessReport
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&report) != nil || d.Decode(new(any)) != io.EOF {
		return invalid
	}
	if report.SchemaVersion != 1 || report.SourceSHA != source || report.BuildRunID != run || report.APKSHA256 != digest || report.VerifiedAt.IsZero() || report.VerifiedAt.After(now) || now.Sub(report.VerifiedAt) > 7*24*time.Hour {
		return invalid
	}
	for _, name := range requiredReadinessGates {
		gate, ok := report.Gates[name]
		if !ok || gate.Status != "passed" || !safeReadinessPath(gate.Evidence) || !readinessDigest.MatchString(gate.EvidenceSHA256) {
			return invalid
		}
	}
	return nil
}

func safeReadinessPath(name string) bool {
	return len(name) <= 1024 && strings.HasPrefix(name, "artifacts/") && strings.HasSuffix(name, ".json") && path.Clean(name) == name && !strings.ContainsAny(name, "\\:\x00\r\n") && strings.TrimSpace(name) == name
}

// Check every component before reading: a safe-looking path through a linked
// artifacts directory must not read outside the repository. The final file must
// be regular, bounded, and still the file that was inspected before opening.
func readReadinessArtifact(root, name string) ([]byte, error) {
	invalid := errors.New("release readiness evidence file is missing or unsafe")
	if !safeReadinessPath(name) {
		return nil, invalid
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return nil, invalid
	}
	var info os.FileInfo
	for ancestor := base; ; ancestor = filepath.Dir(ancestor) {
		info, err = os.Lstat(ancestor)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, invalid
		}
		if filepath.Dir(ancestor) == ancestor {
			break
		}
	}
	current := base
	parts := strings.Split(name, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, invalid
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, invalid
		}
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > readinessFileLimit {
		return nil, invalid
	}
	file, err := os.Open(current)
	if err != nil {
		return nil, invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, readinessFileLimit+1))
	if err != nil || len(data) > readinessFileLimit {
		return nil, invalid
	}
	return data, nil
}

func verifyReadinessEvidence(root string, data []byte, source, run, digest string, now time.Time) error {
	if err := validateReadiness(data, source, run, digest, now); err != nil {
		return err
	}
	var report readinessReport
	if json.Unmarshal(data, &report) != nil {
		return errors.New("release readiness report invalid")
	}
	invalid := errors.New("release readiness proof missing, stale, incomplete or mismatched")
	for _, name := range requiredReadinessGates {
		gate := report.Gates[name]
		proofBytes, err := readReadinessArtifact(root, gate.Evidence)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(proofBytes)
		if hex.EncodeToString(hash[:]) != gate.EvidenceSHA256 {
			return invalid
		}
		var proof readinessProof
		decoder := json.NewDecoder(bytes.NewReader(proofBytes))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&proof) != nil || decoder.Decode(new(any)) != io.EOF {
			return invalid
		}
		if proof.SchemaVersion != 1 || proof.Gate != name || proof.Status != "passed" || proof.SourceSHA != source || proof.BuildRunID != run || proof.APKSHA256 != digest || proof.VerifiedAt.IsZero() || proof.VerifiedAt.After(report.VerifiedAt) || proof.VerifiedAt.After(now) || now.Sub(proof.VerifiedAt) > 7*24*time.Hour || strings.TrimSpace(proof.Description) == "" || len(proof.Description) > 4096 {
			return invalid
		}
	}
	return nil
}

func verifyReadinessFile() error {
	root, err := os.Getwd()
	if err != nil {
		return errors.New("release readiness repository unavailable")
	}
	data, err := readReadinessArtifact(root, "artifacts/android-readiness.json")
	if err != nil {
		return errors.New("required android-readiness.json is missing")
	}
	return verifyReadinessEvidence(root, data, os.Getenv("ANDROID_SOURCE_SHA"), os.Getenv("VERIFIED_RUN_ID"), os.Getenv("VERIFIED_APK_SHA256"), time.Now().UTC())
}
