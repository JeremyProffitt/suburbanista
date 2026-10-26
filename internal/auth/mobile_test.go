package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
)

type memoryDocuments struct {
	mu    sync.Mutex
	items map[string]Document
}

func (m *memoryDocuments) Load(_ context.Context, key string) (Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.items[key]
	if !ok {
		return Document{}, ErrMissing
	}
	d.Data = append([]byte(nil), d.Data...)
	return d, nil
}
func (m *memoryDocuments) Commit(_ context.Context, docs ...Document) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range docs {
		existing, ok := m.items[d.Key]
		if ok && existing.Version != d.Version || !ok && d.Version != 0 {
			return ErrConflict
		}
	}
	for _, d := range docs {
		d.Version++
		d.Data = append([]byte(nil), d.Data...)
		m.items[d.Key] = d
	}
	return nil
}

type syntheticAuthority struct {
	owner       string
	now         *time.Time
	state, pkce string
	calls       int
	expired     bool
	store       Documents
	t           *testing.T
	familyID    string
}

func (a *syntheticAuthority) AuthorizationURL(state, pkce string) string {
	a.state = state
	a.pkce = pkce
	return "https://www.amazon.com/ap/oa"
}
func (a *syntheticAuthority) ExchangeCode(context.Context, string, string) (amazon.Tokens, error) {
	a.calls++
	return amazon.Tokens{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: a.now.Add(time.Hour)}, nil
}
func (a *syntheticAuthority) Validate(_ context.Context, access string) (amazon.Identity, error) {
	a.calls++
	if a.expired && access == "synthetic-access" {
		return amazon.Identity{}, &amazon.Error{Outcome: amazon.InvalidAccessToken}
	}
	return amazon.Identity{Subject: a.owner, ValidatedAt: *a.now, ExpiresAt: a.now.Add(time.Hour)}, nil
}
func (a *syntheticAuthority) Refresh(context.Context, string) (amazon.Tokens, error) {
	a.calls++
	return amazon.Tokens{AccessToken: "synthetic-new-access", RefreshToken: "synthetic-new-refresh", ExpiresAt: a.now.Add(time.Hour)}, nil
}
func (a *syntheticAuthority) ValidateRefreshed(ctx context.Context, access string) (amazon.Identity, error) {
	if a.familyID != "" {
		doc, e := a.store.Load(ctx, "family#"+a.familyID)
		if e != nil {
			a.t.Fatal(e)
		}
		family, _ := decodeDocument[familyDocument](doc)
		if family.Tokens.RefreshToken != "synthetic-new-refresh" {
			a.t.Fatal("validated before durable refresh save")
		}
	}
	return a.Validate(ctx, access)
}
func mobileFixture(t *testing.T, docs Documents) (*MobileService, *syntheticAuthority, *time.Time) {
	t.Helper()
	if docs == nil {
		docs = &memoryDocuments{items: map[string]Document{}}
	}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	a := &syntheticAuthority{owner: "synthetic-owner", now: &now, store: docs, t: t}
	return &MobileService{Store: docs, Amazon: a, Owner: a.owner, Now: func() time.Time { return now }}, a, &now
}
func startFixture(t *testing.T, s *MobileService) (MobileStart, string) {
	t.Helper()
	verifier, e := randomHandle()
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(verifier)
	digest := sha256.Sum256(raw)
	start, e := s.Start(context.Background(), base64.RawURLEncoding.EncodeToString(digest[:]), "synthetic-phone", "192.0.2.1")
	if e != nil {
		t.Fatal(e)
	}
	return start, verifier
}
func loginFixture(t *testing.T, s *MobileService, a *syntheticAuthority) MobileSession {
	t.Helper()
	start, verifier := startFixture(t, s)
	_, cookie, e := s.Begin(context.Background(), start.Code)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Callback(context.Background(), a.state, "synthetic-code", cookie); e != nil {
		t.Fatal(e)
	}
	session, e := s.Poll(context.Background(), start.Code, verifier, "192.0.2.1")
	if e != nil {
		t.Fatal(e)
	}
	return session
}
func mobileConformance(t *testing.T, docs Documents) {
	t.Helper()
	s, a, now := mobileFixture(t, docs)
	ctx := context.Background()
	session := loginFixture(t, s, a)
	familyID, e := s.Authorize(ctx, session.AccessHandle)
	if e != nil {
		t.Fatal("issued access rejected", e)
	}
	a.familyID = familyID
	a.expired = true
	rotated, e := s.Refresh(ctx, session.RefreshHandle)
	if e != nil {
		t.Fatal("refresh failed", e)
	}
	if rotated.RefreshHandle == session.RefreshHandle || rotated.AccessHandle == session.AccessHandle {
		t.Fatal("session handles did not rotate")
	}
	if _, e = s.Refresh(ctx, session.RefreshHandle); !errors.Is(e, ErrRefreshRace) {
		t.Fatal("concurrent refresh not rejected", e)
	}
	*now = now.Add(11 * time.Second)
	if _, e = s.Refresh(ctx, session.RefreshHandle); !errors.Is(e, ErrRevoked) {
		t.Fatal("superseded handle did not revoke", e)
	}
	if _, e = s.Authorize(ctx, rotated.AccessHandle); !errors.Is(e, ErrRevoked) {
		t.Fatal("revoked family still authorized", e)
	}
	a.expired = false
	another := loginFixture(t, s, a)
	if e = s.Logout(ctx, another.AccessHandle); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authorize(ctx, another.AccessHandle); !errors.Is(e, ErrRevoked) {
		t.Fatal("logout did not revoke", e)
	}
}
func TestMobileSessionConformance(t *testing.T) { mobileConformance(t, nil) }
func TestMobileHandoffBindingReplayAndCancellation(t *testing.T) {
	s, a, _ := mobileFixture(t, nil)
	ctx := context.Background()
	start, verifier := startFixture(t, s)
	wrong, _ := randomHandle()
	if _, e := s.Poll(ctx, start.Code, wrong, "192.0.2.1"); !errors.Is(e, ErrInvalid) {
		t.Fatal("wrong verifier accepted")
	}
	if _, e := s.Poll(ctx, start.Code, verifier, "192.0.2.1"); !errors.Is(e, ErrPending) {
		t.Fatal("pending not reported")
	}
	_, cookie, e := s.Begin(ctx, start.Code)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Callback(ctx, a.state, "synthetic-code", wrong); !errors.Is(e, ErrInvalid) {
		t.Fatal("wrong browser accepted")
	}
	if a.calls != 0 {
		t.Fatal("unbound browser contacted Amazon")
	}
	if e = s.Callback(ctx, a.state, "synthetic-code", cookie); e != nil {
		t.Fatal(e)
	}
	if e = s.Callback(ctx, a.state, "synthetic-code", cookie); !errors.Is(e, ErrExpired) {
		t.Fatal("callback replay accepted")
	}
	session, e := s.Poll(ctx, start.Code, verifier, "192.0.2.1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Poll(ctx, start.Code, verifier, "192.0.2.1"); !errors.Is(e, ErrExpired) {
		t.Fatal("poll replay returned session")
	}
	familyID, e := s.Authorize(ctx, session.AccessHandle)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Cancel(ctx, start.Code, verifier); e != nil {
		t.Fatal("consumed handoff cancel failed", e)
	}
	if _, e = s.Authorize(ctx, session.AccessHandle); !errors.Is(e, ErrRevoked) {
		t.Fatal("consumed cancel left orphan session")
	}
	fdoc, e := s.Store.Load(ctx, "family#"+familyID)
	if e != nil {
		t.Fatal(e)
	}
	family, _ := decodeDocument[familyDocument](fdoc)
	if family.Tokens.RefreshToken != "" {
		t.Fatal("canceled grant retained refresh token")
	}
	if e = s.Cancel(ctx, start.Code, verifier); e != nil {
		t.Fatal("cancel not idempotent")
	}
	next, v := startFixture(t, s)
	if e = s.Cancel(ctx, next.Code, v); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Begin(ctx, next.Code); !errors.Is(e, ErrExpired) {
		t.Fatal("canceled login opened")
	}
}
func TestMobileOwnerExpiryAndRateLimits(t *testing.T) {
	s, a, now := mobileFixture(t, nil)
	ctx := context.Background()
	start, v := startFixture(t, s)
	_, cookie, e := s.Begin(ctx, start.Code)
	if e != nil {
		t.Fatal(e)
	}
	a.owner = "different-owner"
	if e = s.Callback(ctx, a.state, "synthetic-code", cookie); !errors.Is(e, ErrNotOwner) {
		t.Fatal("nonowner authenticated")
	}
	*now = now.Add(11 * time.Minute)
	if _, e = s.Poll(ctx, start.Code, v, "192.0.2.1"); !errors.Is(e, ErrExpired) {
		t.Fatal("expired handoff accepted")
	}
	for i := 0; i < 5; i++ {
		startFixture(t, s)
	}
	raw := make([]byte, 32)
	if _, e = s.Start(ctx, base64.RawURLEncoding.EncodeToString(raw), "phone", "192.0.2.1"); !errors.Is(e, ErrRate) {
		t.Fatal("start limit not enforced")
	}
}
func TestMobileAtomicConflictDoesNotPartiallyWrite(t *testing.T) {
	m := &memoryDocuments{items: map[string]Document{}}
	ctx := context.Background()
	first := withData(Document{Key: "first", ExpiresAt: time.Now().Add(time.Hour)}, "one")
	if e := m.Commit(ctx, first); e != nil {
		t.Fatal(e)
	}
	second := withData(Document{Key: "second", ExpiresAt: first.ExpiresAt}, "two")
	if e := m.Commit(ctx, first, second); !errors.Is(e, ErrConflict) {
		t.Fatal("duplicate accepted")
	}
	if _, e := m.Load(ctx, "second"); !errors.Is(e, ErrMissing) {
		t.Fatal("partial transaction")
	}
}
func TestMobileOnlyHashedHandleKeys(t *testing.T) {
	s, a, _ := mobileFixture(t, nil)
	session := loginFixture(t, s, a)
	m := s.Store.(*memoryDocuments)
	for key, doc := range m.items {
		if key == session.AccessHandle || key == session.RefreshHandle {
			t.Fatal("plaintext handle key")
		}
		if len(key) > 7 && key[:7] == "access#" {
			var value map[string]any
			_ = json.Unmarshal(doc.Data, &value)
			if _, ok := value["AccessHandle"]; ok {
				t.Fatal("access secret stored in session index")
			}
		}
	}
}
