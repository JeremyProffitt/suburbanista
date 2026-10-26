package ownerclient

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

const InstalledSigningCertificate = "70cd737ed7411587a2afee7b31372ceb605ea33aa71f5261313307f8757b8853"

// VerifyUpdate shares the same trust checks between the app and background job.
func VerifyUpdate(data []byte, publicKey string, installedCode int64, androidAPI int, now time.Time) (contract.ReleaseManifest, bool, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(publicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || installedCode <= 0 {
		return contract.ReleaseManifest{}, false, errors.New("Update verification is unavailable in this build.")
	}
	manifest, err := contract.VerifyReleaseManifest(data, ServiceURL+"/api/releases/latest", contract.ReleaseTrust{Origin: ServiceURL, PublicKeys: map[string]ed25519.PublicKey{"primary": key}}, contract.InstalledRelease{VersionCode: 0, AndroidAPI: androidAPI, SigningCertificateSHA256: InstalledSigningCertificate}, now)
	if err != nil {
		return manifest, false, errors.New("The update manifest did not pass verification.")
	}
	return manifest, manifest.VersionCode > installedCode, nil
}
