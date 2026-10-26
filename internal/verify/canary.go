package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CanaryDigest allows a completed job's logs to be checked without retaining its secret.
type CanaryDigest struct {
	Length int    `json:"length"`
	SHA256 string `json:"sha256"`
}

func CanaryDigests(forms []string) []CanaryDigest {
	var result []CanaryDigest
	for _, form := range forms {
		sum := sha256.Sum256([]byte(form))
		result = append(result, CanaryDigest{len(form), hex.EncodeToString(sum[:])})
	}
	return result
}

func CheckCanaryDigests(data []byte, digests []CanaryDigest) error {
	if len(digests) != 4 {
		return errors.New("four canary encodings are required")
	}
	for _, digest := range digests {
		if digest.Length < 16 || digest.Length > 4096 || !digestPattern.MatchString(digest.SHA256) {
			return errors.New("invalid canary digest")
		}
		decoded, _ := hex.DecodeString(digest.SHA256)
		var expected [32]byte
		copy(expected[:], decoded)
		for offset := 0; offset+digest.Length <= len(data); offset++ {
			sum := sha256.Sum256(data[offset : offset+digest.Length])
			if sum == expected {
				return errors.New("canary found in evidence")
			}
		}
	}
	return nil
}

var DeploymentRequired = []string{"canary-log-encodings", "canary-artifact-encodings", "failed-and-successful-injection", "runtime-selftest", "canary-argv"}

func DeploymentCanary(digestJSON, log []byte, artifacts map[string][]byte, commit, runID string, now time.Time) Report {
	r := Report{SchemaVersion: "1", Mode: "deployment/canary", Commit: commit, RunID: runID, GeneratedAt: now.UTC()}
	var digests []CanaryDigest
	valid := json.Unmarshal(digestJSON, &digests) == nil && CheckCanaryDigests(nil, digests) == nil
	logSum := sha256.Sum256(log)
	evidence := []Evidence{{Path: "deployment.log", SHA256: fmt.Sprintf("%x", logSum)}}
	checks := []bool{valid && len(log) > 0 && CheckCanaryDigests(log, digests) == nil, valid && len(artifacts) > 0,
		strings.Contains(string(log), "canary forced-failure: parameter rejected; captured child output clean") && strings.Contains(string(log), "canary success: captured child output clean"),
		shaPattern.MatchString(commit) && strings.Contains(string(log), "Health version verified: "+commit), strings.Contains(string(log), "canary argv: encoded forms absent")}
	paths := make([]string, 0, len(artifacts))
	for path := range artifacts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		data := artifacts[path]
		if CheckCanaryDigests(data, digests) != nil {
			checks[1] = false
		}
		sum := sha256.Sum256(data)
		evidence = append(evidence, Evidence{Path: path, SHA256: fmt.Sprintf("%x", sum)})
	}
	for i, name := range DeploymentRequired {
		status := "pass"
		actual := "measured assertion passed"
		if !checks[i] {
			status = "fail"
			actual = "required assertion failed"
		}
		r.Checks = append(r.Checks, Check{Name: name, Required: true, Measured: true, Status: status, Expected: "no canary encodings in completed deployment logs or build artifacts; rejected and successful injection with live health", Actual: actual, Evidence: evidence})
	}
	return r
}
