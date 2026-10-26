package onstar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Command string

const (
	Start       Command = "start"
	CancelStart Command = "cancelStart"
	LockDoor    Command = "lockDoor"
	UnlockDoor  Command = "unlockDoor"
	LockTrunk   Command = "lockTrunk"
	UnlockTrunk Command = "unlockTrunk"
	Alert       Command = "alert"
	CancelAlert Command = "cancelAlert"
	FlashLights Command = "flashLights"
	StopLights  Command = "stopLights"
)

type CommandOptions struct {
	CabinTemperature *int
	Delay            *int
	Duration         *int
	Actions          []string
}

type CommandResult struct {
	State               string
	RequestID           string
	RequestTime         time.Time
	StatusURL           string
	Reason              string
	URLClass            string
	ProviderStatusClass string
	HTTPStatusClass     string
}

// Submit sends one v3 request. The caller must persist dispatch intent and check
// owner authority, capability, confirmation and kill switches before calling.
// No error permits an automatic second submission or a v1 fallback.
func (c *Client) Submit(ctx context.Context, token GMToken, vin string, command Command, options CommandOptions) (CommandResult, error) {
	if err := c.commandToken(token, vin); err != nil {
		return CommandResult{State: "failed", Reason: "not-submitted"}, err
	}
	path, body, err := commandRequest(command, options)
	if err != nil {
		return CommandResult{State: "failed", Reason: "not-submitted"}, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return CommandResult{State: "failed", Reason: "not-submitted"}, failure("invalid-command-options")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var response json.RawMessage
	if err := c.request(ctx, http.MethodPost, APIBase+"/veh/cmd/v3/"+path+"/"+vin, "application/json; charset=UTF-8", token.AccessToken, strings.NewReader(string(encoded)), &response); err != nil {
		result := CommandResult{State: "outcome-unknown", Reason: "submission-unconfirmed"}
		if typed, ok := err.(*Error); ok && typed.Status >= 400 && typed.Status < 500 {
			result.State = "failed"
			result.Reason = "provider-rejected"
		}
		return result, err
	}
	result, err := parseCommandResult(response)
	result.HTTPStatusClass = "2xx"
	return result, err
}

// CommandStatus performs one GET for an already submitted request. Poll cadence,
// retry budget and overall deadlines belong to the worker, not this transport.
func (c *Client) CommandStatus(ctx context.Context, token GMToken, vin, requestID, statusURL string) (CommandResult, error) {
	if err := c.commandToken(token, vin); err != nil {
		return CommandResult{State: "outcome-unknown", Reason: "status-unavailable"}, err
	}
	endpoint, urlClass, err := commandStatusReference(statusURL)
	if err != nil || requestID == "" {
		return CommandResult{State: "outcome-unknown", Reason: "invalid-status-reference", URLClass: urlClass}, failure("invalid-status-reference")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var response json.RawMessage
	if err := c.request(ctx, http.MethodGet, endpoint, "application/json; charset=UTF-8", token.AccessToken, nil, &response); err != nil {
		return CommandResult{State: "outcome-unknown", RequestID: requestID, Reason: "status-unavailable"}, err
	}
	result, err := parseCommandResult(response)
	result.HTTPStatusClass = "2xx"
	if result.RequestID != "" && result.RequestID != requestID {
		result.State = "outcome-unknown"
		result.RequestID = requestID
		result.Reason = "status-request-mismatch"
		return result, failure("status-request-mismatch")
	}
	result.RequestID = requestID
	return result, err
}

func (c *Client) commandToken(token GMToken, vin string) error {
	if !token.VehicleLinked || token.VIN != vin || !validVIN(vin) {
		return failure("vehicle-not-linked")
	}
	if token.AccessToken == "" || token.ExpiresAt <= c.now().Unix() {
		return failure("reauth-required")
	}
	return nil
}

func commandRequest(command Command, options CommandOptions) (string, map[string]any, error) {
	body := map[string]any{}
	path := string(command)
	delay := 0
	if options.Delay != nil {
		delay = *options.Delay
	}
	if delay < 0 {
		return "", nil, failure("invalid-command-options")
	}
	alert := command == Alert || command == FlashLights
	door := command == LockDoor || command == UnlockDoor || command == LockTrunk || command == UnlockTrunk
	if options.CabinTemperature != nil && command != Start || options.Delay != nil && !alert && !door || options.Duration != nil && !alert || len(options.Actions) > 0 && !alert {
		return "", nil, failure("invalid-command-options")
	}
	switch command {
	case Start:
		if options.CabinTemperature != nil {
			body["cabinTemperature"] = *options.CabinTemperature
		}
	case CancelStart, CancelAlert:
	case StopLights:
		path = "cancelAlert"
	case LockDoor, UnlockDoor, LockTrunk, UnlockTrunk:
		if command == LockDoor {
			path = "lock"
		}
		if command == UnlockDoor {
			path = "unlock"
		}
		body[string(command)+"Request"] = map[string]int{"delay": delay}
	case Alert, FlashLights:
		path = "alert"
		actions := options.Actions
		if len(actions) == 0 {
			actions = []string{"Honk", "Flash"}
			if command == FlashLights {
				actions = []string{"Flash"}
			}
		}
		seen := map[string]bool{}
		for _, action := range actions {
			if action != "Honk" && action != "Flash" || seen[action] {
				return "", nil, failure("invalid-command-options")
			}
			seen[action] = true
		}
		duration := 1
		if options.Duration != nil {
			duration = *options.Duration
		}
		if duration <= 0 {
			return "", nil, failure("invalid-command-options")
		}
		body = map[string]any{"action": actions, "delay": delay, "duration": duration}
		// Deliberately omit upstream DoorOpen/IgnitionOn safety overrides.
	default:
		return "", nil, failure("unsupported-command")
	}
	return path, body, nil
}

func commandStatusURL(value string) (string, error) {
	endpoint, _, err := commandStatusReference(value)
	return endpoint, err
}
func commandStatusReference(value string) (string, string, error) {
	base, _ := url.Parse(APIBase)
	if value == "" {
		return "", "missing", failure("invalid-status-url")
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", "invalid", failure("invalid-status-url")
	}
	u = base.ResolveReference(u)
	// HTTPS without a port and HTTPS :443 are the same origin. Canonicalize
	// only that exact host/default-port pair; all other origin checks remain.
	if u.Scheme == "https" && u.Hostname() == base.Hostname() && u.Port() == "443" {
		u.Host = base.Host
	}
	class := "valid"
	switch {
	case u.Scheme != "https":
		class = "scheme"
	case u.Hostname() != base.Hostname():
		class = "host"
	case u.Port() != base.Port():
		class = "port"
	case u.Host != base.Host:
		class = "host"
	case u.User != nil:
		class = "userinfo"
	case u.Fragment != "":
		class = "fragment"
	case u.Path == "" || u.Path == "/":
		class = "root"
	}
	if class != "valid" {
		return "", class, failure("invalid-status-url")
	}
	return u.String(), class, nil
}
func providerStatusClass(status string) string {
	switch strings.ToLower(status) {
	case "success", "completed", "complete":
		return "success"
	case "failure", "failed", "error":
		return "failure"
	case "inprogress", "in_progress", "pending":
		return "pending"
	case "":
		return "missing"
	}
	return "unknown"
}

func parseCommandResult(data []byte) (CommandResult, error) {
	return parseCommandEnvelope(data, false)
}
func parseCommandEnvelope(data []byte, nested bool) (CommandResult, error) {
	var envelope struct {
		RequestID       string          `json:"requestId"`
		RequestTime     string          `json:"requestTime"`
		Status          string          `json:"status"`
		URL             string          `json:"url"`
		Error           json.RawMessage `json:"error"`
		CommandResponse json.RawMessage `json:"commandResponse"`
	}
	unknown := CommandResult{State: "outcome-unknown", Reason: "invalid-command-response"}
	if json.Unmarshal(data, &envelope) != nil {
		return unknown, failure("invalid-command-response")
	}
	if len(envelope.CommandResponse) > 0 && string(envelope.CommandResponse) != "null" {
		if envelope.Status != "" || nested {
			unknown.Reason = "ambiguous-command-response"
			return unknown, failure("ambiguous-command-response")
		}
		return parseCommandEnvelope(envelope.CommandResponse, true)
	}
	endpoint, urlClass, urlErr := commandStatusReference(envelope.URL)
	result := CommandResult{State: "outcome-unknown", RequestID: envelope.RequestID, Reason: "unknown-command-status", URLClass: urlClass, ProviderStatusClass: providerStatusClass(envelope.Status)}
	if envelope.RequestTime != "" {
		stamp, err := time.Parse(time.RFC3339Nano, envelope.RequestTime)
		if err != nil {
			result.Reason = "invalid-command-time"
			return result, failure("invalid-command-time")
		}
		result.RequestTime = stamp
	}
	if envelope.URL != "" {
		if urlErr != nil {
			result.Reason = "invalid-status-url"
			return result, urlErr
		}
		result.StatusURL = endpoint
	}
	switch strings.ToLower(envelope.Status) {
	case "success", "completed", "complete":
		result.State = "succeeded"
		result.Reason = "provider-confirmed"
	case "failure", "failed", "error":
		result.State = "failed"
		result.Reason = "provider-failure"
	case "inprogress", "in_progress", "pending":
		result.State = "submitted"
		result.Reason = "provider-pending"
	default:
		result.Reason = "unknown-command-status"
		return result, failure("unknown-command-status")
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" && string(envelope.Error) != "{}" {
		result.State = "failed"
		result.Reason = "provider-failure"
	}
	if result.State == "submitted" && (result.RequestID == "" || result.StatusURL == "" || result.RequestTime.IsZero()) {
		result.State = "outcome-unknown"
		result.Reason = "incomplete-command-response"
		return result, failure("incomplete-command-response")
	}
	return result, nil
}
