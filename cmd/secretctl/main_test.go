package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
)

type mapParameterStub struct {
	key        []byte
	creates    int
	kind, tier string
}

func (s *mapParameterStub) Metadata(context.Context) (mapParameterMetadata, bool, error) {
	if s.key == nil {
		return mapParameterMetadata{}, false, nil
	}
	return mapParameterMetadata{Name: mapParameterName, Type: s.kind, Tier: s.tier, Version: 1}, true, nil
}
func (s *mapParameterStub) Read(context.Context) ([]byte, error) { return bytes.Clone(s.key), nil }
func (s *mapParameterStub) Create(_ context.Context, key []byte) error {
	if s.key != nil {
		return errors.New("overwrite forbidden")
	}
	s.creates++
	s.key = bytes.Clone(key)
	s.kind = "SecureString"
	s.tier = "Standard"
	return nil
}

func TestSSMMapKeyCreatesOnceAndOnlySendsDigestToGitHub(t *testing.T) {
	store := &mapParameterStub{}
	configured := false
	var output bytes.Buffer
	run := func(args []string, input []byte) ([]byte, error) {
		if args[0] != "secret" {
			t.Fatal("wrong GitHub scope")
		}
		if args[1] == "list" {
			if configured {
				return []byte(`[{"name":"MAP_READ_KEY_SHA256","updatedAt":"2026-10-24"}]`), nil
			}
			return []byte(`[]`), nil
		}
		if args[1] != "set" || args[2] != "MAP_READ_KEY_SHA256" || args[4] != repository {
			t.Fatal("wrong digest destination")
		}
		digest := sha256.Sum256(store.key)
		encoded := hex.EncodeToString(digest[:])
		if string(input) != encoded {
			t.Fatal("GitHub received something other than digest")
		}
		if strings.Contains(strings.Join(args, " "), string(store.key)) || strings.Contains(strings.Join(args, " "), encoded) {
			t.Fatal("credential in arguments")
		}
		configured = true
		return nil, nil
	}
	if err := setupSSMMapKey(context.Background(), store, run, &output); err != nil {
		t.Fatal(err)
	}
	first := bytes.Clone(store.key)
	defer clear(first)
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(string(first))
	if err != nil || len(decoded) != 32 {
		t.Fatal("generated key is not32randombytes")
	}
	clear(decoded)
	if err = setupSSMMapKey(context.Background(), store, run, &output); err != nil {
		t.Fatal(err)
	}
	if store.creates != 1 || !bytes.Equal(first, store.key) {
		t.Fatal("existing parameter overwritten")
	}
	digest := sha256.Sum256(first)
	if strings.Contains(output.String(), string(first)) || strings.Contains(output.String(), hex.EncodeToString(digest[:])) {
		t.Fatal("credential printed")
	}
	if !strings.Contains(output.String(), `"DigestConfigured":true`) || !strings.Contains(output.String(), `"Created":false`) {
		t.Fatal("safe receipt missing")
	}
}

func TestSSMMapKeyPreservesInvalidExistingParameter(t *testing.T) {
	for _, kind := range []string{"String", "SecureString"} {
		store := &mapParameterStub{key: []byte("invalid"), kind: kind, tier: "Standard"}
		var output bytes.Buffer
		writes := 0
		run := func(args []string, input []byte) ([]byte, error) {
			if args[1] == "set" {
				writes++
			}
			return []byte(`[]`), nil
		}
		if err := setupSSMMapKey(context.Background(), store, run, &output); err == nil {
			t.Fatal("incompatible parameter accepted")
		}
		if writes != 0 || store.creates != 0 || string(store.key) != "invalid" {
			t.Fatal("existing data changed")
		}
	}
}

type mapSSMHTTP func(*http.Request) (*http.Response, error)

func (f mapSSMHTTP) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSSMMapKeyIsSecureStandardBodyOnly(t *testing.T) {
	key := []byte(strings.Repeat("synthetic", 8))
	defer clear(key)
	for _, status := range []int{200, 403} {
		store := signedMapParameters{credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic-id", SecretAccessKey: "synthetic-private"}, nil
		}), client: &http.Client{Transport: mapSSMHTTP(func(r *http.Request) (*http.Response, error) {
			if r.Method != "POST" || r.URL.String() != "https://ssm.us-east-1.amazonaws.com/" || r.Header.Get("X-Amz-Target") != "AmazonSSM.PutParameter" || r.Header.Get("Authorization") == "" {
				t.Fatal("SSM request target/signature wrong")
			}
			var input struct {
				Name, Type, Tier, KeyId, Value string
				Overwrite                      bool
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Name != mapParameterName || input.Type != "SecureString" || input.Tier != "Standard" || input.KeyId != "alias/aws/ssm" || input.Overwrite || input.Value != string(key) {
				t.Fatal("SSM encryption/create-only contract differs")
			}
			body := `{"Version":1,"Tier":"Standard"}`
			if status != 200 {
				body = string(key)
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
		})}}
		err := store.Create(context.Background(), key)
		if (err == nil) != (status == 200) {
			t.Fatal("wrong SSM create result")
		}
		if err != nil && strings.Contains(err.Error(), string(key)) {
			t.Fatal("SSM error leaked body")
		}
	}
}

