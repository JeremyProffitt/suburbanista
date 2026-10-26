// Command secretctl installs generated keys without exposing secret values.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const repository = "JeremyProffittOrg/Suburban-Remote"

type runCommand func([]string, []byte) ([]byte, error)
type metadata struct {
	Name      string `json:"name"`
	UpdatedAt string `json:"updatedAt"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "map-key" {
		if err := runMapKey(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "owner-id" {
		if err := runOwnerID(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "lwa" {
		if err := runLWA(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "amazon" {
		if err := runAmazon(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "generate" {
		fmt.Fprintln(os.Stderr, "usage: secretctl generate [--recovery-dir PATH]")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("generate", flag.ExitOnError)
	recoveryDir := flags.String("recovery-dir", defaultRecoveryDir(), "owner-only Android signing recovery directory")
	flags.Parse(os.Args[2:])
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	if err := generateWithRecovery(runGH, os.Stdout, *recoveryDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := generateAndroid(runGH, *recoveryDir, os.Stdout, realAndroidRecovery); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runGH(args []string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	hideProcess(cmd)
	cmd.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	cmd.Stdout = &output
	// Never relay subprocess diagnostics: a failed credential write may echo input.
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("GitHub operation failed or timed out; check names-only GitHub access before retrying")
	}
	return output.Bytes(), nil
}

func inventory(run runCommand, kind string) (map[string]metadata, error) {
	data, err := run([]string{kind, "list", "--repo", repository, "--json", "name,updatedAt"}, nil)
	if err != nil {
		return nil, err
	}
	var entries []metadata
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, errors.New("invalid names-only GitHub inventory")
	}
	result := make(map[string]metadata, len(entries))
	for _, entry := range entries {
		result[entry.Name] = entry
	}
	return result, nil
}

func generate(run runCommand, output io.Writer) error {
	return generateUsing(run, output, newKey)
}

func generateUsing(run runCommand, output io.Writer, key func(string) ([]byte, []byte, error)) error {
	secrets, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	variables, err := inventory(run, "variable")
	if err != nil {
		return err
	}
	if _, exists := variables["CREDENTIAL_ENCRYPTION_KEY_ACTIVE"]; exists {
		value, err := run([]string{"variable", "get", "CREDENTIAL_ENCRYPTION_KEY_ACTIVE", "--repo", repository}, nil)
		if err != nil {
			return err
		}
		active := strings.TrimSpace(string(value))
		if active != "A" && active != "B" {
			return errors.New("CREDENTIAL_ENCRYPTION_KEY_ACTIVE must be A or B")
		}
	}
	if _, key := secrets["UPDATE_MANIFEST_SIGNING_KEY"]; !key {
		if _, public := variables["UPDATE_MANIFEST_PUBLIC_KEY"]; public {
			return errors.New("UPDATE_MANIFEST_PUBLIC_KEY exists without its signing secret; restore the matching key before continuing")
		}
	} else if _, public := variables["UPDATE_MANIFEST_PUBLIC_KEY"]; !public {
		return errors.New("signing secret exists without UPDATE_MANIFEST_PUBLIC_KEY; restore its public verification key without replacing the secret")
	}
	var created []string
	for _, name := range []string{"CREDENTIAL_ENCRYPTION_KEY_A", "CREDENTIAL_ENCRYPTION_KEY_B", "UPDATE_MANIFEST_SIGNING_KEY", "ONSTAR_DEVICE_ID"} {
		if _, exists := secrets[name]; exists {
			fmt.Fprintf(output, "%s: preserved\n", name)
			continue
		}
		value, public, err := key(name)
		if err != nil {
			return errors.New("key generation failed")
		}
		_, err = run([]string{"secret", "set", name, "--repo", repository}, value)
		clear(value)
		if err != nil {
			return fmt.Errorf("%s: write failed; no value was logged", name)
		}
		created = append(created, name)
		if len(public) != 0 {
			if _, err := run([]string{"variable", "set", "UPDATE_MANIFEST_PUBLIC_KEY", "--repo", repository}, public); err != nil {
				return errors.New("signing secret installed but public verification key write failed; retry with the same protected recovery directory")
			}
		}
	}
	verified, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	for _, name := range created {
		entry, exists := verified[name]
		if !exists || entry.UpdatedAt == "" {
			return fmt.Errorf("%s: metadata verification failed", name)
		}
		fmt.Fprintf(output, "%s: installed; metadata verified\n", name)
	}
	if _, exists := variables["CREDENTIAL_ENCRYPTION_KEY_ACTIVE"]; !exists {
		if _, err := run([]string{"variable", "set", "CREDENTIAL_ENCRYPTION_KEY_ACTIVE", "--repo", repository}, []byte("A")); err != nil {
			return errors.New("active key slot variable write failed")
		}
		fmt.Fprintln(output, "CREDENTIAL_ENCRYPTION_KEY_ACTIVE: A")
	}
	verifiedVariables, err := inventory(run, "variable")
	if err != nil {
		return err
	}
	for _, name := range []string{"UPDATE_MANIFEST_PUBLIC_KEY", "CREDENTIAL_ENCRYPTION_KEY_ACTIVE"} {
		entry, exists := verifiedVariables[name]
		if !exists || entry.UpdatedAt == "" {
			return fmt.Errorf("%s: variable metadata verification failed", name)
		}
	}
	fmt.Fprintln(output, "Amazon and GM credentials require attended authorization.")
	return nil
}

func newKey(name string) ([]byte, []byte, error) {
	switch name {
	case "CREDENTIAL_ENCRYPTION_KEY_A", "CREDENTIAL_ENCRYPTION_KEY_B":
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		value := []byte(base64.StdEncoding.EncodeToString(raw))
		clear(raw)
		return value, nil, nil
	case "UPDATE_MANIFEST_SIGNING_KEY":
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		value := []byte(base64.StdEncoding.EncodeToString(private))
		clear(private)
		return value, []byte(base64.StdEncoding.EncodeToString(public)), nil
	case "ONSTAR_DEVICE_ID":
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, nil, err
		}
		raw[6] = raw[6]&0x0f | 0x40
		raw[8] = raw[8]&0x3f | 0x80
		value := []byte(fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]))
		clear(raw[:])
		return value, nil, nil
	default:
		return nil, nil, errors.New("unsupported generated key")
	}
}
