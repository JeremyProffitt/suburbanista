package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func syntheticEnvironment() map[string]string {
	return map[string]string{
		"DOMAIN_NAME": "Suburban.jeremy.ninja", "CERTIFICATE_ARN": "fixture-arn", "HOSTED_ZONE_ID": "fixture-zone", "DEPLOYMENT_CANARY": "fixture-canary\nsecond-line",
		"CREDENTIAL_ENCRYPTION_KEY_A": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32)), "CREDENTIAL_ENCRYPTION_KEY_B": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{34}, 32)),
		"CREDENTIAL_ENCRYPTION_KEY_ACTIVE": "A", "OWNER_AMAZON_USER_ID": "synthetic-owner", "ENROLLMENT_VERSION": "1", "GITHUB_SHA": strings.Repeat("a", 40), "ONSTAR_DEVICE_ID": "synthetic-device-identifier",
		"LWA_CLIENT_ID": "amzn1.application-oa2-client.synthetic", "LWA_CLIENT_SECRET": "synthetic-lwa-private-value",
		"ALEXA_SKILL_ID": "amzn1.ask.skill.00000000-0000-4000-8000-000000000001",
	}
}

func TestMapDigestOptionalRevocationAndValidation(t *testing.T) {
	data, err := os.ReadFile("../../samconfig.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", strings.Repeat("a", 64), "malformed"} {
		env := syntheticEnvironment()
		env["MAP_READ_KEY_SHA256"] = value
		args, payload, err := prepare(data, func(k string) string { return env[k] })
		if value == "malformed" {
			if err == nil {
				t.Fatal("malformed digest accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var params []parameter
		if json.Unmarshal(payload, &params) != nil {
			t.Fatal("invalid payload")
		}
		found := false
		for _, p := range params {
			if p.Key == "MapReadKeySHA256" {
				found = true
				if p.Value != value {
					t.Fatal("digest changed")
				}
			}
		}
		if !found {
			t.Fatal("missing digest parameter prevents revocation")
		}
		if value != "" && strings.Contains(strings.Join(args, " "), value) {
			t.Fatal("digest in arguments")
		}
	}
}

func TestExpectedFailure(t *testing.T) {
	failure := errors.New("exit 255")
	for _, tc := range []struct {
		output string
		err    error
		want   bool
	}{
		{"Parameter PollingEnabled must be one of AllowedValues", failure, true},
		{"Parameter PollingEnabled is not allowed", failure, true},
		{"AccessDenied", failure, false},
		{"An error occurred (ValidationError): Parameter PollingEnabled failed to satisfy constraint: Member must satisfy enum value set: [false,true]", failure, true},
		{"AccessDenied: PollingEnabled is not allowed", failure, false},
		{"An error occurred (AccessDenied): not authorized to submit PollingEnabled with AllowedValues", failure, false},
		{"ValidationError: Parameter Other failed to satisfy constraint: Member must satisfy enum value set: [false,true]", failure, false},
		{"ValidationError: Parameter PollingEnabled failed to satisfy constraint: malformed pattern", failure, false},
		{"PollingEnabled: networking failed", failure, false},
		{"PollingEnabled must be one of AllowedValues", nil, false},
	} {
		if expectedFailure([]byte(tc.output), tc.err) != tc.want {
			t.Errorf("unexpected validation classification for %q", tc.output)
		}
	}
}

func TestParametersAndSecretBoundary(t *testing.T) {
	data, err := os.ReadFile("../../samconfig.toml")
	if err != nil {
		t.Fatal(err)
	}
	secret := "fixture-canary\nsecond-line"
	env := syntheticEnvironment()
	args, payload, err := prepare(data, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	var params []parameter
	if err = json.Unmarshal(payload, &params); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range params {
		got[p.Key] = p.Value
	}
	if got["PollingEnabled"] != "true" || got["VehicleKey"] != "truck-8f41b6d379a04ab4a8c2f7ca1268d405" || got["DomainName"] != "suburban.jeremy.ninja" || got["DeploymentCanary"] != secret {
		t.Fatal("parameters not preserved")
	}
	if got["ReleaseBucketName"] == "" {
		t.Fatal("owner release bucket was not configured")
	}
	for key, name := range privateParameters {
		if got[key] != env[name] {
			t.Fatalf("missing private parameter %s", key)
		}
	}
	for key, name := range map[string]string{"CredentialEncryptionKeyActive": "CREDENTIAL_ENCRYPTION_KEY_ACTIVE", "OwnerAmazonUserID": "OWNER_AMAZON_USER_ID", "EnrollmentVersion": "ENROLLMENT_VERSION", "DeploymentNonce": "GITHUB_SHA", "AlexaSkillId": "ALEXA_SKILL_ID"} {
		if got[key] != env[name] {
			t.Fatalf("missing plain parameter %s", key)
		}
	}
	for _, form := range encoded(secret) {
		if strings.Contains(strings.Join(args, " "), form) {
			t.Fatal("secret in argv")
		}
	}
	failure := "AWS failed: " + strings.Join(encoded(secret), " ")
	redacted := redact(failure, secret)
	for _, form := range encoded(secret) {
		if strings.Contains(redacted, form) {
			t.Fatal("failure output leaked")
		}
	}
	var masks bytes.Buffer
	mask(&masks, secret)
	if !strings.Contains(masks.String(), "::add-mask::") {
		t.Fatal("mask absent")
	}
}
func TestRejectSecretTags(t *testing.T) {
	env := syntheticEnvironment()
	env["DEPLOYMENT_CANARY"] = "synthetic-canary"
	for _, secret := range secrets(func(k string) string { return env[k] }) {
		for _, form := range encoded(secret) {
			data := []byte("[default.deploy.parameters]\nstack_name='suburban-remote'\nregion='us-east-1'\ns3_bucket='bucket'\ntags='Owner=" + form + "'\n")
			_, _, err := prepare(data, func(k string) string { return env[k] })
			if err == nil || err.Error() != "secret in argument or tag" {
				t.Fatal("private tag did not reach intended rejection", err)
			}
		}
	}
}

func TestPrivateOutputLeakDetectionAndRedaction(t *testing.T) {
	env := syntheticEnvironment()
	values := secrets(func(k string) string { return env[k] })
	if !cleanOutput([]byte("safe diagnostic"), values) {
		t.Fatal("clean output rejected")
	}
	for _, secret := range values {
		for _, form := range encoded(secret) {
			output := []byte("diagnostic " + form)
			if cleanOutput(output, values) {
				t.Fatal("private output accepted")
			}
			if strings.Contains(redactAll(string(output), values), form) {
				t.Fatal("redaction leaked")
			}
		}
	}
}
func TestMissingAndInvalidPrivateConfiguration(t *testing.T) {
	data, err := os.ReadFile("../../samconfig.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DEPLOYMENT_CANARY", "CREDENTIAL_ENCRYPTION_KEY_A", "CREDENTIAL_ENCRYPTION_KEY_B", "ONSTAR_DEVICE_ID", "CREDENTIAL_ENCRYPTION_KEY_ACTIVE", "OWNER_AMAZON_USER_ID", "ENROLLMENT_VERSION", "GITHUB_SHA"} {
		t.Run(name, func(t *testing.T) {
			env := syntheticEnvironment()
			delete(env, name)
			_, _, err := prepare(data, func(k string) string { return env[k] })
			if err == nil || err.Error() != "missing "+name {
				t.Fatal("missing setting not rejected", err)
			}
		})
	}
	for name, value := range map[string]string{"CREDENTIAL_ENCRYPTION_KEY_A": "not-base64", "CREDENTIAL_ENCRYPTION_KEY_B": base64.StdEncoding.EncodeToString([]byte("short")), "CREDENTIAL_ENCRYPTION_KEY_ACTIVE": "C", "ENROLLMENT_VERSION": "0", "GITHUB_SHA": strings.Repeat("z", 40)} {
		env := syntheticEnvironment()
		env[name] = value
		if _, _, err := prepare(data, func(k string) string { return env[k] }); err == nil {
			t.Fatal("invalid setting accepted", name)
		}
	}
}
func TestLocalGuardPrecedesSecretOutput(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "false")
	t.Setenv("DEPLOYMENT_CANARY", "local-synthetic-private")
	previous := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	failure := run()
	w.Close()
	os.Stdout = previous
	var output bytes.Buffer
	_, _ = output.ReadFrom(r)
	r.Close()
	if failure == nil || failure.Error() != "deployment is allowed only in GitHub Actions on main" || output.Len() != 0 {
		t.Fatal("local guard emitted private output")
	}
}