func TestSSMMapLiveVerificationUsesOnlyCachedGETAndSafeStatuses(t *testing.T) {
	store := &mapParameterStub{key: []byte(strings.Repeat("synthetic", 8)), kind: "SecureString", tier: "Standard"}
	var output bytes.Buffer
	calls := 0
	client := &http.Client{Transport: mapSSMHTTP(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "suburban.jeremy.ninja" {
			t.Fatal("unsafe live proof request")
		}
		status := 401
		if r.URL.Path == "/api/map.png" && r.URL.Query().Get("key") == string(store.key) {
			status = 503
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(store.key))), Header: http.Header{}}, nil
	})}
	if err := verifySSMMapAccess(context.Background(), store, client, &output); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || store.creates != 0 || strings.Contains(output.String(), string(store.key)) || strings.Contains(output.String(), "https://") {
		t.Fatal("live proof leaked or mutated")
	}
	if !strings.Contains(output.String(), `"valid-key":503`) || !strings.Contains(output.String(), `"key-cannot-read-command-status":401`) {
		t.Fatal("safe status evidence missing")
	}
}

func TestSSMMapVerificationPollsPendingWithoutRetainingPrivateData(t *testing.T) {
	for _, ready := range []bool{false, true} {
		store := &mapParameterStub{key: []byte(strings.Repeat("synthetic", 8)), kind: "SecureString", tier: "Standard"}
		var output bytes.Buffer
		calls, polls, waits := 0, 0, 0
		client := &http.Client{Transport: mapSSMHTTP(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "GET" || (r.URL.Path != "/api/map.png" && r.URL.Path != "/api/commands/status") {
				t.Fatal("verification sent another operation")
			}
			status := 401
			if r.URL.Path == "/api/map.png" && r.URL.Query().Get("key") == string(store.key) {
				polls++
				status = http.StatusAccepted
				if ready && polls == 2 {
					status = http.StatusOK
				}
			}
			data := []byte(store.key)
			if status == http.StatusOK {
				var encoded bytes.Buffer
				if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
					t.Fatal(err)
				}
				data = encoded.Bytes()
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: http.Header{}}, nil
		})}
		err := verifySSMMapAccessWithWait(context.Background(), store, client, &output, func(context.Context) bool { waits++; return true })
		if ready && (err != nil || polls != 2 || waits != 1 || calls != 5 || !strings.Contains(output.String(), `"valid-key":200`)) {
			t.Fatal("pending map did not become ready", err)
		}
		if !ready && (err == nil || polls != 9 || waits != 8 || calls != 9) {
			t.Fatal("pending verification did not stop at its bound")
		}
		if strings.Contains(output.String(), string(store.key)) || strings.Contains(output.String(), "https://") || store.creates != 0 {
			t.Fatal("verification leaked or changed key")
		}
	}
}

