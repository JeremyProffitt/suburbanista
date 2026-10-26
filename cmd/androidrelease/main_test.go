package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"go.yaml.in/yaml/v3"
)

func TestAndroidPushHoldsPublicationForVerifiedArtifact(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/android.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err = yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build", "sign"} {
		job, ok := workflow.Jobs[name]
		if !ok {
			t.Fatalf("missing %s job", name)
		}
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "gh release create") || strings.Contains(step.Run, "aws s3api put-object") {
				t.Fatalf("%s publishes before runtime gates", name)
			}
		}
	}
	publish := workflow.Jobs["publish"]
	if publish.If != "github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main'" {
		t.Fatal("publication must require main dispatch")
	}
	var script strings.Builder
	for _, step := range publish.Steps {
		script.WriteString(step.Run)
		script.WriteByte('\n')
	}
	for _, required := range []string{"gh run download", "verify-artifact", "VERIFIED_APK_SHA256", "--target \"$ANDROID_SOURCE_SHA\""} {
		if !strings.Contains(script.String(), required) {
			t.Fatalf("publication missing %s", required)
		}
	}
	if strings.Contains(script.String(), "fyne package") || strings.Contains(script.String(), "androidrelease sign") {
		t.Fatal("publication must not rebuild or resign verified bytes")
	}
	var buildScript strings.Builder
	for _, step := range workflow.Jobs["build"].Steps {
		buildScript.WriteString(step.Run)
		buildScript.WriteByte('\n')
	}
	buildText := buildScript.String()
	for _, required := range []string{"build-widgets", "merge-widgets", "golang.org/x/mobile/cmd/gomobile", "golang.org/x/mobile/cmd/gobind"} {
		if !strings.Contains(buildText, required) {
			t.Fatalf("widget packaging missing %s", required)
		}
	}
	if strings.Index(buildText, "merge-widgets") > strings.Index(buildText, "platform-sdk") {
		t.Fatal("widget resources must merge before platform SDK patch and alignment")
	}
}

