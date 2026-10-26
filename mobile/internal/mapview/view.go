// Package mapview composes the private cached background and latest position.
// It has no network or vehicle dependencies.
package mapview

import (
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"image"
	"image/color"
	"image/draw"
	"math"
	"time"
)

type Transform struct{ Zoom, X, Y, MaxZoom float64 }

func (t Transform) limit() float64 {
	if t.MaxZoom < 1 || math.IsNaN(t.MaxZoom) || math.IsInf(t.MaxZoom, 0) {
		return 8
	}
	return math.Min(32, t.MaxZoom)
}

// Initial shows the same horizontal ground extent as the former 768-pixel map, while
// retaining the larger surrounding image for zooming out without a request.
func Initial(v contract.MapView, w, h int) Transform {
	z := math.Max(1, math.Min(4, float64(v.Width)/768))
	return (Transform{Zoom: z, MaxZoom: 8 * z}).Recenter(v, w, h)
}

// Reframe preserves the selected map scale when an older small image is
// replaced by the new larger profile. Ordinary refreshes retain user gestures.
func Reframe(t Transform, old, next contract.MapView, w, h int) Transform {
	if t.Zoom <= 0 || old.Width <= 0 || sourceGeoHeight(old) <= 0 {
		return Initial(next, w, h)
	}
	if old.Width == next.Width && old.Height == next.Height && old.Zoom == next.Zoom && sourceGeoHeight(old) == sourceGeoHeight(next) {
		return t
	}
	oldH, nextH := GeoHeight(old, w, h), GeoHeight(next, w, h)
	oldSourceH, nextSourceH := sourceGeoHeight(old), sourceGeoHeight(next)
	if oldH <= 0 || nextH <= 0 || nextSourceH <= 0 {
		return Initial(next, w, h)
	}
	t = t.Clamp(w, oldH)
	ox, oy := world(old.CenterLatitude, old.CenterLongitude, old.Zoom)
	ox += (float64(w)/2-t.X)/t.Zoom*float64(old.Width)/float64(w) - float64(old.Width)/2
	oy += (float64(oldH)/2-t.Y)/t.Zoom*float64(oldSourceH)/float64(oldH) - float64(old.Height)/2
	factor := math.Exp2(next.Zoom - old.Zoom)
	nx, ny := world(next.CenterLatitude, next.CenterLongitude, next.Zoom)
	dx := ox*factor - nx
	span := 256 * math.Exp2(next.Zoom)
	if dx > span/2 {
		dx -= span
	}
	if dx < -span/2 {
		dx += span
	}
	x, y := dx+float64(next.Width)/2, oy*factor-ny+float64(next.Height)/2
	t.Zoom *= float64(next.Width) / float64(old.Width) / factor
	t.MaxZoom = Initial(next, w, h).MaxZoom
	t.X = float64(w)/2 - x/float64(next.Width)*float64(w)*t.Zoom
	t.Y = float64(nextH)/2 - y/float64(nextSourceH)*float64(nextH)*t.Zoom
	return t.Clamp(w, nextH)
}

// The stored projection covers the whole canvas. Its burned-in footer is not
// geographic imagery; it is composed once after the movable geographic region.
func sourceGeoHeight(v contract.MapView) int {
	rows := v.NoticeRows
	if rows == 0 {
		rows = providerNoticeRows
	}
	if rows < 0 || rows > 256 {
		return 0
	}
	return max(0, v.Height-min(rows, v.Height))
}

// GeoHeight is the movable viewport height, excluding the fixed credits band.
// Gesture callers pass this height to Clamp, Pan and Scale.
func GeoHeight(v contract.MapView, w, h int) int { return max(0, h-ViewNoticeHeight(v, w, h)) }

var glowWeights = func() (weights [7][7]float64) {
	for y := -3; y <= 3; y++ {
		for x := -3; x <= 3; x++ {
			weights[y+3][x+3] = math.Exp(-float64(x*x+y*y) / 4)
		}
	}
	return
}()