func TestSSMMapVerificationConfinesLargeImageRedirect(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 3072, 2048))); err != nil {
		t.Fatal(err)
	}
	validPNG := encoded.Bytes()
	oversizedBody := append(bytes.Clone(validPNG), make([]byte, contract.MaxMapImageBytes+1-len(validPNG))...)
	defer clear(oversizedBody)
	query := url.Values{"X-Amz-Algorithm": {"AWS4-HMAC-SHA256"}, "X-Amz-Credential": {"synthetic/20261025/us-east-1/s3/aws4_request"}, "X-Amz-Date": {time.Now().UTC().Format("20060102T150405Z")}, "X-Amz-Expires": {"120"}, "X-Amz-SignedHeaders": {"host"}, "X-Amz-Signature": {strings.Repeat("a", 64)}}
	ticket := "https://s3.us-east-1.amazonaws.com/synthetic-map-bucket/map-cache/" + strings.Repeat("a", 32) + ".png?" + query.Encode()
	var oversizedDimensions bytes.Buffer
	if err := png.Encode(&oversizedDimensions, image.NewNRGBA(image.Rect(0, 0, contract.MaxMapDimension+1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, address string
		code, s3code  int
		body          []byte
		length        int64
		valid         bool
		wantS3        int
	}{
		{"temporary", ticket, 307, 200, validPNG, int64(len(validPNG)), true, 1},
		{"found", ticket, 302, 200, validPNG, int64(len(validPNG)), true, 1},
		{"foreign-host", strings.Replace(ticket, "s3.us-east-1.amazonaws.com", "example.invalid", 1), 307, 200, validPNG, int64(len(validPNG)), false, 0},
		{"other-object", strings.Replace(ticket, "/map-cache/", "/other/", 1), 307, 200, validPNG, int64(len(validPNG)), false, 0},
		{"expired", strings.Replace(ticket, "X-Amz-Expires=120", "X-Amz-Expires=999", 1), 307, 200, validPNG, int64(len(validPNG)), false, 0},
		{"second-redirect", ticket, 307, 307, validPNG, int64(len(validPNG)), false, 1},
		{"not-png", ticket, 307, 200, []byte("synthetic-not-an-image"), int64(len("synthetic-not-an-image")), false, 1},
		{"missing-length", ticket, 307, 200, validPNG, -1, false, 1},
		{"wrong-length", ticket, 307, 200, validPNG, int64(len(validPNG) + 1), false, 1},
		{"too-large", ticket, 307, 200, validPNG, contract.MaxMapImageBytes + 1, false, 1},
		{"body-too-large", ticket, 307, 200, oversizedBody, int64(len(validPNG)), false, 1},
		{"dimensions", ticket, 307, 200, oversizedDimensions.Bytes(), int64(oversizedDimensions.Len()), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &mapParameterStub{key: []byte(strings.Repeat("synthetic", 8)), kind: "SecureString", tier: "Standard"}
			var output bytes.Buffer
			s3calls, apiCalls := 0, 0
			client := &http.Client{Transport: mapSSMHTTP(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("unexpected method")
				}
				if r.URL.Host == "s3.us-east-1.amazonaws.com" {
					s3calls++
					if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.Query().Has("key") {
						t.Fatal("map credential forwarded")
					}
					return &http.Response{StatusCode: tc.s3code, Body: io.NopCloser(bytes.NewReader(tc.body)), ContentLength: tc.length, Header: http.Header{"Location": {ticket}}}, nil
				}
				if r.URL.Host != "suburban.jeremy.ninja" {
					t.Fatal("unapproved destination")
				}
				apiCalls++
				code := 401
				if r.URL.Path == "/api/map.png" && r.URL.Query().Get("key") == string(store.key) {
					code = tc.code
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": {tc.address}}}, nil
			})}
			client.Jar, _ = cookiejar.New(nil)
			cookieURL, _ := url.Parse("https://s3.us-east-1.amazonaws.com/")
			client.Jar.SetCookies(cookieURL, []*http.Cookie{{Name: "synthetic", Value: "private-cookie", Path: "/"}})
			err := verifySSMMapAccessWithWait(context.Background(), store, client, &output, func(context.Context) bool { return true })
			if (err == nil) != tc.valid || s3calls != tc.wantS3 {
				t.Fatalf("unexpected redirect result: valid=%t calls=%d error=%v", err == nil, s3calls, err)
			}
			if tc.valid && (apiCalls != 4 || !strings.Contains(output.String(), `"valid-key":200`)) {
				t.Fatal("safe success proof missing")
			}
			if tc.valid {
				var proof map[string]int
				if json.Unmarshal(output.Bytes(), &proof) != nil || proof["image-width"] != 3072 || proof["image-height"] != 2048 || proof["image-bytes"] != len(validPNG) {
					t.Fatal("measured image dimensions and byte count missing")
				}
				if proof["valid-key"] != 200 || proof["missing-key"] != 401 || proof["bad-key"] != 401 || proof["key-cannot-read-command-status"] != 401 {
					t.Fatal("access-status evidence changed")
				}
			}
			if strings.Contains(output.String(), string(store.key)) || strings.Contains(output.String(), "https://") || (err != nil && strings.Contains(err.Error(), "https://")) {
				t.Fatal("private request details leaked")
			}
		})
	}
	t.Run("unauthorized-redirect-not-followed", func(t *testing.T) {
		store := &mapParameterStub{key: []byte(strings.Repeat("synthetic", 8)), kind: "SecureString", tier: "Standard"}
		var output bytes.Buffer
		calls := 0
		client := &http.Client{Transport: mapSSMHTTP(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Host != "suburban.jeremy.ninja" {
				t.Fatal("unauthorized redirect followed")
			}
			if r.URL.Path == "/api/map.png" && r.URL.Query().Get("key") == string(store.key) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(validPNG)), ContentLength: int64(len(validPNG)), Header: http.Header{}}, nil
			}
			return &http.Response{StatusCode: 307, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": {ticket}}}, nil
		})}
		if err := verifySSMMapAccessWithWait(context.Background(), store, client, &output, func(context.Context) bool { return true }); err == nil || calls != 4 {
			t.Fatal("unauthorized redirect did not fail safely")
		}
	})
}

