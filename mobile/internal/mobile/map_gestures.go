package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	touch "fyne.io/fyne/v2/driver/mobile"
	"fyne.io/fyne/v2/widget"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/mapview"
	"image/color"
	"math"
)

// mapInteraction only transforms already fetched pixels. Multi-touch uses the
// pinned framework's finger IDs; it never reads location or calls a client.
type mapInteraction struct {
	widget.BaseWidget
	screen  *Screen
	fingers map[int]fyne.Position
}

func (s *Screen) newMapInteraction() fyne.CanvasObject {
	m := &mapInteraction{screen: s, fingers: map[int]fyne.Position{}}
	m.ExtendBaseWidget(m)
	return m
}
func (m *mapInteraction) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}
func (m *mapInteraction) TouchDown(e *touch.TouchEvent) {
	m.screen.cancelCommandTouches()
	// The driver can omit a secondary cancel outside this widget. A new
	// primary gesture must never inherit a stale secondary finger.
	if e.ID == 0 {
		clear(m.fingers)
	}
	m.fingers[e.ID] = e.Position
}
func (m *mapInteraction) TouchUp(e *touch.TouchEvent)     { delete(m.fingers, e.ID) }
func (m *mapInteraction) TouchCancel(e *touch.TouchEvent) { delete(m.fingers, e.ID) }
func (m *mapInteraction) TouchMoved(e *touch.TouchEvent) {
	previous, ok := m.fingers[e.ID]
	if !ok {
		return
	}
	m.fingers[e.ID] = e.Position
	if len(m.fingers) == 1 {
		m.pan(e.Position.X-previous.X, e.Position.Y-previous.Y)
		return
	}
	if len(m.fingers) != 2 {
		return
	}
	for id, other := range m.fingers {
		if id == e.ID {
			continue
		}
		before := math.Hypot(float64(previous.X-other.X), float64(previous.Y-other.Y))
		after := math.Hypot(float64(e.Position.X-other.X), float64(e.Position.Y-other.Y))
		if before > 1 {
			m.scale(after/before, (other.X+previous.X)/2, (other.Y+previous.Y)/2)
			m.pan((e.Position.X-previous.X)/2, (e.Position.Y-previous.Y)/2)
		}
	}
}
func (m *mapInteraction) Scrolled(e *fyne.ScrollEvent) {
	m.scale(math.Exp(float64(e.Scrolled.DY)/200), e.Position.X, e.Position.Y)
}
func (m *mapInteraction) pan(dx, dy float32) {
	size := m.Size()
	if size.Width <= 0 || size.Height <= 0 {
		return
	}
	m.screen.mapTransform = m.screen.mapTransform.Pan(float64(dx/size.Width*360), float64(dy/size.Width*360), 360, mapview.GeoHeight(m.screen.mapView, 360, int(size.Height/size.Width*360+.5)))
	m.screen.refreshMapView()
}
func (m *mapInteraction) scale(f float64, x, y float32) {
	size := m.Size()
	if size.Width <= 0 || size.Height <= 0 {
		return
	}
	m.screen.mapTransform = m.screen.mapTransform.Scale(f, float64(x/size.Width*360), float64(y/size.Width*360), 360, mapview.GeoHeight(m.screen.mapView, 360, int(size.Height/size.Width*360+.5)))
	m.screen.refreshMapView()
}
