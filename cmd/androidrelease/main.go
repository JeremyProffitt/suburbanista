// androidrelease prepares and verifies the direct-install Android artifact.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"image/png"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

func version(value string) (int64, error) {
	n, e := strconv.ParseInt(value, 10, 64)
	if e != nil || n <= 0 || n > contract.MaxAndroidVersionCode {
		return 0, errors.New("invalid Android version")
	}
	return n, nil
}
func nextVersion(data []byte) (int64, error) {
	var pages [][]struct {
		Tag string `json:"tag_name"`
	}
	if json.Unmarshal(data, &pages) != nil {
		return 0, errors.New("invalid release inventory")
	}
	var highest int64
	for _, page := range pages {
		for _, release := range page {
			if strings.HasPrefix(release.Tag, "android-v") {
				n, e := version(strings.TrimPrefix(release.Tag, "android-v"))
				if e != nil {
					return 0, e
				}
				if n > highest {
					highest = n
				}
			}
		}
	}
	return version(strconv.FormatInt(highest+1, 10))
}
func prepare(dir string, n int64) error {
	icon, err := os.Open(filepath.Join(dir, "Icon.png"))
	if err != nil {
		return errors.New("committed Icon.png is required")
	}
	config, decodeErr := png.DecodeConfig(icon)
	closeErr := icon.Close()
	if decodeErr != nil || config.Width <= 0 || config.Height <= 0 {
		return errors.New("committed Icon.png must be a valid PNG")
	}
	if closeErr != nil {
		return closeErr
	}
	updateMetadata := ""
	if publicKey := os.Getenv("UPDATE_MANIFEST_PUBLIC_KEY"); publicKey != "" {
		decoded, err := base64.StdEncoding.Strict().DecodeString(publicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return errors.New("invalid update public key")
		}
		updateMetadata = `<meta-data android:name="suburban.updatePublicKey" android:value="` + publicKey + `" />`
	}
	manifest := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="ninja.jeremy.suburbanremote" android:versionCode="%d" android:versionName="1.0.%d">
 <uses-permission android:name="android.permission.INTERNET" />
 <uses-permission android:name="android.permission.POST_NOTIFICATIONS" />
 <uses-permission android:name="android.permission.ACCESS_COARSE_LOCATION" />
 <uses-permission android:name="android.permission.ACCESS_FINE_LOCATION" />
 <uses-permission android:name="android.permission.FOREGROUND_SERVICE" />
 <uses-permission android:name="android.permission.FOREGROUND_SERVICE_LOCATION" />
 <uses-permission android:name="android.permission.WAKE_LOCK" />
 <application android:label="Suburbanista" android:debuggable="false" android:allowBackup="false">
  %s
  <activity android:name="org.golang.app.GoNativeActivity" android:label="Suburbanista" android:configChanges="orientation|keyboardHidden|uiMode" android:exported="true" android:theme="@android:style/Theme">
   <meta-data android:name="android.app.lib_name" android:value="suburbanremote" />
   <intent-filter><action android:name="android.intent.action.MAIN" /><category android:name="android.intent.category.LAUNCHER" /></intent-filter>
  </activity>
  <receiver android:name="org.golang.app.FyneNotificationReceiver" android:exported="false" />
 </application>
</manifest>
`, n, n, updateMetadata)
	if err := os.WriteFile(filepath.Join(dir, "AndroidManifest.xml"), []byte(manifest), 0600); err != nil {
		return err
	}
	return nil
}
func forms(value string) []string {
	j, _ := json.Marshal(value)
	return []string{value, base64.StdEncoding.EncodeToString([]byte(value)), string(j[1 : len(j)-1]), url.QueryEscape(value)}
}
func mask(value string) {
	for _, form := range forms(value) {
		for _, line := range strings.Split(form, "\n") {
			if line != "" {
				fmt.Printf("::add-mask::%s\n", strings.NewReplacer("%", "%25", "\r", "%0D").Replace(line))
			}
		}
	}
}
func command(private []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	hideAndroidCommand(cmd)
	out, err := cmd.CombinedOutput()
	for _, value := range private {
		for _, form := range forms(value) {
			if form != "" && bytes.Contains(out, []byte(form)) {
				return "", errors.New("tool output contained a private value")
			}
		}
	}
	if err != nil {
		if len(private) == 0 {
			return "", fmt.Errorf("%s failed: %s", filepath.Base(name), strings.TrimSpace(string(out)))
		}
		return "", fmt.Errorf("%s failed", filepath.Base(name))
	}
	return string(out), nil
}

// platformSDK changes only the typed SDK version attributes in compiled Android
// XML. Layout follows AOSP ResourceTypes.h: chunk headers, resource map, and
// ResXMLTree_attrExt/ResXMLTree_attribute. No resource IDs or code are rebuilt.
func platformSDK(data []byte) ([]byte, error) {
	invalid := errors.New("unsupported compiled Android manifest")
	if len(data) < 8 || binary.LittleEndian.Uint16(data) != 3 || binary.LittleEndian.Uint16(data[2:]) != 8 || int(binary.LittleEndian.Uint32(data[4:])) != len(data) {
		return nil, invalid
	}
	result := bytes.Clone(data)
	var resources []uint32
	patched := map[uint32]bool{}
	for offset := 8; offset < len(data); {
		if len(data)-offset < 8 {
			return nil, invalid
		}
		kind := binary.LittleEndian.Uint16(data[offset:])
		header := int(binary.LittleEndian.Uint16(data[offset+2:]))
		size := int(binary.LittleEndian.Uint32(data[offset+4:]))
		if header < 8 || size < header || size > len(data)-offset {
			return nil, invalid
		}
		chunk := data[offset : offset+size]
		if kind == 0x0180 {
			if resources != nil || header != 8 || (size-header)%4 != 0 {
				return nil, invalid
			}
			resources = make([]uint32, (size-header)/4)
			for i := range resources {
				resources[i] = binary.LittleEndian.Uint32(chunk[header+i*4:])
			}
		}
		if kind == 0x0102 {
			if header < 16 || size-header < 20 || resources == nil {
				return nil, invalid
			}
			ext := chunk[header:]
			first := int(binary.LittleEndian.Uint16(ext[8:]))
			stride := int(binary.LittleEndian.Uint16(ext[10:]))
			count := int(binary.LittleEndian.Uint16(ext[12:]))
			if first < 20 || stride < 20 || first > len(ext) || count > (len(ext)-first)/stride {
				return nil, invalid
			}
			for i := 0; i < count; i++ {
				at := header + first + i*stride
				attribute := chunk[at : at+stride]
				name := binary.LittleEndian.Uint32(attribute[4:])
				if int64(name) >= int64(len(resources)) || (resources[name] != 0x0101020c && resources[name] != 0x01010270) {
					continue
				}
				id := resources[name]
				old, newValue := uint32(21), uint32(23)
				if id == 0x01010270 {
					old, newValue = 29, 36
				}
				if patched[id] || binary.LittleEndian.Uint16(attribute[12:]) != 8 || attribute[15] != 0x10 || binary.LittleEndian.Uint32(attribute[16:]) != old {
					return nil, invalid
				}
				patched[id] = true
				binary.LittleEndian.PutUint32(result[offset+at+8:], 0xffffffff)
				binary.LittleEndian.PutUint32(result[offset+at+16:], newValue)
			}
		}
		offset += size
	}
	if len(patched) != 2 {
		return nil, invalid
	}
	return result, nil
}
func patchPlatformSDK(apk, destination string) error {
	app, err := zip.OpenReader(apk)
	if err != nil {
		return err
	}
	defer app.Close()
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer out.Close()
	w := zip.NewWriter(out)
	found := false
	for _, f := range app.File {
		if strings.HasPrefix(f.Name, "META-INF/") {
			continue
		}
		in, e := f.Open()
		if e != nil {
			w.Close()
			return e
		}
		header := f.FileHeader
		target, e := w.CreateHeader(&header)
		if e != nil {
			in.Close()
			w.Close()
			return e
		}
		if f.Name == "AndroidManifest.xml" {
			if found {
				in.Close()
				w.Close()
				return errors.New("duplicate compiled manifest")
			}
			found = true
			data, e := io.ReadAll(io.LimitReader(in, 4*1024*1024))
			in.Close()
			if e != nil {
				w.Close()
				return e
			}
			data, e = platformSDK(data)
			if e != nil {
				w.Close()
				return e
			}
			_, e = target.Write(data)
			if e != nil {
				w.Close()
				return e
			}
		} else {
			_, e = io.Copy(target, in)
			in.Close()
			if e != nil {
				w.Close()
				return e
			}
		}
	}
	if !found {
		w.Close()
		return errors.New("compiled manifest missing")
	}
	return w.Close()
}
func verifyAPK(apk, tools, ndk, cert string, n int64) (string, error) {
	output, err := command(nil, filepath.Join(tools, "apksigner"), "verify", "--verbose", "--print-certs", apk)
	if err != nil {
		return "", err
	}
	for _, marker := range []string{"Verified using v2 scheme (APK Signature Scheme v2): true", "Verified using v3 scheme (APK Signature Scheme v3): true", "Signer #1 certificate SHA-256 digest: " + cert} {
		if !strings.Contains(output, marker) {
			return "", errors.New("APK signature or certificate mismatch")
		}
	}
	if _, err = command(nil, filepath.Join(tools, "zipalign"), "-c", "-P", "16", "-v", "4", apk); err != nil {
		return "", err
	}
	badging, err := command(nil, filepath.Join(tools, "aapt2"), "dump", "badging", apk)
	if err != nil {
		return "", err
	}
	if err = verifyBadging(badging, n); err != nil {
		return "", err
	}
	r, err := zip.OpenReader(apk)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if err = verifyWidgetContents(r.File); err != nil {
		return "", err
	}
	abis := map[string]bool{}
	work, err := os.MkdirTemp("", "apk-alignment-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	for i, f := range r.File {
		if !strings.HasPrefix(f.Name, "lib/") || !strings.HasSuffix(f.Name, ".so") {
			continue
		}
		parts := strings.Split(f.Name, "/")
		if len(parts) != 3 {
			return "", errors.New("invalid native library path")
		}
		abis[parts[1]] = true
		in, e := f.Open()
		if e != nil {
			return "", e
		}
		path := filepath.Join(work, strconv.Itoa(i)+".so")
		out, e := os.Create(path)
		if e != nil {
			in.Close()
			return "", e
		}
		_, e = io.Copy(out, in)
		in.Close()
		out.Close()
		if e != nil {
			return "", e
		}
		elf, e := command(nil, filepath.Join(ndk, "toolchains", "llvm", "prebuilt", "linux-x86_64", "bin", "llvm-readelf"), "-lW", path)
		if e != nil {
			return "", e
		}
		loads := 0
		for _, line := range strings.Split(elf, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == "LOAD" {
				loads++
				if fields[len(fields)-1] != "0x4000" {
					return "", errors.New("native library not aligned to 16KB")
				}
			}
		}
		if loads == 0 {
			return "", errors.New("native library has no LOAD segments")
		}
	}
	if !abis["arm64-v8a"] || !abis["x86_64"] {
		return "", errors.New("required Android ABIs missing")
	}
	return output, nil
}
func verifyBadging(badging string, n int64) error {
	for _, marker := range []string{"name='" + contract.AndroidPackageID + "'", "versionCode='" + strconv.FormatInt(n, 10) + "'", "versionName='1.0." + strconv.FormatInt(n, 10) + "'", "minSdkVersion:'23'", "targetSdkVersion:'36'", "application-label:'Suburbanista'", "application: label='Suburbanista' icon='res/mipmap-xxxhdpi-v4/icon.png'"} {
		if !strings.Contains(badging, marker) {
			return errors.New("APK identity or SDK mismatch")
		}
	}
	return nil
}
func sign(n int64, tools, ndk string) error {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_REF") != "refs/heads/main" || os.Getenv("GITHUB_REPOSITORY") != "JeremyProffittOrg/Suburban-Remote" || (os.Getenv("GITHUB_EVENT_NAME") != "push" && os.Getenv("GITHUB_EVENT_NAME") != "workflow_dispatch") {
		return errors.New("signing requires GitHub Actions main")
	}
	names := []string{"ANDROID_SIGNING_KEYSTORE_B64", "ANDROID_KEYSTORE_PASSWORD", "ANDROID_KEY_PASSWORD", "UPDATE_MANIFEST_SIGNING_KEY"}
	private := []string{}
	for _, name := range names {
		v := os.Getenv(name)
		if v == "" {
			return fmt.Errorf("missing %s", name)
		}
		private = append(private, v)
		mask(v)
	}
	cert := os.Getenv("ANDROID_SIGNING_CERT_SHA256")
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(cert) || os.Getenv("ANDROID_KEY_ALIAS") == "" {
		return errors.New("invalid Android signing configuration")
	}
	key, err := base64.StdEncoding.DecodeString(private[0])
	if err != nil {
		return errors.New("invalid keystore encoding")
	}
	defer clear(key)
	dir, err := os.MkdirTemp(os.Getenv("RUNNER_TEMP"), "android-sign-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "release.jks")
	if err = os.WriteFile(path, key, 0600); err != nil {
		return err
	}
	if err = os.MkdirAll("artifacts", 0700); err != nil {
		return err
	}
	apk := filepath.Join("artifacts", contract.ReleaseArtifactID(n))
	if _, err = command(private, filepath.Join(tools, "apksigner"), "sign", "--ks", path, "--ks-key-alias", os.Getenv("ANDROID_KEY_ALIAS"), "--ks-pass", "env:ANDROID_KEYSTORE_PASSWORD", "--key-pass", "env:ANDROID_KEY_PASSWORD", "--v2-signing-enabled", "true", "--v3-signing-enabled", "true", "--out", apk, "aligned.apk"); err != nil {
		return err
	}
	evidence, err := verifyAPK(apk, tools, ndk, cert, n)
	if err != nil {
		return err
	}
	fmt.Print(evidence)
	data, err := os.ReadFile(apk)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	now := time.Now().UTC().Truncate(time.Second)
	manifest := contract.ReleaseManifest{SchemaVersion: 1, PackageID: contract.AndroidPackageID, VersionName: "1.0." + strconv.FormatInt(n, 10), VersionCode: n, MinimumAndroidAPI: 23, ReleasedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour), ArtifactID: contract.ReleaseArtifactID(n), APKBytes: int64(len(data)), APKSHA256: hex.EncodeToString(digest[:]), SigningCertificateSHA256: cert, DownloadRoute: contract.ReleaseDownloadRoute(n)}
	signing, err := base64.StdEncoding.DecodeString(private[3])
	if err != nil || len(signing) != ed25519.PrivateKeySize {
		return errors.New("invalid release signing key")
	}
	defer clear(signing)
	pub, err := base64.StdEncoding.DecodeString(os.Getenv("UPDATE_MANIFEST_PUBLIC_KEY"))
	if err != nil || !bytes.Equal(pub, ed25519.PrivateKey(signing).Public().(ed25519.PublicKey)) {
		return errors.New("release key pair mismatch")
	}
	payload, _ := json.Marshal(manifest)
	envelope := contract.SignedRelease{SchemaVersion: 1, KeyID: "primary", Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(signing, payload))}
	encoded, _ := json.MarshalIndent(envelope, "", "  ")
	if _, err = contract.VerifyReleaseManifest(encoded, "https://suburban.jeremy.ninja/api/releases/latest", contract.ReleaseTrust{Origin: "https://suburban.jeremy.ninja", PublicKeys: map[string]ed25519.PublicKey{"primary": pub}}, contract.InstalledRelease{VersionCode: n - 1, AndroidAPI: 36, SigningCertificateSHA256: cert}, now); err != nil {
		return err
	}
	if err = os.WriteFile("artifacts/release-manifest.json", encoded, 0600); err != nil {
		return err
	}
	report, _ := json.MarshalIndent(manifest, "", "  ")
	return os.WriteFile("artifacts/android-release.json", report, 0600)
}

// verifyArtifact rechecks the signed bytes selected after device verification.
// Publication never rebuilds or resigns the APK that passed the runtime gates.
func verifyArtifact(n int64) error {
	pub, err := base64.StdEncoding.DecodeString(os.Getenv("UPDATE_MANIFEST_PUBLIC_KEY"))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid release public key")
	}
	encoded, err := os.ReadFile("artifacts/release-manifest.json")
	if err != nil {
		return err
	}
	cert := os.Getenv("ANDROID_SIGNING_CERT_SHA256")
	manifest, err := contract.VerifyReleaseManifest(encoded, "https://suburban.jeremy.ninja/api/releases/latest", contract.ReleaseTrust{Origin: "https://suburban.jeremy.ninja", PublicKeys: map[string]ed25519.PublicKey{"primary": pub}}, contract.InstalledRelease{VersionCode: n - 1, AndroidAPI: 36, SigningCertificateSHA256: cert}, time.Now().UTC())
	if err != nil {
		return err
	}
	if manifest.VersionCode != n || manifest.APKSHA256 != os.Getenv("VERIFIED_APK_SHA256") {
		return errors.New("artifact does not match device-verified version and digest")
	}
	apk := filepath.Join("artifacts", manifest.ArtifactID)
	if err = verifyArtifactBytes(apk, manifest); err != nil {
		return err
	}
	if _, err = verifyAPK(apk, os.Getenv("ANDROID_BUILD_TOOLS"), os.Getenv("ANDROID_NDK_HOME"), cert, n); err != nil {
		return err
	}
	// Replace the unsigned human-readable report with verified signed metadata.
	report, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("artifacts/android-release.json", report, 0600)
}

func verifyArtifactBytes(path string, manifest contract.ReleaseManifest) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(f, manifest.APKBytes+1))
	if err != nil || size != manifest.APKBytes || hex.EncodeToString(hash.Sum(nil)) != manifest.APKSHA256 {
		return errors.New("signed APK size or digest mismatch")
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "command required")
		os.Exit(1)
	}
	if os.Args[1] == "next-version" {
		data, err := os.ReadFile("releases.json")
		if err == nil {
			var n int64
			n, err = nextVersion(data)
			if err == nil {
				fmt.Println(n)
				return
			}
		}
		fmt.Fprintln(os.Stderr, "cannot determine next Android version")
		os.Exit(1)
	}
	n, err := version(os.Getenv("ANDROID_VERSION_CODE"))
	if err == nil {
		switch os.Args[1] {
		case "prepare":
			err = prepare("mobile/cmd/mobile", n)
		case "platform-sdk":
			err = patchPlatformSDK("universal.apk", "universal-supported.apk")
		case "merge-widgets":
			if len(os.Args) != 7 {
				err = errors.New("usage: androidrelease merge-widgets APK AAR OUTPUT BUILD_TOOLS ANDROID_JAR")
			} else {
				err = mergeWidgets(os.Args[2], os.Args[3], "mobile/nativewidgets/res", "mobile/nativewidgets/AndroidManifest.xml", os.Args[4], os.Args[5], os.Args[6])
			}
		case "sign":
			err = sign(n, os.Getenv("ANDROID_BUILD_TOOLS"), os.Getenv("ANDROID_NDK_HOME"))
		case "build-widgets":
			if len(os.Args) != 4 {
				err = errors.New("usage: androidrelease build-widgets OUTPUT_AAR ANDROID_JAR")
			} else {
				err = buildWidgetAAR(os.Args[2], os.Args[3])
			}
		case "verify-artifact":
			err = verifyReadinessFile()
			if err == nil {
				err = verifyArtifact(n)
			}
		default:
			err = errors.New("unknown Android release command")
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
