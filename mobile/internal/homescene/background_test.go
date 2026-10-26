package homescene

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	xdraw "golang.org/x/image/draw"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestBackdropCatalogAssets(t *testing.T) {
	type receipt struct {
		ID            string `json:"id"`
		Width, Height int
		SHA256        string `json:"sha256"`
	}
	var receipts []receipt
	sheet := image.NewNRGBA(image.Rect(0, 0, 1800, 1320))
	p := painter{sheet, 1, 1}
	p.fill(sheet.Bounds(), color.NRGBA{9, 19, 27, 255})
	seen := map[[32]byte]bool{}
	packagedBytes := 0
	for i, b := range appearancecore.Backdrops() {
		raw, err := backgroundFiles.ReadFile("backgrounds/" + b.ID + ".jpg")
		if err != nil {
			t.Fatal(b.ID, err)
		}
		sum := sha256.Sum256(raw)
		if seen[sum] {
			t.Fatal("duplicate background", b.ID)
		}
		seen[sum] = true
		packagedBytes += len(raw)
		_, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || format != "jpeg" {
			t.Fatal("background must use photographic JPEG", b.ID, err)
		}
		img := asset("background-test/"+b.ID, func() ([]byte, error) { return raw, nil })
		if img == nil {
			t.Fatal("invalid background", b.ID)
		}
		cache.Lock()
		count := len(cache.images)
		cache.Unlock()
		if count > 5 {
			t.Fatal("decoded rotation cache exceeds bound")
		}
		if img.Bounds().Dx() < 720 || img.Bounds().Dy() < 480 || img.Bounds().Dx() > 1024 {
			t.Fatal("background too small", b.ID)
		}
		offset := -10.0
		if b.ID == "charlotte" {
			offset = -25
		}
		if offset+360*float64(img.Bounds().Dy())/float64(img.Bounds().Dx()) < 300 {
			t.Fatal("background does not reach five pixels above map", b.ID)
		}
		layer := backdropLayer(b.ID, 360)
		if layer == nil || layer.Bounds() != image.Rect(0, 0, 360, 300) {
			t.Fatal("background coverage changed", b.ID)
		}
		prepared.Lock()
		withinBudget := prepared.bytes <= preparedBudget && len(prepared.items) <= 12
		prepared.Unlock()
		if !withinBudget {
			t.Fatal("prepared artwork cache exceeds bound")
		}
		x, y := (i%5)*360, (i/5)*220
		xdraw.CatmullRom.Scale(sheet, image.Rect(x, y, x+360, y+192), img, img.Bounds(), draw.Src, nil)
		p.text(b.Name, float64(x+180), float64(y+197), 14, "center")
		receipts = append(receipts, receipt{ID: b.ID, Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), SHA256: fmtHash(sum)})
	}
	if len(seen) != 29 {
		t.Fatal("missing background")
	}
	if packagedBytes > 10*1024*1024 {
		t.Fatalf("photographic asset budget exceeded: %d bytes", packagedBytes)
	}
	if folder := os.Getenv("SCENE_PROOF_DIR"); folder != "" {
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(folder, "background-contact-sheet.png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, sheet)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.MarshalIndent(receipts, "", "  ")
		if err := os.WriteFile(filepath.Join(folder, "background-assets.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func fmtHash(sum [32]byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, v := range sum {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&15]
	}
	return string(out)
}
