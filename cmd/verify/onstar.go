package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/verify"
	"github.com/JeremyProffittOrg/suburban-remote/internal/worker"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

func runOnStar(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("onstar", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "", "deployed required")
	live := flags.Bool("live", false, "perform authorized read-only provider proof")
	function := flags.String("function", "", "exact worker Lambda ARN")
	nonce := flags.String("nonce", "", "expected deployment nonce")
	commit := flags.String("commit", os.Getenv("GITHUB_SHA"), "expected full commit SHA")
	runID := flags.String("run-id", os.Getenv("GITHUB_RUN_ID"), "unique persisted proof run ID")
	path := flags.String("report", "artifacts/onstar-invocation.json", "safe evidence envelope")
	firstPath := flags.String("first-report", "", "first safe invocation report for aggregate mode")
	secondPath := flags.String("second-report", "", "second safe invocation report for aggregate mode")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return 2
	}
	if *mode == "aggregate" {
		var inputs [2]verify.Report
		var evidence [2]verify.Evidence
		for i, p := range []string{*firstPath, *secondPath} {
			data, e := os.ReadFile(p)
			if e != nil || len(data) > 128*1024 || verify.RedactCheck(data, nil) != nil || json.Unmarshal(data, &inputs[i]) != nil {
				fmt.Fprintln(stderr, "onstar invocation report unavailable or invalid")
				return 1
			}
			digest := sha256.Sum256(data)
			evidence[i] = verify.Evidence{Path: p, SHA256: fmt.Sprintf("%x", digest)}
		}
		report := verify.OnStarPair(inputs[0], inputs[1], *commit, *runID, time.Now())
		report.Checks[0].Evidence = []verify.Evidence{evidence[0]}
		report.Checks[1].Evidence = []verify.Evidence{evidence[1]}
		digest, e := report.Write(*path, verify.OnStarPairRequired, nil)
		if digest != "" {
			fmt.Fprintf(stdout, "report=%s sha256=%s runId=%s\n", *path, digest, *runID)
		}
		if e != nil {
			fmt.Fprintln(stderr, "two-cold-start proof failed")
			return 1
		}
		return 0
	}
	arnPattern := regexp.MustCompile(`^arn:aws:lambda:us-east-1:759775734231:function:[A-Za-z0-9_-]{1,64}$`)
	idPattern := regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	if *mode != "deployed" || !*live || !arnPattern.MatchString(*function) || *nonce != *commit || !idPattern.MatchString(*runID) || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(*commit) || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_REF") != "refs/heads/main" || os.Getenv("GITHUB_REPOSITORY") != "JeremyProffittOrg/Suburban-Remote" || os.Getenv("GITHUB_SHA") != *commit {
		fmt.Fprintln(stderr, "onstar proof requires authorized main CI, explicit live mode, and exact deployment identifiers")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()
	cfg, e := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"), config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }), config.WithHTTPClient(&http.Client{Timeout: 130 * time.Second}))
	if e != nil {
		fmt.Fprintln(stderr, "onstar proof AWS configuration failed")
		return 1
	}
	input, _ := json.Marshal(worker.Request{Live: true, RunID: *runID, DeployNonce: *nonce})
	result, e := lambda.NewFromConfig(cfg).Invoke(ctx, &lambda.InvokeInput{FunctionName: function, Payload: input, InvocationType: types.InvocationTypeRequestResponse, LogType: types.LogTypeTail})
	var payload, logs []byte
	invokeOK := false
	if e == nil && result != nil {
		payload = result.Payload
		logs, _ = base64.StdEncoding.DecodeString(aws.ToString(result.LogResult))
		invokeOK = result.StatusCode == 200 && result.FunctionError == nil
	}
	defer clear(payload)
	defer clear(logs)
	report := verify.OnStarInvocation(payload, logs, invokeOK, *commit, *runID, *nonce, time.Now())
	digest, e := report.Write(*path, verify.OnStarRequired, nil)
	if digest != "" {
		fmt.Fprintf(stdout, "report=%s sha256=%s runId=%s\n", *path, digest, *runID)
	}
	if e != nil {
		fmt.Fprintln(stderr, "onstar proof failed; safe evidence report contains failed checks")
		return 1
	}
	fmt.Fprintln(stdout, "onstar deployed invocation passed; no actuation or coordinate-schema proof implied")
	return 0
}
