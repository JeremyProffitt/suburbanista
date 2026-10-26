package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const widgetMobileVersion = "v0.0.0-20260908204917-8b95e45f8d3e"
const widgetImportPath = "github.com/JeremyProffittOrg/suburban-remote/mobile/nativewidgets"

func buildWidgetAAR(output, sdkJar string) error {
	output, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	sdkJar, err = filepath.Abs(sdkJar)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	run := func(dir string, env []string, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		hideAndroidCommand(cmd)
		out, e := cmd.CombinedOutput()
		if e != nil {
			return nil, fmt.Errorf("widget %s failed: %s", filepath.Base(name), strings.TrimSpace(string(out)))
		}
		return out, nil
	}
	moduleDir, err := filepath.Abs("mobile")
	if err != nil {
		return err
	}
	moduleJSON, err := run(moduleDir, nil, "go", "list", "-m", "-json", "golang.org/x/mobile")
	if err != nil {
		return err
	}
	var module struct{ Dir, Version string }
	if json.Unmarshal(moduleJSON, &module) != nil || module.Version != widgetMobileVersion || module.Dir == "" {
		return errors.New("widget runtime must use approved x/mobile pin")
	}
	// Bind emits all supported ABIs. Architecture-specific font/image helpers
	// must be copied too; an arm64-only graph omits x86 SIMD CPU detection.
	var list []byte
	for _, architecture := range []string{"arm", "arm64", "386", "amd64"} {
		graph, graphErr := run(moduleDir, []string{"GOOS=android", "GOARCH=" + architecture, "CGO_ENABLED=1"}, "go", "list", "-deps", "-e", "-json", "-tags", "widgetbind", "./nativewidgets")
		if graphErr != nil {
			return graphErr
		}
		list = append(list, graph...)
		list = append(list, '\n')
	}
	work, err := os.MkdirTemp("", "suburban-widget-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	gopath := filepath.Join(work, "gopath")
	source := filepath.Join(gopath, "src")
	if err = os.MkdirAll(source, 0700); err != nil {
		return err
	}
	copyFile := func(from, to string) error {
		data, e := os.ReadFile(from)
		if e != nil {
			return e
		}
		if e = os.MkdirAll(filepath.Dir(to), 0700); e != nil {
			return e
		}
		return os.WriteFile(to, data, 0600)
	}
	// gobind also consumes runtime files that are not ordinary Go import edges.
	if err = filepath.WalkDir(module.Dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("widget runtime symlink not allowed")
		}
		rel, e := filepath.Rel(module.Dir, path)
		if e != nil {
			return e
		}
		return copyFile(path, filepath.Join(source, "golang.org", "x", "mobile", rel))
	}); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(list))
	count := 0
	staged := map[string]bool{}
	for {
		var pkg struct {
			Dir, ImportPath string
			Standard        bool
			EmbedFiles      []string
			Error           *struct{ Err string }
		}
		err = decoder.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if pkg.Standard || pkg.ImportPath == "C" || strings.HasPrefix(pkg.ImportPath, "Java/") || strings.HasPrefix(pkg.ImportPath, "golang.org/x/mobile/") {
			continue
		}
		if staged[pkg.ImportPath] {
			continue
		}
		if pkg.Error != nil || pkg.Dir == "" {
			return fmt.Errorf("widget dependency unavailable: %s", pkg.ImportPath)
		}
		if !safeImportPath(pkg.ImportPath) {
			return errors.New("unsafe widget dependency path")
		}
		entries, e := os.ReadDir(pkg.Dir)
		if e != nil {
			return e
		}
		target := filepath.Join(source, filepath.FromSlash(pkg.ImportPath))
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("widget dependency symlink not allowed")
			}
			if e = copyFile(filepath.Join(pkg.Dir, entry.Name()), filepath.Join(target, entry.Name())); e != nil {
				return e
			}
		}
		for _, embedded := range pkg.EmbedFiles {
			if !safeRelativePath(embedded) {
				return errors.New("unsafe widget embed path")
			}
			if e = copyFile(filepath.Join(pkg.Dir, embedded), filepath.Join(target, embedded)); e != nil {
				return e
			}
		}
		count++
		staged[pkg.ImportPath] = true
	}
	if count == 0 {
		return errors.New("widget dependency graph empty")
	}
	bootstrap := filepath.Join(source, "suburbanwidgetbootstrap")
	if err = os.MkdirAll(bootstrap, 0700); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(bootstrap, "bootstrap.go"), []byte("package suburbanwidgetbootstrap\nfunc Ready() bool { return true }\n"), 0600); err != nil {
		return err
	}
	env := []string{"GO111MODULE=off", "GOWORK=off", "GOPATH=" + gopath, "CGO_LDFLAGS=-Wl,-z,max-page-size=16384"}
	bootstrapAAR := filepath.Join(work, "bootstrap.aar")
	if _, err = run(work, env, "gomobile", "bind", "-target", "android/arm64", "-androidapi", "23", "-bootclasspath", sdkJar, "-o", bootstrapAAR, "suburbanwidgetbootstrap"); err != nil {
		return err
	}
	bound, err := zip.OpenReader(bootstrapAAR)
	if err != nil {
		return err
	}
	members, err := zipIndex(bound.File)
	if err != nil {
		bound.Close()
		return err
	}
	classes, err := zipBytes(members["classes.jar"], 32<<20)
	bound.Close()
	if err != nil {
		return err
	}
	classpath := filepath.Join(work, "bootstrap-classes.jar")
	if err = os.WriteFile(classpath, classes, 0600); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return err
	}
	if _, err = run(work, env, "gomobile", "bind", "-tags", "widgetbind", "-trimpath", "-ldflags", "-s -w", "-target", "android", "-androidapi", "23", "-classpath", classpath, "-bootclasspath", sdkJar, "-o", output, widgetImportPath); err != nil {
		return err
	}
	fmt.Printf("Widget AAR built with approved x/mobile pin; staged %d dependency packages.\n", count)
	return nil
}

func safeRelativePath(path string) bool {
	return path != "" && !filepath.IsAbs(path) && filepath.Clean(path) != ".." && !strings.HasPrefix(filepath.Clean(path), ".."+string(filepath.Separator)) && !strings.Contains(path, "\\")
}
func safeImportPath(path string) bool { return safeRelativePath(path) && !strings.Contains(path, ":") }
