package web

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrivateReleaseStoreSignsExactS3Request(t *testing.T) {
	credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "synthetic-access", SecretAccessKey: "synthetic-private", SessionToken: "synthetic-session"}, nil
	})
	calls := 0
	store := S3Releases{Bucket: "synthetic-bucket", Credentials: credentials, HTTP: &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "s3.us-east-1.amazonaws.com" || r.URL.Path != "/synthetic-bucket/suburban-remote/android/latest.json" || r.Header.Get("X-Amz-Content-Sha256") != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" || r.Header.Get("Authorization") == "" {
			t.Fatal("S3 request origin, path, or signing headers")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"synthetic":true}`))}, nil
	})}}
	data, err := store.Manifest(context.Background(), 0)
	if err != nil || string(data) != `{"synthetic":true}` || calls != 1 {
		t.Fatal("manifest request failed")
	}
	address, err := store.Download(context.Background(), 7)
	if err != nil {
		t.Fatal("presign failed")
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Path != "/synthetic-bucket/suburban-remote/android/7/suburban-remote-7.apk" || parsed.Query().Get("X-Amz-Expires") != "300" || parsed.Query().Get("X-Amz-SignedHeaders") != "host" {
		t.Fatal("private download path or lifetime")
	}
}

type releaseStub struct {
	Data                 []byte
	URL                  string
	Err                  error
	Manifests, Downloads int
}

func (s *releaseStub) Manifest(context.Context, int64) ([]byte, error) {
	s.Manifests++
	return s.Data, s.Err
}
func (s *releaseStub) Download(context.Context, int64) (string, error) {
	s.Downloads++
	return s.URL, s.Err
}
func releaseSession(t *testing.T, now time.Time) (*auth.MobileService, string) {
	t.Helper()
	family, issued, err := auth.NewFamily("synthetic-owner", "synthetic-owner", "synthetic-owner", "synthetic-phone", now)
	if err != nil {
		t.Fatal(err)
	}
	fdata, _ := json.Marshal(struct {
		Family      auth.Family
		Tokens      amazon.Tokens
		ValidatedAt time.Time
	}{family, amazon.Tokens{AccessToken: "synthetic-server-token"}, now})
	adata, _ := json.Marshal(struct {
		FamilyID  string
		ExpiresAt time.Time
	}{family.ID, issued.AccessExpiresAt})
	hash := sha256.Sum256([]byte(issued.AccessHandle))
	digest := hex.EncodeToString(hash[:])
	docs := sessionDocuments{items: map[string]auth.Document{"family#" + family.ID: {Key: "family#" + family.ID, Version: 1, ExpiresAt: family.Deadline, Data: fdata}, "access#" + digest: {Key: "access#" + digest, Version: 1, ExpiresAt: issued.AccessExpiresAt, Data: adata}}}
	return &auth.MobileService{Store: docs, Owner: "synthetic-owner", Now: func() time.Time { return now }}, issued.AccessHandle
}
func releaseEnvelope(t *testing.T, now time.Time, version int64) []byte {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{23}, 32))
	manifest := contract.ReleaseManifest{SchemaVersion: 1, PackageID: contract.AndroidPackageID, VersionName: "1.0.7", VersionCode: version, MinimumAndroidAPI: 23, ReleasedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), ArtifactID: contract.ReleaseArtifactID(version), APKBytes: 12345, APKSHA256: strings.Repeat("a", 64), SigningCertificateSHA256: strings.Repeat("b", 64), DownloadRoute: contract.ReleaseDownloadRoute(version)}
	payload, _ := json.Marshal(manifest)
	out, _ := json.Marshal(contract.SignedRelease{SchemaVersion: 1, KeyID: "primary", Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload))})
	return out
}
func TestReleaseRoutesRequireOwnerAndPreserveSignedBytes(t *testing.T) {
	now := time.Now().UTC()
	mobile, access := releaseSession(t, now)
	releases := &releaseStub{Data: releaseEnvelope(t, now, 7), URL: "https://s3.us-east-1.amazonaws.com/synthetic-bucket/suburban-remote/android/7/suburban-remote-7.apk?X-Amz-Signature=synthetic"}
	adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: mobile, Provider: &state.Store{}, Releases: releases})
	if err != nil {
		t.Fatal(err)
	}
	event := request(t)
	event.RawPath = "/api/releases/latest"
	event.RawQueryString = ""
	event.Headers = map[string]string{}
	event.Cookies = nil
	event.RequestContext.HTTP.Method = "GET"
	out, err := adapter.Handle(context.Background(), event)
	if err != nil || out.StatusCode != 401 || releases.Manifests != 0 {
		t.Fatal("anonymous manifest access reached store")
	}
	event.Headers["authorization"] = "Bearer " + access
	out, err = adapter.Handle(context.Background(), event)
	if err != nil || out.StatusCode != 200 || out.Body != string(releases.Data) || out.Headers["cache-control"] != "no-store" {
		t.Fatal("signed manifest bytes or cache policy changed")
	}
	public := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{23}, 32)).Public().(ed25519.PublicKey)
	if _, err = contract.VerifyReleaseManifest([]byte(out.Body), "https://suburban.jeremy.ninja/api/releases/latest", contract.ReleaseTrust{Origin: "https://suburban.jeremy.ninja", PublicKeys: map[string]ed25519.PublicKey{"primary": public}}, contract.InstalledRelease{VersionCode: 6, AndroidAPI: 29, SigningCertificateSHA256: strings.Repeat("b", 64)}, now); err != nil {
		t.Fatal("API damaged trusted manifest", err)
	}
	event.RawPath = "/api/releases/7/download"
	out, err = adapter.Handle(context.Background(), event)
	var ticket struct {
		DownloadURL string `json:"downloadUrl"`
		Expires     int    `json:"expiresInSeconds"`
	}
	if err != nil || out.StatusCode != 200 || json.Unmarshal([]byte(out.Body), &ticket) != nil || ticket.DownloadURL != releases.URL || ticket.Expires != 300 || out.Headers["cache-control"] != "no-store" {
		t.Fatal("owner download capability invalid")
	}
	if strings.Contains(out.Body, "synthetic-server-token") || strings.Contains(out.Body, access) {
		t.Fatal("session authority leaked in download reply")
	}
	delete(event.Headers, "authorization")
	out, _ = adapter.Handle(context.Background(), event)
	if out.StatusCode != 401 || releases.Downloads != 1 {
		t.Fatal("anonymous download reached presigner")
	}
}
func TestReleaseVersionHostAndPrivateErrorsFailClosed(t *testing.T) {
	now := time.Now().UTC()
	mobile, access := releaseSession(t, now)
	releases := &releaseStub{Data: releaseEnvelope(t, now, 7), URL: "https://attacker.invalid/other.apk"}
	adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: mobile, Provider: &state.Store{}, Releases: releases})
	if err != nil {
		t.Fatal(err)
	}
	event := request(t)
	event.Headers = map[string]string{"authorization": "Bearer " + access}
	event.RawQueryString = ""
	event.RequestContext.HTTP.Method = "GET"
	for _, path := range []string{"/api/releases/0/download", "/api/releases/07/download", "/api/releases/8/download", "/api/releases/7/download"} {
		event.RawPath = path
		out, e := adapter.Handle(context.Background(), event)
		if e != nil || out.StatusCode < 400 {
			t.Fatal("bad version or foreign host accepted")
		}
	}
	releases.Err = errors.New("synthetic-private-storage-detail")
	event.RawPath = "/api/releases/latest"
	out, _ := adapter.Handle(context.Background(), event)
	if out.StatusCode < 400 || strings.Contains(out.Body, "synthetic-private-storage-detail") {
		t.Fatal("storage error leaked")
	}
}

func request(t *testing.T) events.APIGatewayV2HTTPRequest {
	t.Helper()
	data, err := os.ReadFile("testdata/http-api-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var event events.APIGatewayV2HTTPRequest
	if err = json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestPublicVersionedIcons(t *testing.T) {
	adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		width, height int
	}{{"suburban-icon-v1-108.png", 108, 108}, {"suburban-icon-v1-512.png", 512, 512}, {"suburban-silver-v1-wide.png", 1536, 1024}} {
		event := request(t)
		event.RawPath = "/assets/" + test.name
		event.RawQueryString = ""
		event.Cookies = nil
		delete(event.Headers, "authorization")
		response, err := adapter.Handle(context.Background(), event)
		if err != nil || response.StatusCode != 200 || !response.IsBase64Encoded {
			t.Fatal("icon binary response unavailable")
		}
		body, err := base64.StdEncoding.DecodeString(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		expected, _ := iconAssets.ReadFile("static/" + test.name)
		if !bytes.Equal(body, expected) {
			t.Fatal("icon bytes changed in adapter")
		}
		config, err := png.DecodeConfig(bytes.NewReader(body))
		if err != nil || config.Width != test.width || config.Height != test.height {
			t.Fatal("icon dimensions invalid")
		}
		if response.Headers["content-type"] != "image/png" || response.Headers["cache-control"] != "public, max-age=31536000, immutable" || response.Headers["x-content-type-options"] != "nosniff" {
			t.Fatal("icon headers invalid")
		}
	}
}

func TestBaselineRoutes(t *testing.T) {
	build := Build{Version: strings.Repeat("a", 40), Built: "2026-10-22T12:00:00Z"}
	adapter, err := New(build)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, query string
		status            int
	}{
		{"health", "/healthz", "", 200}, {"privacy", "/privacy", "", 200},
		{"missing-state", "/auth/lwa/callback", "", 400},
		{"malformed-state", "/auth/lwa/callback", "state=%21", 400},
		{"duplicate-state", "/auth/lwa/callback", "state=a&state=b", 400},
		{"unrecognized-state", "/auth/lwa/callback", "state=" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := request(t)
			event.RawPath = tc.path
			event.RawQueryString = tc.query
			response, err := adapter.Handle(context.Background(), event)
			if err != nil || response.StatusCode != tc.status {
				t.Fatalf("status=%d err=%v", response.StatusCode, err)
			}
			if len(response.Body) > 1024 || response.Headers["cache-control"] != "no-store" {
				t.Fatal("response size or cache policy")
			}
			if tc.name == "health" {
				var got Build
				if json.Unmarshal([]byte(response.Body), &got) != nil || got != build {
					t.Fatalf("incorrect build metadata: %q", response.Body)
				}
			}
			if tc.path == "/auth/lwa/callback" && (len(response.Cookies) > 0 || response.Headers["location"] != "") {
				t.Fatal("callback must not issue session or redirect")
			}
		})
	}
	for _, bad := range []Build{{}, {Version: build.Version, Built: "not-time"}, {Version: "main", Built: build.Built}, {Version: build.Version, Built: "2026-10-22T12:00:00-04:00"}} {
		if _, err := New(bad); err == nil {
			t.Fatal("invalid build accepted")
		}
	}
}
