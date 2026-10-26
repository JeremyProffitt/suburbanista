package homescene

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/vehicleasset"
	xdraw "golang.org/x/image/draw"
)

// Only public packaged artwork is kept here. Private map pixels are never
// retained by this process-wide cache. Byte and entry bounds cap rotation cost.
var prepared struct {
	sync.Mutex
	items map[string]*image.NRGBA
	used  map[string]uint64
	clock uint64
	bytes int
}

const preparedBudget = 32 << 20

func preparedArtwork(key string, build func() *image.NRGBA) *image.NRGBA {
	prepared.Lock()
	defer prepared.Unlock()
	if prepared.items == nil {
		prepared.items = map[string]*image.NRGBA{}
		prepared.used = map[string]uint64{}
	}
	prepared.clock++
	if img := prepared.items[key]; img != nil {
		prepared.used[key] = prepared.clock
		return img
	}
	img := build()
	if img == nil || len(img.Pix) > preparedBudget {
		return img
	}
	for len(prepared.items) >= 12 || prepared.bytes+len(img.Pix) > preparedBudget {
		old := ""
		stamp := ^uint64(0)
		for id, last := range prepared.used {
			if last < stamp {
				old, stamp = id, last
			}
		}
		if old == "" {
			break
		}
		prepared.bytes -= len(prepared.items[old].Pix)
		delete(prepared.items, old)
		delete(prepared.used, old)
	}
	prepared.items[key] = img
	prepared.used[key] = prepared.clock
	prepared.bytes += len(img.Pix)
	return img
}

func backdropLayer(id string, width int) *image.NRGBA {
	return preparedArtwork(fmt.Sprintf("backdrop/%s/%d", id, width), func() *image.NRGBA {
		scale := float64(width) / 360
		layer := image.NewNRGBA(image.Rect(0, 0, width, int(math.Round(300*scale))))
		p := painter{layer, scale, 1}
		p.fill(layer.Bounds(), color.NRGBA{119, 120, 117, 255})
		plate := asset("background/"+id, func() ([]byte, error) { return backgroundFiles.ReadFile("backgrounds/" + id + ".jpg") })
		if plate == nil {
			return nil
		}
		offset := -10.0
		if id == "charlotte" {
			offset = -25
		}
		height := 360 * float64(plate.Bounds().Dy()) / float64(plate.Bounds().Dx())
		xdraw.CatmullRom.Scale(layer, p.rect(0, offset, 360, height), plate, plate.Bounds(), draw.Src, nil)
		return layer
	})
}

func foregroundLayer(in Input, width int, style appearancecore.Style, palette appearancecore.Palette, valid bool) *image.NRGBA {
	key := fmt.Sprintf("foreground/%d/%s/%v/%t/%t/%t/%t", width, style.ID, palette.Accent, in.Allowed["start"], in.Allowed["unlock"], in.Allowed["lock"], in.Night && in.Settings.AutomaticSky)
	return preparedArtwork(key, func() *image.NRGBA {
		scale := float64(width) / 360
		layer := image.NewNRGBA(image.Rect(0, 0, width, int(math.Round(349*scale))))
		p := painter{layer, scale, 1}
		hero := asset("vehicle", func() ([]byte, error) { return vehicleasset.PNG, nil })
		r := p.rect(18, 100, 324, 164)
		p.shadowFit(hero, r, color.NRGBA{0, 0, 0, 153}, 5, 6)
		p.shadowFit(hero, r, color.NRGBA{245, 249, 255, 111}, 7, 0)
		p.fit(hero, r)
		if in.Night && in.Settings.AutomaticSky {
			p.shadowFit(headlights(hero), r, color.NRGBA{245, 246, 228, 180}, 7, 0)
		}
		if valid {
			for i, action := range []string{"start", "unlock", "lock"} {
				img := asset("icon/"+style.ID+"/"+action+fmt.Sprint(palette.Accent), func() ([]byte, error) { return appearancecore.IconPNG(style, action, palette) })
				if !in.Allowed[action] {
					img = artworkOpacity(img, .45)
				}
				center := 16 + (float64(i)+.5)*328/3
				art := p.rect(center-38, 266.5, 76, 76)
				p.shadowFit(img, art, color.NRGBA{225, 232, 242, 179}, 3, 0)
				p.fit(img, art)
			}
		}
		return layer
	})
}

