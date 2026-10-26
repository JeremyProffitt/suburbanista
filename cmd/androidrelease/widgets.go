package main

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func androidTool(dir, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

func zipBytes(file *zip.File, limit int64) ([]byte, error) {
	if file == nil || file.UncompressedSize64 > uint64(limit) {
		return nil, errors.New("APK member missing or oversized")
	}
	r, e := file.Open()
	if e != nil {
		return nil, e
	}
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, errors.New("cannot read bounded APK member")
	}
	return b, nil
}

func zipIndex(files []*zip.File) (map[string]*zip.File, error) {
	out := map[string]*zip.File{}
	for _, f := range files {
		if out[f.Name] != nil || strings.Contains(f.Name, "\\") || strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "../") {
			return nil, errors.New("invalid or duplicate ZIP member")
		}
		out[f.Name] = f
	}
	return out, nil
}

func validBackgroundService(attrs map[string]string) bool {
	if attrs["exported"] != "false" {
		return false
	}
	switch attrs["name"] {
	case "nativewidgets.TrackingService":
		return attrs["process"] == ":tracking" && attrs["foregroundServiceType"] == "location" && attrs["stopWithTask"] == "false" && attrs["permission"] == ""
	case "nativewidgets.UpdateJobService":
		return attrs["process"] == ":updates" && attrs["permission"] == "android.permission.BIND_JOB_SERVICE" && attrs["foregroundServiceType"] == ""
	}
	return false
}

// mergeWidgetManifest adds only the known native receivers and services. The original
// application, identity, permissions and activity attributes are retained.
func mergeWidgetManifest(original, addition []byte) ([]byte, error) {
	tokens, err := decodeManifest(original)
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(addition))
	var extra []xml.Token
	depth := 0
	receivers := map[string]bool{}
	services := map[string]bool{}
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if t.Name.Local != "application" {
					return nil, errors.New("widget manifest must contain application children")
				}
				continue
			}
			if depth == 2 {
				if t.Name.Local != "receiver" && t.Name.Local != "service" {
					return nil, errors.New("native manifest component invalid")
				}
				attrs := map[string]string{}
				for _, a := range t.Attr {
					if a.Name.Space == androidNamespace {
						attrs[a.Name.Local] = a.Value
					}
				}
				n := attrs["name"]
				if t.Name.Local == "service" {
					if !validBackgroundService(attrs) || services[n] {
						return nil, errors.New("unsafe background service")
					}
					services[n] = true
					extra = append(extra, xml.CopyToken(t))
					continue
				}
				if (n != "nativewidgets.CompactProvider" && n != "nativewidgets.FullProvider") || receivers[n] || attrs["process"] != ":widgets" || attrs["exported"] != "false" {
					return nil, errors.New("unsafe widget receiver")
				}
				receivers[n] = true
			}
			extra = append(extra, xml.CopyToken(t))
		case xml.EndElement:
			if depth > 1 {
				extra = append(extra, t)
			}
			depth--
		case xml.CharData:
			if depth > 1 {
				extra = append(extra, xml.CopyToken(t))
			}
		}
	}
	if depth != 0 || len(receivers) != 2 || len(services) != 2 {
		return nil, errors.New("native receivers and services required")
	}
	var out []xml.Token
	found := false
	for _, token := range tokens {
		if end, ok := token.(xml.EndElement); ok && end.Name.Local == "application" {
			if found {
				return nil, errors.New("duplicate application")
			}
			found = true
			out = append(out, extra...)
		}
		out = append(out, token)
	}
	if !found {
		return nil, errors.New("application missing")
	}
	return encodeManifest(out)
}

