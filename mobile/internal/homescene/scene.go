// Package homescene is the single, driver-free visual definition used by the
// foreground app and Android RemoteViews. Its inputs never fetch or act on a car.
package homescene

import (
	"bytes"
	"embed"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed backgrounds/*.jpg
var backgroundFiles embed.FS

// Exact bundled faces from pinned Fyne v2.8.2, without its UI runtime.
//
//go:embed fonts/NotoSans-Regular.ttf
var regularFont []byte

//go:embed fonts/NotoSans-Bold.ttf
var boldFont []byte

type Input struct {
	MapNoticeHeight                                                           float64
	Sunrise, Sunset                                                           time.Time
	Settings                                                                  appearancecore.Settings
	Now                                                                       time.Time
	HighLow, Temperature, Range, Fuel, Odometer, Updated, Status, Attribution string
	Weather                                                                   string
	Night                                                                     bool
	Map                                                                       image.Image
	// MapRenderer composes already-cached map pixels at the actual viewport size.
	// It must perform no network, credential or vehicle operation.
	MapRenderer func(width, height int) image.Image
	// Nonzero revisions opt into per-surface map reuse. Bump for changed
	// pixels, position, gesture transform or authorization. Zero disables reuse.
	MapRevision        uint64
	Allowed            map[string]bool
	FontScale          float64
	FuelPercent        *float64
	FuelFreshness      string
	RangeFreshness     string
	LocationRefreshing bool
	RefreshAngle       float64
	ScreenLockVisible  bool
	ScreenLocked       bool
}
type Frame struct {
	Image   *image.NRGBA
	Regions map[string]image.Rectangle
}

var cache struct {
	sync.Mutex
	images map[string]image.Image
	used   map[string]uint64
	clock  uint64
}

func asset(key string, load func() ([]byte, error)) image.Image {
	cache.Lock()
	defer cache.Unlock()
	if cache.images == nil {
		cache.images = map[string]image.Image{}
		cache.used = map[string]uint64{}
	}
	cache.clock++
	if img := cache.images[key]; img != nil {
		cache.used[key] = cache.clock
		return img
	}
	data, err := load()
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	if strings.HasPrefix(key, "icon/") {
		img = visibleArtwork(img)
	}
	// One backdrop, the vehicle and three command images fit in five entries.
	// Rotation must not retain all decoded assets for the lifetime of the app.
	if len(cache.images) >= 5 {
		oldest := ""
		var stamp uint64 = ^uint64(0)
		for id, last := range cache.used {
			if last < stamp {
				oldest, stamp = id, last
			}
		}
		delete(cache.images, oldest)
		delete(cache.used, oldest)
	}
	cache.images[key] = img
	cache.used[key] = cache.clock
	return img
}
func HasBackdrop(id string) bool {
	_, err := backgroundFiles.ReadFile("backgrounds/" + id + ".jpg")
	return err == nil
}

var typeface = func() *opentype.Font {
	f, err := opentype.Parse(regularFont)
	if err != nil {
		panic(err)
	}
	return f
}()

type painter struct {
	dst              *image.NRGBA
	scale, fontScale float64
}

var boldTypeface = func() *opentype.Font {
	f, err := opentype.Parse(boldFont)
	if err != nil {
		panic(err)
	}
	return f
}()

func (p painter) rect(x, y, w, h float64) image.Rectangle {
	return image.Rect(int(math.Round(x*p.scale)), int(math.Round(y*p.scale)), int(math.Round((x+w)*p.scale)), int(math.Round((y+h)*p.scale)))
}
func (p painter) fill(r image.Rectangle, c color.NRGBA) {
	fillPixels(p.dst, r, c)
}
func (p painter) roundRect(x, y, w, h, radius float64, c color.NRGBA) {
	r := p.rect(x, y, w, h)
	for yy := r.Min.Y; yy < r.Max.Y; yy++ {
		for xx := r.Min.X; xx < r.Max.X; xx++ {
			px, py := float64(xx)/p.scale, float64(yy)/p.scale
			cx := math.Max(x+radius, math.Min(x+w-radius, px))
			cy := math.Max(y+radius, math.Min(y+h-radius, py))
			if (px-cx)*(px-cx)+(py-cy)*(py-cy) <= radius*radius {
				p.fill(image.Rect(xx, yy, xx+1, yy+1), c)
			}
		}
	}
}
func (p painter) text(value string, x, y, size float64, align string) {
	p.textFace(value, x, y, size, align, false)
}

func (p painter) textWidth(value string, size float64, bold bool) float64 {
	faceType := typeface
	if bold {
		faceType = boldTypeface
	}
	face, err := opentype.NewFace(faceType, &opentype.FaceOptions{Size: size * p.scale * p.fontScale, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return 0
	}
	defer face.Close()
	return float64(font.MeasureString(face, value)) / 64 / p.scale
}
func (p painter) textFace(value string, x, y, size float64, align string, bold bool) {
	faceType := typeface
	if bold {
		faceType = boldTypeface
	}
	face, err := opentype.NewFace(faceType, &opentype.FaceOptions{Size: size * p.scale * p.fontScale, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return
	}
	defer face.Close()
	d := font.Drawer{Dst: p.dst, Src: image.White, Face: face}
	xx := x * p.scale
	w := float64(d.MeasureString(value)) / 64
	if align == "center" {
		xx -= w / 2
	} else if align == "right" {
		xx -= w
	}
	// A neutral silver shadow keeps text legible without a white attribution panel.
	d.Src = image.NewUniform(color.NRGBA{0, 0, 0, 185})
	d.Dot = fixed.P(int(xx+1), int((y+size)*p.scale+1))
	d.DrawString(value)
	d.Src = image.White
	d.Dot = fixed.P(int(xx), int((y+size)*p.scale))
	d.DrawString(value)
}
func (p painter) fit(img image.Image, r image.Rectangle) {
	if img == nil || r.Empty() {
		return
	}
	b := img.Bounds()
	scale := math.Min(float64(r.Dx())/float64(b.Dx()), float64(r.Dy())/float64(b.Dy()))
	w, h := int(float64(b.Dx())*scale), int(float64(b.Dy())*scale)
	target := image.Rect(r.Min.X+(r.Dx()-w)/2, r.Min.Y+(r.Dy()-h)/2, r.Min.X+(r.Dx()+w)/2, r.Min.Y+(r.Dy()+h)/2)
	xdraw.CatmullRom.Scale(p.dst, target, img, b, draw.Over, nil)
}
func (p painter) ellipse(cx, cy, rx, ry float64, c color.NRGBA) {
	r := p.rect(cx-rx, cy-ry, 2*rx, 2*ry).Intersect(p.dst.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dx := (float64(x)/p.scale - cx) / rx
			dy := (float64(y)/p.scale - cy) / ry
			if dx*dx+dy*dy <= 1 {
				p.fill(image.Rect(x, y, x+1, y+1), c)
			}
		}
	}
}
func (p painter) line(x1, y1, x2, y2 float64, c color.NRGBA) {
	steps := int(math.Max(math.Abs(x2-x1), math.Abs(y2-y1))*p.scale) + 1
	for i := 0; i <= steps; i++ {
		f := float64(i) / float64(steps)
		x, y := (x1+(x2-x1)*f)*p.scale, (y1+(y2-y1)*f)*p.scale
		p.fill(image.Rect(int(x), int(y), int(x)+max(1, int(p.scale)), int(y)+max(1, int(p.scale))), c)
	}
}

func Render(in Input, width int, compact bool) Frame {
	width = max(180, min(width, 1440))
	height := 640
	if compact {
		height = 349
	}
	return RenderSize(in, width, int(math.Round(float64(height)*float64(width)/360)), compact)
}

// RenderSize preserves the approved top scene. Extra portrait height extends
// only the map; footer controls retain their five-reference-pixel insets.
func RenderSize(in Input, width, height int, compact bool) Frame {
	var renderer Renderer
	return renderer.RenderSize(in, width, height, compact)
}

func renderBase(in Input, width, height int, compact bool) Frame {
	width, height, scale := dimensions(width, height, compact)
	referenceHeight := float64(height) / scale
	frame := Frame{Image: image.NewNRGBA(image.Rect(0, 0, width, height)), Regions: LayoutRegions(in, width, height, compact)}
	fs := in.FontScale
	if fs <= 0 {
		fs = 1
	}
	p := painter{frame.Image, scale, math.Min(fs, 1.6)}
	if in.Settings.Mode == "" {
		in.Settings = appearancecore.Default()
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	body, toolbar := color.NRGBA{10, 32, 19, 255}, color.NRGBA{16, 51, 38, 255}
	palette, paletteErr := in.Settings.PaletteAt(in.Now)
	if paletteErr == nil && !strings.EqualFold(in.Settings.Background, appearancecore.Default().Background) {
		body, toolbar = palette.Background, palette.Surface
	}
	p.fill(frame.Image.Bounds(), body)
	p.fill(p.rect(0, 0, 360, 44), toolbar)
	backdrop := in.Settings.BackdropAt(in.Now)
	plate := backdropLayer(backdrop, width)
	scene := p.rect(0, 44, 360, 300)
	if plate != nil {
		draw.Draw(p.dst, scene, plate, image.Point{}, draw.Src)
	} else {
		p.fill(scene, color.NRGBA{30, 43, 53, 255})
		p.text("Background artwork unavailable", 180, 150, 14, "center")
	}
	if in.Settings.AutomaticSky {
		if p.solar(scene, in.Now, in.Sunrise, in.Sunset) {
			condition := in.Weather
			switch condition {
			case "Dawn", "Sunrise", "Sunset", "Blue hour", "Night":
				condition = "Clear daylight"
			}
			p.weather(scene, condition, false)
		} else {
			p.weather(scene, in.Weather, in.Night)
		}
	}
	p.text(value(in.HighLow, "--° / --°"), 12, 12, 15, "left")
	p.text(value(in.Range, "Range --"), 180, 11, 16, "center")
	if in.FuelPercent == nil {
		p.text("Fuel --", 348, 14, 12, "right")
	} else {
		p.fuelPump(262, 14)
		for i := 0; i < 10; i++ {
			p.fill(p.rect(280+float64(i)*7, 13, 5, 18), color.NRGBA{179, 206, 192, 255})
			ink := toolbar
			if float64(i)*10 < *in.FuelPercent {
				ink = color.NRGBA{107, 232, 173, 255}
			}
			p.fill(p.rect(281+float64(i)*7, 14, 3, 16), ink)
		}
	}
	// Weather panel is black alpha 0.1. Effects never touch this foreground layer.
	temperature := value(in.Temperature, "--°")
	weatherWidth := math.Max(104, 61.28+p.textWidth(temperature, 28.8, true))
	p.roundRect(2, 46, weatherWidth, 53.28, 8, color.NRGBA{0, 0, 0, 26})
	p.weatherIcon(in.Weather, in.Night, 28, 73)
	p.textFace(temperature, 59, 57, 28.8, "left", true)
	style, err := in.Settings.SelectedStyleAt(in.Now)
	foreground := foregroundLayer(in, width, style, palette, err == nil)
	overlayPixels(p.dst, foreground.Bounds(), foreground, image.Point{})
	p.text(value(in.Odometer, "Mileage --"), 344, 241, 14, "right")
	p.roundRect(1, 323, 20, 20, 3, color.NRGBA{0, 0, 0, 26})
	p.roundRect(339, 323, 20, 20, 3, color.NRGBA{0, 0, 0, 26})
	p.arrow(11, 333, -1, false)
	p.arrow(349, 333, 1, false)
	if in.ScreenLockVisible {
		p.roundRect(339, 283, 20, 20, 3, color.NRGBA{0, 0, 0, 26})
		ink := color.NRGBA{245, 250, 247, 255}
		p.roundRect(344, 292, 10, 7, 1, ink)
		p.line(346, 292, 346, 288, ink)
		p.line(346, 288, 348, 286, ink)
		p.line(348, 286, 350, 286, ink)
		if in.ScreenLocked {
			p.line(350, 286, 352, 288, ink)
			p.line(352, 288, 352, 292, ink)
		} else {
			p.line(350, 286, 353, 288, ink)
		}
	}
	if in.Settings.ButtonRotation.Enabled {
		p.roundRect(110, 323, 20, 20, 3, color.NRGBA{0, 0, 0, 26})
		p.roundRect(230, 323, 20, 20, 3, color.NRGBA{0, 0, 0, 26})
		p.arrow(120, 333, -1, true)
		p.arrow(240, 333, 1, true)
	}
	status := visibleStatus(in.Status)
	if status != "" {
		p.text(status, 180, 253, 8, "center")
	}
	if !compact {
		mapBottom := referenceHeight - 45.2
		footerTop := referenceHeight - 40.2
		r := p.rect(0, 349, 360, mapBottom-349)
		p.fill(r, color.NRGBA{14, 34, 46, 255})
		p.text(value(in.Updated, "Source time unknown"), 180, referenceHeight-30, 12, "center")
		p.footerControl(5, footerTop, false)
		p.footerControl(319.8, footerTop, true)
	}
	return frame
}

// The approved vehicle asset's visible C-shaped lamp occupies this normalized
// crop. A brightness/color mask follows its real pixels, not a floating ellipse.
func headlights(hero image.Image) image.Image {
	if hero == nil {
		return nil
	}
	b := hero.Bounds()
	out := image.NewNRGBA(b)
	for y := int(float64(b.Dy()) * .40); y < int(float64(b.Dy())*.58); y++ {
		for x := int(float64(b.Dx()) * .24); x < int(float64(b.Dx())*.37); x++ {
			c := color.NRGBAModel.Convert(hero.At(x, y)).(color.NRGBA)
			if c.G > 150 && c.B > 180 && int(c.B) > int(c.R)+5 {
				out.SetNRGBA(x, y, color.NRGBA{245, 246, 228, 130})
			}
		}
	}
	return out
}
func value(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
func (p painter) arrow(x, y float64, direction int, vertical bool) {
	c := color.NRGBA{240, 244, 248, 255}
	if vertical {
		p.line(x-4, y-float64(direction)*2, x, y+float64(direction)*2, c)
		p.line(x, y+float64(direction)*2, x+4, y-float64(direction)*2, c)
	} else {
		p.line(x-float64(direction)*2, y-4, x+float64(direction)*2, y, c)
		p.line(x+float64(direction)*2, y, x-float64(direction)*2, y+4, c)
	}
}
func visibleArtwork(img image.Image) image.Image {
	b := img.Bounds()
	visible := image.Rectangle{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a != 0 {
				r := image.Rect(x, y, x+1, y+1)
				if visible.Empty() {
					visible = r
				} else {
					visible = visible.Union(r)
				}
			}
		}
	}
	if visible.Empty() {
		return img
	}
	out := image.NewNRGBA(image.Rect(0, 0, visible.Dx(), visible.Dy()))
	draw.Draw(out, out.Bounds(), img, visible.Min, draw.Src)
	return out
}

func artworkOpacity(img image.Image, opacity float64) image.Image {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			c.A = uint8(float64(c.A) * opacity)
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}

// Three linear-time box passes approximate the approved soft CSS drop shadow.
// Work is confined to the artwork bounds instead of allocating a full frame or
// issuing millions of single-pixel draw calls at handset density.
func (p painter) shadowFit(img image.Image, r image.Rectangle, ink color.NRGBA, blur, offsetY float64) {
	if img == nil || r.Empty() {
		return
	}
	radius := max(1, int(math.Round(blur*p.scale/2)))
	pad := radius*3 + 1
	bounds := image.Rect(0, 0, r.Dx()+2*pad, r.Dy()+2*pad)
	layer := image.NewNRGBA(bounds)
	(painter{layer, p.scale, p.fontScale}).fit(img, image.Rect(pad, pad, pad+r.Dx(), pad+r.Dy()))
	alpha := make([]uint8, bounds.Dx()*bounds.Dy())
	for i := range alpha {
		alpha[i] = layer.Pix[i*4+3]
	}
	temp := make([]uint8, len(alpha))
	w, h := bounds.Dx(), bounds.Dy()
	divisor := 2*radius + 1
	for pass := 0; pass < 3; pass++ {
		for y := 0; y < h; y++ {
			sum := 0
			for x := 0; x <= radius && x < w; x++ {
				sum += int(alpha[y*w+x])
			}
			for x := 0; x < w; x++ {
				temp[y*w+x] = uint8(sum / divisor)
				if old := x - radius; old >= 0 {
					sum -= int(alpha[y*w+old])
				}
				if next := x + radius + 1; next < w {
					sum += int(alpha[y*w+next])
				}
			}
		}
		for x := 0; x < w; x++ {
			sum := 0
			for y := 0; y <= radius && y < h; y++ {
				sum += int(temp[y*w+x])
			}
			for y := 0; y < h; y++ {
				alpha[y*w+x] = uint8(sum / divisor)
				if old := y - radius; old >= 0 {
					sum -= int(temp[old*w+x])
				}
				if next := y + radius + 1; next < h {
					sum += int(temp[next*w+x])
				}
			}
		}
	}
	for i, a := range alpha {
		layer.Pix[i*4], layer.Pix[i*4+1], layer.Pix[i*4+2], layer.Pix[i*4+3] = ink.R, ink.G, ink.B, uint8(int(a)*int(ink.A)/255)
	}
	dest := bounds.Add(image.Pt(r.Min.X-pad, r.Min.Y-pad+int(math.Round(offsetY*p.scale))))
	overlayPixels(p.dst, dest, layer, image.Point{})
}

func (p painter) ring(x, y, radius float64, ink color.NRGBA) {
	const steps = 64
	for i := 0; i < steps; i++ {
		a, b := float64(i)*2*math.Pi/steps, float64(i+1)*2*math.Pi/steps
		p.line(x+radius*math.Cos(a), y+radius*math.Sin(a), x+radius*math.Cos(b), y+radius*math.Sin(b), ink)
	}
}
func (p painter) footerControl(x, y float64, gear bool) {
	p.roundRect(x, y, 35.2, 35.2, 8, color.NRGBA{41, 63, 81, 255})
	white := color.NRGBA{255, 255, 255, 255}
	step := 18.4 / 24
	ox, oy := x+8.4, y+8.4
	line := func(x1, y1, x2, y2 float64) { p.line(ox+x1*step, oy+y1*step, ox+x2*step, oy+y2*step, white) }
	if !gear {
		p.ring(ox+12*step, oy+12*step, 6*step, white)
		p.ellipse(ox+12*step, oy+12*step, 2*step, 2*step, white)
		line(12, 2, 12, 6)
		line(12, 18, 12, 22)
		line(2, 12, 6, 12)
		line(18, 12, 22, 12)
		return
	}
	points := [][2]float64{{9, 3}, {10, 2}, {14, 2}, {15, 5}, {18, 6}, {21, 8}, {20, 11}, {20, 14}, {21, 17}, {18, 19}, {15, 20}, {14, 22}, {10, 22}, {9, 20}, {6, 19}, {3, 17}, {4, 14}, {4, 11}, {3, 8}, {6, 6}, {9, 5}, {9, 3}}
	for i := 1; i < len(points); i++ {
		line(points[i-1][0], points[i-1][1], points[i][0], points[i][1])
	}
	p.ring(ox+12*step, oy+12*step, 3*step, white)
}
func (p painter) fuelPump(x, y float64) {
	ink := color.NRGBA{245, 250, 247, 255}
	s := .7
	line := func(x1, y1, x2, y2 float64) { p.line(x+x1*s, y+y1*s, x+x2*s, y+y2*s, ink) }
	for _, v := range [][4]float64{{3, 22, 3, 4}, {3, 4, 13, 4}, {13, 4, 13, 22}, {1, 22, 15, 22}, {5, 7, 11, 7}, {11, 7, 11, 13}, {11, 13, 5, 13}, {5, 13, 5, 7}, {13, 14, 15, 14}, {15, 14, 15, 18}, {15, 18, 19, 18}, {19, 18, 19, 9}, {19, 9, 15, 5}} {
		line(v[0], v[1], v[2], v[3])
	}
}
