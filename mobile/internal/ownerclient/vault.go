package ownerclient

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Vault stores only Android Keystore authenticated ciphertext. The key never
// leaves Android. There is deliberately no preferences or plaintext fallback.
type Vault struct {
	Path  string
	Crypt func([]byte, bool) ([]byte, error)
}

func (v Vault) Load() (SavedState, error) {
	var state SavedState
	b, e := os.ReadFile(v.Path)
	if errors.Is(e, os.ErrNotExist) {
		return state, nil
	}
	if e != nil || len(b) > 32768 {
		return state, errors.New("Secure storage unavailable.")
	}
	plain, e := v.Crypt(b, false)
	if e != nil {
		return state, errors.New("Secure storage could not be unlocked.")
	}
	defer clear(plain)
	if json.Unmarshal(plain, &state) != nil {
		return SavedState{}, errors.New("Secure storage invalid.")
	}
	return state, nil
}
func (v Vault) Save(state SavedState) error {
	b, e := json.Marshal(state)
	if e != nil {
		return errors.New("Secure storage failed.")
	}
	defer clear(b)
	sealed, e := v.Crypt(b, true)
	if e != nil {
		return errors.New("Secure storage failed.")
	}
	if e = os.MkdirAll(filepath.Dir(v.Path), 0700); e != nil {
		return errors.New("Secure storage failed.")
	}
	f, e := os.CreateTemp(filepath.Dir(v.Path), ".session-")
	if e != nil {
		return errors.New("Secure storage failed.")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(sealed); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return errors.New("Secure storage failed.")
	}
	if os.Rename(name, v.Path) != nil {
		return errors.New("Secure storage failed.")
	}
	return nil
}
func (v Vault) Clear() error {
	e := os.Remove(v.Path)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	return e
}
