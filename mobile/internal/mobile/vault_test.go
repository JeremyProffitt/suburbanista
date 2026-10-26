package mobile

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVaultOnlyWritesAuthenticatedCiphertext(t *testing.T) {
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	v := Vault{Path: filepath.Join(t.TempDir(), "session.enc"), Crypt: func(b []byte, seal bool) ([]byte, error) {
		nonce := make([]byte, aead.NonceSize())
		if seal {
			return aead.Seal(nil, nonce, b, nil), nil
		}
		return aead.Open(nil, nonce, b, nil)
	}}
	state := SavedState{Session: Session{AccessHandle: "private-handle"}, Pending: &PendingLogin{Verifier: "private-verifier"}}
	if e := v.Save(state); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(v.Path)
	if bytes.Contains(raw, []byte("private")) {
		t.Fatal("plaintext written")
	}
	got, e := v.Load()
	if e != nil || got.Session.AccessHandle != state.Session.AccessHandle || got.Pending.Verifier != state.Pending.Verifier {
		t.Fatal("roundtrip failed")
	}
	raw[0] ^= 1
	os.WriteFile(v.Path, raw, 0600)
	if _, e = v.Load(); e == nil {
		t.Fatal("tamper accepted")
	}
}
func TestVaultDoesNotFallBackToPlaintext(t *testing.T) {
	v := Vault{Path: filepath.Join(t.TempDir(), "session.enc"), Crypt: func([]byte, bool) ([]byte, error) { return nil, errors.New("key unavailable") }}
	if e := v.Save(SavedState{Session: Session{AccessHandle: "private"}}); e == nil {
		t.Fatal("missing key accepted")
	}
	if _, e := os.Stat(v.Path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("fallback file created")
	}
}
