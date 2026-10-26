package ownerclient

import (
	"context"
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/localfile"
)

// WithLock serializes the complete read/refresh/write transaction across the
// application and native widget processes. The OS releases the lock on death.
func (v Vault) WithLock(ctx context.Context, fn func() error) error {
	if v.Path == "" {
		return errors.New("Secure storage unavailable.")
	}
	err := localfile.WithLock(ctx, v.Path, fn)
	if errors.Is(err, localfile.ErrUnavailable) {
		return errors.New("Secure storage unavailable.")
	}
	return err
}
