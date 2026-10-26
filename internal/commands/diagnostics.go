package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/aws/aws-lambda-go/lambdacontext"
)

// CommandDiagnostic is the entire provider-operation log contract. It excludes
// provider identifiers, URLs, bodies, account identities, and credential values.
type CommandDiagnostic struct {
	Timestamp            time.Time `json:"ts"`
	Level                string    `json:"level"`
	Service              string    `json:"svc"`
	Attempt              int       `json:"attempt"`
	RequestID            string    `json:"requestId,omitempty"`
	Kind                 string    `json:"kind"`
	Operation            string    `json:"op"`
	CommandID            string    `json:"commandId"`
	State                string    `json:"outcome"`
	Reason               string    `json:"reason"`
	DurationMilliseconds int64     `json:"durationMs"`
	HasProviderID        bool      `json:"hasProviderId"`
	HasStatusURL         bool      `json:"hasStatusUrl"`
	ErrorKind            string    `json:"errorKind"`
	HTTPStatusClass      string    `json:"httpStatusClass"`
	URLClass             string    `json:"urlClass"`
	ProviderStatusClass  string    `json:"providerStatusClass"`
}

func safeCommandReason(value string) string {
	switch value {
	case "not-submitted", "submission-unconfirmed", "provider-rejected", "provider-pending", "provider-confirmed", "provider-failure", "invalid-command-response", "ambiguous-command-response", "invalid-command-time", "invalid-status-url", "unknown-command-status", "unknown-provider-status", "incomplete-command-response", "incomplete-provider-acceptance", "status-unavailable", "invalid-status-reference", "status-request-mismatch", "status-unconfirmed":
		return value
	}
	return "provider-outcome-unclassified"
}
func safeErrorKind(err error) string {
	if err == nil {
		return "none"
	}
	var e *onstar.Error
	if !errors.As(err, &e) {
		return "unclassified"
	}
	switch e.Kind {
	case "vehicle-not-linked", "reauth-required", "invalid-command-options", "unsupported-command", "invalid-status-reference", "invalid-status-url", "invalid-command-response", "ambiguous-command-response", "invalid-command-time", "unknown-command-status", "incomplete-command-response", "status-request-mismatch", "provider-error", "access-denied", "rate-limited", "deadline", "network-unavailable", "invalid-response", "invalid-request":
		return e.Kind
	}
	return "unclassified"
}
func safeURLClass(value string) string {
	switch value {
	case "missing", "valid", "invalid", "scheme", "host", "port", "userinfo", "fragment", "root":
		return value
	}
	return "unknown"
}
func safeProviderStatus(value string) string {
	switch value {
	case "success", "failure", "pending", "missing", "unknown":
		return value
	}
	return "unknown"
}
func safeHTTPClass(value string) string {
	switch value {
	case "2xx", "3xx", "4xx", "5xx":
		return value
	}
	return "none"
}

var lambdaRequestIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func (s *Service) observe(ctx context.Context, operation, id string, result onstar.CommandResult, err error, started time.Time) {
	if !commandIDPattern.MatchString(id) || (operation != "submit" && operation != "status") {
		return
	}
	state := "outcome-unknown"
	switch result.State {
	case "submitted", "succeeded", "failed", "outcome-unknown":
		state = result.State
	}
	httpClass := safeHTTPClass(result.HTTPStatusClass)
	var providerError *onstar.Error
	if errors.As(err, &providerError) && providerError.Status >= 200 && providerError.Status < 600 {
		httpClass = map[int]string{2: "2xx", 3: "3xx", 4: "4xx", 5: "5xx"}[providerError.Status/100]
	}
	duration := s.now().Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	entry := CommandDiagnostic{Kind: "vehicle-command", Operation: operation, CommandID: id, State: state, Reason: safeCommandReason(result.Reason), DurationMilliseconds: duration, HasProviderID: result.RequestID != "", HasStatusURL: result.StatusURL != "", ErrorKind: safeErrorKind(err), HTTPStatusClass: httpClass, URLClass: safeURLClass(result.URLClass), ProviderStatusClass: safeProviderStatus(result.ProviderStatusClass)}
	entry.Timestamp = s.now()
	entry.Level = "INFO"
	entry.Service = "worker"
	entry.Attempt = 1
	if err != nil || state == "failed" || state == "outcome-unknown" {
		entry.Level = "WARN"
	}
	if lc, ok := lambdacontext.FromContext(ctx); ok && lambdaRequestIDPattern.MatchString(lc.AwsRequestID) {
		entry.RequestID = lc.AwsRequestID
	}
	if s.LogCommand != nil {
		s.LogCommand(entry)
		return
	}
	encoded, _ := json.Marshal(entry)
	fmt.Println(string(encoded))
}
