package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDPAPIRecoveryRoundTrip(t *testing.T) {
	plain := []byte("synthetic-test-recovery-password")
	protected, err := protectRecovery(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(protected, plain) {
		t.Fatal("plaintext in protected output")
	}
	restored, err := unprotectRecovery(protected)
	if err != nil || !bytes.Equal(restored, plain) {
		t.Fatal("DPAPI round trip failed")
	}
	protected[len(protected)-1] ^= 1
	if _, err := unprotectRecovery(protected); err == nil {
		t.Fatal("tampered recovery accepted")
	}
}

func TestGeneratedRecoveryPreservesIdentityAfterPartialWrite(t *testing.T) {
	secrets := map[string]string{}
	variables := map[string]string{}
	failed := false
	var firstDevice string
	var output bytes.Buffer
	run := func(args []string, input []byte) ([]byte, error) {
		values := secrets
		if args[0] == "variable" {
			values = variables
		}
		switch args[1] {
		case "list":
			var entries []metadata
			for name := range values {
				entries = append(entries, metadata{Name: name, UpdatedAt: "2026-10-22"})
			}
			return json.Marshal(entries)
		case "get":
			return []byte(values[args[2]]), nil
		case "set":
			if args[2] == "ONSTAR_DEVICE_ID" && !failed {
				firstDevice = string(input)
				failed = true
				return nil, errors.New("synthetic failure")
			}
			if _, exists := values[args[2]]; exists {
				t.Fatal("existing secret overwritten")
			}
			values[args[2]] = string(input)
			return nil, nil
		}
		t.Fatal("unexpected call")
		return nil, nil
	}
	dir := t.TempDir()
	if err := generateWithRecovery(run, &output, dir); err == nil {
		t.Fatal("failed write passed")
	}
	if err := generateWithRecovery(run, &output, dir); err != nil {
		t.Fatal(err)
	}
	if secrets["ONSTAR_DEVICE_ID"] != firstDevice {
		t.Fatal("device identity regenerated after failed upload")
	}
	if err := generateWithRecovery(run, &output, dir); err != nil {
		t.Fatal(err)
	}
	protected, err := os.ReadFile(filepath.Join(dir, "generated-keys.dpapi"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range secrets {
		if bytes.Contains(protected, []byte(value)) || strings.Contains(output.String(), value) {
			t.Fatal("secret exposed")
		}
	}
	if err := generateWithRecovery(run, &output, t.TempDir()); err == nil {
		t.Fatal("existing identity without recovery accepted")
	}
}

func TestAndroidRecoverySigningTools(t *testing.T) {
	if os.Getenv("SECRETCTL_SIGNING_TOOL_TEST") != "1" {
		t.Skip("explicit synthetic JDK tool proof only")
	}
	dir := t.TempDir()
	first, err := realAndroidRecovery(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := realAndroidRecovery(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.CertificateSHA256 != second.CertificateSHA256 || !bytes.Equal(first.Keystore, second.Keystore) || !bytes.Equal(first.StorePassword, second.StorePassword) || !bytes.Equal(first.KeyPassword, second.KeyPassword) {
		t.Fatal("recovery identity changed")
	}
	passwords, err := os.ReadFile(filepath.Join(dir, "android-passwords.dpapi"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(passwords, first.StorePassword) || bytes.Contains(passwords, first.KeyPassword) {
		t.Fatal("plaintext passwords persisted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "signing-proof-") {
			t.Fatal("signing scratch file retained")
		}
	}
}
