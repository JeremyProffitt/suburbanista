package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestEmptySeedMakesNoAWSClientOrWrites(t *testing.T) {
	message, e := run(context.Background(), func(string) string { return "" }, func(context.Context) (importer, error) { t.Fatal("empty seed initialized AWS"); return nil, nil })
	if e != nil || message != "seed: skipped, no bootstrap present" {
		t.Fatal("empty seed did not skip")
	}
}

func TestSeedOnlyAuthorizesMainRepositoryCI(t *testing.T) {
	values := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_REF": "refs/heads/main", "GITHUB_REPOSITORY": "JeremyProffittOrg/Suburban-Remote"}
	get := func(k string) string { return values[k] }
	if !allowedCI(get) {
		t.Fatal("main CI rejected")
	}
	for _, key := range []string{"GITHUB_ACTIONS", "GITHUB_REF", "GITHUB_REPOSITORY"} {
		original := values[key]
		values[key] = "other"
		if allowedCI(get) {
			t.Fatal("unapproved import accepted")
		}
		values[key] = original
	}
}
func TestSeedMasksOnlyWorkflowCommands(t *testing.T) {
	var out bytes.Buffer
	mask(&out, "synthetic%value\r\nwith punctuation!")
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.HasPrefix(line, "::add-mask::") || strings.Contains(line, "\r") {
			t.Fatal("mask emitted unsafe workflow line")
		}
	}
}
func TestMalformedSeedNeverInitializesAWS(t *testing.T) {
	for _, raw := range []string{"{}", "{} trailing", `{"schema":1,"unexpected":"synthetic"}`} {
		_, e := run(context.Background(), func(name string) string {
			if name == "ONSTAR_TOKEN_BOOTSTRAP" {
				return raw
			}
			return "1"
		}, func(context.Context) (importer, error) { t.Fatal("invalid seed initialized AWS"); return nil, nil })
		if e == nil {
			t.Fatal("invalid seed accepted")
		}
	}
}
