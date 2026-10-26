package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeArchive(t *testing.T, sizes ...int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deployment.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(file)
	w := tar.NewWriter(z)
	for i, size := range sizes {
		if err := w.WriteHeader(&tar.Header{Name: filepath.Join("bin", string(rune('a'+i))), Mode: 0o755, Size: int64(size), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(make([]byte, size)); err != nil {
			t.Fatal(err)
		}
	}
	if w.Close() != nil || z.Close() != nil || file.Close() != nil {
		t.Fatal("archive write failed")
	}
	return path
}

func TestReadDeploymentArchiveAcceptsCurrentBuildSize(t *testing.T) {
	// Matches the 111 MB archive of five Lambdas plus deploy tools.
	artifacts := readDeploymentArchive(writeArchive(t, 60<<20, 51<<20))
	if len(artifacts) != 2 {
		t.Fatal("current-size deployment archive was not read")
	}
}

func TestReadDeploymentArchiveRejectsOversizedBuild(t *testing.T) {
	if readDeploymentArchive(writeArchive(t, 200<<20, 57<<20)) != nil {
		t.Fatal("archive above the memory bound was read")
	}
}
