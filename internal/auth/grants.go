// Package auth defines owner session policy independently of HTTP and storage.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"
)

var (
	ErrNotOwner    = errors.New("not-owner")
	ErrExpired     = errors.New("grant-expired")
	ErrRevoked     = errors.New("grant-revoked")
	ErrHandle      = errors.New("invalid-handle")
	ErrRefreshRace = errors.New("refresh-conflict")
)

const refreshGrace = 10 * time.Second

// Family contains only hashes. Every replacement must be persisted with a
// conditional check on Version before its issued secrets leave the server.
type Family struct {
	ID, Owner, DeviceLabel          string
	CreatedAt, LastUsedAt, Deadline time.Time
	CurrentHash                     [32]byte
	Version                         uint64
	Revoked                         bool
}

type Issued struct {
	RefreshHandle, AccessHandle string
	AccessHash                  [32]byte
	AccessExpiresAt             time.Time
	Handle                      StoredHandle
}

// StoredHandle is a separate hashed-handle item retained until the family
// deadline. Keeping tombstones makes even old superseded-handle reuse detectable.
type StoredHandle struct {
	FamilyID     string
	Hash         [32]byte
	Generation   uint64
	SupersededAt time.Time
}

type Rotation struct {
	Family   Family
	Previous StoredHandle
	Issued   Issued
}

func randomHandle() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("session randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func handleHash(value string) ([32]byte, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != 32 || len(value) != 43 {
		return [32]byte{}, ErrHandle
	}
	return sha256.Sum256([]byte(value)), nil
}

func issue(now, deadline time.Time) (Issued, error) {
	refresh, err := randomHandle()
	if err != nil {
		return Issued{}, err
	}
	access, err := randomHandle()
	if err != nil {
		return Issued{}, err
	}
	expiry := now.Add(15 * time.Minute)
	if deadline.Before(expiry) {
		expiry = deadline
	}
	return Issued{RefreshHandle: refresh, AccessHandle: access, AccessHash: sha256.Sum256([]byte(access)), AccessExpiresAt: expiry}, nil
}

// NewFamily requires the verified subject AND durable owner binding to match
// deployment configuration. A caller cannot bootstrap an owner through this API.
func NewFamily(configuredOwner, boundOwner, verifiedSubject, label string, now time.Time) (Family, Issued, error) {
	now = now.UTC()
	if configuredOwner == "" || configuredOwner != boundOwner || configuredOwner != verifiedSubject {
		return Family{}, Issued{}, ErrNotOwner
	}
	if now.IsZero() || len(label) > 128 {
		return Family{}, Issued{}, errors.New("invalid grant metadata")
	}
	deadline := now.AddDate(5, 0, 0)
	issued, err := issue(now, deadline)
	if err != nil {
		return Family{}, Issued{}, err
	}
	id, err := randomHandle()
	if err != nil {
		return Family{}, Issued{}, err
	}
	family := Family{ID: id, Owner: configuredOwner, DeviceLabel: label, CreatedAt: now, LastUsedAt: now, Deadline: deadline, CurrentHash: sha256.Sum256([]byte(issued.RefreshHandle)), Version: 1}
	issued.Handle = StoredHandle{FamilyID: id, Hash: family.CurrentHash, Generation: 1}
	return family, issued, nil
}

// CheckFamily is required at mint and dispatch even if the access handle is
// otherwise valid. TTL deletion delay never extends authorization.
func CheckFamily(family Family, configuredOwner string, now time.Time) error {
	if configuredOwner == "" || family.Owner != configuredOwner {
		return ErrNotOwner
	}
	if family.Revoked {
		return ErrRevoked
	}
	if family.Version == 0 || family.CreatedAt.IsZero() || family.LastUsedAt.Before(family.CreatedAt) || family.LastUsedAt.After(now) || !family.Deadline.Equal(family.CreatedAt.AddDate(5, 0, 0)) {
		return ErrExpired
	}
	if !now.Before(family.Deadline) || !now.Before(family.LastUsedAt.AddDate(0, 0, 30)) {
		return ErrExpired
	}
	return nil
}

// Rotate returns the replacement and credentials; it does not persist them.
// Storage must atomically write the family, superseded-handle tombstone, new
// handle and access session in one transaction conditioned on the old family
// version. Do not return credentials before that transaction commits. Separate
// writes can lose replay detection after a crash and are not supported.
// On ErrRevoked it returns the revoked replacement, which must also be persisted.
// Unknown handles do not revoke a family (otherwise a guessed handle causes DoS).
// A matching previous handle inside grace gets no credentials and no state change.
func Rotate(family Family, stored StoredHandle, configuredOwner, handle string, now time.Time) (Rotation, error) {
	now = now.UTC()
	result := Rotation{Family: family, Previous: stored}
	if err := CheckFamily(family, configuredOwner, now); err != nil {
		return result, err
	}
	hash, err := handleHash(handle)
	if err != nil {
		return result, err
	}
	if stored.FamilyID != family.ID || stored.Generation == 0 || stored.Generation > family.Version || subtle.ConstantTimeCompare(hash[:], stored.Hash[:]) != 1 {
		return result, ErrHandle
	}
	if family.Version == ^uint64(0) {
		return result, ErrExpired
	}
	if !stored.SupersededAt.IsZero() {
		if now.Before(stored.SupersededAt) {
			return result, ErrHandle
		}
		if now.Before(stored.SupersededAt.Add(refreshGrace)) {
			return result, ErrRefreshRace
		}
		result.Family.Revoked = true
		result.Family.Version++
		return result, ErrRevoked
	}
	if stored.Generation != family.Version || subtle.ConstantTimeCompare(hash[:], family.CurrentHash[:]) != 1 {
		return result, ErrHandle
	}
	issued, err := issue(now, family.Deadline)
	if err != nil {
		return result, err
	}
	result.Previous.SupersededAt = now
	result.Family.CurrentHash = sha256.Sum256([]byte(issued.RefreshHandle))
	result.Family.LastUsedAt = now
	result.Family.Version++
	issued.Handle = StoredHandle{FamilyID: family.ID, Hash: result.Family.CurrentHash, Generation: result.Family.Version}
	result.Issued = issued
	return result, nil
}
