package securelocal

import (
	"bytes"
	"testing"
)

func TestCurrentUserProtection(t *testing.T) {
	plain := []byte("synthetic-credential")
	sealed, err := Protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("plaintext leaked")
	}
	opened, err := Unprotect(sealed)
	if err != nil || !bytes.Equal(opened, plain) {
		t.Fatal("round trip failed")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := Unprotect(sealed); err == nil {
		t.Fatal("tamper accepted")
	}
}