func TestMapKeyHiddenDigestOnlySetup(t *testing.T) {
	for _, tc := range []struct {
		name, value                     string
		exists, replace, write, invalid bool
	}{
		{"new", strings.Repeat("synthetic", 8), false, false, true, false},
		{"preserve", strings.Repeat("synthetic", 8), true, false, false, false},
		{"rotate", strings.Repeat("synthetic", 8), true, true, true, false},
		{"skip", "", false, false, false, false},
		{"invalid", "short", false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			writes, prompts := 0, 0
			exists := tc.exists
			run := func(args []string, input []byte) ([]byte, error) {
				if strings.Contains(strings.Join(args, " "), tc.value) && tc.value != "" {
					t.Fatal("raw key in arguments")
				}
				if args[0] != "secret" {
					t.Fatal("wrong scope")
				}
				switch args[1] {
				case "list":
					if exists {
						return []byte(`[{"name":"MAP_READ_KEY_SHA256","updatedAt":"2026-10-24"}]`), nil
					}
					return []byte(`[]`), nil
				case "set":
					writes++
					exists = true
					digest := sha256.Sum256([]byte(tc.value))
					if args[2] != "MAP_READ_KEY_SHA256" || string(input) != hex.EncodeToString(digest[:]) {
						t.Fatal("wrong digest/name")
					}
					return nil, nil
				}
				t.Fatal("unexpected operation")
				return nil, nil
			}
			err := setupMapKey(run, func(string) ([]byte, error) { prompts++; return []byte(tc.value), nil }, &output, tc.replace)
			if (err != nil) != tc.invalid || (writes == 1) != tc.write {
				t.Fatal("wrong setup result", err, writes)
			}
			if tc.exists && !tc.replace && prompts != 0 {
				t.Fatal("prompted for existing secret")
			}
			if tc.value != "" && strings.Contains(output.String(), tc.value) {
				t.Fatal("raw key printed")
			}
		})
	}
}

func TestGeneratedKeyFormats(t *testing.T) {
	for _, name := range []string{"CREDENTIAL_ENCRYPTION_KEY_A", "CREDENTIAL_ENCRYPTION_KEY_B"} {
		value, _, err := newKey(name)
		raw, decodeErr := base64.StdEncoding.DecodeString(string(value))
		if err != nil || decodeErr != nil || len(raw) != 32 {
			t.Fatal("invalid AES-256 key")
		}
	}
	value, publicValue, err := newKey("UPDATE_MANIFEST_SIGNING_KEY")
	private, e1 := base64.StdEncoding.DecodeString(string(value))
	public, e2 := base64.StdEncoding.DecodeString(string(publicValue))
	if err != nil || e1 != nil || e2 != nil || len(private) != ed25519.PrivateKeySize {
		t.Fatal("invalid Ed25519 key")
	}
	message := []byte("synthetic release manifest")
	if !ed25519.Verify(public, message, ed25519.Sign(private, message)) {
		t.Fatal("signature verification failed")
	}
	device, _, err := newKey("ONSTAR_DEVICE_ID")
	if err != nil || len(device) != 36 || device[14] != '4' || !strings.ContainsRune("89ab", rune(device[19])) {
		t.Fatal("invalid device identity format")
	}
}

