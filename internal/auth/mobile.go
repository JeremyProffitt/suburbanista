package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
)

var ErrPending = errors.New("pending")
var ErrRate = errors.New("rate-limited")
var ErrInvalid = errors.New("invalid-request")

type Authority interface {
	AuthorizationURL(string, string) string
	ExchangeCode(context.Context, string, string) (amazon.Tokens, error)
	Validate(context.Context, string) (amazon.Identity, error)
	ValidateRefreshed(context.Context, string) (amazon.Identity, error)
	Refresh(context.Context, string) (amazon.Tokens, error)
}
type MobileService struct {
	Store  Documents
	Amazon Authority
	Owner  string
	Now    func() time.Time
}
type MobileStart struct {
	Code                string    `json:"code"`
	BrowserURL          string    `json:"browserUrl"`
	ExpiresAt           time.Time `json:"expiresAt"`
	PollIntervalSeconds int       `json:"pollIntervalSeconds"`
}
type MobileSession struct {
	Status          string    `json:"status"`
	AccessHandle    string    `json:"accessHandle"`
	RefreshHandle   string    `json:"refreshHandle"`
	AccessExpiresAt time.Time `json:"accessExpiresAt"`
}
type handoff struct {
	Challenge, DeviceLabel, Status, State, BrowserHash, PKCE string
	Issued                                                   MobileSession
	FamilyID                                                 string
	Tokens                                                   amazon.Tokens
}
type statePointer struct {
	Handoff string
	Used    bool
}
type familyDocument struct {
	Family      Family
	Tokens      amazon.Tokens
	ValidatedAt time.Time
	BusyUntil   time.Time
}
type accessDocument struct {
	FamilyID  string
	ExpiresAt time.Time
}

