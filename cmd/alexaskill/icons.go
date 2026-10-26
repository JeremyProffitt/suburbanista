package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"reflect"
	"time"
)

const suburbanSkill = "amzn1.ask.skill.3464130d-73f4-4fa7-bd96-b527f3f052ad"
const smallIconURI = "https://suburban.jeremy.ninja/assets/suburbanista-icon-v2-108.png"
const largeIconURI = "https://suburban.jeremy.ninja/assets/suburbanista-icon-v2-512.png"

type manifestWriter func([]byte) (string, func(), error)

func verifyPublishedIcons(ctx context.Context, client *http.Client) error {
	for _, icon := range []struct {
		uri  string
		size int
	}{{smallIconURI, 108}, {largeIconURI, 512}} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, icon.uri, nil)
		if err != nil {
			return errors.New("invalid icon URL")
		}
		response, err := client.Do(request)
		if err != nil {
			return errors.New("published icon unavailable")
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
		response.Body.Close()
		contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if readErr != nil || response.StatusCode != http.StatusOK || contentType != "image/png" || len(data) > 2*1024*1024 {
			return errors.New("published icon is not a valid PNG response")
		}
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width != icon.size || config.Height != icon.size {
			return errors.New("published icon dimensions invalid")
		}
	}
	return nil
}

func iconManifest(data []byte) ([]byte, error) {
	if checkManifest(data, false) != nil {
		return nil, errors.New("icon update manifest identity invalid")
	}
	var root, manifest, publishing, locales, locale map[string]json.RawMessage
	decode := func(raw []byte, out *map[string]json.RawMessage) bool {
		return json.Unmarshal(raw, out) == nil && *out != nil
	}
	if !decode(data, &root) || !decode(root["manifest"], &manifest) || !decode(manifest["publishingInformation"], &publishing) || !decode(publishing["locales"], &locales) || !decode(locales["en-US"], &locale) {
		return nil, errors.New("icon update locale unavailable")
	}
	locale["smallIconUri"], _ = json.Marshal(smallIconURI)
	locale["largeIconUri"], _ = json.Marshal(largeIconURI)
	locale["name"], _ = json.Marshal(skillName)
	locales["en-US"], _ = json.Marshal(locale)
	publishing["locales"], _ = json.Marshal(locales)
	manifest["publishingInformation"], _ = json.Marshal(publishing)
	root["manifest"], _ = json.Marshal(manifest)
	return json.Marshal(root)
}
func sameJSON(a, b []byte) bool {
	var first, second any
	d := json.NewDecoder(bytes.NewReader(a))
	d.UseNumber()
	if d.Decode(&first) != nil {
		return false
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&second) != nil {
		return false
	}
	return reflect.DeepEqual(first, second)
}
func verifyIcons(before, after []byte) error {
	wanted, err := iconManifest(before)
	if err != nil {
		return err
	}
	if !sameJSON(wanted, after) {
		return errors.New("branding readback differs from name-and-icons manifest update")
	}
	return nil
}
func writeIconManifest(data []byte) (string, func(), error) {
	file, err := os.CreateTemp("", "suburban-alexa-icons-*.json")
	if err != nil {
		return "", nil, errors.New("cannot prepare icon manifest")
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err = file.Write(data); err != nil {
		file.Close()
		cleanup()
		return "", nil, errors.New("cannot prepare icon manifest")
	}
	if err = file.Close(); err != nil {
		cleanup()
		return "", nil, errors.New("cannot prepare icon manifest")
	}
	return file.Name(), cleanup, nil
}
func iconWait(ctx context.Context) error {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// updateIcons never creates a skill. It preserves the entire existing manifest
// and changes only the en-US display name and two publishing icon URLs in development stage.
func updateIcons(ctx context.Context, identity result, token string, run executor, write manifestWriter, wait func(context.Context) error) (result, error) {
	if identity.SkillID != suburbanSkill || identity.Created || !identity.VendorMatchedReference || !identity.ManifestVerified || write == nil {
		return identity, errors.New("icon update requires verified existing Suburban skill")
	}
	before, err := invoke(ctx, run, token, "get-skill-manifest", "--skill-id", identity.SkillID, "--stage", "development")
	if err != nil {
		return identity, err
	}
	merged, err := iconManifest(before.Body)
	if err != nil {
		return identity, err
	}
	if !sameJSON(merged, before.Body) {
		path, cleanup, err := write(merged)
		if err != nil {
			return identity, errors.New("cannot prepare icon manifest")
		}
		if cleanup == nil {
			return identity, errors.New("icon manifest cleanup unavailable")
		}
		defer cleanup()
		if _, err = invoke(ctx, run, token, "update-skill-manifest", "--skill-id", identity.SkillID, "--stage", "development", "--manifest", "file:"+path); err != nil {
			return identity, errors.New("icon update outcome unverified; no retry")
		}
		if wait == nil {
			wait = iconWait
		}
		succeeded := false
		for attempt := 0; attempt < 24; attempt++ {
			status, err := invoke(ctx, run, token, "get-skill-status", "--skill-id", identity.SkillID)
			if err != nil {
				return identity, err
			}
			var body struct {
				Manifest struct {
					LastUpdateRequest struct {
						Status string `json:"status"`
					} `json:"lastUpdateRequest"`
				} `json:"manifest"`
			}
			if json.Unmarshal(status.Body, &body) != nil {
				return identity, errors.New("invalid icon update status")
			}
			switch body.Manifest.LastUpdateRequest.Status {
			case "SUCCEEDED":
				succeeded = true
			case "FAILED":
				return identity, errors.New("icon manifest update failed")
			case "IN_PROGRESS":
			default:
				return identity, errors.New("icon manifest update status unrecognized")
			}
			if succeeded {
				break
			}
			if attempt == 23 {
				return identity, errors.New("icon manifest update timed out")
			}
			if err = wait(ctx); err != nil {
				return identity, errors.New("icon update wait canceled")
			}
		}
	}
	after, err := invoke(ctx, run, token, "get-skill-manifest", "--skill-id", identity.SkillID, "--stage", "development")
	if err != nil {
		return identity, err
	}
	if err = verifyIcons(before.Body, after.Body); err != nil {
		return identity, err
	}
	identity.IconsVerified = true
	identity.SmallIconURI = smallIconURI
	identity.LargeIconURI = largeIconURI
	return identity, nil
}