// Renderer is one surface's cache. Its retained base contains no map pixels;
// map gestures only copy that base and compose the current local map image.
type Renderer struct {
	sync.Mutex
	key         sceneKey
	base        Frame
	valid       bool
	mapRevision uint64
	mapSize     image.Point
	mapImage    image.Image
}
type sceneKey struct {
	screenLockVisible, screenLocked                                           bool
	width, height                                                             int
	compact                                                                   bool
	fontScale                                                                 float64
	backdrop, style                                                           string
	palette                                                                   appearancecore.Palette
	autoSky, rotate, night, start, unlock, lock                               bool
	minute, rise, set                                                         int64
	highLow, temperature, rangeText, fuel, odometer, updated, status, weather string
	fuelKnown                                                                 bool
	fuelPercent                                                               float64
}

func normalizeInput(in Input) Input {
	// Presentation only: retain source freshness on the underlying readings.
	in.Fuel = strings.ReplaceAll(in.Fuel, " · stale", "")
	in.Range = strings.ReplaceAll(in.Range, " · stale", "")
	in.Odometer = strings.ReplaceAll(in.Odometer, " · stale", "")
	if in.Settings.Mode == "" {
		in.Settings = appearancecore.Default()
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	if in.FontScale <= 0 {
		in.FontScale = 1
	}
	in.FontScale = math.Min(in.FontScale, 1.6)
	return in
}
func makeSceneKey(in Input, width, height int, compact bool) sceneKey {
	p, _ := in.Settings.PaletteAt(in.Now)
	style, _ := in.Settings.SelectedStyleAt(in.Now)
	k := sceneKey{width: width, height: height, compact: compact, fontScale: in.FontScale, backdrop: in.Settings.BackdropAt(in.Now), style: style.ID, palette: p, autoSky: in.Settings.AutomaticSky, rotate: in.Settings.ButtonRotation.Enabled, night: in.Night, start: in.Allowed["start"], unlock: in.Allowed["unlock"], lock: in.Allowed["lock"], rise: in.Sunrise.Unix(), set: in.Sunset.Unix(), highLow: in.HighLow, temperature: in.Temperature, rangeText: in.Range, fuel: in.Fuel, odometer: in.Odometer, updated: in.Updated, status: in.Status, weather: in.Weather}
	if in.Settings.AutomaticSky {
		k.minute = in.Now.Unix() / 60
	}
	k.screenLockVisible, k.screenLocked = in.ScreenLockVisible, in.ScreenLocked
	if in.FuelPercent != nil {
		k.fuelKnown = true
		k.fuelPercent = *in.FuelPercent
	}
	return k
}
func (r *Renderer) RenderSize(in Input, width, height int, compact bool) Frame {
	in = normalizeInput(in)
	r.Lock()
	defer r.Unlock()
	key := makeSceneKey(in, width, height, compact)
	if !r.valid || r.key != key {
		r.base = renderBase(in, width, height, compact)
		r.key = key
		r.valid = true
	}
	frame := Frame{Image: image.NewNRGBA(r.base.Image.Bounds()), Regions: cloneRegions(r.base.Regions)}
	copy(frame.Image.Pix, r.base.Image.Pix)
	if !compact {
		size := frame.Regions["map"].Size()
		if in.MapRenderer == nil {
			r.mapImage = nil
			r.mapRevision = 0
		} else if in.MapRevision == 0 || r.mapRevision != in.MapRevision || r.mapSize != size {
			r.mapImage = in.MapRenderer(size.X, size.Y)
			r.mapRevision = in.MapRevision
			r.mapSize = size
		}
		if in.MapRenderer != nil {
			in.Map = r.mapImage
			in.MapRenderer = nil
		}
	}
	composeMap(frame, in, compact)
	return frame
}
func cloneRegions(in map[string]image.Rectangle) map[string]image.Rectangle {
	out := make(map[string]image.Rectangle, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
