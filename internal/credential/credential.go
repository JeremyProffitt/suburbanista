// Package credential encrypts provider credentials and rotates fixed key slots.
// It never loads keys, logs data, or calls a provider or storage service.
package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"math"
	"strconv"
	"strings"
)

type Slot string

const (
	SlotA Slot = "A"
	SlotB Slot = "B"
)

// Failure exposes the application state and reason without credential details.
type Failure struct {
	State  string
	Reason string
}

func (f *Failure) Error() string { return f.State + ": " + f.Reason }

var (
	ErrKeyMismatch   = &Failure{State: "reauth-required", Reason: "key-mismatch"}
	ErrConfiguration = errors.New("invalid credential key configuration")
	ErrMetadata      = errors.New("invalid credential metadata")
	ErrRotationWrite = errors.New("credential rotation conditional write failed")
)

// Binding identifies the precise owner, provider and durable item being opened.
// Values come from trusted application context, not from the encrypted record.
type Binding struct {
	Owner    string
	Provider string
	ItemKey  string
}

// Value contains only encrypted data and the metadata needed for decryption.
type Value struct {
	KeySlot       Slot   `json:"keySlot"`
	KeyGeneration uint64 `json:"keyGeneration"`
	Nonce         []byte `json:"nonce"`
	Ciphertext    []byte `json:"ciphertext"`
}

type Keys struct {
	active Slot
	slots  map[Slot]cipher.AEAD
}

// New copies key material into AES key schedules; subsequent mutation of a or b
// cannot change this instance. Both fixed slots must contain 32-byte AES keys.
func New(a, b []byte, active Slot) (*Keys, error) {
	if len(a) != 32 || len(b) != 32 || (active != SlotA && active != SlotB) {
		return nil, ErrConfiguration
	}
	k := &Keys{active: active, slots: make(map[Slot]cipher.AEAD, 2)}
	for slot, key := range map[Slot][]byte{SlotA: a, SlotB: b} {
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, ErrConfiguration
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, ErrConfiguration
		}
		k.slots[slot] = aead
	}
	return k, nil
}

func aad(binding Binding, slot Slot, generation uint64) ([]byte, error) {
	for _, value := range []string{binding.Owner, binding.Provider, binding.ItemKey} {
		if value == "" || strings.Contains(value, "|") {
			return nil, ErrMetadata
		}
	}
	if (slot != SlotA && slot != SlotB) || generation == 0 {
		return nil, ErrMetadata
	}
	return []byte(binding.Owner + "|" + binding.Provider + "|" + string(slot) + "|" + strconv.FormatUint(generation, 10) + "|" + binding.ItemKey), nil
}

// Encrypt creates an active-slot value at the caller's positive generation.
// A new item starts at generation 1; token refreshes preserve its generation.
func (k *Keys) Encrypt(binding Binding, plaintext []byte, generation uint64) (Value, error) {
	if k == nil || k.slots[k.active] == nil {
		return Value{}, ErrConfiguration
	}
	additional, err := aad(binding, k.active, generation)
	if err != nil {
		return Value{}, err
	}
	aead := k.slots[k.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return Value{}, errors.New("credential nonce generation failed")
	}
	return Value{KeySlot: k.active, KeyGeneration: generation, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, additional)}, nil
}

// ConditionalWrite must replace the encrypted item only when its token version
// still equals expectedVersion. It must atomically advance that version too.
// A lost race returns an error; the caller must reread before attempting again.
type ConditionalWrite func(ctx context.Context, expectedVersion uint64, replacement Value) error

// Open releases plaintext only after any required rotation is durably accepted.
// Active-slot reads do not write. Inactive-slot reads increment keyGeneration
// and must pass a conditional-write callback. There are no automatic retries.
func (k *Keys) Open(ctx context.Context, binding Binding, value Value, tokenVersion uint64, write ConditionalWrite) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if k == nil {
		return nil, ErrConfiguration
	}
	aead := k.slots[value.KeySlot]
	additional, err := aad(binding, value.KeySlot, value.KeyGeneration)
	if err != nil || aead == nil || len(value.Nonce) != aead.NonceSize() {
		return nil, ErrKeyMismatch
	}
	plaintext, err := aead.Open(nil, value.Nonce, value.Ciphertext, additional)
	if err != nil {
		return nil, ErrKeyMismatch
	}
	if value.KeySlot == k.active {
		return plaintext, nil
	}
	if write == nil || tokenVersion == 0 || value.KeyGeneration == math.MaxUint64 {
		clear(plaintext)
		return nil, ErrRotationWrite
	}
	replacement, err := k.Encrypt(binding, plaintext, value.KeyGeneration+1)
	if err != nil {
		clear(plaintext)
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		clear(plaintext)
		return nil, err
	}
	if err = write(ctx, tokenVersion, replacement); err != nil {
		clear(plaintext)
		return nil, ErrRotationWrite
	}
	return plaintext, nil
}
