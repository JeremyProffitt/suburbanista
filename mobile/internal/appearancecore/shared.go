package appearancecore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/localfile"
)

const sharedName = "appearance-state.json"

// The app and widgets share one authoritative appearance document. Existing
// Fyne preferences are a one-time migration source, never a second write path.
func LoadShared(root string) (Settings, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return UpdateShared(ctx, root, nil)
}

func SaveShared(ctx context.Context, root string, settings Settings) error {
	_, err := UpdateShared(ctx, root, func(current *Settings) error { *current = settings.Clone(); return nil })
	return err
}

func UpdateShared(ctx context.Context, root string, change func(*Settings) error) (result Settings, err error) {
	if root == "" {
		return Default(), errors.New("Appearance storage unavailable")
	}
	path := filepath.Join(root, sharedName)
	err = localfile.WithLock(ctx, path, func() error {
		file, readErr := os.Open(path)
		missing := errors.Is(readErr, os.ErrNotExist)
		if missing {
			result, readErr = LoadPreferencesFile(filepath.Join(root, "preferences.json"))
		} else if readErr == nil {
			var data []byte
			data, readErr = io.ReadAll(io.LimitReader(file, 65537))
			closeErr := file.Close()
			if readErr == nil {
				readErr = closeErr
			}
			if readErr == nil && len(data) > 65536 {
				readErr = errors.New("Appearance preferences invalid")
			}
			if readErr == nil {
				result, readErr = Decode(string(data))
			}
		}
		if readErr != nil {
			return errors.New("Appearance preferences unavailable")
		}
		if change != nil {
			if e := change(&result); e != nil {
				return e
			}
		}
		if !missing && change == nil {
			return nil
		}
		raw, e := Encode(result)
		if e != nil {
			return e
		}
		out, e := os.CreateTemp(root, ".appearance-*")
		if e != nil {
			return e
		}
		name := out.Name()
		defer os.Remove(name)
		if e = out.Chmod(0600); e == nil {
			_, e = out.WriteString(raw)
		}
		if e == nil {
			e = out.Sync()
		}
		closeErr := out.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			return e
		}
		return os.Rename(name, path)
	})
	return
}
