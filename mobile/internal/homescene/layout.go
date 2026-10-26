package homescene

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"

	xdraw "golang.org/x/image/draw"
)

func dimensions(width, height int, compact bool) (int, int, float64) {
	width = max(180, min(width, 1440))
	scale := float64(width) / 360
	height = max(int(math.Round(640*scale)), min(height, 8192))
	if compact {
		height = int(math.Round(349 * scale))
	}
	return width, height, scale
}
func visibleStatus(status string) string {
	status = strings.TrimSpace(status)
	if strings.EqualFold(status, "Ready") || strings.EqualFold(status, "Ready · commands are actions") {
		return ""
	}
	return status
}

// LayoutRegions is the cheap, pure hit-geometry path. It never decodes artwork,
// requests map pixels, composes shadows, or contacts a platform/native service.
func LayoutRegions(in Input, width, height int, compact bool) map[string]image.Rectangle {
	in = normalizeInput(in)
	_, height, scale := dimensions(width, height, compact)
	p := painter{scale: scale, fontScale: in.FontScale}
	refHeight := float64(height) / scale
	weatherWidth := math.Max(104, 61.28+p.textWidth(value(in.Temperature, "--°"), 28.8, true))
	regions := map[string]image.Rectangle{
		"weather":             p.rect(2, 46, weatherWidth, 53.28),
		"background-previous": p.rect(1, 323, 20, 20),
		"background-next":     p.rect(339, 323, 20, 20),
	}
	if in.ScreenLockVisible {
		regions["screen-lock"] = p.rect(339, 283, 20, 20)
	}
	for i, name := range []string{"start", "unlock", "lock"} {
		center := 16 + (float64(i)+.5)*328/3
		regions[name] = p.rect(center-39, 265.5, 78, 78)
	}
	if in.Settings.ButtonRotation.Enabled {
		regions["style-previous"] = p.rect(110, 323, 20, 20)
		regions["style-next"] = p.rect(230, 323, 20, 20)
	}
	if visibleStatus(in.Status) != "" {
		regions["status"] = p.rect(16, 253, 328, 12)
	}
	if !compact {
		bottom, footer := refHeight-45.2, refHeight-40.2
		regions["map"] = p.rect(0, 349, 360, bottom-349)
		regions["recenter"] = p.rect(5, footer, 35.2, 35.2)
		regions["settings"] = p.rect(319.8, footer, 35.2, 35.2)
		regions["location-refresh"] = p.rect(45, footer, 270, 35.2)
		regions["attribution"] = p.rect(335, bottom-15, 24, 14)
	}
	return regions
}

func composeMap(frame Frame, in Input, compact bool) {
	if compact {
		return
	}
	p := painter{frame.Image, float64(frame.Image.Bounds().Dx()) / 360, in.FontScale}
	r := frame.Regions["map"]
	mapImage := in.Map
	if in.MapRenderer != nil {
		mapImage = in.MapRenderer(r.Dx(), r.Dy())
	}
	if mapImage != nil {
		if mapImage.Bounds().Size() == r.Size() {
			draw.Draw(p.dst, r, mapImage, mapImage.Bounds().Min, draw.Src)
		} else {
			xdraw.NearestNeighbor.Scale(p.dst, r, mapImage, mapImage.Bounds(), draw.Src, nil)
		}
	} else {
		p.text("Vehicle map unavailable", 180, 460, 14, "center")
	}
	// The original whole-width provider notice remains in the map pixels. AWS
	// is the short documentation link at the requested lower-right inset.
	bottom := float64(r.Max.Y) / p.scale
	p.text("AWS", 359, bottom-11, 10, "right")
	if in.LocationRefreshing {
		text := value(in.Updated, "Source time unknown")
		x := math.Max(49, 180-p.textWidth(text, 12, false)/2-11)
		y := float64(frame.Image.Bounds().Dy())/p.scale - 22.6
		for i := 0; i < 18; i++ {
			a := in.RefreshAngle + float64(i)*math.Pi/12
			b := a + math.Pi/12
			p.line(x+5*math.Cos(a), y+5*math.Sin(a), x+5*math.Cos(b), y+5*math.Sin(b), color.NRGBA{245, 250, 247, uint8(60 + i*10)})
		}
	}
}
