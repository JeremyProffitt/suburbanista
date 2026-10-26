// paramwriter sends deployment parameters directly to AWS CLI stdin, never argv or disk.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/verify"
	"github.com/pelletier/go-toml/v2"
)

type settings struct {
	Default struct {
		Deploy struct {
			Parameters struct {
				Stack     string `toml:"stack_name"`
				Region    string `toml:"region"`
				Bucket    string `toml:"s3_bucket"`
				Prefix    string `toml:"s3_prefix"`
				Tags      string `toml:"tags"`
				Overrides string `toml:"parameter_overrides"`
			} `toml:"parameters"`
		} `toml:"deploy"`
	} `toml:"default"`
}
type parameter struct {
	Key   string `json:"ParameterKey"`
	Value string `json:"ParameterValue"`
}

func encoded(value string) []string {
	j, _ := json.Marshal(value)
	return []string{value, base64.StdEncoding.EncodeToString([]byte(value)), string(j[1 : len(j)-1]), url.QueryEscape(value)}
}
func mask(w io.Writer, secret string) {
	if secret == "" {
		return
	}
	for _, form := range encoded(secret) {
		for _, line := range strings.Split(form, "\n") {
			if line != "" {
				fmt.Fprintf(w, "::add-mask::%s\n", strings.NewReplacer("%", "%25", "\r", "%0D").Replace(line))
			}
		}
	}
}
func redact(s, secret string) string {
	if secret != "" {
		for _, form := range encoded(secret) {
			s = strings.ReplaceAll(s, form, "[REDACTED]")
		}
		for _, line := range strings.Split(secret, "\n") {
			if line != "" {
				s = strings.ReplaceAll(s, line, "[REDACTED]")
			}
		}
	}
	return s
}

var privateParameters = map[string]string{"DeploymentCanary": "DEPLOYMENT_CANARY", "CredentialEncryptionKeyA": "CREDENTIAL_ENCRYPTION_KEY_A", "CredentialEncryptionKeyB": "CREDENTIAL_ENCRYPTION_KEY_B", "OnstarDeviceID": "ONSTAR_DEVICE_ID", "LwaClientSecret": "LWA_CLIENT_SECRET", "MapReadKeySHA256": "MAP_READ_KEY_SHA256"}