func TestPublicationRequiresExactSignedArtifactBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.apk")
	data := []byte("device-verified-artifact")
	digest := sha256.Sum256(data)
	manifest := contract.ReleaseManifest{APKBytes: int64(len(data)), APKSHA256: hex.EncodeToString(digest[:])}
	for _, tc := range []struct {
		name  string
		data  []byte
		valid bool
	}{
		{"verified", data, true},
		{"changed", []byte("device-verified-artifacX"), false},
		{"truncated", data[:len(data)-1], false},
		{"appended", append(bytes.Clone(data), 'x'), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := verifyArtifactBytes(path, manifest); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestManifestIdentityAndPermissions(t *testing.T) {
	t.Setenv("UPDATE_MANIFEST_PUBLIC_KEY", strings.Repeat("A", 43)+"=")
	dir := t.TempDir()
	var icon bytes.Buffer
	if err := png.Encode(&icon, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Icon.png"), icon.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepare(dir, 17); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "AndroidManifest.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct{ XMLName xml.Name }
	if xml.Unmarshal(data, &root) != nil || root.XMLName.Local != "manifest" {
		t.Fatal("invalid manifest XML")
	}
	retained, err := os.ReadFile(filepath.Join(dir, "Icon.png"))
	if err != nil || !bytes.Equal(retained, icon.Bytes()) {
		t.Fatal("committed icon was changed")
	}
	if strings.Count(string(data), `android:label="Suburbanista"`) != 2 {
		t.Fatal("application and activity display names differ")
	}
	for _, want := range []string{`android:versionCode="17"`, `android:versionName="1.0.17"`, `android:debuggable="false"`, `android:allowBackup="false"`, `android:value="suburbanremote"`, `android.permission.POST_NOTIFICATIONS`, `android.permission.ACCESS_COARSE_LOCATION`, `org.golang.app.FyneNotificationReceiver`} {
		if !strings.Contains(string(data), want) {
			t.Fatal("missing manifest property", want)
		}
	}
	for _, forbidden := range []string{"WRITE_EXTERNAL_STORAGE", "READ_EXTERNAL_STORAGE", "REQUEST_INSTALL_PACKAGES", "ACCESS_BACKGROUND_LOCATION", "uses-sdk", "usesCleartextTraffic", "android:icon="} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("unexpected permission")
		}
	}
	for _, required := range []string{"android.permission.ACCESS_FINE_LOCATION", "android.permission.FOREGROUND_SERVICE", "android.permission.FOREGROUND_SERVICE_LOCATION", "android.permission.WAKE_LOCK", "suburban.updatePublicKey", strings.Repeat("A", 43) + "="} {
		if !strings.Contains(string(data), required) {
			t.Fatal("background permission or embedded public trust missing", required)
		}
	}
}
func TestPrepareRequiresCommittedPNG(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		dir := t.TempDir()
		if invalid {
			if err := os.WriteFile(filepath.Join(dir, "Icon.png"), []byte("invalid image"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if prepare(dir, 2) == nil {
			t.Fatal("missing or invalid icon accepted")
		}
		if _, err := os.Stat(filepath.Join(dir, "AndroidManifest.xml")); !os.IsNotExist(err) {
			t.Fatal("failed preparation wrote manifest")
		}
	}
}
func TestNextVersionNeverReusesPublishedOrDraft(t *testing.T) {
	for _, tc := range []struct {
		data string
		want int64
	}{{`[]`, 1}, {`[[{"tag_name":"android-v1"},{"tag_name":"unrelated"}],[{"tag_name":"android-v7"}]]`, 8}} {
		got, err := nextVersion([]byte(tc.data))
		if err != nil || got != tc.want {
			t.Fatal("incorrect next release version", got, err)
		}
	}
	for _, data := range []string{`{}`, `[[{"tag_name":"android-vbad"}]]`, `[[{"tag_name":"android-v2100000000"}]]`} {
		if _, err := nextVersion([]byte(data)); err == nil {
			t.Fatal("invalid inventory accepted")
		}
	}
}
func TestBuildTools36Badging(t *testing.T) {
	output := "package: name='ninja.jeremy.suburbanremote' versionCode='1' versionName='1.0.1'\nminSdkVersion:'23'\ntargetSdkVersion:'36'\napplication-label:'Suburbanista'\napplication: label='Suburbanista' icon='res/mipmap-xxxhdpi-v4/icon.png'\n"
	if err := verifyBadging(output, 1); err != nil {
		t.Fatal("actual build-tools36 output rejected", err)
	}
	for _, bad := range []string{strings.Replace(output, "minSdkVersion:'23'", "minSdkVersion:'21'", 1), strings.Replace(output, "targetSdkVersion:'36'", "targetSdkVersion:'29'", 1), strings.Replace(output, "versionCode='1'", "versionCode='2'", 1), strings.ReplaceAll(output, "Suburban", "Suburban Remote"), strings.ReplaceAll(output, "res/mipmap-xxxhdpi-v4/icon.png", "other.png")} {
		if err := verifyBadging(bad, 1); err == nil {
			t.Fatal("wrong identity or SDK accepted")
		}
	}
}
func TestSigningRefusesWrongRepositoryBeforeSecrets(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_REF", "refs/heads/main")
	t.Setenv("GITHUB_EVENT_NAME", "push")
	t.Setenv("GITHUB_REPOSITORY", "synthetic/other")
	if err := sign(1, "unavailable", "unavailable"); err == nil || err.Error() != "signing requires GitHub Actions main" {
		t.Fatal("signing repository guard failed")
	}
}

func compiledManifest() []byte {
	data := make([]byte, 8+16+76)
	binary.LittleEndian.PutUint16(data, 3)
	binary.LittleEndian.PutUint16(data[2:], 8)
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)))
	binary.LittleEndian.PutUint16(data[8:], 0x180)
	binary.LittleEndian.PutUint16(data[10:], 8)
	binary.LittleEndian.PutUint32(data[12:], 16)
	binary.LittleEndian.PutUint32(data[16:], 0x0101020c)
	binary.LittleEndian.PutUint32(data[20:], 0x01010270)
	node := data[24:]
	binary.LittleEndian.PutUint16(node, 0x102)
	binary.LittleEndian.PutUint16(node[2:], 16)
	binary.LittleEndian.PutUint32(node[4:], 76)
	binary.LittleEndian.PutUint16(node[24:], 20)
	binary.LittleEndian.PutUint16(node[26:], 20)
	binary.LittleEndian.PutUint16(node[28:], 2)
	for i, value := range []uint32{21, 29} {
		attribute := node[36+i*20:]
		binary.LittleEndian.PutUint32(attribute[4:], uint32(i))
		binary.LittleEndian.PutUint32(attribute[8:], 0xffffffff)
		binary.LittleEndian.PutUint16(attribute[12:], 8)
		attribute[15] = 0x10
		binary.LittleEndian.PutUint32(attribute[16:], value)
	}
	return data
}
func TestPlatformSDKChangesOnlyDeclaredIntegers(t *testing.T) {
	original := compiledManifest()
	patched, err := platformSDK(original)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(patched[76:]) != 23 || binary.LittleEndian.Uint32(patched[96:]) != 36 {
		t.Fatal("incorrect SDK floor or target")
	}
	for i := range original {
		if (i < 76 || i >= 80) && (i < 96 || i >= 100) && original[i] != patched[i] {
			t.Fatal("unrelated manifest byte changed")
		}
	}
	for _, data := range [][]byte{nil, original[:7], original[:len(original)-1], patched} {
		if _, err := platformSDK(data); err == nil {
			t.Fatal("unsupported binary accepted")
		}
	}
	for _, offset := range []int{76, 96} {
		bad := bytes.Clone(original)
		binary.LittleEndian.PutUint32(bad[offset:], 22)
		if _, err := platformSDK(bad); err == nil {
			t.Fatal("unexpected original SDK accepted")
		}
	}
	malformed := bytes.Clone(original)
	binary.LittleEndian.PutUint16(malformed[24+28:], 65535)
	if _, err := platformSDK(malformed); err == nil {
		t.Fatal("attribute boundary violation accepted")
	}
}
func TestPlatformSDKPreservesResourcesAndCode(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "app.apk")
	out, _ := os.Create(apk)
	w := zip.NewWriter(out)
	entries := map[string][]byte{"AndroidManifest.xml": compiledManifest(), "resources.arsc": []byte("original-resources"), "classes.dex": []byte("original-code"), "lib/arm64-v8a/libapp.so": []byte("original-native"), "res/icon.png": []byte("original-icon"), "META-INF/OLD.SF": []byte("old-signature")}
	for name, data := range entries {
		f, e := w.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		f.Write(data)
	}
	w.Close()
	out.Close()
	dest := filepath.Join(dir, "patched.apk")
	if err := patchPlatformSDK(apk, dest); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != len(entries)-1 {
		t.Fatal("unexpected APK entries")
	}
	for _, f := range r.File {
		in, _ := f.Open()
		data, e := io.ReadAll(in)
		in.Close()
		if e != nil {
			t.Fatal(e)
		}
		if f.Name != "AndroidManifest.xml" && !bytes.Equal(data, entries[f.Name]) {
			t.Fatal("non-manifest entry changed")
		}
	}
}

