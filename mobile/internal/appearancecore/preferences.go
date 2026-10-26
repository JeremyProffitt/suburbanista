package appearancecore

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

// LoadPreferencesFile reads the app's existing Fyne preferences file. It never
// rewrites that file, mutates owner credentials or maintains a second store.
// A concurrent non-atomic Fyne write is an error, not permission to guess a style.
func LoadPreferencesFile(path string) (Settings, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), errors.New("appearance preferences unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return Default(), errors.New("appearance preferences invalid")
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil || values == nil {
		return Default(), errors.New("appearance preferences invalid")
	}
	if raw, ok := values[PreferenceKey]; ok {
		var setting string
		if json.Unmarshal(raw, &setting) != nil {
			return Default(), errors.New("appearance preferences invalid")
		}
		return Decode(setting)
	}
	return Default(), nil
}
