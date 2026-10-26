//go:build !windows

package securelocal

import "testing"

func TestUnsupportedPlatformFailsClosed(t *testing.T) {
	if _, err := Protect([]byte("synthetic")); err == nil {
		t.Fatal("unprotected storage allowed")
	}
	if _, err := Unprotect([]byte("synthetic")); err == nil {
		t.Fatal("unprotected storage allowed")
	}
}