func mergeWidgets(apk, aar, resources, manifest, destination, tools, sdkJar string) error {
	app, err := zip.OpenReader(apk)
	if err != nil {
		return err
	}
	defer app.Close()
	original, err := zipIndex(app.File)
	if err != nil {
		return err
	}
	library, err := zip.OpenReader(aar)
	if err != nil {
		return err
	}
	defer library.Close()
	bound, err := zipIndex(library.File)
	if err != nil {
		return err
	}
	if original["classes.dex"] == nil || original["classes2.dex"] != nil {
		return errors.New("expected one original Fyne DEX")
	}
	dex, err := zipBytes(original["classes.dex"], 32<<20)
	if err != nil {
		return err
	}
	if bytes.Contains(dex, []byte("Lgo/Seq;")) || bytes.Contains(dex, []byte("Lnativewidgets/")) {
		return errors.New("widget runtime already present")
	}
	if original["res/mipmap-xxxhdpi-v4/icon.png"] == nil {
		return errors.New("pinned Fyne icon resource missing")
	}
	for name := range original {
		if strings.HasPrefix(name, "res/") && name != "res/mipmap-xxxhdpi-v4/icon.png" {
			return errors.New("unexpected Fyne resource; refusing to renumber IDs")
		}
	}
	resourceDump, err := command(nil, androidTool(tools, "aapt2"), "dump", "resources", apk)
	if err != nil {
		return err
	}
	if strings.Count(resourceDump, "    resource ") != 1 || !strings.Contains(resourceDump, "resource 0x7f020000 mipmap/icon") {
		return errors.New("pinned Fyne resource ID differs")
	}
	work, err := os.MkdirTemp("", "suburban-widget-merge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	compiledRes := filepath.Join(work, "res")
	if err = os.MkdirAll(compiledRes, 0700); err != nil {
		return err
	}
	if err = filepath.WalkDir(resources, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("widget resources cannot be symlinks")
		}
		rel, e := filepath.Rel(resources, path)
		if e != nil {
			return e
		}
		if !strings.HasSuffix(rel, ".xml") && !strings.HasSuffix(rel, ".png") {
			return errors.New("unsupported widget resource")
		}
		target := filepath.Join(compiledRes, rel)
		if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return e
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(target, data, 0600)
	}); err != nil {
		return err
	}
	icon, err := zipBytes(original["res/mipmap-xxxhdpi-v4/icon.png"], 4<<20)
	if err != nil {
		return err
	}
	iconPath := filepath.Join(compiledRes, "mipmap-xxxhdpi-v4", "icon.png")
	if err = os.MkdirAll(filepath.Dir(iconPath), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(iconPath, icon, 0600); err != nil {
		return err
	}
	inputManifest, err := zipBytes(original["AndroidManifest.xml"], 4<<20)
	if err != nil {
		return err
	}
	addition, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	mergedManifest, err := mergeWidgetManifest(inputManifest, addition)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(work, "AndroidManifest.xml")
	if err = os.WriteFile(manifestPath, mergedManifest, 0600); err != nil {
		return err
	}
	stable := filepath.Join(work, "stable-ids.txt")
	if err = os.WriteFile(stable, []byte("ninja.jeremy.suburbanremote:mipmap/icon = 0x7f020000\n"), 0600); err != nil {
		return err
	}
	flat := filepath.Join(work, "resources.zip")
	if _, err = command(nil, androidTool(tools, "aapt2"), "compile", "--dir", compiledRes, "--no-crunch", "-o", flat); err != nil {
		return err
	}
	resourceAPK := filepath.Join(work, "resources.apk")
	if _, err = command(nil, androidTool(tools, "aapt2"), "link", "-o", resourceAPK, "--manifest", manifestPath, "-I", sdkJar, "--stable-ids", stable, "--no-compile-sdk-metadata", flat); err != nil {
		return err
	}
	linked, err := zip.OpenReader(resourceAPK)
	if err != nil {
		return err
	}
	defer linked.Close()
	linkedDump, err := command(nil, androidTool(tools, "aapt2"), "dump", "resources", resourceAPK)
	if err != nil {
		return err
	}
	if !strings.Contains(linkedDump, "resource 0x7f020000 mipmap/icon") {
		return errors.New("original icon ID changed during link")
	}
	for _, name := range []string{"layout/suburban_widget_compact", "layout/suburban_widget_full", "xml/suburban_widget_compact_info", "xml/suburban_widget_full_info"} {
		if !strings.Contains(linkedDump, " "+name) {
			return fmt.Errorf("widget resource missing: %s", name)
		}
	}
	classes, err := zipBytes(bound["classes.jar"], 32<<20)
	if err != nil {
		return err
	}
	jar := filepath.Join(work, "classes.jar")
	if err = os.WriteFile(jar, classes, 0600); err != nil {
		return err
	}
	dexDir := filepath.Join(work, "dex")
	if err = os.Mkdir(dexDir, 0700); err != nil {
		return err
	}
	// Invoke the SDK's existing D8 Java entrypoint directly on every platform.
	if _, err = command(nil, "java", "-cp", filepath.Join(tools, "lib", "d8.jar"), "com.android.tools.r8.D8", "--release", "--min-api", "23", "--lib", sdkJar, "--output", dexDir, jar); err != nil {
		return err
	}
	widgetDex, err := os.ReadFile(filepath.Join(dexDir, "classes.dex"))
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(dexDir, "classes2.dex")); !os.IsNotExist(err) {
		return errors.New("widget binding unexpectedly requires multiple DEX files")
	}
	for _, class := range []string{"Lnativewidgets/CompactProvider;", "Lnativewidgets/FullProvider;", "Lnativewidgets/TrackingService;", "Lnativewidgets/UpdateJobService;", "Lgo/Seq;"} {
		if !bytes.Contains(widgetDex, []byte(class)) {
			return fmt.Errorf("widget DEX missing %s", class)
		}
	}
	additions := map[string]*zip.File{}
	for _, f := range linked.File {
		if f.Name == "AndroidManifest.xml" || f.Name == "resources.arsc" || strings.HasPrefix(f.Name, "res/") {
			additions[f.Name] = f
		}
	}
	abis := map[string]bool{}
	for name := range original {
		parts := strings.Split(name, "/")
		if len(parts) == 3 && parts[0] == "lib" && strings.HasSuffix(parts[2], ".so") {
			abis[parts[1]] = true
		}
	}
	for abi := range abis {
		f := bound["jni/"+abi+"/libgojni.so"]
		if f == nil {
			return fmt.Errorf("widget native ABI missing: %s", abi)
		}
		name := "lib/" + abi + "/libgojni.so"
		if original[name] != nil {
			return errors.New("native library collision")
		}
		additions[name] = f
	}
	for name := range bound {
		if strings.HasPrefix(name, "jni/") {
			parts := strings.Split(name, "/")
			if len(parts) != 3 || !abis[parts[1]] || parts[2] != "libgojni.so" {
				return errors.New("unexpected widget native library")
			}
		}
	}
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(out)
	copyFile := func(name string, f *zip.File) error {
		header := f.FileHeader
		header.Name = name
		if strings.HasSuffix(name, ".so") {
			header.Method = zip.Store
		}
		w, e := writer.CreateHeader(&header)
		if e != nil {
			return e
		}
		r, e := f.Open()
		if e != nil {
			return e
		}
		defer r.Close()
		_, e = io.Copy(w, r)
		return e
	}
	for _, f := range app.File {
		if strings.HasPrefix(f.Name, "META-INF/") || additions[f.Name] != nil {
			continue
		}
		if err = copyFile(f.Name, f); err != nil {
			writer.Close()
			out.Close()
			return err
		}
	}
	names := make([]string, 0, len(additions))
	for name := range additions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err = copyFile(name, additions[name]); err != nil {
			writer.Close()
			out.Close()
			return err
		}
	}
	w, err := writer.Create("classes2.dex")
	if err == nil {
		_, err = w.Write(widgetDex)
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	merged, err := zip.OpenReader(destination)
	if err != nil {
		return err
	}
	defer merged.Close()
	return verifyWidgetContents(merged.File)
}