func TestWidgetMergePreservesRealFyneManifest(t *testing.T) {
	original, err := os.ReadFile("testdata/fyne-manifest.bin")
	if err != nil {
		t.Fatal(err)
	}
	addition, err := os.ReadFile("../../mobile/nativewidgets/AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := mergeWidgetManifest(original, addition)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		XMLName     xml.Name
		Package     string `xml:"package,attr"`
		Application struct {
			Activities []struct {
				Name string `xml:"http://schemas.android.com/apk/res/android name,attr"`
			} `xml:"activity"`
			Receivers []struct {
				Name     string `xml:"http://schemas.android.com/apk/res/android name,attr"`
				Process  string `xml:"http://schemas.android.com/apk/res/android process,attr"`
				Exported string `xml:"http://schemas.android.com/apk/res/android exported,attr"`
			} `xml:"receiver"`
		} `xml:"application"`
	}
	if err = xml.Unmarshal(merged, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Package != "ninja.jeremy.suburbanremote" || len(manifest.Application.Activities) != 1 || manifest.Application.Activities[0].Name != "org.golang.app.GoNativeActivity" || len(manifest.Application.Receivers) != 3 {
		t.Fatal("original identity/activity/receiver changed")
	}
	for _, receiver := range manifest.Application.Receivers {
		if strings.HasPrefix(receiver.Name, "nativewidgets.") && (receiver.Process != ":widgets" || receiver.Exported != "false") {
			t.Fatal("widget process isolation missing")
		}
	}
	before, err := decodeManifest(original)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(merged))
	var after []xml.Token
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		switch token.(type) {
		case xml.StartElement, xml.EndElement:
			after = append(after, token)
		}
	}
	// Strip only the added receiver subtrees; the old document must be intact.
	var retained []xml.Token
	skip := 0
	for _, token := range after {
		switch n := token.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			if n.Name.Local == "receiver" || n.Name.Local == "service" {
				for _, a := range n.Attr {
					if a.Name.Local == "name" && strings.HasPrefix(a.Value, "nativewidgets.") {
						skip = 1
					}
				}
				if skip > 0 {
					continue
				}
			}
			attrs := n.Attr[:0]
			for _, a := range n.Attr {
				if a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
					attrs = append(attrs, a)
				}
			}
			n.Attr = attrs
			token = n
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
		}
		retained = append(retained, token)
	}
	a, _ := encodeManifest(before)
	b, _ := encodeManifest(retained)
	if !bytes.Equal(a, b) {
		t.Fatal("existing manifest semantics were changed")
	}
	for _, bad := range []string{strings.Replace(string(addition), `android:exported="false"`, `android:exported="true"`, 1), strings.Replace(string(addition), `android:process=":widgets"`, `android:process=""`, 1), strings.Replace(string(addition), `android:foregroundServiceType="location"`, `android:foregroundServiceType="camera"`, 1), strings.Replace(string(addition), `android.permission.BIND_JOB_SERVICE`, ``, 1), `<application><service/></application>`} {
		if _, e := mergeWidgetManifest(original, []byte(bad)); e == nil {
			t.Fatal("unsafe widget receiver accepted")
		}
	}
	for _, bad := range [][]byte{nil, original[:len(original)-1], bytes.Repeat([]byte{0xff}, 40)} {
		if _, e := decodeManifest(bad); e == nil {
			t.Fatal("invalid binary XML accepted")
		}
	}
}

