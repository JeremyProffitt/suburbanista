package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteNewPreservesExistingMaster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.png")
	original := []byte("original source bytes")
	if err := writeNew(path, original); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(path, original); err != nil {
		t.Fatal("identical retry rejected", err)
	}
	if err := writeNew(path, []byte("different source")); err == nil {
		t.Fatal("existing master overwritten")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatal("master changed")
	}
}
func TestAssetPathStaysWithinRoot(t *testing.T) {
	root := t.TempDir()
	if inside(root, filepath.Join(root, "masters", "scene.png")) != nil {
		t.Fatal("valid child rejected")
	}
	if inside(root, filepath.Join(root, "..", "elsewhere", "scene.png")) == nil {
		t.Fatal("outside path allowed")
	}
}
