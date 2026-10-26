package contract

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The deterministic seed is public synthetic test data, never a release key.
func releaseFixture() (ReleaseManifest, ReleaseTrust, InstalledRelease, time.Time, ed25519.PrivateKey) {
	seed := sha256.Sum256([]byte("synthetic-release-test-key-not-for-production"))
	key := ed25519.NewKeyFromSeed(seed[:])
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	digest := strings.Repeat("a", 64)
	manifest := ReleaseManifest{SchemaVersion: 1, PackageID: AndroidPackageID, VersionName: "1.0.11", VersionCode: 11, MinimumAndroidAPI: 29, ReleasedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour), ArtifactID: ReleaseArtifactID(11), APKBytes: 123456, APKSHA256: strings.Repeat("b", 64), SigningCertificateSHA256: digest, DownloadRoute: ReleaseDownloadRoute(11)}
	return manifest, ReleaseTrust{Origin: "https://suburban.jeremy.ninja", PublicKeys: map[string]ed25519.PublicKey{"synthetic-key": key.Public().(ed25519.PublicKey)}}, InstalledRelease{VersionCode: 10, AndroidAPI: 29, SigningCertificateSHA256: digest}, now, key
}
func signPayload(payload []byte, key ed25519.PrivateKey) []byte {
	data, _ := json.Marshal(SignedRelease{SchemaVersion: 1, KeyID: "synthetic-key", Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))})
	return data
}
func signFixture(m ReleaseManifest, key ed25519.PrivateKey) []byte {
	payload, _ := json.Marshal(m)
	return signPayload(payload, key)
}

