package ownerclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ServiceURL = "https://suburban.jeremy.ninja"

var ErrPending = errors.New("sign-in pending")
var ErrUnauthorized = errors.New("please sign in again")

type RequestError struct{ StatusCode int }

func (e *RequestError) Error() string { return "The service could not complete this request." }

type Client struct {
	BaseURL  string
	HTTP     *http.Client
	mapCache mapImageCache
}

func NewClient() *Client {
	return &Client{BaseURL: ServiceURL, HTTP: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type Login struct {
	Code                string    `json:"code"`
	BrowserURL          string    `json:"browserUrl"`
	ExpiresAt           time.Time `json:"expiresAt"`
	PollIntervalSeconds int       `json:"pollIntervalSeconds"`
}
type Session struct {
	AccessHandle    string    `json:"accessHandle"`
	RefreshHandle   string    `json:"refreshHandle"`
	AccessExpiresAt time.Time `json:"accessExpiresAt"`
}
type Capability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}
type Dashboard struct {
	ProviderState   string       `json:"providerState"`
	LastProofReason string       `json:"lastProofReason"`
	LocationPending bool         `json:"locationPending"`
	StatusReadAt    time.Time    `json:"statusReadAt"`
	PollingEnabled  bool         `json:"pollingEnabled"`
	ControlsEnabled bool         `json:"controlsEnabled"`
	Capabilities    []Capability `json:"capabilities"`
}

func (c *Client) Dashboard(ctx context.Context, s Session) (Dashboard, error) {
	var d Dashboard
	e := c.Request(ctx, "GET", "/api/dashboard", s.AccessHandle, nil, &d)
	return d, e
}

type PendingLogin struct {
	Login    Login
	Verifier string
}
type SavedState struct {
	PhoneTracking     PhoneTrackingState `json:"phoneTracking,omitempty"`
	LocationRefreshID string             `json:"locationRefreshId,omitempty"`
	Session           Session
	Pending           *PendingLogin
	Command           *CommandIntent
	Verifications     []CommandVerification
}

// CommandIntent is saved before any submission. Retries reuse this ID, including
// after process death, so a lost HTTP response cannot become a second actuation.
type CommandIntent struct {
	RequestID string                  `json:"requestId"`
	Action    string                  `json:"action"`
	CreatedAt time.Time               `json:"createdAt"`
	Options   contract.CommandOptions `json:"options,omitempty"`
}

type CommandResult struct {
	State     string `json:"state"`
	Reason    string `json:"reason"`
	Action    string `json:"action"`
	CommandID string `json:"commandId"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (r CommandResult) Terminal() bool {
	switch r.State {
	case "succeeded", "failed", "expired", "outcome-unknown", "dry-run":
		return true
	case "rejected":
		return definitiveRejection(r.Reason)
	}
	return false
}
func (c *Client) SubmitCommand(ctx context.Context, s Session, intent CommandIntent) (CommandResult, error) {
	var r CommandResult
	e := c.Request(ctx, "POST", "/api/commands", s.AccessHandle, struct {
		Action    string                  `json:"action"`
		RequestID string                  `json:"requestId"`
		Options   contract.CommandOptions `json:"options"`
	}{intent.Action, intent.RequestID, intent.Options}, &r)
	if e == nil {
		e = validateCommandResult(r, intent.Action)
	}
	return r, e
}

func (c *Client) Features(ctx context.Context, s Session) (contract.FeaturesResponse, error) {
	var result contract.FeaturesResponse
	err := c.Request(ctx, "GET", "/api/features", s.AccessHandle, nil, &result)
	return result, err
}

type ReadEnvelope struct {
	State    string                    `json:"state"`
	Reason   string                    `json:"reason"`
	Snapshot *contract.VehicleSnapshot `json:"snapshot"`
}

func (c *Client) VehicleRead(ctx context.Context, s Session, id string, refresh bool) (contract.VehicleSnapshot, error) {
	var result ReadEnvelope
	method, path := "GET", "/api/vehicle/read/status?featureId="+url.QueryEscape(id)
	var body any
	if refresh {
		requestID, _, err := NewVerifier()
		if err != nil {
			return contract.VehicleSnapshot{}, err
		}
		method, path = "POST", "/api/vehicle/read"
		body = map[string]string{"featureId": id, "requestId": requestID}
	}
	if err := c.Request(ctx, method, path, s.AccessHandle, body, &result); err != nil {
		return contract.VehicleSnapshot{}, err
	}
	if result.Snapshot == nil {
		return contract.VehicleSnapshot{FeatureID: id, State: "unavailable", Reason: result.Reason, Freshness: "unknown"}, nil
	}
	if result.Snapshot.FeatureID != id {
		return contract.VehicleSnapshot{}, errors.New("Vehicle response did not match the requested feature.")
	}
	return *result.Snapshot, nil
}
func (c *Client) CommandStatus(ctx context.Context, s Session, id string) (CommandResult, error) {
	var r CommandResult
	e := c.Request(ctx, "GET", "/api/commands/status?requestId="+url.QueryEscape(id), s.AccessHandle, nil, &r)
	if e == nil {
		e = validateCommandResult(r, "")
	}
	return r, e
}
func validateCommandResult(r CommandResult, action string) error {
	switch r.State {
	case "queued", "accepted", "dispatching", "submitted", "succeeded", "failed", "expired", "outcome-unknown", "rejected", "not-found", "dry-run":
	default:
		return errors.New("Invalid command response; outcome not confirmed.")
	}
	if r.State == "rejected" && !definitiveRejection(r.Reason) {
		return errors.New("Command authorization failed; the previous request outcome is not confirmed.")
	}
	if action != "" && r.Action != action {
		return errors.New("Command response did not match this request.")
	}
	return nil
}

func definitiveRejection(reason string) bool {
	switch reason {
	case "controls-disabled", "provider-paused", "capability-unverified":
		return true
	}
	return false
}

func NewVerifier() (string, string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", "", e
	}
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(h[:]), nil
}
func (c *Client) Request(ctx context.Context, method, path, handle string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return errors.New("request unavailable")
		}
		reader = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if e != nil {
		return errors.New("request unavailable")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if handle != "" {
		req.Header.Set("Authorization", "Bearer "+handle)
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Service unavailable. Check your connection and try again.")
	}
	defer resp.Body.Close()
	commandResponse := strings.HasPrefix(path, "/api/commands")
	if resp.StatusCode == http.StatusAccepted && !commandResponse && !strings.HasPrefix(path, "/api/map/refresh") {
		return ErrPending
	}
	if resp.StatusCode == 401 {
		return ErrUnauthorized
	}
	if resp.StatusCode == 410 {
		if strings.HasPrefix(path, "/auth/session/") || strings.HasPrefix(path, "/api/") {
			return ErrUnauthorized
		}
		return errors.New("Sign-in expired. Start again.")
	}
	if (resp.StatusCode < 200 || resp.StatusCode >= 300) && !(commandResponse && resp.StatusCode == 403) {
		return &RequestError{StatusCode: resp.StatusCode}
	}
	if out != nil {
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
		if e != nil || len(raw) > 65536 || json.Unmarshal(raw, out) != nil {
			return errors.New("Invalid service response.")
		}
	}
	return nil
}
func (c *Client) Start(ctx context.Context, challenge string) (Login, error) {
	var r Login
	e := c.Request(ctx, "POST", "/auth/mobile/start", "", map[string]string{"challenge": challenge, "deviceLabel": "Suburban Remote Android"}, &r)
	if e == nil {
		u, x := url.Parse(r.BrowserURL)
		if x != nil || u.Scheme != "https" || u.Host != "suburban.jeremy.ninja" || u.Path != "/auth/mobile/begin" || r.Code == "" {
			e = errors.New("Invalid sign-in response.")
		}
	}
	return r, e
}
func (c *Client) Poll(ctx context.Context, p PendingLogin) (Session, error) {
	var s Session
	e := c.Request(ctx, "POST", "/auth/mobile/poll", "", map[string]string{"code": p.Login.Code, "verifier": p.Verifier}, &s)
	if e == nil && (s.AccessHandle == "" || s.RefreshHandle == "") {
		e = errors.New("Invalid sign-in response.")
	}
	return s, e
}
func (c *Client) Cancel(ctx context.Context, p PendingLogin) error {
	return c.Request(ctx, "POST", "/auth/mobile/cancel", "", map[string]string{"code": p.Login.Code, "verifier": p.Verifier}, nil)
}
func (c *Client) Refresh(ctx context.Context, s Session) (Session, error) {
	var r Session
	e := c.Request(ctx, "POST", "/auth/session/refresh", "", map[string]string{"refreshHandle": s.RefreshHandle}, &r)
	if e == nil && (r.AccessHandle == "" || r.RefreshHandle == "") {
		e = errors.New("Invalid session response.")
	}
	return r, e
}
func (c *Client) Logout(ctx context.Context, s Session) error {
	c.clearMapImage()
	return c.Request(ctx, "POST", "/auth/session/logout", s.AccessHandle, nil, nil)
}
