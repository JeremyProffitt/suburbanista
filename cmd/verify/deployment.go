package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/verify"
)

func runDeployment(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("deployment", flag.ContinueOnError)
	f.SetOutput(stderr)
	mode := f.String("mode", "canary", "verification mode")
	fingerprints := f.String("fingerprints", "canary-fingerprints.json", "public canary digests")
	logPath := f.String("log", "deployment.log", "completed deploy job log")
	archivePath := f.String("artifact", "deployment.tar.gz", "built artifact archive")
	reportPath := f.String("report", "artifacts/deployment-canary.json", "evidence envelope")
	commit := f.String("commit", os.Getenv("GITHUB_SHA"), "commit under test")
	runID := f.String("run-id", os.Getenv("GITHUB_RUN_ID"), "workflow run")
	if f.Parse(args) != nil || f.NArg() != 0 || *mode != "canary" {
		return 2
	}
	outputAbs, _ := filepath.Abs(*reportPath)
	for _, path := range []string{*fingerprints, *logPath, *archivePath} {
		inputAbs, _ := filepath.Abs(path)
		if strings.EqualFold(inputAbs, outputAbs) {
			return 2
		}
	}
	if *commit == "" {
		*commit = readCommit()
	}
	if *runID == "" {
		*runID = "local-canary"
	}
	digests, _ := os.ReadFile(*fingerprints)
	logs, _ := os.ReadFile(*logPath)
	artifacts := readDeploymentArchive(*archivePath)
	r := verify.DeploymentCanary(digests, logs, artifacts, *commit, *runID, time.Now())
	digest, err := r.Write(*reportPath, verify.DeploymentRequired, nil)
	if digest != "" {
		fmt.Fprintf(stdout, "report=%s sha256=%s runId=%s\n", *reportPath, digest, *runID)
	}
	if err != nil {
		fmt.Fprintln(stderr, "canary evidence failed; raw logs were not printed")
		return 1
	}
	fmt.Fprintln(stdout, "Completed deployment logs and build artifacts contain no canary encodings; injection and live selftest passed.")
	return 0
}

const maxDeploymentArchive = 256 << 20

func readDeploymentArchive(path string) map[string][]byte {
	artifacts := map[string][]byte{}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	z, err := gzip.NewReader(file)
	if err != nil {
		return nil
	}
	defer z.Close()
	t := tar.NewReader(z)
	var total int64
	for {
		header, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		total += header.Size
		// Bounds memory, not scope: every byte under the cap is scanned. Five
		// Lambda binaries plus deploy tools reached 106 MiB in October 2026.
		if header.Size < 0 || total > maxDeploymentArchive {
			return nil
		}
		if _, exists := artifacts[header.Name]; exists {
			return nil
		}
		data, err := io.ReadAll(t)
		if err != nil {
			return nil
		}
		artifacts[header.Name] = data
	}
	if total == 0 {
		return nil
	}
	return artifacts
}