func (s *MobileService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func digestString(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}
func validHandle(value string) bool { _, e := handleHash(value); return e == nil }
func decodeDocument[T any](doc Document) (T, error) {
	var value T
	if json.Unmarshal(doc.Data, &value) != nil {
		return value, ErrUnavailable
	}
	return value, nil
}
func (s *MobileService) live(ctx context.Context, key string) (Document, error) {
	doc, e := s.Store.Load(ctx, key)
	if e != nil {
		return Document{}, e
	}
	if !s.now().Before(doc.ExpiresAt) {
		return Document{}, ErrExpired
	}
	return doc, nil
}
func (s *MobileService) limit(ctx context.Context, ip, kind string, max int) error {
	if ip == "" {
		return ErrInvalid
	}
	now := s.now()
	bucket := now.Unix() / 60
	key := fmt.Sprintf("rate#%s#%s#%d", kind, digestString(ip), bucket)
	doc, e := s.Store.Load(ctx, key)
	count := 0
	if e == nil {
		if json.Unmarshal(doc.Data, &count) != nil {
			return ErrUnavailable
		}
	} else if !errors.Is(e, ErrMissing) {
		return e
	} else {
		doc = Document{Key: key, ExpiresAt: time.Unix((bucket+2)*60, 0)}
	}
	if count >= max {
		return ErrRate
	}
	return s.Store.Commit(ctx, withData(doc, count+1))
}
func (s *MobileService) Start(ctx context.Context, challenge, label, ip string) (MobileStart, error) {
	decoded, e := base64.RawURLEncoding.Strict().DecodeString(challenge)
	if e != nil || len(decoded) != 32 || len(challenge) != 43 || len(label) > 128 || strings.ContainsAny(label, "\r\n\t") {
		return MobileStart{}, ErrInvalid
	}
	if e = s.limit(ctx, ip, "start", 5); e != nil {
		return MobileStart{}, e
	}
	code, e := randomHandle()
	if e != nil {
		return MobileStart{}, e
	}
	expires := s.now().Add(10 * time.Minute)
	doc := Document{Key: "mobile#" + digestString(code), ExpiresAt: expires}
	if e = s.Store.Commit(ctx, withData(doc, handoff{Challenge: challenge, DeviceLabel: label, Status: "pending"})); e != nil {
		return MobileStart{}, e
	}
	return MobileStart{code, "https://suburban.jeremy.ninja/auth/mobile/begin?code=" + code, expires, 3}, nil
}
func (s *MobileService) Begin(ctx context.Context, code string) (string, string, error) {
	if !validHandle(code) {
		return "", "", ErrInvalid
	}
	doc, e := s.live(ctx, "mobile#"+digestString(code))
	if e != nil {
		return "", "", e
	}
	h, e := decodeDocument[handoff](doc)
	if e != nil {
		return "", "", e
	}
	if h.Status != "pending" {
		return "", "", ErrExpired
	}
	state, e := randomHandle()
	if e != nil {
		return "", "", e
	}
	browser, e := randomHandle()
	if e != nil {
		return "", "", e
	}
	pkce, e := randomHandle()
	if e != nil {
		return "", "", e
	}
	h.State = digestString(state)
	h.BrowserHash = digestString(browser)
	h.PKCE = pkce
	h.Status = "browser"
	pointer := Document{Key: "mobile-state#" + h.State, ExpiresAt: doc.ExpiresAt}
	if e = s.Store.Commit(ctx, withData(doc, h), withData(pointer, statePointer{Handoff: doc.Key})); e != nil {
		return "", "", e
	}
	return s.Amazon.AuthorizationURL(state, pkce), browser, nil
}
func (s *MobileService) Callback(ctx context.Context, state, code, browser string) error {
	if !validHandle(state) || !validHandle(browser) || code == "" || len(code) > 4096 {
		return ErrInvalid
	}
	pointer, e := s.live(ctx, "mobile-state#"+digestString(state))
	if e != nil {
		return ErrInvalid
	}
	p, e := decodeDocument[statePointer](pointer)
	if e != nil || p.Used {
		return ErrExpired
	}
	doc, e := s.live(ctx, p.Handoff)
	if e != nil {
		return e
	}
	h, e := decodeDocument[handoff](doc)
	if e != nil {
		return e
	}
	if h.Status != "browser" || h.State != digestString(state) || subtle.ConstantTimeCompare([]byte(h.BrowserHash), []byte(digestString(browser))) != 1 {
		return ErrInvalid
	}
	p.Used = true
	h.Status = "processing"
	if e = s.Store.Commit(ctx, withData(pointer, p), withData(doc, h)); e != nil {
		return e
	}
	doc.Version++
	tokens, e := s.Amazon.ExchangeCode(ctx, code, h.PKCE)
	h.PKCE = ""
	if e != nil {
		return ErrUnavailable
	}
	h.Tokens = tokens
	// Persist the refresh grant before identity validation; this encrypted handoff
	// expires in ten minutes and cannot issue a session before owner proof passes.
	if e = s.Store.Commit(ctx, withData(doc, h)); e != nil {
		return e
	}
	doc.Version++
	identity, e := s.Amazon.Validate(ctx, tokens.AccessToken)
	if e != nil {
		var failure *amazon.Error
		if errors.As(e, &failure) && failure.Outcome == amazon.Revoked {
			return ErrNotOwner
		}
		return ErrUnavailable
	}
	if identity.Subject != s.Owner {
		return ErrNotOwner
	}
	boundOwner, e := s.boundOwner(ctx)
	if e != nil {
		return e
	}
	family, issued, e := NewFamily(s.Owner, boundOwner, identity.Subject, h.DeviceLabel, s.now())
	if e != nil {
		return e
	}
	h.Status = "complete"
	h.FamilyID = family.ID
	h.Issued = sessionResult(issued)
	h.Tokens = amazon.Tokens{}
	familyDoc := withData(Document{Key: "family#" + family.ID, ExpiresAt: family.Deadline}, familyDocument{Family: family, Tokens: tokens, ValidatedAt: identity.ValidatedAt})
	refreshDoc := withData(Document{Key: "handle#" + digestString(issued.RefreshHandle), ExpiresAt: family.Deadline}, issued.Handle)
	accessDoc := withData(Document{Key: "access#" + digestString(issued.AccessHandle), ExpiresAt: issued.AccessExpiresAt}, accessDocument{FamilyID: family.ID, ExpiresAt: issued.AccessExpiresAt})
	return s.Store.Commit(ctx, withData(doc, h), familyDoc, refreshDoc, accessDoc)
}
func sessionResult(i Issued) MobileSession {
	return MobileSession{"authenticated", i.AccessHandle, i.RefreshHandle, i.AccessExpiresAt}
}
func (s *MobileService) boundHandoff(ctx context.Context, code, verifier string) (Document, handoff, error) {
	if !validHandle(code) || !validHandle(verifier) {
		return Document{}, handoff{}, ErrInvalid
	}
	raw, _ := base64.RawURLEncoding.Strict().DecodeString(verifier)
	digest := sha256.Sum256(raw)
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	clear(raw)
	doc, e := s.live(ctx, "mobile#"+digestString(code))
	if e != nil {
		return Document{}, handoff{}, e
	}
	h, e := decodeDocument[handoff](doc)
	if e != nil {
		return Document{}, handoff{}, e
	}
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(h.Challenge)) != 1 {
		return Document{}, handoff{}, ErrInvalid
	}
	return doc, h, nil
}
func (s *MobileService) Poll(ctx context.Context, code, verifier, ip string) (MobileSession, error) {
	if e := s.limit(ctx, ip, "poll", 60); e != nil {
		return MobileSession{}, e
	}
	if e := s.limit(ctx, digestString(code), "device-poll", 20); e != nil {
		return MobileSession{}, e
	}
	doc, h, e := s.boundHandoff(ctx, code, verifier)
	if e != nil {
		return MobileSession{}, e
	}
	if h.Status == "consumed" || h.Status == "canceled" {
		return MobileSession{}, ErrExpired
	}
	// Pending polls do not mutate the handoff while its callback is in progress.
	// The durable per-IP counter bounds polling without racing credential storage.
	if h.Status != "complete" {
		return MobileSession{}, ErrPending
	}
	result := h.Issued
	h.Issued = MobileSession{}
	h.Status = "consumed"
	if e = s.Store.Commit(ctx, withData(doc, h)); e != nil {
		return MobileSession{}, e
	}
	return result, nil
}
func (s *MobileService) boundOwner(ctx context.Context) (string, error) {
	doc, e := s.Store.Load(ctx, "owner-binding")
	if errors.Is(e, ErrMissing) {
		doc = withData(Document{Key: "owner-binding", ExpiresAt: s.now().AddDate(100, 0, 0)}, s.Owner)
		if e = s.Store.Commit(ctx, doc); e != nil {
			return "", e
		}
		return s.Owner, nil
	}
	if e != nil {
		return "", e
	}
	var owner string
	if json.Unmarshal(doc.Data, &owner) != nil || owner != s.Owner {
		return "", ErrNotOwner
	}
	return owner, nil
}
func (s *MobileService) Cancel(ctx context.Context, code, verifier string) error {
	doc, h, e := s.boundHandoff(ctx, code, verifier)
	if e != nil {
		return e
	}
	if h.Status == "canceled" {
		return nil
	}
	docs := []Document{}
	if h.FamilyID != "" {
		fdoc, e := s.live(ctx, "family#"+h.FamilyID)
		if e != nil {
			return e
		}
		f, e := decodeDocument[familyDocument](fdoc)
		if e != nil {
			return e
		}
		f.Family.Revoked = true
		f.Family.Version++
		f.Tokens = amazon.Tokens{}
		docs = append(docs, withData(fdoc, f))
	}
	h.Status = "canceled"
	h.Issued = MobileSession{}
	h.Tokens = amazon.Tokens{}
	h.PKCE = ""
	docs = append(docs, withData(doc, h))
	return s.Store.Commit(ctx, docs...)
}
func (s *MobileService) Authorize(ctx context.Context, access string) (string, error) {
	if !validHandle(access) {
		return "", ErrHandle
	}
	doc, e := s.live(ctx, "access#"+digestString(access))
	if e != nil {
		if errors.Is(e, ErrUnavailable) {
			return "", e
		}
		return "", ErrHandle
	}
	a, e := decodeDocument[accessDocument](doc)
	if e != nil || !s.now().Before(a.ExpiresAt) {
		return "", ErrExpired
	}
	fdoc, e := s.live(ctx, "family#"+a.FamilyID)
	if e != nil {
		if errors.Is(e, ErrUnavailable) {
			return "", e
		}
		return "", ErrExpired
	}
	f, e := decodeDocument[familyDocument](fdoc)
	if e != nil {
		return "", e
	}
	if e = CheckFamily(f.Family, s.Owner, s.now()); e != nil {
		return "", e
	}
	if f.ValidatedAt.IsZero() || f.ValidatedAt.After(s.now()) || !s.now().Before(f.ValidatedAt.Add(15*time.Minute)) {
		return "", ErrExpired
	}
	return a.FamilyID, nil
}
func (s *MobileService) Logout(ctx context.Context, access string) error {
	familyID, e := s.Authorize(ctx, access)
	if e != nil {
		return e
	}
	return s.revokeFamily(ctx, familyID)
}
func (s *MobileService) revokeFamily(ctx context.Context, familyID string) error {
	doc, e := s.live(ctx, "family#"+familyID)
	if e != nil {
		return e
	}
	f, e := decodeDocument[familyDocument](doc)
	if e != nil {
		return e
	}
	f.Family.Revoked = true
	f.Family.Version++
	f.Tokens = amazon.Tokens{}
	return s.Store.Commit(ctx, withData(doc, f))
}