func secrets(env func(string) string) []string {
	values := []string{}
	for _, name := range privateParameters {
		if value := env(name); value != "" {
			values = append(values, value)
		}
	}
	return values
}
func cleanOutput(output []byte, values []string) bool {
	for _, secret := range values {
		for _, form := range encoded(secret) {
			if bytes.Contains(output, []byte(form)) {
				return false
			}
		}
	}
	return true
}
func redactAll(output string, values []string) string {
	for _, secret := range values {
		output = redact(output, secret)
	}
	return output
}
func prepare(data []byte, env func(string) string) ([]string, []byte, error) {
	var config settings
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, nil, errors.New("invalid samconfig")
	}
	c := config.Default.Deploy.Parameters
	if c.Stack != "suburban-remote" || c.Region != "us-east-1" || c.Bucket == "" {
		return nil, nil, errors.New("invalid deployment target")
	}
	params := map[string]string{}
	for _, item := range strings.Fields(c.Overrides) {
		k, v, ok := strings.Cut(item, "=")
		if !ok || k == "" {
			return nil, nil, errors.New("invalid parameter override")
		}
		params[k] = v
	}
	for k, e := range map[string]string{"DomainName": "DOMAIN_NAME", "CertificateArn": "CERTIFICATE_ARN", "HostedZoneId": "HOSTED_ZONE_ID", "CredentialEncryptionKeyActive": "CREDENTIAL_ENCRYPTION_KEY_ACTIVE", "OwnerAmazonUserID": "OWNER_AMAZON_USER_ID", "EnrollmentVersion": "ENROLLMENT_VERSION", "DeploymentNonce": "GITHUB_SHA", "AlexaSkillId": "ALEXA_SKILL_ID"} {
		v := env(e)
		if v == "" {
			return nil, nil, fmt.Errorf("missing %s", e)
		}
		params[k] = v
	}
	for key, name := range privateParameters {
		value := env(name)
		if value == "" && key != "MapReadKeySHA256" {
			return nil, nil, fmt.Errorf("missing %s", name)
		}
		params[key] = value
	}
	if value := params["MapReadKeySHA256"]; value != "" {
		if digest, err := hex.DecodeString(value); err != nil || len(digest) != sha256.Size || len(value) != 64 {
			return nil, nil, errors.New("invalid MAP_READ_KEY_SHA256")
		}
	}
	if params["LwaClientID"] = env("LWA_CLIENT_ID"); params["LwaClientID"] == "" {
		return nil, nil, errors.New("missing LWA_CLIENT_ID")
	}
	for _, name := range []string{"CREDENTIAL_ENCRYPTION_KEY_A", "CREDENTIAL_ENCRYPTION_KEY_B"} {
		key, err := base64.StdEncoding.DecodeString(env(name))
		if err != nil || len(key) != 32 {
			clear(key)
			return nil, nil, errors.New("invalid credential encryption key")
		}
		clear(key)
	}
	if active := params["CredentialEncryptionKeyActive"]; active != "A" && active != "B" {
		return nil, nil, errors.New("invalid credential encryption slot")
	}
	if version, err := strconv.ParseInt(params["EnrollmentVersion"], 10, 64); err != nil || version <= 0 {
		return nil, nil, errors.New("invalid enrollment version")
	}
	if sha, err := hex.DecodeString(params["DeploymentNonce"]); err != nil || len(sha) != 20 || len(params["DeploymentNonce"]) != 40 {
		return nil, nil, errors.New("invalid deployment nonce")
	}
	params["DomainName"] = strings.ToLower(params["DomainName"])
	secret := env("DEPLOYMENT_CANARY")
	params["DeploymentCanary"] = secret
	digest := sha256.Sum256([]byte(secret))
	params["CanarySHA256"] = hex.EncodeToString(digest[:])
	bucket := env("CLOUDFORMATION_S3_BUCKET")
	if bucket == "" {
		bucket = c.Bucket
	}
	params["ReleaseBucketName"] = bucket
	args := []string{"cloudformation", "deploy", "--parameter-overrides", "file:///dev/stdin", "--template-file", "packaged.yaml", "--stack-name", c.Stack, "--capabilities", "CAPABILITY_IAM", "--region", c.Region, "--no-fail-on-empty-changeset", "--s3-bucket", bucket, "--s3-prefix", c.Prefix, "--no-cli-pager", "--tags"}
	for _, tag := range strings.Fields(c.Tags) {
		if !strings.Contains(tag, "=") {
			return nil, nil, errors.New("invalid tag")
		}
		args = append(args, tag)
	}
	for _, secret := range secrets(env) {
		for _, a := range args {
			for _, form := range encoded(secret) {
				if strings.Contains(a, form) {
					return nil, nil, errors.New("secret in argument or tag")
				}
			}
		}
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]parameter, 0, len(keys))
	for _, k := range keys {
		values = append(values, parameter{k, params[k]})
	}
	payload, err := json.Marshal(values)
	return args, payload, err
}
func expectedFailure(output []byte, failed error) bool {
	text := strings.ToLower(string(output))
	if failed == nil || !strings.Contains(text, "pollingenabled") || strings.Contains(text, "accessdenied") || strings.Contains(text, "not authorized") {
		return false
	}
	if strings.Contains(text, "allowedvalues") || strings.Contains(text, "is not allowed") {
		return true
	}
	return strings.Contains(text, "validationerror") && strings.Contains(text, "failed to satisfy constraint") && strings.Contains(text, "enum value set")
}
func run() error {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_REF") != "refs/heads/main" {
		return errors.New("deployment is allowed only in GitHub Actions on main")
	}
	secret := os.Getenv("DEPLOYMENT_CANARY")
	canary := len(os.Args) == 2 && os.Args[1] == "--canary"
	if canary {
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return errors.New("canary generation failed")
		}
		secret = base64.StdEncoding.EncodeToString(random[:])
	}
	env := func(k string) string {
		if k == "DEPLOYMENT_CANARY" {
			return secret
		}
		return os.Getenv(k)
	}
	privateValues := secrets(env)
	for _, value := range privateValues {
		mask(os.Stdout, value)
	}
	if canary {
		fingerprints, err := json.Marshal(verify.CanaryDigests(encoded(secret)))
		if err != nil {
			return errors.New("cannot encode canary fingerprints")
		}
		if err := os.WriteFile("canary-fingerprints.json", fingerprints, 0600); err != nil {
			return errors.New("cannot retain public canary fingerprints")
		}
	}
	data, err := os.ReadFile("samconfig.toml")
	if err != nil {
		return errors.New("cannot read samconfig")
	}
	args, payload, err := prepare(data, env)
	if err != nil {
		return err
	}
	if canary {
		fmt.Println("canary argv: encoded forms absent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	deploy := func(input []byte) ([]byte, error) {
		command := exec.CommandContext(ctx, "aws", args...)
		command.Stdin = bytes.NewReader(input)
		return command.CombinedOutput()
	}
	clean := func(output []byte) bool { return cleanOutput(output, privateValues) }
	if canary {
		var invalid []parameter
		if err = json.Unmarshal(payload, &invalid); err != nil {
			return err
		}
		for i := range invalid {
			if invalid[i].Key == "PollingEnabled" {
				invalid[i].Value = "invalid-canary-value"
			}
		}
		bad, _ := json.Marshal(invalid)
		output, failed := deploy(bad)
		if failed == nil {
			return errors.New("forced-failure deployment unexpectedly succeeded")
		}
		if !clean(output) {
			return errors.New("forced-failure child output leaked private value")
		}
		if !expectedFailure(output, failed) {
			fmt.Fprint(os.Stderr, redactAll(string(output), privateValues))
			return errors.New("forced-failure did not reach parameter validation")
		}
		fmt.Println("canary forced-failure: parameter rejected; captured child output clean")
	}
	output, err := deploy(payload)
	if !clean(output) {
		return errors.New("deployment child output leaked private value")
	}
	fmt.Fprint(os.Stdout, redactAll(string(output), privateValues))
	if err != nil {
		return errors.New("CloudFormation deploy failed")
	}
	if canary {
		fmt.Println("canary success: captured child output clean; runtime selftest awaits health verification")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