func verifyWidgetContents(files []*zip.File) error {
	members, err := zipIndex(files)
	if err != nil {
		return err
	}
	dex, err := zipBytes(members["classes2.dex"], 32<<20)
	if err != nil {
		return errors.New("widget DEX is required before signing/publication")
	}
	for _, class := range []string{"Lnativewidgets/CompactProvider;", "Lnativewidgets/FullProvider;", "Lnativewidgets/TrackingService;", "Lnativewidgets/UpdateJobService;", "Lgo/Seq;"} {
		if !bytes.Contains(dex, []byte(class)) {
			return errors.New("native widget provider class missing")
		}
	}
	for _, abi := range []string{"armeabi-v7a", "arm64-v8a", "x86", "x86_64"} {
		if members["lib/"+abi+"/libgojni.so"] == nil {
			return errors.New("native widget ABI missing")
		}
		library, e := zipBytes(members["lib/"+abi+"/libgojni.so"], 128<<20)
		if e != nil {
			return e
		}
		if e = verifyWidgetELF(library, abi); e != nil {
			return e
		}
	}
	manifest, err := zipBytes(members["AndroidManifest.xml"], 4<<20)
	if err != nil {
		return err
	}
	tokens, err := decodeManifest(manifest)
	if err != nil {
		return err
	}
	providers := map[string]bool{}
	services := map[string]bool{}
	for _, token := range tokens {
		start, ok := token.(xml.StartElement)
		if !ok || (start.Name.Local != "receiver" && start.Name.Local != "service") {
			continue
		}
		attrs := map[string]string{}
		for _, a := range start.Attr {
			if a.Name.Space == androidNamespace {
				attrs[a.Name.Local] = a.Value
			}
		}
		name := attrs["name"]
		if start.Name.Local == "service" {
			if !validBackgroundService(attrs) || services[name] {
				return errors.New("background service isolation invalid")
			}
			services[name] = true
			continue
		}
		if name != "nativewidgets.CompactProvider" && name != "nativewidgets.FullProvider" {
			continue
		}
		if providers[name] || attrs["process"] != ":widgets" || attrs["exported"] != "false" {
			return errors.New("widget receiver isolation invalid")
		}
		providers[name] = true
	}
	if len(providers) != 2 || len(services) != 2 {
		return errors.New("registered native providers and services required")
	}
	return nil
}