func world(lat, lon, zoom float64) (float64, float64) {
	scale := 256 * math.Exp2(zoom)
	s := math.Sin(lat * math.Pi / 180)
	return (lon + 180) / 360 * scale, (.5 - math.Log((1+s)/(1-s))/(4*math.Pi)) * scale
}
func Position(v contract.MapView) (float64, float64, error) {
	if v.Width <= 0 || v.Height <= 0 || v.Width > contract.MaxMapDimension || v.Height > contract.MaxMapDimension || v.Zoom < 0 || v.Zoom > 22 || math.IsNaN(v.Zoom) || math.Abs(v.Latitude) > 85.05112878 || math.Abs(v.CenterLatitude) > 85.05112878 || math.Abs(v.Longitude) > 180 || math.Abs(v.CenterLongitude) > 180 || math.IsNaN(v.Latitude+v.Longitude+v.CenterLatitude+v.CenterLongitude) {
		return 0, 0, errors.New("map metadata unavailable")
	}
	x, y := world(v.Latitude, v.Longitude, v.Zoom)
	cx, cy := world(v.CenterLatitude, v.CenterLongitude, v.Zoom)
	dx := x - cx
	span := 256 * math.Exp2(v.Zoom)
	if dx > span/2 {
		dx -= span
	}
	if dx < -span/2 {
		dx += span
	}
	x, y = dx+float64(v.Width)/2, y-cy+float64(v.Height)/2
	if x < 0 || y < 0 || x >= float64(v.Width) || y >= float64(v.Height) {
		return 0, 0, errors.New("position outside cached map")
	}
	return x, y, nil
}
func Valid(v contract.MapView, background image.Image, now time.Time) bool {
	_, _, err := Position(v)
	return err == nil && v.NoticeRows >= 0 && v.NoticeRows <= 256 && v.NoticeRows < v.Height && background != nil && background.Bounds().Dx() == v.Width && background.Bounds().Dy() == v.Height && v.ImageID != "" && v.ImageVersion != "" && v.Attribution != "" && !v.GeneratedAt.IsZero() && !v.GeneratedAt.After(now) && v.ExpiresAt.After(now) && !v.ExpiresAt.After(v.GeneratedAt.Add(30*24*time.Hour))
}

// ObservationLabel never substitutes a cache generation or delivery timestamp
// for the vehicle source's observation time.
func ObservationLabel(v contract.MapView, now time.Time) string {
	if v.ObservedAt.IsZero() || v.ObservedAt.After(now.Add(time.Minute)) {
		return "source time unknown"
	}
	return appearancecore.DisplayTime(v.ObservedAt, now, now.Location())
}
func (t Transform) Clamp(w, h int) Transform {
	if math.IsNaN(t.Zoom) || math.IsInf(t.Zoom, 0) || t.Zoom < 1 {
		t.Zoom = 1
	}
	t.Zoom = math.Min(t.limit(), t.Zoom)
	if math.IsNaN(t.X) || math.IsInf(t.X, 0) {
		t.X = 0
	}
	if math.IsNaN(t.Y) || math.IsInf(t.Y, 0) {
		t.Y = 0
	}
	t.X = math.Max(float64(w)*(1-t.Zoom), math.Min(0, t.X))
	t.Y = math.Max(float64(h)*(1-t.Zoom), math.Min(0, t.Y))
	return t
}
func (t Transform) Pan(dx, dy float64, w, h int) Transform {
	t.X += dx
	t.Y += dy
	return t.Clamp(w, h)
}
func (t Transform) Scale(factor, anchorX, anchorY float64, w, h int) Transform {
	t = t.Clamp(w, h)
	z := math.Max(1, math.Min(t.limit(), t.Zoom*factor))
	ratio := z / t.Zoom
	t.X = anchorX - (anchorX-t.X)*ratio
	t.Y = anchorY - (anchorY-t.Y)*ratio
	t.Zoom = z
	return t.Clamp(w, h)
}
func (t Transform) Recenter(v contract.MapView, w, h int) Transform {
	x, y, err := Position(v)
	geoH, sourceH := GeoHeight(v, w, h), sourceGeoHeight(v)
	if err != nil || geoH <= 0 || sourceH <= 0 {
		return t.Clamp(w, geoH)
	}
	t = t.Clamp(w, geoH)
	t.X = float64(w)/2 - x/float64(v.Width)*float64(w)*t.Zoom
	t.Y = float64(geoH)/2 - y/float64(sourceH)*float64(geoH)*t.Zoom
	return t.Clamp(w, geoH)
}
func (t Transform) Anchor(v contract.MapView, w, h int) (float64, float64, error) {
	x, y, err := Position(v)
	geoH, sourceH := GeoHeight(v, w, h), sourceGeoHeight(v)
	if err != nil || geoH <= 0 || sourceH <= 0 {
		return 0, 0, errors.New("map geography unavailable")
	}
	t = t.Clamp(w, geoH)
	return x/float64(v.Width)*float64(w)*t.Zoom + t.X, y/float64(sourceH)*float64(geoH)*t.Zoom + t.Y, nil
}

