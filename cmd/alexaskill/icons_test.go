package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

type iconTransport func(*http.Request) (*http.Response, error)

func (f iconTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPublishedIconsRequireCorrectPNGDimensions(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		calls := 0
		client := &http.Client{Transport: iconTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			size := 108
			if r.URL.String() == largeIconURI {
				size = 512
			} else if r.URL.String() != smallIconURI {
				t.Fatal("unexpected public URL")
			}
			if wrong {
				size = 1
			}
			var b bytes.Buffer
			_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, size, size)))
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(bytes.NewReader(b.Bytes()))}, nil
		})}
		err := verifyPublishedIcons(context.Background(), client)
		if wrong && err == nil || !wrong && (err != nil || calls != 2) {
			t.Fatal("published image validation wrong")
		}
	}
}

var richManifest = []byte(`{"manifest":{"manifestVersion":"1.0","publishingInformation":{"locales":{"en-US":{"name":"Suburban Remote","description":"preserve description","smallIconUri":"https://example.invalid/old.png"},"en-GB":{"name":"Other locale","largeIconUri":"https://example.invalid/gb.png"}},"category":"CONNECTED_CAR"},"apis":{"custom":{"endpoint":{"uri":"arn:aws:lambda:us-east-1:123456789012:function:synthetic"},"interfaces":[]}},"permissions":[{"name":"synthetic-permission"}],"privacyAndCompliance":{"usesPersonalInfo":true},"unknownFutureField":{"largeNumber":9007199254740993}}}`)

func iconIdentity() result {
	return result{SkillID: suburbanSkill, VendorMatchedReference: true, ManifestVerified: true}
}
func TestIconsOnlyMergePreservesAllOtherFields(t *testing.T) {
	merged, e := iconManifest(richManifest)
	if e != nil {
		t.Fatal(e)
	}
	if verifyIcons(richManifest, merged) != nil {
		t.Fatal("icons-only merge rejected")
	}
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(merged)))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		t.Fatal("invalid merged JSON")
	}
	m := root["manifest"].(map[string]any)
	locales := m["publishingInformation"].(map[string]any)["locales"].(map[string]any)
	us := locales["en-US"].(map[string]any)
	if us["name"] != "Suburbanista" || us["smallIconUri"] != smallIconURI || us["largeIconUri"] != largeIconURI || us["description"] != "preserve description" {
		t.Fatal("wrong locale changes")
	}
	if !strings.Contains(string(merged), "9007199254740993") || !strings.Contains(string(merged), "https://example.invalid/gb.png") {
		t.Fatal("unrelated manifest field altered")
	}
	changed := []byte(strings.Replace(string(merged), "preserve description", "unexpected edit", 1))
	if verifyIcons(richManifest, changed) == nil {
		t.Fatal("unrelated readback mutation accepted")
	}
}
func TestIconUpdateOneSubmissionAndReadback(t *testing.T) {
	merged, _ := iconManifest(richManifest)
	script := &scripted{t: t, responses: []execution{envelope(200, string(richManifest)), envelope(202, `{}`), envelope(200, `{"manifest":{"lastUpdateRequest":{"status":"IN_PROGRESS"}}}`), envelope(200, `{"manifest":{"lastUpdateRequest":{"status":"SUCCEEDED"}}}`), envelope(200, string(merged))}}
	cleaned := false
	writer := func(data []byte) (string, func(), error) {
		if !sameJSON(data, merged) {
			t.Fatal("wrong update bytes")
		}
		return "synthetic-icon-manifest.json", func() { cleaned = true }, nil
	}
	out, e := updateIcons(context.Background(), iconIdentity(), "synthetic-management-token", script.run, writer, func(context.Context) error { return nil })
	if e != nil || !out.IconsVerified || out.Created || !cleaned || out.SmallIconURI != smallIconURI || out.LargeIconURI != largeIconURI {
		t.Fatal("icon update unverified", e)
	}
	updates := 0
	for _, call := range script.calls {
		if call[1] == "create-skill-for-vendor" {
			t.Fatal("icon update created skill")
		}
		if call[1] == "update-skill-manifest" {
			updates++
		}
	}
	if updates != 1 {
		t.Fatal("icon update submission repeated")
	}
}
func TestIconUpdateRefusesWrongIdentityAndChangedReadback(t *testing.T) {
	for _, identity := range []result{{}, {SkillID: referenceSkill, VendorMatchedReference: true, ManifestVerified: true}, {SkillID: suburbanSkill, Created: true, VendorMatchedReference: true, ManifestVerified: true}} {
		_, e := updateIcons(context.Background(), identity, "synthetic", func(context.Context, []string) execution { t.Fatal("unsafe identity made request"); return execution{} }, writeIconManifest, nil)
		if e == nil {
			t.Fatal("wrong identity accepted")
		}
	}
	merged, _ := iconManifest(richManifest)
	changed := strings.Replace(string(merged), "preserve description", "unexpected edit", 1)
	script := &scripted{t: t, responses: []execution{envelope(200, string(merged)), envelope(200, changed)}}
	if _, e := updateIcons(context.Background(), iconIdentity(), "synthetic", script.run, func([]byte) (string, func(), error) { t.Fatal("unchanged icons submitted update"); return "", nil, nil }, nil); e == nil {
		t.Fatal("changed readback accepted")
	}
}
