package mobile

import (
	"image/color"
	"math"
	"time"
)

func smooth(value float64) float64 {
	if value <= 0 {
		return 0
	}
	if value >= 1 {
		return 1
	}
	return value * value * (3 - 2*value)
}
func daylightMix(now, rise, set time.Time) (day, twilight float64) {
	if rise.IsZero() || set.IsZero() || !set.After(rise) {
		return 0, 0
	}
	width := 45 * time.Minute
	day = smooth(float64(now.Sub(rise)+width)/float64(2*width)) * (1 - smooth(float64(now.Sub(set)+width)/float64(2*width)))
	near := math.Min(math.Abs(float64(now.Sub(rise))), math.Abs(float64(now.Sub(set))))
	twilight = smooth(1 - near/float64(width))
	return
}
func mixColor(a, b color.NRGBA, t float64) color.NRGBA {
	return color.NRGBA{R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t), G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t), B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t), A: 255}
}
func skyColors(now time.Time, w *Weather) (color.NRGBA, color.NRGBA) {
	nightTop := color.NRGBA{R: 4, G: 15, B: 35, A: 255}
	nightBottom := color.NRGBA{R: 17, G: 35, B: 57, A: 255}
	if w == nil {
		return nightTop, nightBottom
	}
	day, dusk := daylightMix(now, w.Sunrise, w.Sunset)
	top := mixColor(nightTop, color.NRGBA{R: 44, G: 133, B: 205, A: 255}, day)
	bottom := mixColor(nightBottom, color.NRGBA{R: 151, G: 205, B: 230, A: 255}, day)
	top = mixColor(top, color.NRGBA{R: 73, G: 65, B: 111, A: 255}, dusk*.8)
	bottom = mixColor(bottom, color.NRGBA{R: 238, G: 139, B: 91, A: 255}, dusk*.8)
	cloud := w.CloudPercent / 100 * .35
	return mixColor(top, color.NRGBA{R: 69, G: 83, B: 101, A: 255}, cloud), mixColor(bottom, color.NRGBA{R: 105, G: 118, B: 132, A: 255}, cloud)
}