func TestCompiledBackgroundServicesKeepIsolation(t *testing.T) {
	data, err := os.ReadFile("testdata/background-services-manifest.bin")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := decodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	services := 0
	for _, token := range tokens {
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "service" {
			continue
		}
		attrs := map[string]string{}
		for _, attr := range start.Attr {
			if attr.Name.Space == androidNamespace {
				attrs[attr.Name.Local] = attr.Value
			}
		}
		if !validBackgroundService(attrs) {
			t.Fatalf("compiled service rejected: %s type=%s", attrs["name"], attrs["foregroundServiceType"])
		}
		services++
		attrs["exported"] = "true"
		if validBackgroundService(attrs) {
			t.Fatal("exported compiled service accepted")
		}
		attrs["exported"] = "false"
		if attrs["name"] == "nativewidgets.TrackingService" {
			attrs["foregroundServiceType"] = "0x00000088"
			if validBackgroundService(attrs) {
				t.Fatal("additional camera service type accepted")
			}
		}
	}
	if services != 2 {
		t.Fatal("compiled service fixture incomplete")
	}
}

func TestWidgetGateRejectsLegacyAPKAndMalformedNativeLibrary(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	f, err := writer.Create("classes.dex")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("legacy-app-code"))
	writer.Close()
	reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyWidgetContents(reader.File); err == nil || err.Error() != "widget DEX is required before signing/publication" {
		t.Fatal("legacy APK passed widget gate", err)
	}
	if err = verifyWidgetELF([]byte("not a native library"), "arm64-v8a"); err == nil {
		t.Fatal("invalid widget native code accepted")
	}
}
