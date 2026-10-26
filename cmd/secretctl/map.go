package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
)

func runMapKey(args []string, output io.Writer) error {
	if len(args) == 1 && (args[0] == "--ssm" || args[0] == "--verify-ssm") {
		return runSSMMapKey(output, args[0] == "--verify-ssm")
	}
	replace := len(args) == 1 && args[0] == "--replace"
	if len(args) != 0 && !replace {
		return errors.New("usage: secretctl map-key [--replace | --ssm | --verify-ssm]")
	}
	return setupMapKey(runGH, func(label string) ([]byte, error) {
		fmt.Fprint(output, label)
		value, err := readAmazonInput()
		fmt.Fprintln(output)
		if err == nil {
			fmt.Fprintf(output, "Input length: %d characters; length does not prove randomness.\n", len(value))
		}
		return value, err
	}, output, replace)
}

func setupMapKey(run runCommand, prompt hiddenPrompt, output io.Writer, replace bool) error {
	entries, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	const name = "MAP_READ_KEY_SHA256"
	fmt.Fprintln(output, "Repository: "+repository+"; scope: repository secret "+name)
	fmt.Fprintln(output, "Map endpoint: https://suburban.jeremy.ninja/api/map.png")
	if _, exists := entries[name]; exists && !replace {
		fmt.Fprintln(output, name+": preserved; use --replace only to rotate the map key.")
		return nil
	}
	fmt.Fprintln(output, "Create and retain a random URL-safe key of 43-128 characters in your password manager (at least 32 random bytes). Only its SHA256 digest is sent to GitHub. Do not send the key in chat or email.")
	value, err := prompt("Map key (hidden; empty skips): ")
	defer clear(value)
	if err != nil {
		return err
	}
	if len(value) == 0 {
		fmt.Fprintln(output, name+": skipped; no values changed.")
		return nil
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`).Match(value) {
		return errors.New("invalid map key format; no values changed")
	}
	digest := sha256.Sum256(value)
	encoded := []byte(hex.EncodeToString(digest[:]))
	defer clear(encoded)
	if _, err = run([]string{"secret", "set", name, "--repo", repository}, encoded); err != nil {
		return errors.New("map digest write failed; no value was logged")
	}
	verified, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	if verified[name].UpdatedAt == "" {
		return errors.New("map digest metadata verification failed")
	}
	fmt.Fprintln(output, name+": installed; metadata verified. Deploy through a main push to activate. Rotation revokes the previous keyed URL after deployment.")
	return nil
}
