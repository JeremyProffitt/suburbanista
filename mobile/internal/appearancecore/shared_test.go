package appearancecore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSharedAppearanceMigrationAndConcurrentNavigation(t *testing.T) {
	root := t.TempDir()
	settings := Default()
	settings.BackgroundRotation.Selected = []string{"charlotte", "beach", "mountains"}
	raw, err := Encode(settings)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(map[string]string{PreferenceKey: raw, "unrelated": "preserved"})
	legacyPath := filepath.Join(root, "preferences.json")
	if err = os.WriteFile(legacyPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadShared(root)
	if err != nil || loaded.Backdrop != settings.Backdrop {
		t.Fatal("migration failed")
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var jobs sync.WaitGroup
	for i := 0; i < 8; i++ {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			_, e := UpdateShared(context.Background(), root, func(s *Settings) error { s.NavigateBackground(1, now); return nil })
			if e != nil {
				t.Error(e)
			}
		}()
	}
	jobs.Wait()
	loaded, err = LoadShared(root)
	if err != nil || loaded.Backdrop != "mountains" {
		t.Fatalf("concurrent updates lost: %q %v", loaded.Backdrop, err)
	}
	retained, err := os.ReadFile(legacyPath)
	if err != nil || string(retained) != string(legacy) {
		t.Fatal("legacy or unrelated preferences changed")
	}
	if err = os.WriteFile(legacyPath, []byte("broken legacy file"), 0600); err != nil {
		t.Fatal(err)
	}
	if loaded, err = LoadShared(root); err != nil || loaded.Backdrop != "mountains" {
		t.Fatal("canonical store reread legacy state")
	}
	if err = os.WriteFile(filepath.Join(root, sharedName), []byte("broken canonical file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadShared(root); err == nil {
		t.Fatal("corrupt canonical store silently replaced")
	}
}

func TestSharedAppearanceCanceledWritePreservesState(t *testing.T) {
	root := t.TempDir()
	original, err := LoadShared(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	changed := original.Clone()
	changed.Backdrop = "beach"
	if SaveShared(ctx, root, changed) == nil {
		t.Fatal("canceled transaction wrote")
	}
	retained, err := LoadShared(root)
	if err != nil || retained.Backdrop != original.Backdrop {
		t.Fatal("canceled transaction changed preferences")
	}
}
