// Package verify validates acceptance evidence without treating declarations as measurements.
package verify

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Evidence struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Check struct {
	Name     string     `json:"name"`
	Required bool       `json:"required"`
	Status   string     `json:"status"`
	Measured bool       `json:"measured"`
	Expected string     `json:"expected"`
	Actual   string     `json:"actual"`
	Evidence []Evidence `json:"evidence"`
	Notes    string     `json:"notes"`
}
type Report struct {
	SchemaVersion string    `json:"schemaVersion"`
	Mode          string    `json:"mode"`
	Commit        string    `json:"commit"`
	RunID         string    `json:"runId"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Checks        []Check   `json:"checks"`
}

var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.`),
	regexp.MustCompile(`(?i)Bearer\s+[^\s"\\]+`),
	regexp.MustCompile(`\b[A-HJ-NPR-Z0-9]{17}\b`),
	regexp.MustCompile(`(?i)(?:code|state|access_token)=[^\s"&\\]{17,}`),
	regexp.MustCompile(`(?i)"(?:refresh_token|access_token|client_secret|password|onstar_pin)"\s*:\s*"[^"\s]+"`),
	regexp.MustCompile(`(?i)\b(?:latitude|longitude|lat|lon|lng)[\\"\s:=]+[-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[-+]?\d+)?`),
}

// RedactCheck rejects sensitive evidence; errors never repeat the matching value.
func RedactCheck(data []byte, canaries []string) error {
	return redactCheck(data, canaries, 0)
}

func redactCheck(data []byte, canaries []string, depth int) error {
	for _, pattern := range sensitivePatterns {
		if pattern.Match(data) {
			return errors.New("report contains prohibited sensitive material")
		}
	}
	for _, value := range canaries {
		if canaryContains(data, value) {
			return errors.New("report contains a canary")
		}
	}
	// A report note can itself contain JSON. Check decoded strings as well so
	// serialization cannot conceal escaped credential fields or canaries.
	var decoded any
	if json.Unmarshal(data, &decoded) == nil {
		if depth >= 8 {
			return errors.New("report nesting exceeds privacy limit")
		}
		var visit func(any) error
		visit = func(value any) error {
			switch v := value.(type) {
			case string:
				return redactCheck([]byte(v), canaries, depth+1)
			case []any:
				for _, child := range v {
					if err := visit(child); err != nil {
						return err
					}
				}
			case map[string]any:
				canonical, _ := json.Marshal(v)
				for _, pattern := range sensitivePatterns {
					if pattern.Match(canonical) {
						return errors.New("report contains prohibited sensitive material")
					}
				}
				for key, child := range v {
					if err := redactCheck([]byte(key), canaries, depth+1); err != nil {
						return err
					}
					if err := visit(child); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := visit(decoded); err != nil {
			return err
		}
	}
	return nil
}

func canaryContains(data []byte, value string) bool {
	if value == "" {
		return false
	}
	forms := []string{value, base64.StdEncoding.EncodeToString([]byte(value)), base64.RawURLEncoding.EncodeToString([]byte(value)), url.QueryEscape(value), hex.EncodeToString([]byte(value))}
	quoted, _ := json.Marshal(value)
	forms = append(forms, string(quoted[1:len(quoted)-1]))
	for _, form := range forms {
		if strings.Contains(string(data), form) {
			return true
		}
	}
	return false
}

func (r Report) Validate(required []string) error {
	var failures []string
	if r.SchemaVersion != "1" || r.Mode == "" || !shaPattern.MatchString(r.Commit) || r.RunID == "" || r.GeneratedAt.IsZero() {
		failures = append(failures, "invalid report metadata")
	}
	seen := map[string]Check{}
	for _, c := range r.Checks {
		if _, exists := seen[c.Name]; exists || c.Name == "" {
			failures = append(failures, "duplicate or empty check name")
		}
		seen[c.Name] = c
		if c.Status != "pass" && c.Status != "fail" && c.Status != "skip" && c.Status != "blocked" {
			failures = append(failures, "invalid check status")
		}
		if c.Required && (c.Status != "pass" || !c.Measured) {
			failures = append(failures, "required check did not pass with measurement: "+c.Name)
		}
		for _, e := range c.Evidence {
			if e.Path == "" || !digestPattern.MatchString(e.SHA256) {
				failures = append(failures, "invalid evidence reference")
			}
		}
	}
	for _, name := range required {
		c, ok := seen[name]
		if !ok || !c.Required || c.Status != "pass" || !c.Measured {
			failures = append(failures, "missing or unsuccessful required check: "+name)
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// Write saves failed reports too, but never writes evidence that fails the privacy gate.
func (r Report) Write(path string, required []string, canaries []string) (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := RedactCheck(data, canaries); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest), r.Validate(required)
}
