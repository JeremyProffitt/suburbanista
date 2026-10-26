// appearanceassets packages approved three-icon sheets at Android touch sizes.
package main

import (
	"encoding/json"
	"fmt"
	"golang.org/x/image/draw"
	"image"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	if err := build(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func build() error {
	data, err := os.ReadFile("internal/appearancecore/catalog.json")
	if err != nil {
		return err
	}
	var catalog []struct{ ID, Holiday string }
	if err = json.Unmarshal(data, &catalog); err != nil {
		return err
	}
	dest := "internal/appearancecore/icons"
	if err = os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	for _, style := range catalog {
		source := filepath.Join("..", "artifacts", "theme-config-review", "icons", style.Holiday, style.ID+".png")
		f, err := os.Open(source)
		if err != nil {
			return err
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			return err
		}
		b := img.Bounds()
		for index, action := range []string{"start", "unlock", "lock"} {
			cell := image.Rect(b.Min.X+index*b.Dx()/3, b.Min.Y, b.Min.X+(index+1)*b.Dx()/3, b.Max.Y)
			crop := image.Rectangle{}
			for y := cell.Min.Y; y < cell.Max.Y; y++ {
				for x := cell.Min.X; x < cell.Max.X; x++ {
					_, _, _, a := img.At(x, y).RGBA()
					if a > 0 {
						crop = crop.Union(image.Rect(x, y, x+1, y+1))
					}
				}
			}
			if crop.Empty() {
				return fmt.Errorf("empty icon %s %s", style.ID, action)
			}
			out := image.NewNRGBA(image.Rect(0, 0, 192, 192))
			scale := min(184.0/float64(crop.Dx()), 184.0/float64(crop.Dy()))
			w, h := int(float64(crop.Dx())*scale), int(float64(crop.Dy())*scale)
			draw.CatmullRom.Scale(out, image.Rect((192-w)/2, (192-h)/2, (192+w)/2, (192+h)/2), img, crop, draw.Src, nil)
			file, err := os.Create(filepath.Join(dest, style.ID+"-"+action+".png"))
			if err != nil {
				return err
			}
			err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(file, out)
			file.Close()
			if err != nil {
				return err
			}
		}
	}
	fmt.Printf("Packaged %d styles, %d icons at 192px\n", len(catalog), len(catalog)*3)
	return nil
}