// Render keeps the marker 38 reference pixels wide. Attribution belongs to the
// shared scene's fixed overlay and is never transformed with the map pixels.
func Render(v contract.MapView, background, marker image.Image, w, h int, t Transform) image.Image {
	if w < 1 || h < 1 || w > 4096 || h > 4096 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	if background == nil {
		return out
	}
	// Retain every column of the supplied notice strip after all map/marker
	// composition, including early returns. No provider name/year is invented.
	if t.Zoom == 0 {
		t = Initial(v, w, h)
	}
	geoH, sourceH := GeoHeight(v, w, h), sourceGeoHeight(v)
	t = t.Clamp(w, geoH)
	b := background.Bounds()
	source := raster(background)
	defer retainProviderNotice(out, source, v.NoticeRows)
	if geoH <= 0 || sourceH <= 0 {
		return out
	}
	xOffsets := make([]int, w)
	for x := range xOffsets {
		xOffsets[x] = min(b.Dx()-1, max(0, int((float64(x)-t.X)/t.Zoom*float64(b.Dx())/float64(w)))) * 4
	}
	for y := 0; y < geoH; y++ {
		sy := int((float64(y) - t.Y) / t.Zoom * float64(sourceH) / float64(geoH))
		row := source.Pix[sy*source.Stride:]
		dst := out.Pix[y*out.Stride:]
		for x := 0; x < w; x++ {
			copy(dst[x*4:x*4+4], row[xOffsets[x]:xOffsets[x]+4])
		}
	}
	ax, ay, err := t.Anchor(v, w, h)
	if err != nil || marker == nil {
		return out
	}
	m := marker.Bounds()
	mw := int(math.Max(1, 38*float64(w)/360))
	mh := int(math.Max(1, float64(mw)*float64(m.Dy())/float64(m.Dx())))
	mini := image.NewNRGBA(image.Rect(0, 0, mw, mh))
	markerPixels := raster(marker)
	for y := 0; y < mh; y++ {
		row := markerPixels.Pix[y*m.Dy()/mh*markerPixels.Stride:]
		for x := 0; x < mw; x++ {
			sx := x * m.Dx() / mw * 4
			copy(mini.Pix[y*mini.Stride+x*4:y*mini.Stride+x*4+4], row[sx:sx+4])
		}
	}
	r := image.Rect(int(ax)-mw/2, int(ay)-mh/2, int(ax)-mw/2+mw, int(ay)-mh/2+mh)
	// A small neutral glow improves contrast without moving the geographic
	// anchor or changing the marker's size as map pixels are magnified.
	glow := image.NewNRGBA(image.Rect(0, 0, mw+6, mh+6))
	for y := 0; y < mh+6; y++ {
		for x := 0; x < mw+6; x++ {
			var sum, weight float64
			for dy := -3; dy <= 3; dy++ {
				for dx := -3; dx <= 3; dx++ {
					w := glowWeights[dy+3][dx+3]
					sx, sy := x-3+dx, y-3+dy
					if sx >= 0 && sy >= 0 && sx < mw && sy < mh {
						sum += float64(mini.Pix[sy*mini.Stride+sx*4+3]) * w
					}
					weight += w
				}
			}
			glow.SetNRGBA(x, y, color.NRGBA{R: 220, G: 226, B: 236, A: uint8(sum / weight * .65)})
		}
	}
	draw.Draw(out, r.Inset(-3), glow, image.Point{}, draw.Over)
	draw.Draw(out, r, mini, image.Point{}, draw.Over)
	return out
}

// Normalize once per call when a decoder supplied premultiplied pixels. The
// sample loops use byte rows, not image.Image.At/color.Color heap boxing.
func raster(img image.Image) *image.NRGBA {
	if pixels, ok := img.(*image.NRGBA); ok {
		return pixels
	}
	b := img.Bounds()
	pixels := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(pixels, pixels.Bounds(), img, b.Min, draw.Src)
	return pixels
}

// The static-map provider places its copyright and scale in the bottom 20
// pixels. Retain that band, not the additional scenery above it: freezing 64
// rows made the bottom of the map look like a second, unzoomed map.
const providerNoticeRows = 20

// NoticeHeight preserves the provider text band's aspect ratio at the target
// width. It is independent of the user's zoom and pan transform.
func NoticeHeight(sourceWidth, sourceHeight, w, h int) int {
	return noticeHeight(sourceWidth, sourceHeight, w, h, providerNoticeRows)
}
func ViewNoticeHeight(v contract.MapView, w, h int) int {
	return noticeHeight(v.Width, v.Height, w, h, v.NoticeRows)
}
func noticeHeight(sourceWidth, sourceHeight, w, h, rows int) int {
	if rows < 0 || rows > 256 {
		return 0
	}
	if rows == 0 {
		rows = providerNoticeRows
	}
	if sourceWidth <= 0 || sourceHeight <= 0 || w <= 0 || h <= 0 {
		return 0
	}
	return min(h, max(1, int(math.Ceil(float64(min(rows, sourceHeight))*float64(w)/float64(sourceWidth)))))
}
func retainProviderNotice(out *image.NRGBA, source image.Image, rows int) {
	if rows == 0 {
		rows = providerNoticeRows
	}
	b := source.Bounds()
	height := noticeHeight(b.Dx(), b.Dy(), out.Bounds().Dx(), out.Bounds().Dy(), rows)
	if height == 0 {
		return
	}
	top := b.Max.Y - min(rows, b.Dy())
	pixels := raster(source)
	xOffsets := make([]int, out.Bounds().Dx())
	for x := range xOffsets {
		xOffsets[x] = x * b.Dx() / out.Bounds().Dx() * 4
	}
	for y := 0; y < height; y++ {
		sy := top + y*(b.Max.Y-top)/height
		row := pixels.Pix[(sy-b.Min.Y)*pixels.Stride:]
		dst := out.Pix[(out.Bounds().Dy()-height+y)*out.Stride:]
		for x := 0; x < out.Bounds().Dx(); x++ {
			copy(dst[x*4:x*4+4], row[xOffsets[x]:xOffsets[x]+4])
		}
	}
}
