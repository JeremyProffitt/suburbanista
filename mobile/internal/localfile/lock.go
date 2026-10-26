// Package localfile serializes app/widget local-file transactions. The OS
// releases its lock on process death; it never uses a timed lock-file lease.
package localfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

var ErrUnavailable = errors.New("Local storage unavailable")

func WithLock(ctx context.Context, path string, fn func() error) error {
	if path == "" {
		return ErrUnavailable
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return ErrUnavailable
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return ErrUnavailable
	}
	defer file.Close()
	for {
		locked, err := tryLock(file)
		if err != nil {
			return ErrUnavailable
		}
		if locked {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer unlock(file)
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