func TestValidReleaseManifest(t *testing.T) {
	m, trust, installed, now, key := releaseFixture()
	got, err := VerifyReleaseManifest(signFixture(m, key), trust.Origin+"/api/releases/latest", trust, installed, now)
	if err != nil || got != m {
		t.Fatal("valid synthetic manifest rejected", err)
	}
}
func TestReleaseAcceptanceCriteria(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ReleaseManifest)
	}{
		{"wrong-package", func(m *ReleaseManifest) { m.PackageID = "another.package" }},
		{"same-version", func(m *ReleaseManifest) { m.VersionCode = 10; m.VersionName = "1.0.10" }},
		{"downgrade", func(m *ReleaseManifest) { m.VersionCode = 9; m.VersionName = "1.0.9" }},
		{"version-overflow", func(m *ReleaseManifest) { m.VersionCode = MaxAndroidVersionCode + 1 }},
		{"version-name", func(m *ReleaseManifest) { m.VersionName = "2.0.11" }},
		{"future-release", func(m *ReleaseManifest) { m.ReleasedAt = m.ExpiresAt }},
		{"expired", func(m *ReleaseManifest) { m.ExpiresAt = m.ReleasedAt }},
		{"wrong-signer", func(m *ReleaseManifest) { m.SigningCertificateSHA256 = strings.Repeat("c", 64) }},
		{"incompatible-api", func(m *ReleaseManifest) { m.MinimumAndroidAPI = 30 }},
		{"empty-apk", func(m *ReleaseManifest) { m.APKBytes = 0 }},
		{"bad-checksum", func(m *ReleaseManifest) { m.APKSHA256 = "invalid" }},
		{"mutable-artifact", func(m *ReleaseManifest) { m.ArtifactID = "latest.apk" }},
		{"presigned-url", func(m *ReleaseManifest) {
			m.DownloadRoute = "https://bucket.s3.amazonaws.com/app.apk?X-Amz-Signature=synthetic"
		}},
		{"download-ticket", func(m *ReleaseManifest) { m.DownloadRoute += "?ticket=synthetic" }},
		{"schema", func(m *ReleaseManifest) { m.SchemaVersion = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, trust, installed, now, key := releaseFixture()
			tc.mutate(&m)
			if _, err := VerifyReleaseManifest(signFixture(m, key), trust.Origin+"/manifest.json", trust, installed, now); err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
}

func TestSignedBytesAndStrictJSON(t *testing.T) {
	m, trust, installed, now, key := releaseFixture()
	payload, _ := json.Marshal(m)
	for _, bad := range [][]byte{append(append([]byte(nil), payload...), []byte(` {}`)...), []byte(strings.Replace(string(payload), `"versionCode":11`, `"versionCode":11,"versionCode":12`, 1)), []byte(strings.Replace(string(payload), `"versionCode":11`, `"VersionCode":11`, 1)), []byte(strings.Replace(string(payload), `"versionCode":11`, `"versionCode":null`, 1)), []byte(strings.Replace(string(payload), `"versionCode":11`, `"versionCode":11,"unexpected":true`, 1))} {
		if _, err := VerifyReleaseManifest(signPayload(bad, key), trust.Origin+"/manifest.json", trust, installed, now); err == nil {
			t.Fatal("ambiguous signed JSON accepted")
		}
	}
	var envelope SignedRelease
	_ = json.Unmarshal(signPayload(payload, key), &envelope)
	envelope.Payload = base64.StdEncoding.EncodeToString(append(payload, ' '))
	changed, _ := json.Marshal(envelope)
	if _, err := VerifyReleaseManifest(changed, trust.Origin+"/manifest.json", trust, installed, now); err == nil {
		t.Fatal("modified signed bytes accepted")
	}
	envelope.KeyID = "unknown-key"
	changed, _ = json.Marshal(envelope)
	if _, err := VerifyReleaseManifest(changed, trust.Origin+"/manifest.json", trust, installed, now); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestReleaseOriginAndTrustChecks(t *testing.T) {
	m, trust, installed, now, key := releaseFixture()
	data := signFixture(m, key)
	for _, source := range []string{"http://suburban.jeremy.ninja/manifest", "https://evil.invalid/manifest", "https://suburban.jeremy.ninja@evil.invalid/manifest", "https://suburban.jeremy.ninja:443/manifest", "https://suburban.jeremy.ninja/manifest?redirect=bad", "https://suburban.jeremy.ninja/manifest#fragment"} {
		if _, err := VerifyReleaseManifest(data, source, trust, installed, now); err == nil {
			t.Fatal("untrusted source accepted")
		}
	}
	trust.PublicKeys["synthetic-key"] = []byte("invalid-length")
	if _, err := VerifyReleaseManifest(data, trust.Origin+"/manifest", trust, installed, now); err == nil {
		t.Fatal("invalid public key accepted")
	}
}

func FuzzReleaseManifest(f *testing.F) {
	m, trust, installed, now, key := releaseFixture()
	valid := signFixture(m, key)
	f.Add(valid)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schemaVersion":1,"schemaVersion":2}`))
	f.Add([]byte(`null`))
	for _, mutate := range []func(*ReleaseManifest){
		func(m *ReleaseManifest) { m.VersionCode = installed.VersionCode },
		func(m *ReleaseManifest) { m.PackageID = "wrong.package" },
		func(m *ReleaseManifest) { m.ExpiresAt = now },
		func(m *ReleaseManifest) { m.ReleasedAt = now.Add(time.Minute) },
		func(m *ReleaseManifest) { m.SigningCertificateSHA256 = strings.Repeat("c", 64) },
	} {
		bad := m
		mutate(&bad)
		f.Add(signFixture(bad, key))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := VerifyReleaseManifest(data, trust.Origin+"/manifest", trust, installed, now)
		if err == nil {
			if got.PackageID != AndroidPackageID || got.VersionCode <= installed.VersionCode || got.VersionCode > MaxAndroidVersionCode || got.SigningCertificateSHA256 != installed.SigningCertificateSHA256 || got.ReleasedAt.After(now) || !got.ExpiresAt.After(now) {
				t.Fatal("accepted manifest violates invariant")
			}
		}
	})
}