func verifyWidgetELF(data []byte, abi string) error {
	binary, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return errors.New("widget native library is not ELF")
	}
	defer binary.Close()
	want := map[string]elf.Machine{"armeabi-v7a": elf.EM_ARM, "arm64-v8a": elf.EM_AARCH64, "x86": elf.EM_386, "x86_64": elf.EM_X86_64}
	if binary.Machine != want[abi] {
		return errors.New("widget native library ABI differs")
	}
	loads := 0
	for _, segment := range binary.Progs {
		if segment.Type == elf.PT_LOAD {
			loads++
			if segment.Align != 16384 {
				return errors.New("widget native library is not 16KB aligned")
			}
		}
	}
	if loads == 0 {
		return errors.New("widget native library LOAD segments missing")
	}
	symbols, err := binary.DynamicSymbols()
	if err != nil {
		return errors.New("widget JNI symbols missing")
	}
	required := map[string]bool{"JNI_OnLoad": false, "Java_go_Seq_init": false, "Java_go_Seq_setContext": false, "Java_nativewidgets_CompactProvider__1_1NewCompactProvider": false, "Java_nativewidgets_FullProvider__1_1NewFullProvider": false, "Java_nativewidgets_CompactProvider_onReceive": false, "Java_nativewidgets_FullProvider_onReceive": false, "Java_nativewidgets_TrackingService__1_1NewTrackingService": false, "Java_nativewidgets_TrackingService_onStartCommand": false, "Java_nativewidgets_TrackingService_onBind": false, "Java_nativewidgets_UpdateJobService__1_1NewUpdateJobService": false, "Java_nativewidgets_UpdateJobService_onStartJob": false, "Java_nativewidgets_UpdateJobService_onStopJob": false}
	for _, symbol := range symbols {
		if _, exists := required[symbol.Name]; exists && symbol.Section != elf.SHN_UNDEF {
			required[symbol.Name] = true
		}
	}
	for _, found := range required {
		if !found {
			return errors.New("widget JNI entrypoint missing")
		}
	}
	return nil
}