func TestAndroidPartialInstallResumesSameKey(t *testing.T) {
	secrets := map[string]string{}
	variables := map[string]string{}
	failed := false
	var calls []string
	var output bytes.Buffer
	run := func(args []string, input []byte) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
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
			if args[2] == "ANDROID_KEYSTORE_PASSWORD" && !failed {
				failed = true
				return nil, errors.New("unsafe secret diagnostic")
			}
			if _, exists := values[args[2]]; exists {
				t.Fatal("existing value overwritten")
			}
			values[args[2]] = string(input)
			return nil, nil
		}
		t.Fatal("unexpected call")
		return nil, nil
	}
	loads := 0
	loader := func(_ string, allowCreate bool) (androidRecovery, error) {
		loads++
		if loads > 1 && allowCreate {
			t.Fatal("partial install allowed a new key")
		}
		return androidRecovery{Keystore: []byte("synthetic-keystore-value"), StorePassword: []byte("synthetic-store-password"), KeyPassword: []byte("synthetic-key-password"), CertificateSHA256: strings.Repeat("a", 64)}, nil
	}
	if err := generateAndroid(run, "synthetic-path", &output, loader); err == nil || strings.Contains(err.Error(), "unsafe") {
		t.Fatal("partial failure accepted or leaked")
	}
	if err := generateAndroid(run, "synthetic-path", &output, loader); err != nil {
		t.Fatal(err)
	}
	if err := generateAndroid(run, "synthetic-path", &output, loader); err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 3 || len(variables) != 2 {
		t.Fatal("incomplete Android inventory")
	}
	for _, value := range secrets {
		if strings.Contains(output.String(), value) || strings.Contains(strings.Join(calls, "\n"), value) {
			t.Fatal("secret leaked")
		}
	}
}

func TestAndroidUnknownPartialInventoryRejected(t *testing.T) {
	loaded := false
	run := func(args []string, _ []byte) ([]byte, error) {
		if args[1] != "list" {
			t.Fatal("unexpected mutation")
		}
		if args[0] == "secret" {
			return []byte(`[{"name":"ANDROID_KEY_PASSWORD","updatedAt":"2026-10-22"}]`), nil
		}
		return []byte(`[]`), nil
	}
	var output bytes.Buffer
	err := generateAndroid(run, "synthetic", &output, func(string, bool) (androidRecovery, error) { loaded = true; return androidRecovery{}, nil })
	if err == nil || loaded {
		t.Fatal("unknown partial inventory accepted")
	}
}

func TestRecoveryCannotBeInsideRepository(t *testing.T) {
	if _, err := recoveryLocation(filepath.Join(".", "recovery")); err == nil {
		t.Fatal("repository recovery allowed")
	}
}

func TestAndroidOversizeKeystoreHasNoWrites(t *testing.T) {
	writes := 0
	run := func(args []string, _ []byte) ([]byte, error) {
		if args[1] == "set" {
			writes++
			t.Fatal("oversize key triggered write")
		}
		return []byte(`[]`), nil
	}
	var output bytes.Buffer
	err := generateAndroid(run, "synthetic", &output, func(string, bool) (androidRecovery, error) {
		return androidRecovery{Keystore: make([]byte, 48*1024)}, nil
	})
	if err == nil || writes != 0 {
		t.Fatal("oversize keystore accepted")
	}
}

func TestGenerateStdinAndPreservation(t *testing.T) {
	secrets := map[string]string{}
	variables := map[string]string{}
	var calls, privateValues []string
	run := func(args []string, input []byte) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if !strings.Contains(strings.Join(args, " "), repository) {
			t.Fatal("wrong repository")
		}
		values := secrets
		if args[0] == "variable" {
			values = variables
		}
		if args[1] == "list" {
			var entries []metadata
			for name := range values {
				entries = append(entries, metadata{Name: name, UpdatedAt: "2026-10-22T00:00:00Z"})
			}
			return json.Marshal(entries)
		}
		if args[1] == "get" && args[0] == "variable" {
			return []byte(values[args[2]]), nil
		}
		if args[1] != "set" || len(input) == 0 {
			t.Fatal("missing stdin")
		}
		if _, exists := values[args[2]]; exists {
			t.Fatal("existing value overwritten")
		}
		values[args[2]] = string(input)
		if args[0] == "secret" {
			privateValues = append(privateValues, string(input))
		}
		return nil, nil
	}
	var output bytes.Buffer
	if err := generate(run, &output); err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 4 || len(variables) != 2 {
		t.Fatal("wrong generated inventory")
	}
	if err := generate(run, &output); err != nil {
		t.Fatal(err)
	}
	for _, value := range privateValues {
		if strings.Contains(output.String(), value) || strings.Contains(strings.Join(calls, "\n"), value) {
			t.Fatal("secret appeared in output or arguments")
		}
	}
}

func TestWriteFailureDoesNotReportSuccess(t *testing.T) {
	run := func(args []string, input []byte) ([]byte, error) {
		if args[1] == "list" {
			return []byte("[]"), nil
		}
		return nil, errors.New("unsafe diagnostic must not escape")
	}
	var output bytes.Buffer
	err := generate(run, &output)
	if err == nil || strings.Contains(err.Error(), "unsafe diagnostic") || strings.Contains(output.String(), "installed") {
		t.Fatal("failed write leaked or reported success")
	}
}
