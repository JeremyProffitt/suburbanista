// backgroundassets preserves approved PNG masters and packages photographic
// JPEG derivatives. Transparent vehicle and button assets are never read.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const targetWidth = 1024
const quality = 90

type receipt struct {
	ID            string `json:"id"`
	MasterPath    string `json:"masterPath"`
	MasterSHA256  string `json:"masterSha256"`
	MasterBytes   int    `json:"masterBytes"`
	MasterWidth   int    `json:"masterWidth"`
	MasterHeight  int    `json:"masterHeight"`
	RuntimePath   string `json:"runtimePath"`
	RuntimeSHA256 string `json:"runtimeSha256"`
	RuntimeBytes  int    `json:"runtimeBytes"`
	RuntimeWidth  int    `json:"runtimeWidth"`
	RuntimeHeight int    `json:"runtimeHeight"`
	JPEGQuality   int    `json:"jpegQuality"`
}
type report struct {
	Method       string    `json:"method"`
	MasterBytes  int       `json:"masterBytes"`
	RuntimeBytes int       `json:"runtimeBytes"`
	Assets       []receipt `json:"assets"`
}

func main() {
	root := flag.String("repo", "..", "repository root")
	flag.Parse()
	if err := build(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func inside(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("asset path leaves repository")
	}
	return nil
}
func directory(root, path string) (string, error) {
	if err := inside(root, path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return resolved, inside(root, resolved)
}

// Existing files are never overwritten, including on a retry. Identical output
// is reusable; any differing file stops before original source removal.
func writeNew(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if errors.Is(err, os.ErrExist) {
		existing, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("existing asset differs: %s", path)
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func build(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	runtime, err := directory(root, filepath.Join(root, "mobile", "internal", "homescene", "backgrounds"))
	if err != nil {
		return err
	}
	masters, err := directory(root, filepath.Join(root, "artifacts", "appearance-implementation", "masters"))
	if err != nil {
		return err
	}
	proof, err := directory(root, filepath.Join(root, "artifacts", "appearance-implementation", "packaging"))
	if err != nil {
		return err
	}
	catalog := appearancecore.Backdrops()
	if len(catalog) != 29 {
		return errors.New("background catalog must contain 29 assets")
	}
	audit := report{Method: "Go image/jpeg quality90; CatmullRom maximum1024px width; preserve aspect ratio, no crop; exact original PNG masters retained"}
	originals := image.NewNRGBA(image.Rect(0, 0, 1800, 1440))
	derivatives := image.NewNRGBA(originals.Bounds())
	var sources []string
	for i, item := range catalog {
		source := filepath.Join(runtime, item.ID+".png")
		master := filepath.Join(masters, item.ID+".png")
		destination := filepath.Join(runtime, item.ID+".jpg")
		for _, p := range []string{source, master, destination} {
			if err := inside(root, p); err != nil {
				return err
			}
			if info, e := os.Lstat(p); e == nil && info.Mode()&os.ModeSymlink != 0 {
				return errors.New("asset symlink not permitted")
			}
		}
		raw, e := os.ReadFile(source)
		if errors.Is(e, os.ErrNotExist) {
			raw, e = os.ReadFile(master)
		}
		if e != nil {
			return e
		}
		original, e := png.Decode(bytes.NewReader(raw))
		if e != nil {
			return e
		}
		b := original.Bounds()
		if b.Dx() < 720 || b.Dy() < 480 {
			return fmt.Errorf("master too small: %s", item.ID)
		}
		if err := writeNew(master, raw); err != nil {
			return err
		}
		verified, e := os.ReadFile(master)
		if e != nil || !bytes.Equal(raw, verified) {
			return errors.New("master readback failed")
		}
		w := min(targetWidth, b.Dx())
		h := int(float64(b.Dy())*float64(w)/float64(b.Dx()) + .5)
		if h < 480 {
			return fmt.Errorf("derivative too short: %s", item.ID)
		}
		resized := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(resized, resized.Bounds(), original, b, draw.Src, nil)
		var encoded bytes.Buffer
		if e = jpeg.Encode(&encoded, resized, &jpeg.Options{Quality: quality}); e != nil {
			return e
		}
		data := encoded.Bytes()
		if err := writeNew(destination, data); err != nil {
			return err
		}
		derivative, e := jpeg.Decode(bytes.NewReader(data))
		if e != nil {
			return e
		}
		actual, e := os.ReadFile(destination)
		if e != nil || !bytes.Equal(data, actual) {
			return errors.New("derivative readback failed")
		}
		audit.Assets = append(audit.Assets, receipt{item.ID, filepath.ToSlash(filepath.Join("artifacts", "appearance-implementation", "masters", item.ID+".png")), hash(raw), len(raw), b.Dx(), b.Dy(), filepath.ToSlash(filepath.Join("mobile", "internal", "homescene", "backgrounds", item.ID+".jpg")), hash(data), len(data), w, h, quality})
		audit.MasterBytes += len(raw)
		audit.RuntimeBytes += len(data)
		tile(originals, original, i, item.Name)
		tile(derivatives, derivative, i, item.Name)
		sources = append(sources, source)
	}
	for _, entry := range []struct {
		name string
		img  image.Image
	}{{"masters-contact-sheet.png", originals}, {"derivatives-contact-sheet.png", derivatives}} {
		var data bytes.Buffer
		if err := png.Encode(&data, entry.img); err != nil {
			return err
		}
		if err := writeNew(filepath.Join(proof, entry.name), data.Bytes()); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return err
	}
	if err := writeNew(filepath.Join(proof, "background-packaging.json"), append(data, '\n')); err != nil {
		return err
	}
	// All29 originals and derivatives have passed readback before removing only
	// the duplicate runtime PNGs. The exact PNG bytes remain at the checked paths.
	for i, source := range sources {
		if err := inside(runtime, source); err != nil {
			return err
		}
		raw, err := os.ReadFile(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		master, err := os.ReadFile(filepath.Join(masters, catalog[i].ID+".png"))
		if err != nil || !bytes.Equal(raw, master) {
			return errors.New("master preservation check failed before removal")
		}
		if err := os.Remove(source); err != nil {
			return err
		}
	}
	fmt.Printf("Packaged %d backgrounds: masters=%d bytes; runtime=%d bytes; reduction=%.2f%%\n", len(audit.Assets), audit.MasterBytes, audit.RuntimeBytes, 100*(1-float64(audit.RuntimeBytes)/float64(audit.MasterBytes)))
	return nil
}
func tile(sheet *image.NRGBA, img image.Image, index int, name string) {
	x, y := (index%5)*360, (index/5)*240
	xdraw.CatmullRom.Scale(sheet, image.Rect(x, y, x+360, y+216), img, img.Bounds(), draw.Src, nil)
	draw.Draw(sheet, image.Rect(x, y+216, x+360, y+240), image.NewUniform(color.NRGBA{9, 19, 27, 255}), image.Point{}, draw.Src)
	d := font.Drawer{Dst: sheet, Src: image.White, Face: basicfont.Face7x13, Dot: fixed.P(x+8, y+232)}
	d.DrawString(name)
}
