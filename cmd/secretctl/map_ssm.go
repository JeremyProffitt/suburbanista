package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const mapParameterName = "suburban-map-key"
const mapParameterRegion = "us-east-1"
const mapParameterAccount = "759775734231"

type mapParameterMetadata struct {
	Name, Type, Tier string
	Version          int64
}
type mapParameterStore interface {
	Metadata(context.Context) (mapParameterMetadata, bool, error)
	Read(context.Context) ([]byte, error)
	Create(context.Context, []byte) error
}

// This narrow client uses the existing AWS credential provider and SigV4
// signer. The raw key exists only in memory and the encrypted SSM API body.
type signedMapParameters struct {
	credentials aws.CredentialsProvider
	client      *http.Client
}

func (s signedMapParameters) call(ctx context.Context, operation string, input, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	payload, err := json.Marshal(input)
	if err != nil {
		return errors.New("SSM request unavailable")
	}
	defer clear(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://ssm.us-east-1.amazonaws.com/", bytes.NewReader(payload))
	if err != nil {
		return errors.New("SSM request unavailable")
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonSSM."+operation)
	credentials, err := s.credentials.Retrieve(ctx)
	if err != nil {
		return errors.New("SSM credentials unavailable")
	}
	digest := sha256.Sum256(payload)
	if v4.NewSigner().SignHTTP(ctx, credentials, req, hex.EncodeToString(digest[:]), "ssm", mapParameterRegion, time.Now().UTC()) != nil {
		return errors.New("SSM signing failed")
	}
	response, err := s.client.Do(req)
	if err != nil {
		return errors.New("SSM request failed; no request or response was logged")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("SSM %s failed with HTTP %d; existing values were not overwritten", operation, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	defer clear(raw)
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, output) != nil {
		return errors.New("SSM response invalid; no response was logged")
	}
	return nil
}

func (s signedMapParameters) Metadata(ctx context.Context) (mapParameterMetadata, bool, error) {
	var out struct {
		Parameters []mapParameterMetadata
		NextToken  string
	}
	err := s.call(ctx, "DescribeParameters", map[string]any{"ParameterFilters": []map[string]any{{"Key": "Name", "Option": "Equals", "Values": []string{mapParameterName}}}, "MaxResults": 50}, &out)
	if err != nil {
		return mapParameterMetadata{}, false, err
	}
	if out.NextToken != "" || len(out.Parameters) > 1 {
		return mapParameterMetadata{}, false, errors.New("SSM exact-name inventory incomplete; no value changed")
	}
	if len(out.Parameters) == 0 {
		return mapParameterMetadata{}, false, nil
	}
	if out.Parameters[0].Name != mapParameterName {
		return mapParameterMetadata{}, false, errors.New("SSM parameter scope differs")
	}
	return out.Parameters[0], true, nil
}
func (s signedMapParameters) Read(ctx context.Context) ([]byte, error) {
	var out struct {
		Parameter struct {
			Name, Type, Value string
			Version           int64
		}
	}
	if err := s.call(ctx, "GetParameter", map[string]any{"Name": mapParameterName, "WithDecryption": true}, &out); err != nil {
		return nil, err
	}
	if out.Parameter.Name != mapParameterName || out.Parameter.Type != "SecureString" {
		return nil, errors.New("SSM map parameter type or name differs")
	}
	return []byte(out.Parameter.Value), nil
}
func (s signedMapParameters) Create(ctx context.Context, key []byte) error {
	var out struct {
		Version int64
		Tier    string
	}
	if err := s.call(ctx, "PutParameter", map[string]any{"Name": mapParameterName, "Type": "SecureString", "Tier": "Standard", "KeyId": "alias/aws/ssm", "Overwrite": false, "Value": string(key), "Description": "Suburban cached PNG read credential; owner authorized 2026-10-24"}, &out); err != nil {
		return err
	}
	if out.Version != 1 || out.Tier != "Standard" {
		return errors.New("SSM create metadata unexpected; no overwrite attempted")
	}
	return nil
}

func runSSMMapKey(output io.Writer, verify bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(mapParameterRegion), config.WithHTTPClient(client), config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }))
	if err != nil {
		return errors.New("AWS configuration unavailable")
	}
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil || aws.ToString(identity.Account) != mapParameterAccount {
		return errors.New("AWS account verification failed; no map value changed")
	}
	store := signedMapParameters{credentials: cfg.Credentials, client: client}
	if verify {
		return verifySSMMapAccess(ctx, store, client, output)
	}
	return setupSSMMapKey(ctx, store, runGH, output)
}

