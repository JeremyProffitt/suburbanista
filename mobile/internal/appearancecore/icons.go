package appearancecore

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

//go:embed icons/*.png
var iconFiles embed.FS

// Icon recolors only the green lighting on normal icons. Neutral metal and
// alpha edges remain unchanged; seasonal artwork retains its approved colors.
func IconPNG(style Style, action string, palette Palette) ([]byte, error) {
	if action != "start" && action != "unlock" && action != "lock" {
		return nil, fmt.Errorf("unknown action")
	}
	data, err := iconFiles.ReadFile("icons/" + style.ID + "-" + action + ".png")
	if err != nil {
		return nil, fmt.Errorf("icon unavailable")
	}
	if style.Holiday == "" {
		src, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		out := image.NewNRGBA(src.Bounds())
		for y := 0; y < out.Bounds().Dy(); y++ {
			for x := 0; x < out.Bounds().Dx(); x++ {
				c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
				if int(c.G) > int(c.R)+18 && int(c.G) > int(c.B)+12 {
					brightness := int(c.G)
					c.R = uint8(int(palette.Accent.R) * brightness / 255)
					c.G = uint8(int(palette.Accent.G) * brightness / 255)
					c.B = uint8(int(palette.Accent.B) * brightness / 255)
				}
				out.SetNRGBA(x, y, c)
			}
		}
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, out); err != nil {
			return nil, err
		}
		data = buffer.Bytes()
	}
	return data, nil
}
