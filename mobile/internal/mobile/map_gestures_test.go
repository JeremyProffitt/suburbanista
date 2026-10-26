package mobile

import (
	"fyne.io/fyne/v2"
	touch "fyne.io/fyne/v2/driver/mobile"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/mapview"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"testing"
)

func TestMapGesturesAreLocalAndUseFingerIDs(t *testing.T) {
	s := &Screen{mapTransform: mapview.Transform{Zoom: 2, X: -180, Y: -123}, quickButtons: map[string]*iconAction{"start": {}, "lock": {}, "unlock": {}}}
	s.quickButtons["start"].activationTouch.begin(0) // canceled by OS without TouchCancel
	m := s.newMapInteraction().(*mapInteraction)
	m.Resize(fyne.NewSize(360, 246))
	point := func(id int, x, y float32) *touch.TouchEvent {
		return &touch.TouchEvent{ID: id, PointEvent: fyne.PointEvent{Position: fyne.NewPos(x, y)}}
	}
	m.TouchDown(point(4, 120, 80))
	s.quickButtons["start"].activationTouch.end(0)
	if s.quickButtons["start"].activationTouch.accept() {
		t.Fatal("new map gesture retained canceled command touch")
	}
	m.TouchMoved(point(4, 130, 90))
	if s.mapTransform.X != -170 || s.mapTransform.Y != -113 {
		t.Fatal("drag did not move cached pixels")
	}
	m.TouchDown(point(9, 240, 90))
	before := s.mapTransform.Zoom
	m.TouchMoved(point(9, 260, 90))
	if s.mapTransform.Zoom <= before {
		t.Fatal("two finger pinch did not zoom")
	}
	m.TouchCancel(point(4, 130, 90))
	m.TouchUp(point(9, 260, 90))
	if len(m.fingers) != 0 {
		t.Fatal("cancelled finger retained")
	}
	// No client, session or provider is supplied: gestures must remain local.
	m.TouchDown(point(9, 260, 90))
	m.TouchDown(point(0, 120, 80))
	if len(m.fingers) != 1 {
		t.Fatal("stale secondary finger survived a fresh gesture")
	}
}

func TestMapGestureBoundsExcludeFixedCredits(t *testing.T) {
	s := &Screen{mapView: contract.MapView{Width: 3072, Height: 2048, NoticeRows: 192}, mapTransform: mapview.Transform{Zoom: 4}}
	m := s.newMapInteraction().(*mapInteraction)
	m.Resize(fyne.NewSize(360, 326))
	m.pan(0, -10000)
	geo := mapview.GeoHeight(s.mapView, 360, 326)
	if s.mapTransform.Y != float64(geo)*(1-s.mapTransform.Zoom) {
		t.Fatal("gesture pan included credits in map bounds")
	}
	m.scale(.25, 180, float32(geo)/2)
	if s.mapTransform.Zoom != 1 || s.mapTransform.X != 0 || s.mapTransform.Y != 0 {
		t.Fatal("zoom-out did not reach full geographic bounds")
	}
}