func setupSSMMapKey(ctx context.Context, store mapParameterStore, run runCommand, output io.Writer) error {
	metadata, exists, err := store.Metadata(ctx)
	if err != nil {
		return err
	}
	if exists && (metadata.Type != "SecureString" || metadata.Tier != "Standard") {
		return errors.New("existing suburban-map-key must be Standard SecureString; preserved without change")
	}
	if _, err = inventory(run, "secret"); err != nil {
		return err
	}
	created := false
	var key []byte
	if exists {
		key, err = store.Read(ctx)
		if err != nil {
			return err
		}
	} else {
		var random [32]byte
		if _, err = rand.Read(random[:]); err != nil {
			return errors.New("map key generation failed")
		}
		key = []byte(base64.RawURLEncoding.EncodeToString(random[:]))
		clear(random[:])
		if err = store.Create(ctx, key); err != nil {
			clear(key)
			return err
		}
		created = true
		readback, e := store.Read(ctx)
		if e != nil {
			clear(key)
			return e
		}
		same := subtle.ConstantTimeCompare(key, readback) == 1
		clear(readback)
		if !same {
			clear(key)
			return errors.New("SSM map key readback differs; GitHub digest not changed")
		}
	}
	defer clear(key)
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`).Match(key) {
		return errors.New("stored map key format invalid; existing parameter preserved")
	}
	metadata, exists, err = store.Metadata(ctx)
	if err != nil {
		return err
	}
	if !exists || metadata.Type != "SecureString" || metadata.Tier != "Standard" || metadata.Version <= 0 {
		return errors.New("SSM map metadata verification failed")
	}
	digest := sha256.Sum256(key)
	encoded := []byte(hex.EncodeToString(digest[:]))
	defer clear(encoded)
	if _, err = run([]string{"secret", "set", "MAP_READ_KEY_SHA256", "--repo", repository}, encoded); err != nil {
		return errors.New("map digest write failed; SSM key retained for a safe retry")
	}
	verified, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	if verified["MAP_READ_KEY_SHA256"].UpdatedAt == "" {
		return errors.New("GitHub map digest metadata verification failed")
	}
	return json.NewEncoder(output).Encode(struct {
		Parameter, Account, Region, Type, Tier string
		Version                                int64
		Created, DigestConfigured              bool
	}{mapParameterName, mapParameterAccount, mapParameterRegion, metadata.Type, metadata.Tier, metadata.Version, created, true})
}

func verifySSMMapAccess(ctx context.Context, store mapParameterStore, client *http.Client, output io.Writer) error {
	return verifySSMMapAccessWithWait(ctx, store, client, output, func(ctx context.Context) bool {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	})
}

func verifySSMMapAccessWithWait(ctx context.Context, store mapParameterStore, client *http.Client, output io.Writer, wait func(context.Context) bool) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	// Redirect handling is explicit even when a caller supplied a permissive
	// client. A fresh S3 request must not inherit cookies or API credentials.
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	boundedClient.Jar = nil
	metadata, exists, err := store.Metadata(ctx)
	if err != nil {
		return err
	}
	if !exists || metadata.Type != "SecureString" || metadata.Tier != "Standard" {
		return errors.New("Standard SecureString map parameter required")
	}
	key, err := store.Read(ctx)
	if err != nil {
		return err
	}
	defer clear(key)
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`).Match(key) {
		return errors.New("stored map key format invalid")
	}
	bad := bytes.Clone(key)
	defer clear(bad)
	if bad[0] == 'A' {
		bad[0] = 'B'
	} else {
		bad[0] = 'A'
	}
	statuses := map[string]int{}
	for _, check := range []struct {
		name, path string
		key        []byte
	}{{"valid-key", "/api/map.png", key}, {"missing-key", "/api/map.png", nil}, {"bad-key", "/api/map.png", bad}, {"key-cannot-read-command-status", "/api/commands/status", key}} {
		u := url.URL{Scheme: "https", Host: "suburban.jeremy.ninja", Path: check.path}
		if check.key != nil {
			u.RawQuery = url.Values{"key": []string{string(check.key)}}.Encode()
		}
		for attempt := 0; ; attempt++ {
			req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if e != nil {
				return errors.New("map verification request invalid")
			}
			response, e := boundedClient.Do(req)
			if e != nil {
				return errors.New("map verification request failed; URL not logged")
			}
			if check.name == "valid-key" && (response.StatusCode == http.StatusFound || response.StatusCode == http.StatusTemporaryRedirect) {
				address := response.Header.Get("Location")
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				response.Body.Close()
				if !contract.ValidMapDownloadURL(address, "", time.Now()) {
					return errors.New("map verification download location invalid")
				}
				// This new request has no Authorization, Cookie or map-key header.
				download, e := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
				if e != nil {
					return errors.New("map verification download request invalid")
				}
				response, e = boundedClient.Do(download)
				if e != nil {
					return errors.New("map verification download failed; URL not logged")
				}
				if response.StatusCode != http.StatusOK {
					response.Body.Close()
					return errors.New("map verification download did not return an image")
				}
			}
			statuses[check.name] = response.StatusCode
			// Never retain image, key-bearing URL or body. Only the authorized
			// map GET may trigger a cached-image miss; no vehicle wake is sent.
			if check.name == "valid-key" && response.StatusCode == http.StatusOK {
				if response.ContentLength <= 0 || response.ContentLength > contract.MaxMapImageBytes {
					response.Body.Close()
					return errors.New("map verification image length invalid")
				}
				data, readErr := io.ReadAll(io.LimitReader(response.Body, contract.MaxMapImageBytes+1))
				response.Body.Close()
				config, decodeErr := png.DecodeConfig(bytes.NewReader(data))
				valid := readErr == nil && len(data) <= contract.MaxMapImageBytes && int64(len(data)) == response.ContentLength && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) && decodeErr == nil && config.Width > 0 && config.Height > 0 && config.Width <= contract.MaxMapDimension && config.Height <= contract.MaxMapDimension
				clear(data)
				if !valid {
					return errors.New("map verification PNG invalid")
				}
				// Measurements come from the checked response bytes, not render
				// configuration. No additional request or image retention is needed.
				statuses["image-width"] = config.Width
				statuses["image-height"] = config.Height
				statuses["image-bytes"] = len(data)
			} else {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 3*1024*1024+1))
				response.Body.Close()
			}
			if check.name != "valid-key" || response.StatusCode != http.StatusAccepted {
				break
			}
			if attempt >= 8 || !wait(ctx) {
				return errors.New("map generation remained pending within the bounded verification window")
			}
		}
	}
	if err = json.NewEncoder(output).Encode(statuses); err != nil {
		return err
	}
	if (statuses["valid-key"] != 200 && statuses["valid-key"] != 503) || statuses["missing-key"] != 401 || statuses["bad-key"] != 401 || statuses["key-cannot-read-command-status"] != 401 {
		return errors.New("map access verification did not meet expected status contract")
	}
	return nil
}
