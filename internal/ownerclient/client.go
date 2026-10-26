// Package ownerclient is the authenticated HTTPS client for the operator report
// exporter. It never owns GM or Amazon credentials.
package ownerclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/securelocal"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

const Origin = "https://suburban.jeremy.ninja"

var ErrSignIn = errors.New("owner sign-in required; run scripts/owner-login.bat")

type Session struct {
	AccessHandle    string    `json:"accessHandle"`
	RefreshHandle   string    `json:"refreshHandle"`
	AccessExpiresAt time.Time `json:"accessExpiresAt"`
}
type Login struct {
	Code                string    `json:"code"`
	BrowserURL          string    `json:"browserUrl"`
	ExpiresAt           time.Time `json:"expiresAt"`
	PollIntervalSeconds int       `json:"pollIntervalSeconds"`
}
type Store interface {
	Load() (Session, error)
	Save(Session) error
}
type DPAPIStore struct{ Path string }

func DefaultStore() (DPAPIStore, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return DPAPIStore{}, ErrSignIn
	}
	return DPAPIStore{filepath.Join(home, ".suburban-remote", "owner-session.dpapi")}, nil
}
func (s DPAPIStore) Load() (Session, error) {
	var out Session
	raw, e := os.ReadFile(s.Path)
	if e != nil {
		return out, ErrSignIn
	}
	plain, e := securelocal.Unprotect(raw)
	if e != nil {
		return out, ErrSignIn
	}
	defer clear(plain)
	if json.Unmarshal(plain, &out) != nil || out.RefreshHandle == "" {
		return Session{}, ErrSignIn
	}
	return out, nil
}
func (s DPAPIStore) Save(value Session) error {
	plain, e := json.Marshal(value)
	if e != nil {
		return ErrSignIn
	}
	defer clear(plain)
	sealed, e := securelocal.Protect(plain)
	if e != nil {
		return ErrSignIn
	}
	if os.MkdirAll(filepath.Dir(s.Path), 0700) != nil {
		return ErrSignIn
	}
	temp, e := os.CreateTemp(filepath.Dir(s.Path), "session-*.dpapi")
	if e != nil {
		return ErrSignIn
	}
	name := temp.Name()
	defer os.Remove(name)
	if temp.Chmod(0600) != nil {
		temp.Close()
		return ErrSignIn
	}
	_, e = temp.Write(sealed)
	closeErr := temp.Close()
	if e != nil || closeErr != nil {
		return ErrSignIn
	}
	if os.Rename(name, s.Path) != nil {
		return ErrSignIn
	}
	return nil
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Store   Store
	mu      sync.Mutex
}

func New(store Store) *Client {
	return &Client{BaseURL: Origin, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Store: store}
}
func (c *Client) request(ctx context.Context, method, path, handle string, input, out any) (int, error) {
	var body io.Reader
	if input != nil {
		data, e := json.Marshal(input)
		if e != nil {
			return 0, errors.New("request invalid")
		}
		body = bytes.NewReader(data)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if e != nil {
		return 0, errors.New("request unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	if handle != "" {
		req.Header.Set("Authorization", "Bearer "+handle)
	}
	res, e := c.HTTP.Do(req)
	if e != nil {
		return 0, errors.New("owner service unavailable; no automatic retry")
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 410 {
		return res.StatusCode, ErrSignIn
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 128*1024+1))
	if e != nil || len(raw) > 128*1024 {
		return res.StatusCode, errors.New("owner response invalid")
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return res.StatusCode, errors.New("owner response invalid")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return res.StatusCode, errors.New("owner request not confirmed; inspect status before retrying")
	}
	return res.StatusCode, nil
}
func (c *Client) authorized(ctx context.Context, method, path string, input, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Store == nil {
		return ErrSignIn
	}
	session, e := c.Store.Load()
	if e != nil {
		return ErrSignIn
	}
	if !session.AccessExpiresAt.After(time.Now().Add(time.Minute)) {
		var next Session
		if _, e = c.request(ctx, "POST", "/auth/session/refresh", "", map[string]string{"refreshHandle": session.RefreshHandle}, &next); e != nil {
			return e
		}
		if next.AccessHandle == "" || next.RefreshHandle == "" {
			return ErrSignIn
		}
		if e = c.Store.Save(next); e != nil {
			return e
		}
		session = next
	}
	_, e = c.request(ctx, method, path, session.AccessHandle, input, out)
	return e
}
func (c *Client) Features(ctx context.Context) (contract.FeaturesResponse, error) {
	var out contract.FeaturesResponse
	e := c.authorized(ctx, "GET", "/api/features", nil, &out)
	return out, e
}
func (c *Client) Dashboard(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	e := c.authorized(ctx, "GET", "/api/dashboard", nil, &out)
	return out, e
}
func (c *Client) Status(ctx context.Context, id string) (contract.AlexaCommandResponse, error) {
	var out contract.AlexaCommandResponse
	path := "/api/commands/status"
	if id != "" {
		path += "?requestId=" + url.QueryEscape(id)
	}
	e := c.authorized(ctx, "GET", path, nil, &out)
	return out, e
}
func (c *Client) Read(ctx context.Context, id string, trace bool) (contract.AlexaCommandResponse, error) {
	var out contract.AlexaCommandResponse
	e := c.authorized(ctx, "POST", "/api/vehicle/read", map[string]any{"featureId": id, "trace": trace}, &out)
	return out, e
}
func (c *Client) Submit(ctx context.Context, id, requestID string, options contract.CommandOptions) (contract.AlexaCommandResponse, error) {
	var out contract.AlexaCommandResponse
	e := c.authorized(ctx, "POST", "/api/commands", map[string]any{"action": id, "requestId": requestID, "options": options}, &out)
	return out, e
}

// Login is attended: present opens a validated URL locally, never in protocol output.
func (c *Client) Login(ctx context.Context, present func(string) error) error {
	raw := make([]byte, 32)
	if _, e := rand.Read(raw); e != nil {
		return ErrSignIn
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256(raw)
	clear(raw)
	var login Login
	if _, e := c.request(ctx, "POST", "/auth/mobile/start", "", map[string]string{"challenge": base64.RawURLEncoding.EncodeToString(hash[:]), "deviceLabel": "Suburban local tools"}, &login); e != nil {
		return e
	}
	address, e := url.Parse(login.BrowserURL)
	if e != nil || address.Scheme != "https" || address.Host != "suburban.jeremy.ninja" || address.Path != "/auth/mobile/begin" || address.Query().Get("code") != login.Code || login.Code == "" {
		return errors.New("owner login URL invalid")
	}
	if e = present(login.BrowserURL); e != nil {
		return e
	}
	timer := time.NewTicker(3 * time.Second)
	defer timer.Stop()
	for time.Now().Before(login.ExpiresAt) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		var session Session
		status, e := c.request(ctx, "POST", "/auth/mobile/poll", "", map[string]string{"code": login.Code, "verifier": verifier}, &session)
		if status == 202 {
			continue
		}
		if e != nil {
			return e
		}
		if session.AccessHandle == "" || session.RefreshHandle == "" {
			return ErrSignIn
		}
		return c.Store.Save(session)
	}
	return errors.New("owner login expired")
}
