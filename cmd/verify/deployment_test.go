package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremyProffittOrg/suburban-remote/internal/verify"
)

func TestDeploymentCanaryFixtures(t *testing.T) {
	for _, name := range []string{"good", "bad"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/deployment/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Log      string `json:"log"`
				Artifact string `json:"artifact"`
			}
			if json.Unmarshal(data, &fixture) != nil {
				t.Fatal("invalid fixture")
			}
			dir := t.TempDir()
			secret := "synthetic-canary-value-for-tests"
			digests, _ := json.Marshal(verify.CanaryDigests([]string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), secret, secret}))
			os.WriteFile(filepath.Join(dir, "fingerprints.json"), digests, 0600)
			os.WriteFile(filepath.Join(dir, "log"), []byte(fixture.Log), 0600)
			var archive bytes.Buffer
			z := gzip.NewWriter(&archive)
			tw := tar.NewWriter(z)
			content := []byte(fixture.Artifact)
			if err := tw.WriteHeader(&tar.Header{Name: "bootstrap", Mode: 0600, Size: int64(len(content))}); err != nil {
				t.Fatal(err)
			}
			tw.Write(content)
			tw.Close()
			z.Close()
			os.WriteFile(filepath.Join(dir, "artifact.tar.gz"), archive.Bytes(), 0600)
			var output bytes.Buffer
			code := run([]string{"deployment", "--mode", "canary", "--fingerprints", filepath.Join(dir, "fingerprints.json"), "--log", filepath.Join(dir, "log"), "--artifact", filepath.Join(dir, "artifact.tar.gz"), "--report", filepath.Join(dir, "report.json"), "--commit", strings.Repeat("a", 40), "--run-id", "fixture"}, &output, &output)
			if (code == 0) != (name == "good") {
				t.Fatalf("unexpected exit %d", code)
			}
			if strings.Contains(output.String(), secret) {
				t.Fatal("canary printed")
			}
			if _, err := os.Stat(filepath.Join(dir, "report.json")); err != nil {
				t.Fatal("report missing")
			}
		})
	}
}
