package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/gofiber/fiber/v3"
)

const releasePrefix = "suburban-remote/android/"

type ReleaseStore interface {
	Manifest(context.Context, int64) ([]byte, error)
	Download(context.Context, int64) (string, error)
}

// S3Releases uses the deployment bucket's private release prefix. No bucket is
// made public. The API role can read only this prefix and cannot publish.
type S3Releases struct {
	Bucket      string
	Credentials aws.CredentialsProvider
	HTTP        *http.Client
}

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func (s S3Releases) request(ctx context.Context, key string) (*http.Request, error) {
	if !bucketName.MatchString(s.Bucket) || s.Credentials == nil {
		return nil, errors.New("releases unavailable")
	}
	return http.NewRequestWithContext(ctx, http.MethodGet, "https://s3.us-east-1.amazonaws.com/"+s.Bucket+"/"+releasePrefix+key, nil)
}

func (s S3Releases) Manifest(ctx context.Context, version int64) ([]byte, error) {
	name := "latest.json"
	if version > 0 {
		name = strconv.FormatInt(version, 10) + "/release-manifest.json"
	}
	if version < 0 || version > contract.MaxAndroidVersionCode {
		return nil, errors.New("invalid release")
	}
	req, err := s.request(ctx, name)
	if err != nil {
		return nil, err
	}
	credentials, err := s.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, errors.New("releases unavailable")
	}
	req.Header.Set("X-Amz-Content-Sha256", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err = v4.NewSigner().SignHTTP(ctx, credentials, req, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "s3", "us-east-1", time.Now().UTC()); err != nil {
		return nil, errors.New("releases unavailable")
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("releases unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("releases unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, contract.MaxReleaseEnvelopeBytes+1))
	if err != nil || len(data) == 0 || len(data) > contract.MaxReleaseEnvelopeBytes {
		return nil, errors.New("invalid release manifest")
	}
	return data, nil
}

func (s S3Releases) Download(ctx context.Context, version int64) (string, error) {
	if version <= 0 || version > contract.MaxAndroidVersionCode {
		return "", errors.New("invalid release")
	}
	req, err := s.request(ctx, strconv.FormatInt(version, 10)+"/"+contract.ReleaseArtifactID(version))
	if err != nil {
		return "", err
	}
	query := req.URL.Query()
	query.Set("X-Amz-Expires", "300")
	req.URL.RawQuery = query.Encode()
	credentials, err := s.Credentials.Retrieve(ctx)
	if err != nil {
		return "", errors.New("releases unavailable")
	}
	address, _, err := v4.NewSigner().PresignHTTP(ctx, credentials, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now().UTC())
	if err != nil {
		return "", errors.New("releases unavailable")
	}
	return address, nil
}

func releasePayload(data []byte) (contract.ReleaseManifest, error) {
	var envelope contract.SignedRelease
	var payload contract.ReleaseManifest
	if json.Unmarshal(data, &envelope) != nil || envelope.SchemaVersion != 1 || envelope.KeyID != "primary" {
		return payload, auth.ErrUnavailable
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(envelope.Payload)
	if err != nil || len(raw) > contract.MaxReleasePayloadBytes || json.Unmarshal(raw, &payload) != nil || payload.PackageID != contract.AndroidPackageID || payload.VersionCode <= 0 || payload.VersionCode > contract.MaxAndroidVersionCode || payload.ArtifactID != contract.ReleaseArtifactID(payload.VersionCode) || payload.DownloadRoute != contract.ReleaseDownloadRoute(payload.VersionCode) || !payload.ExpiresAt.After(time.Now()) {
		return payload, auth.ErrUnavailable
	}
	return payload, nil
}

func registerReleases(app *fiber.App, services Services) {
	authorize := func(c fiber.Ctx) error { _, err := services.Mobile.Authorize(c.Context(), bearer(c)); return err }
	app.Get("/api/releases/latest", func(c fiber.Ctx) error {
		if err := authorize(c); err != nil {
			return sessionError(c, err)
		}
		if services.Releases == nil {
			return publicError(c, auth.ErrUnavailable)
		}
		data, err := services.Releases.Manifest(c.Context(), 0)
		if err != nil {
			return publicError(c, auth.ErrUnavailable)
		}
		if _, err = releasePayload(data); err != nil {
			return publicError(c, err)
		}
		c.Set("Content-Type", "application/json")
		return c.Send(data)
	})
	app.Get("/api/releases/:version/download", func(c fiber.Ctx) error {
		if err := authorize(c); err != nil {
			return sessionError(c, err)
		}
		version, err := strconv.ParseInt(c.Params("version"), 10, 64)
		if err != nil || version <= 0 || version > contract.MaxAndroidVersionCode || strconv.FormatInt(version, 10) != c.Params("version") {
			return publicError(c, auth.ErrInvalid)
		}
		if services.Releases == nil {
			return publicError(c, auth.ErrUnavailable)
		}
		data, err := services.Releases.Manifest(c.Context(), version)
		if err != nil {
			return publicError(c, auth.ErrUnavailable)
		}
		manifest, err := releasePayload(data)
		if err != nil || manifest.VersionCode != version {
			return publicError(c, auth.ErrUnavailable)
		}
		address, err := services.Releases.Download(c.Context(), version)
		if err != nil {
			return publicError(c, auth.ErrUnavailable)
		}
		parsed, err := url.Parse(address)
		if err != nil || parsed.Scheme != "https" || parsed.Host != "s3.us-east-1.amazonaws.com" || parsed.User != nil || parsed.Fragment != "" {
			return publicError(c, auth.ErrUnavailable)
		}
		return c.JSON(fiber.Map{"downloadUrl": address, "expiresInSeconds": 300, "apkSha256": manifest.APKSHA256, "apkBytes": manifest.APKBytes})
	})
}