// CommandFamily is used only behind authenticated API or IAM worker ingress.
// It rechecks durable family revocation and never returns tokens to a client.
func (s *MobileService) CommandFamily(ctx context.Context, familyID string) (string, error) {
	if !validHandle(familyID) {
		return "", ErrHandle
	}
	doc, e := s.live(ctx, "family#"+familyID)
	if e != nil {
		return "", e
	}
	f, e := decodeDocument[familyDocument](doc)
	if e != nil {
		return "", e
	}
	if e = CheckFamily(f.Family, s.Owner, s.now()); e != nil {
		return "", e
	}
	if f.Tokens.AccessToken == "" {
		return "", ErrHandle
	}
	return f.Tokens.AccessToken, nil
}
func (s *MobileService) Refresh(ctx context.Context, handle string) (MobileSession, error) {
	if !validHandle(handle) {
		return MobileSession{}, ErrHandle
	}
	hdoc, e := s.live(ctx, "handle#"+digestString(handle))
	if e != nil {
		if errors.Is(e, ErrUnavailable) {
			return MobileSession{}, e
		}
		return MobileSession{}, ErrHandle
	}
	stored, e := decodeDocument[StoredHandle](hdoc)
	if e != nil {
		return MobileSession{}, e
	}
	doc, e := s.live(ctx, "family#"+stored.FamilyID)
	if e != nil {
		return MobileSession{}, e
	}
	f, e := decodeDocument[familyDocument](doc)
	if e != nil {
		return MobileSession{}, e
	}
	rotation, e := Rotate(f.Family, stored, s.Owner, handle, s.now())
	if e != nil {
		if errors.Is(e, ErrRevoked) && rotation.Family.Revoked {
			f.Family = rotation.Family
			f.Tokens = amazon.Tokens{}
			if write := s.Store.Commit(ctx, withData(doc, f)); write != nil {
				return MobileSession{}, write
			}
		}
		return MobileSession{}, e
	}
	if s.now().Before(f.BusyUntil) {
		return MobileSession{}, ErrRefreshRace
	}
	f.BusyUntil = s.now().Add(15 * time.Second)
	if e = s.Store.Commit(ctx, withData(doc, f)); e != nil {
		return MobileSession{}, e
	}
	doc.Version++
	identity, e := s.Amazon.Validate(ctx, f.Tokens.AccessToken)
	var invalid *amazon.Error
	if errors.As(e, &invalid) && invalid.Outcome == amazon.InvalidAccessToken {
		tokens, refreshErr := s.Amazon.Refresh(ctx, f.Tokens.RefreshToken)
		if refreshErr != nil {
			return MobileSession{}, s.authorityFailure(ctx, doc, f, refreshErr)
		}
		f.Tokens = tokens
		if e = s.Store.Commit(ctx, withData(doc, f)); e != nil {
			return MobileSession{}, e
		}
		doc.Version++
		identity, e = s.Amazon.ValidateRefreshed(ctx, tokens.AccessToken)
	}
	if e != nil {
		return MobileSession{}, s.authorityFailure(ctx, doc, f, e)
	}
	if identity.Subject != s.Owner {
		return MobileSession{}, s.authorityFailure(ctx, doc, f, &amazon.Error{Outcome: amazon.Revoked})
	}
	f.Family = rotation.Family
	f.ValidatedAt = identity.ValidatedAt
	f.BusyUntil = time.Time{}
	refreshDoc := withData(Document{Key: "handle#" + digestString(rotation.Issued.RefreshHandle), ExpiresAt: f.Family.Deadline}, rotation.Issued.Handle)
	accessDoc := withData(Document{Key: "access#" + digestString(rotation.Issued.AccessHandle), ExpiresAt: rotation.Issued.AccessExpiresAt}, accessDocument{FamilyID: f.Family.ID, ExpiresAt: rotation.Issued.AccessExpiresAt})
	if e = s.Store.Commit(ctx, withData(doc, f), withData(hdoc, rotation.Previous), refreshDoc, accessDoc); e != nil {
		return MobileSession{}, e
	}
	return sessionResult(rotation.Issued), nil
}
func (s *MobileService) authorityFailure(ctx context.Context, doc Document, f familyDocument, cause error) error {
	var problem *amazon.Error
	if errors.As(cause, &problem) && problem.Outcome == amazon.Revoked {
		f.Family.Revoked = true
		f.Family.Version++
		f.Tokens = amazon.Tokens{}
	}
	f.BusyUntil = time.Time{}
	if e := s.Store.Commit(ctx, withData(doc, f)); e != nil {
		return e
	}
	if f.Family.Revoked {
		return ErrRevoked
	}
	return ErrUnavailable
}
