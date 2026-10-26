// Command verify checks acceptance evidence. Only its explicit CI live mode
// invokes the deployed worker for read-only provider proof.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/verify"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "onstar" {
		return runOnStar(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "redaction" {
		if len(args) < 3 || args[1] != "--paths" {
			fmt.Fprintln(stderr, "usage: verify redaction --paths internal cmd artifacts")
			return 2
		}
		count, err := verify.ScanSourcePrivacy(args[2:], []string{os.Getenv("VERIFY_CANARY")})
		if err != nil {
			fmt.Fprintln(stderr, "privacy scan failed: "+err.Error())
			return 1
		}
		fmt.Fprintf(stdout, "privacy scan passed: %d files; no live-provider proof implied\n", count)
		return 0
	}
	if len(args) > 0 && args[0] == "deployment" {
		return runDeployment(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "template" {
		return runTemplate(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "preflight" {
		fmt.Fprintln(stderr, "usage: verify preflight --input artifacts/preflight-input.json --report artifacts/preflight.json")
		return 2
	}
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "artifacts/preflight-input.json", "dated non-secret prerequisite and decision evidence")
	reportPath := flags.String("report", "artifacts/preflight.json", "output report")
	commit := flags.String("commit", os.Getenv("GITHUB_SHA"), "full Git commit SHA")
	runID := flags.String("run-id", os.Getenv("GITHUB_RUN_ID"), "CI run ID or local identifier")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	if *commit == "" {
		*commit = readCommit()
	}
	if *runID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			fmt.Fprintln(stderr, "cannot generate run identifier")
			return 1
		}
		*runID = "local-" + hex.EncodeToString(id[:])
	}
	inputAbs, _ := filepath.Abs(*inputPath)
	outputAbs, _ := filepath.Abs(*reportPath)
	if strings.EqualFold(inputAbs, outputAbs) {
		fmt.Fprintln(stderr, "input and report paths must differ")
		return 2
	}
	data, readErr := os.ReadFile(*inputPath)
	if readErr != nil {
		data = nil
	}
	canaries := []string{"SUBURBAN_VERIFY_CANARY_DO_NOT_DISCLOSE"}
	if value := os.Getenv("VERIFY_CANARY"); value != "" {
		canaries = append(canaries, value)
	}
	if err := verify.RedactCheck(data, canaries); err != nil {
		fmt.Fprintln(stderr, "input failed privacy validation; no report written")
		return 1
	}
	report := verify.Preflight(data, *inputPath, *commit, *runID, time.Now())
	if readErr != nil {
		report.Checks = append(report.Checks, verify.Check{Name: "input-readable", Required: true, Status: "fail", Measured: true, Expected: "readable input evidence", Actual: "input file unavailable"})
	}
	digest, err := report.Write(*reportPath, verify.PreflightRequired, canaries)
	if digest != "" {
		fmt.Fprintf(stdout, "report=%s sha256=%s runId=%s\n", *reportPath, digest, *runID)
	}
	if err != nil {
		fmt.Fprintln(stderr, "preflight failed: required evidence or report validation did not pass")
		return 1
	}
	fmt.Fprintln(stdout, "Preflight evidence is complete; blocked prerequisites remain blocked.")
	return 0
}

func runTemplate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("template", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "template.yaml", "SAM or CloudFormation template")
	baseline := flags.String("schema-baseline", "", "actual deployed template for frozen table schemas")
	reportPath := flags.String("report", "", "optional evidence envelope")
	commit := flags.String("commit", os.Getenv("GITHUB_SHA"), "full Git SHA")
	runID := flags.String("run-id", os.Getenv("GITHUB_RUN_ID"), "CI run ID")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if *commit == "" {
		*commit = readCommit()
	}
	if *runID == "" {
		*runID = "local-template"
	}
	var baselineData []byte
	if *baseline != "" {
		var err error
		baselineData, err = os.ReadFile(*baseline)
		if err != nil {
			fmt.Fprintln(stderr, "cannot read deployed schema baseline")
			return 1
		}
	}
	paths := []string{*input}
	if *input == "template.yaml" {
		if _, err := os.Stat("packaged.yaml"); err == nil {
			paths = append(paths, "packaged.yaml")
		}
	}
	var combined verify.Report
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(stderr, "cannot read template")
			return 1
		}
		r := verify.Template(data, path, baselineData, *commit, *runID, time.Now())
		if i == 0 {
			combined = r
		} else {
			for _, check := range r.Checks {
				check.Name = "packaged/" + check.Name
				combined.Checks = append(combined.Checks, check)
			}
		}
	}
	var err error
	if *reportPath != "" {
		for _, path := range append(paths, *baseline) {
			inAbs, _ := filepath.Abs(path)
			outAbs, _ := filepath.Abs(*reportPath)
			if strings.EqualFold(inAbs, outAbs) {
				fmt.Fprintln(stderr, "report must not overwrite input")
				return 2
			}
		}
		canaries := []string{"SUBURBAN_VERIFY_CANARY_DO_NOT_DISCLOSE", os.Getenv("VERIFY_CANARY")}
		var digest string
		digest, err = combined.Write(*reportPath, verify.TemplateRequired, canaries)
		if digest != "" {
			fmt.Fprintf(stdout, "report=%s sha256=%s runId=%s\n", *reportPath, digest, *runID)
		}
	} else {
		err = combined.Validate(verify.TemplateRequired)
	}
	if err != nil {
		for _, check := range combined.Checks {
			if check.Status == "fail" {
				fmt.Fprintf(stderr, "%s: %s\n", check.Name, check.Actual)
			}
		}
		return 1
	}
	fmt.Fprintln(stdout, "Template static checks passed; no deployed runtime proof is implied.")
	for _, check := range combined.Checks {
		if check.Status == "blocked" || check.Status == "skip" {
			fmt.Fprintf(stdout, "%s: %s\n", check.Name, check.Actual)
		}
	}
	return 0
}

// Read repository metadata directly, without starting a shell or Git process.
func readCommit() string {
	gitDir := ".git"
	info, err := os.Stat(gitDir)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		data, err := os.ReadFile(gitDir)
		if err != nil {
			return ""
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(line, "gitdir: ") {
			return ""
		}
		gitDir = strings.TrimPrefix(line, "gitdir: ")
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(head))
	if !strings.HasPrefix(value, "ref: ") {
		return value
	}
	ref := strings.TrimPrefix(value, "ref: ")
	data, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref)))
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(packed), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref {
			return fields[0]
		}
	}
	return ""
}
