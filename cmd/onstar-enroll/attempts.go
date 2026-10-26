package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
)

type attempts struct {
	Attempts      int       `json:"attempts"`
	CooldownUntil time.Time `json:"cooldownUntil"`
}

func (a attempts) allowed(now time.Time) error {
	if now.Before(a.CooldownUntil) {
		return fmt.Errorf("authentication cooldown active until %s; no provider request sent", a.CooldownUntil.UTC().Format(time.RFC3339))
	}
	if a.Attempts >= 2 {
		return errors.New("two attended enrollment attempts used; diagnose the failure before an owner-approved reset")
	}
	return nil
}
func loadAttempts(path string) (attempts, error) {
	var state attempts
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil || json.Unmarshal(data, &state) != nil || state.Attempts < 0 {
		return state, errors.New("cannot read enrollment attempt budget")
	}
	return state, nil
}
func saveAttempts(path string, state attempts) error {
	data, err := json.Marshal(state)
	if err != nil {
		return errors.New("cannot encode attempt metadata")
	}
	f, err := os.CreateTemp(filepath.Dir(path), "attempts-*.tmp")
	if err != nil {
		return errors.New("cannot persist attempt budget")
	}
	temp := f.Name()
	defer os.Remove(temp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot persist attempt budget")
	}
	if os.Rename(temp, path) != nil {
		return errors.New("cannot replace attempt budget")
	}
	return nil
}
func recordAuthFailure(path string, state attempts, err error) error {
	var provider *onstar.Error
	if errors.As(err, &provider) && provider.AuthEndpoint && (provider.Status == 401 || provider.Status == 403 || provider.Kind == "access-denied") {
		state.CooldownUntil = time.Now().Add(6 * time.Hour)
		if e := saveAttempts(path, state); e != nil {
			return e
		}
		return fmt.Errorf("%w; authentication cooldown until %s", err, state.CooldownUntil.UTC().Format(time.RFC3339))
	}
	return err
}
func lockEnrollment(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, errors.New("cannot open enrollment lock")
	}
	if err := acquireEnrollmentLock(f); err != nil {
		_ = f.Close()
		return nil, errors.New("another enrollment is active or the file lock is unavailable")
	}
	var once sync.Once
	// Keep the pathname and inode stable. Closing the handle also releases the
	// kernel lock on process exit, including Ctrl+C and hard termination.
	return func() { once.Do(func() { _ = releaseEnrollmentLock(f); _ = f.Close() }) }, nil
}
