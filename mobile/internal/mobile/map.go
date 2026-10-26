package mobile

import (
	"bytes"
	"context"
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/mapview"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/vehicleasset"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"image"
	"image/png"
	"time"
)

func (s *Screen) refreshCachedMap(ctx context.Context) {
	if ctx.Err() != nil || s.preview.Load() || s.home == nil || s.saved.Session.AccessHandle == "" {
		return
	}
	// Screen.run owns a vault transaction here. Queue the complete image load;
	// the worker takes only a short session snapshot after this transaction ends.
	s.pollMap(s.locationRefresh.mapGeneration.Load(), true, false)
}

func (s *Screen) acceptMap(view contract.MapView, img image.Image) {
	height := 246
	if s.home != nil && s.home.mapPanel != nil {
		size := s.home.mapPanel.Size()
		if size.Width > 0 {
			height = int(size.Height/size.Width*360 + .5)
		}
	}
	s.mapTransform = mapview.Reframe(s.mapTransform, s.mapView, view, 360, height)
	s.mapView, s.vehicleMapImage = view, img
}

// Poll outside Screen.run: its vault transaction must finish before any wait.
// A foreground exit, Preview or sign-out uses pauseLocationRefresh to cancel.
func (s *Screen) pollPendingMap(generation uint64) {
	s.pollMap(generation, false, false)
}

func (s *Screen) visibleMapReady() bool {
	return s.foreground.Load() && !s.preview.Load() && s.home != nil && s.client != nil && s.saved.Session.RefreshHandle != ""
}

func (s *Screen) requestVisibleMap() {
	if s.visibleMapReady() {
		s.pollMap(s.locationRefresh.mapGeneration.Load(), true, true)
	}
}

func (s *Screen) refreshMapOnClock(minuteTick uint64) {
	if minuteTick%2 == 0 {
		s.requestVisibleMap()
	}
}

func (s *Screen) pollMap(generation uint64, immediate, foregroundOnly bool) {
	if s.preview.Load() || !s.locationRefresh.mapBusy.CompareAndSwap(false, true) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	s.locationRefresh.Lock()
	if generation != s.locationRefresh.mapGeneration.Load() {
		s.locationRefresh.Unlock()
		cancel()
		s.locationRefresh.mapBusy.Store(false)
		return
	}
	s.locationRefresh.mapCancel = cancel
	s.locationRefresh.Unlock()
	go func() {
		defer func() {
			cancel()
			s.locationRefresh.Lock()
			s.locationRefresh.mapCancel = nil
			s.locationRefresh.Unlock()
			s.locationRefresh.mapBusy.Store(false)
		}()
		wait := s.locationRefresh.mapWait
		if wait == nil {
			wait = func(ctx context.Context) bool {
				timer := time.NewTimer(10 * time.Second)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return false
				case <-timer.C:
					return true
				}
			}
		}
		first := immediate
		for first || wait(ctx) {
			first = false
			if ctx.Err() != nil || foregroundOnly && !s.visibleMapReady() {
				return
			}
			session, _, err := s.locationSession(ctx, false, false)
			if err != nil {
				return
			}
			view, img, err := s.client.MapView(ctx, session)
			if errors.Is(err, ErrUnauthorized) {
				if _, _, err = s.locationSession(ctx, false, true); err != nil {
					return
				}
				continue
			}
			if err != nil {
				continue
			}
			if ctx.Err() == nil && generation == s.locationRefresh.mapGeneration.Load() {
				s.applyRefreshedLocation(view, img)
			}
			return
		}
	}()
}

var mapMarker = func() image.Image { img, _ := png.Decode(bytes.NewReader(vehicleasset.MarkerPNG)); return img }()

func (s *Screen) renderCachedMap(w, h int) image.Image {
	if s.preview.Load() || !mapview.Valid(s.mapView, s.vehicleMapImage, time.Now()) {
		return nil
	}
	transform := s.mapTransform
	transform.X *= float64(w) / 360
	// Credits round to physical pixels independently of the geographic area.
	// Scale the geographic offset by its actual viewport, not the full frame.
	referenceHeight := int(float64(h)*360/float64(w) + .5)
	referenceGeo := mapview.GeoHeight(s.mapView, 360, referenceHeight)
	if referenceGeo > 0 {
		transform.Y *= float64(mapview.GeoHeight(s.mapView, w, h)) / float64(referenceGeo)
	}
	return mapview.Render(s.mapView, s.vehicleMapImage, mapMarker, w, h, transform)
}
func (s *Screen) recenterMap() {
	// Fetch the latest map even when the previous image is missing or expired.
	// This joins any pending read and does not request a vehicle wake.
	s.requestVisibleMap()
	if s.preview.Load() || !mapview.Valid(s.mapView, s.vehicleMapImage, time.Now()) {
		return
	}
	height := 246
	if s.home != nil && s.home.mapPanel != nil {
		size := s.home.mapPanel.Size()
		if size.Width > 0 {
			height = int(size.Height/size.Width*360 + .5)
		}
	}
	s.mapTransform = s.mapTransform.Recenter(s.mapView, 360, height)
	s.paintHome()
}
