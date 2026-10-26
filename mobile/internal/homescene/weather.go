package homescene

import (
	"image"
	"image/color"
	"math"
	"strings"
	"time"
)

func smooth(v float64) float64 { v = math.Max(0, math.Min(1, v)); return v * v * (3 - 2*v) }
func (p painter) solar(r image.Rectangle, now, rise, set time.Time) bool {
	if rise.IsZero() || set.IsZero() || !set.After(rise) || now.Before(rise.Add(-12*time.Hour)) || now.After(set.Add(12*time.Hour)) {
		return false
	}
	width := 45 * time.Minute
	day := smooth(float64(now.Sub(rise)+width)/float64(2*width)) * (1 - smooth(float64(now.Sub(set)+width)/float64(2*width)))
	near := math.Min(math.Abs(float64(now.Sub(rise))), math.Abs(float64(now.Sub(set))))
	twilight := smooth(1 - near/float64(width))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		f := float64(y-r.Min.Y) / float64(r.Dy())
		nightAlpha := uint8((1 - day) * (140 - 40*f))
		p.fill(image.Rect(r.Min.X, y, r.Max.X, y+1), color.NRGBA{8, 16, 40, nightAlpha})
		p.fill(image.Rect(r.Min.X, y, r.Max.X, y+1), color.NRGBA{170, 88, 72, uint8(twilight * (30 + 35*f))})
	}
	return true
}

var Conditions = []string{"Dawn", "Sunrise", "Clear daylight", "Overcast", "Fog", "Rain", "Thunderstorm", "Snow", "Sunset", "Blue hour", "Night"}

func (p painter) weather(r image.Rectangle, condition string, night bool) {
	key := strings.ToLower(condition)
	tint := color.NRGBA{}
	switch key {
	case "dawn", "sunrise", "sunset":
		tint = color.NRGBA{140, 66, 70, 65}
	case "blue hour":
		tint = color.NRGBA{20, 37, 105, 95}
	case "overcast":
		tint = color.NRGBA{62, 74, 90, 55}
	case "fog":
		tint = color.NRGBA{215, 224, 225, 105}
	case "rain":
		tint = color.NRGBA{38, 58, 85, 65}
	case "thunderstorm", "thunder":
		tint = color.NRGBA{26, 35, 60, 100}
	case "snow":
		tint = color.NRGBA{193, 212, 225, 40}
	}
	if night || key == "night" {
		p.fill(r, color.NRGBA{8, 16, 40, 130})
	}
	p.fill(r, tint)
	if key == "overcast" || key == "thunderstorm" || key == "thunder" {
		for _, v := range [][4]float64{{42, 65, 55, 13}, {95, 72, 55, 17}, {235, 68, 66, 17}, {310, 84, 65, 20}} {
			p.ellipse(v[0], v[1], v[2], v[3], color.NRGBA{205, 215, 224, 100})
		}
	}
	if key == "rain" || key == "thunderstorm" || key == "thunder" {
		for i := 0; i < 85; i++ {
			x := float64((i * 47) % 360)
			y := float64(45 + (i*61)%285)
			p.line(x, y, x-3, y+9, color.NRGBA{190, 212, 235, 41})
		}
	}
	if key == "snow" {
		for i := 0; i < 95; i++ {
			x := float64((i * 47) % 360)
			y := float64(47 + (i*61)%290)
			p.ellipse(x, y, 1.1, 1.1, color.NRGBA{242, 248, 255, 180})
		}
	}
}
func (p painter) weatherIcon(condition string, night bool, x, y float64) {
	if condition == "" {
		p.text("?", x, y-18, 30, "center")
		return
	}
	key := strings.ToLower(condition)
	if key == "partly cloudy" {
		if night {
			p.moon(x+8, y-10, 10.5)
		} else {
			p.ellipse(x+8, y-10, 10.5, 10.5, color.NRGBA{255, 204, 84, 255})
		}
		cloud := color.NRGBA{236, 245, 252, 255}
		p.ellipse(x-10, y+5, 15, 9, cloud)
		p.ellipse(x+1, y, 13, 13, cloud)
		p.ellipse(x+13, y+7, 9, 8, cloud)
		return
	}
	p.ellipse(x, y, 16, 16, color.NRGBA{220, 230, 240, 25})
	sun := color.NRGBA{255, 208, 90, 255}
	if night || key == "night" {
		sun = color.NRGBA{221, 236, 255, 255}
	}
	if night || key == "night" {
		p.moon(x, y-4, 12)
	} else {
		p.ellipse(x, y-4, 12, 12, sun)
	}
	if key == "overcast" || key == "rain" || key == "snow" || key == "thunderstorm" || key == "thunder" || key == "fog" {
		p.ellipse(x-5, y+4, 15, 7, color.NRGBA{220, 231, 240, 255})
		p.ellipse(x+6, y+1, 10, 9, color.NRGBA{220, 231, 240, 255})
	}
	if key == "snow" {
		for _, dx := range []float64{-8, 0, 8} {
			p.line(x+dx, y+14, x+dx, y+21, color.NRGBA{240, 250, 255, 255})
			p.line(x+dx-3, y+17, x+dx+3, y+17, color.NRGBA{240, 250, 255, 255})
		}
	} else if key == "rain" || key == "thunderstorm" || key == "thunder" {
		for _, dx := range []float64{-8, 0, 8} {
			p.line(x+dx, y+14, x+dx-3, y+20, color.NRGBA{100, 190, 250, 255})
		}
	}
}

func (p painter) moon(x, y, radius float64) {
	r := p.rect(x-radius, y-radius, 2*radius, 2*radius).Intersect(p.dst.Bounds())
	for yy := r.Min.Y; yy < r.Max.Y; yy++ {
		for xx := r.Min.X; xx < r.Max.X; xx++ {
			px, py := float64(xx)/p.scale, float64(yy)/p.scale
			outer := (px-x)*(px-x)+(py-y)*(py-y) <= radius*radius
			inner := (px-x-radius*.45)*(px-x-radius*.45)+(py-y+radius*.35)*(py-y+radius*.35) < radius*radius
			if outer && !inner {
				p.fill(image.Rect(xx, yy, xx+1, yy+1), color.NRGBA{221, 236, 255, 255})
			}
		}
	}
}

// Preview is driven only by the app's foreground clock, never by a request.
// Widgets render the actual condition and do not intercept launcher holds.
type Preview struct {
	pressed, started time.Time
	prior            string
	active           bool
}

func (p *Preview) Active() bool { return p.active }
func (p *Preview) Press(now time.Time, current string) {
	if p.active {
		return
	}
	p.pressed = now
	p.prior = current
}
func (p *Preview) Release() {
	if !p.active {
		p.pressed = time.Time{}
	}
}
func (p *Preview) Cancel() { *p = Preview{} }
func (p *Preview) Condition(now time.Time, actual string) (string, bool) {
	if !p.active && !p.pressed.IsZero() && now.Sub(p.pressed) >= 15*time.Second {
		p.started = p.pressed.Add(15 * time.Second)
		p.active = true
	}
	if !p.active {
		return actual, false
	}
	index := int(now.Sub(p.started) / (5 * time.Second))
	if index >= len(Conditions) {
		prior := p.prior
		p.Cancel()
		return prior, false
	}
	return Conditions[max(index, 0)], true
}
