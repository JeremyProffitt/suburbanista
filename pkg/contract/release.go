// Package contract contains wire contracts shared by the Go server and Android app.
package contract

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	AndroidPackageID              = "ninja.jeremy.suburbanremote"
	MaxAndroidVersionCode   int64 = 2100000000
	MaxReleaseEnvelopeBytes       = 24 * 1024
	MaxReleasePayloadBytes        = 8 * 1024
)

// SignedRelease carries a base64 encoding of the exact signed JSON bytes. Never
// reserialize Payload before verification. Private signing keys are CI-only.
type SignedRelease struct {
	SchemaVersion int    `json:"schemaVersion"`
	KeyID         string `json:"keyId"`
	Payload       string `json:"payload"`
	Signature     string `json:"signature"`
}

type ReleaseManifest struct {
	SchemaVersion            int       `json:"schemaVersion"`
	PackageID                string    `json:"packageId"`
	VersionName              string    `json:"versionName"`
	VersionCode              int64     `json:"versionCode"`
	MinimumAndroidAPI        int       `json:"minimumAndroidApi"`
	ReleasedAt               time.Time `json:"releasedAt"`
	ExpiresAt                time.Time `json:"expiresAt"`
	ArtifactID               string    `json:"artifactId"`
	APKBytes                 int64     `json:"apkBytes"`
	APKSHA256                string    `json:"apkSha256"`
	SigningCertificateSHA256 string    `json:"signingCertificateSha256"`
	DownloadRoute            string    `json:"downloadRoute"`
}

// ReleaseTrust is pinned app configuration, never information from the fetched
// manifest. Origin is the exact HTTPS origin, with no path, query or fragment.
type ReleaseTrust struct {
	Origin     string
	PublicKeys map[string]ed25519.PublicKey
}
type InstalledRelease struct {
	VersionCode              int64
	AndroidAPI               int
	SigningCertificateSHA256 string
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ReleaseArtifactID(versionCode int64) string {
	return "suburban-remote-" + strconv.FormatInt(versionCode, 10) + ".apk"
}
func ReleaseDownloadRoute(versionCode int64) string {
	return "/api/releases/" + strconv.FormatInt(versionCode, 10) + "/download"
}

// VerifyReleaseManifest validates an update for the actual installed package.
// sourceURL must be the final response URL after any HTTP redirects. This function
// performs no fetch and provides no signing or installation implementation.
// Repeated checks of the same newer release are allowed until installation, so a
// cancelled prompt can be shown again; versions <= installed are rejected.
func VerifyReleaseManifest(data []byte, sourceURL string, trust ReleaseTrust, installed InstalledRelease, now time.Time) (ReleaseManifest, error) {
	var manifest ReleaseManifest
	if len(data) == 0 || len(data) > MaxReleaseEnvelopeBytes {
		return manifest, errors.New("invalid release envelope size")
	}
	origin, err := url.Parse(trust.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || origin.Opaque != "" {
		return manifest, errors.New("invalid trusted release origin")
	}
	source, err := url.Parse(sourceURL)
	if err != nil || source.Scheme != "https" || source.Host != origin.Host || source.User != nil || source.Fragment != "" || source.Opaque != "" || source.RawPath != "" || source.RawQuery != "" || source.ForceQuery || !strings.HasPrefix(source.Path, "/") {
		return manifest, errors.New("untrusted release source")
	}
	if installed.VersionCode < 0 || installed.VersionCode > MaxAndroidVersionCode || installed.AndroidAPI <= 0 || !digestPattern.MatchString(installed.SigningCertificateSHA256) || now.IsZero() {
		return manifest, errors.New("invalid installed release context")
	}
	var envelope SignedRelease
	if decodeObject(data, []string{"schemaVersion", "keyId", "payload", "signature"}, &envelope) != nil || envelope.SchemaVersion != 1 || !keyIDPattern.MatchString(envelope.KeyID) {
		return manifest, errors.New("invalid release envelope")
	}
	key, exists := trust.PublicKeys[envelope.KeyID]
	if !exists || len(key) != ed25519.PublicKeySize {
		return manifest, errors.New("unknown release verification key")
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(envelope.Payload)
	if err != nil || len(payload) == 0 || len(payload) > MaxReleasePayloadBytes || base64.StdEncoding.EncodeToString(payload) != envelope.Payload {
		return manifest, errors.New("invalid signed payload encoding")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != envelope.Signature || !ed25519.Verify(key, payload, signature) {
		return manifest, errors.New("invalid release signature")
	}
	fields := []string{"schemaVersion", "packageId", "versionName", "versionCode", "minimumAndroidApi", "releasedAt", "expiresAt", "artifactId", "apkBytes", "apkSha256", "signingCertificateSha256", "downloadRoute"}
	if decodeObject(payload, fields, &manifest) != nil || manifest.SchemaVersion != 1 {
		return ReleaseManifest{}, errors.New("invalid release payload schema")
	}
	if manifest.PackageID != AndroidPackageID {
		return ReleaseManifest{}, errors.New("release package mismatch")
	}
	if manifest.VersionCode <= installed.VersionCode || manifest.VersionCode > MaxAndroidVersionCode || manifest.VersionName != "1.0."+strconv.FormatInt(manifest.VersionCode, 10) {
		return ReleaseManifest{}, errors.New("release version is invalid, replayed or downgraded")
	}
	if manifest.MinimumAndroidAPI <= 0 || manifest.MinimumAndroidAPI > installed.AndroidAPI {
		return ReleaseManifest{}, errors.New("release is incompatible with Android API")
	}
	if manifest.ReleasedAt.IsZero() || manifest.ExpiresAt.IsZero() || manifest.ReleasedAt.After(now) || !manifest.ExpiresAt.After(now) || !manifest.ExpiresAt.After(manifest.ReleasedAt) {
		return ReleaseManifest{}, errors.New("release is future-dated or expired")
	}
	if manifest.APKBytes <= 0 || !digestPattern.MatchString(manifest.APKSHA256) || manifest.SigningCertificateSHA256 != installed.SigningCertificateSHA256 {
		return ReleaseManifest{}, errors.New("release artifact integrity or signer mismatch")
	}
	if manifest.ArtifactID != ReleaseArtifactID(manifest.VersionCode) || manifest.DownloadRoute != ReleaseDownloadRoute(manifest.VersionCode) {
		return ReleaseManifest{}, errors.New("release artifact or stable download route mismatch")
	}
	return manifest, nil
}

// decodeObject rejects duplicate, missing, unknown and case-aliased names before
// typed decoding. encoding/json alone accepts duplicate and case-aliased keys.
func decodeObject(data []byte, fields []string, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("object required")
	}
	allowed := map[string]bool{}
	for _, field := range fields {
		allowed[field] = true
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok || !allowed[name] || seen[name] {
			return errors.New("invalid field")
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("invalid field value")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || len(seen) != len(allowed) {
		return errors.New("incomplete or trailing object")
	}
	return json.Unmarshal(data, dst)
}
