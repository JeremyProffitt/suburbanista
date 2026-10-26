package homescene

import (
	"image"
	"image/color"
)

// These byte kernels use the same 16-bit alpha arithmetic as image/draw, without
// boxing color.Color for every pixel on NRGBA destinations.
func blendPixel(dst []byte, r, g, b, a uint8) {
	if a == 0 {
		return
	}
	if a == 255 {
		dst[0], dst[1], dst[2], dst[3] = r, g, b, 255
		return
	}
	sa := uint32(a) * 257
	inv := uint32(65535) - sa
	da := uint32(dst[3]) * 257
	alpha := da*inv/65535 + sa
	values := [3]uint8{r, g, b}
	for c := 0; c < 3; c++ {
		s := uint32(values[c]) * 257 * uint32(a) / 255
		d := uint32(dst[c]) * 257 * uint32(dst[3]) / 255
		v := d*inv/65535 + s
		if alpha != 65535 {
			v = v * 65535 / alpha
		}
		dst[c] = uint8(v >> 8)
	}
	dst[3] = uint8(alpha >> 8)
}
func fillPixels(dst *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	r = r.Intersect(dst.Bounds())
	if r.Empty() || c.A == 0 {
		return
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := dst.Pix[dst.PixOffset(r.Min.X, y):]
		for x := 0; x < r.Dx(); x++ {
			blendPixel(row[x*4:x*4+4], c.R, c.G, c.B, c.A)
		}
	}
}
func overlayPixels(dst *image.NRGBA, r image.Rectangle, src *image.NRGBA, origin image.Point) {
	clipped := r.Intersect(dst.Bounds())
	if clipped.Empty() {
		return
	}
	origin = origin.Add(clipped.Min.Sub(r.Min))
	for y := 0; y < clipped.Dy(); y++ {
		d := dst.Pix[dst.PixOffset(clipped.Min.X, clipped.Min.Y+y):]
		s := src.Pix[src.PixOffset(origin.X, origin.Y+y):]
		for x := 0; x < clipped.Dx(); x++ {
			q := s[x*4 : x*4+4]
			blendPixel(d[x*4:x*4+4], q[0], q[1], q[2], q[3])
		}
	}
}
