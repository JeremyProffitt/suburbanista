package mobile

import (
	"context"
	"errors"
	"image"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/ownerclient"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

type locationRefreshUI struct {
	sync.Mutex
	cancel        context.CancelFunc
	busy          atomic.Bool
	started       atomic.Int64
	generation    atomic.Uint64
	wait          func(context.Context) bool
	mapCancel     context.CancelFunc
	mapBusy       atomic.Bool
	mapGeneration atomic.Uint64
	mapWait       func(context.Context) bool
}

func (s *Screen) sceneRefreshState() (bool, float64) {
	if !s.locationRefresh.busy.Load() {
		return false, 0
	}
	return true, float64((time.Now().UnixNano()-s.locationRefresh.started.Load())%int64(time.Second)) / float64(time.Second) * 2 * math.Pi
}
func (s *Screen) requestLocationRefresh() {
	if s.preview.Load() {
		s.showPagedInfo("Location refresh", []string{"Return to Live to refresh the vehicle location."})
		return
	}
	s.startLocationRefresh(true)
}
func (s *Screen) pauseLocationRefresh() {
	s.locationRefresh.Lock()
	cancel := s.locationRefresh.cancel
	mapCancel := s.locationRefresh.mapCancel
	s.locationRefresh.mapGeneration.Add(1)
	s.locationRefresh.Unlock()
	if cancel != nil {
		cancel()
	}
	if mapCancel != nil {
		mapCancel()
	}
}
func (s *Screen) resumeLocationRefresh() { s.startLocationRefresh(false) }

// Lock only the encrypted session snapshot/update, never polling or waiting.
func (s *Screen) locationSession(ctx context.Context, create, forceRefresh bool) (Session, string, error) {
	var session Session
	var id string
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	err := s.vault.WithLock(ctx, func() error {
		saved, err := s.vault.Load()
		if err != nil {
			return err
		}
		if saved.Session.RefreshHandle == "" {
			return ErrUnauthorized
		}
		changed := false
		if forceRefresh || time.Until(saved.Session.AccessExpiresAt) < time.Minute {
			saved.Session, err = s.client.Refresh(ctx, saved.Session)
			if err != nil {
				return err
			}
			changed = true
		}
		if create && saved.LocationRefreshID == "" {
			saved.LocationRefreshID, _, err = NewVerifier()
			if err != nil {
				return err
			}
			changed = true
		}
		if changed {
			if err = s.vault.Save(saved); err != nil {
				return err
			}
		}
		session, id = saved.Session, saved.LocationRefreshID
		return nil
	})
	return session, id, err
}
func (s *Screen) clearLocationRefresh(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.vault.WithLock(ctx, func() error {
		saved, err := s.vault.Load()
		if err != nil {
			return err
		}
		if saved.LocationRefreshID != id {
			return nil
		}
		saved.LocationRefreshID = ""
		return s.vault.Save(saved)
	})
}
func (s *Screen) startLocationRefresh(explicit bool) {
	if s.preview.Load() || s.client == nil || !s.locationRefresh.busy.CompareAndSwap(false, true) {
		return
	}
	s.locationRefresh.started.Store(time.Now().UnixNano())
	generation := s.locationRefresh.generation.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	s.locationRefresh.Lock()
	s.locationRefresh.cancel = cancel
	s.locationRefresh.Unlock()
	go s.animateLocationRefresh(ctx, generation)
	go func() {
		defer func() {
			cancel()
			s.locationRefresh.Lock()
			s.locationRefresh.cancel = nil
			s.locationRefresh.Unlock()
			s.locationRefresh.busy.Store(false)
			fyne.Do(func() {
				if s.home != nil {
					s.refreshSceneInput(time.Now())
					s.home.sky.Refresh()
				}
			})
		}()
		session, id, err := s.locationSession(ctx, explicit, false)
		if err != nil {
			if explicit {
				s.locationRefreshError("Sign in to refresh the vehicle location.")
			}
			return
		}
		if id == "" {
			return
		}
		if explicit {
			_, err = s.client.RequestLocationRefresh(ctx, session, id)
			if errors.Is(err, ErrUnauthorized) {
				session, _, err = s.locationSession(ctx, false, true)
				if err == nil {
					_, err = s.client.RequestLocationRefresh(ctx, session, id)
				}
			}
			var rejected *ownerclient.RequestError
			if errors.As(err, &rejected) && (rejected.StatusCode == 400 || rejected.StatusCode == 403 || rejected.StatusCode == 409 || rejected.StatusCode == 429) {
				s.clearLocationRefresh(id)
				s.locationRefreshError("Location refresh is not available yet. Please wait briefly and try again.")
				return
			}
			if errors.Is(err, ErrUnauthorized) {
				s.locationRefreshError("Sign in to refresh the vehicle location.")
				return
			}
			// An ambiguous POST retains its ID. Status reads and an explicit retry use
			// this same durable ID, so a lost response cannot cause a second wake.
		}
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		wait := s.locationRefresh.wait
		if wait == nil {
			wait = func(ctx context.Context) bool {
				select {
				case <-ctx.Done():
					return false
				case <-ticker.C:
					return true
				}
			}
		}
		retriedMissing := false
		for {
			if !wait(ctx) {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					s.locationRefreshError("Location update is taking longer than expected. Tap Last Updated to check the same request.")
				}
				return
			}
			session, _, err = s.locationSession(ctx, false, false)
			if err != nil {
				if errors.Is(err, ErrUnauthorized) {
					s.locationRefreshError("Sign in to check the location update.")
					return
				}
				continue
			}
			pollCtx, done := context.WithTimeout(ctx, 8*time.Second)
			status, statusErr := s.client.LocationRefreshStatus(pollCtx, session, id)
			done()
			if errors.Is(statusErr, ErrUnauthorized) {
				_, _, _ = s.locationSession(ctx, false, true)
				continue
			}
			if statusErr != nil {
				var missing *ownerclient.RequestError
				if errors.As(statusErr, &missing) && missing.StatusCode == 404 {
					if explicit && !retriedMissing {
						retriedMissing = true
						_, _ = s.client.RequestLocationRefresh(ctx, session, id)
						continue
					}
					s.locationRefreshError("No location request was found. Tap Last Updated to retry the same request.")
					return
				}
				continue
			}
			if status.RequestID != id {
				continue
			}
			if status.State == "failed" {
				s.clearLocationRefresh(id)
				if status.Reason == "refresh-superseded" {
					view, img, e := s.client.MapView(ctx, session)
					if errors.Is(e, ownerclient.ErrMapPending) {
						s.pollPendingMap(s.locationRefresh.mapGeneration.Load())
						return
					}
					if e == nil && view.ReceivedAt.After(status.RequestedAt) {
						s.applyRefreshedLocation(view, img)
						return
					}
				}
				s.locationRefreshError("Location refresh could not complete: " + refreshReason(status.Reason))
				return
			}
			if status.State == "resolving" {
				// Image generation is demand-only. A status poll never requests it.
				// This GET starts or joins generation without another vehicle wake.
				view, img, mapErr := s.client.MapView(ctx, session)
				if mapErr == nil && refreshObservationMatches(status, view) {
					s.clearLocationRefresh(id)
					s.applyRefreshedLocation(view, img)
					return
				}
				continue
			}
			if status.State != "ready" {
				continue
			}
			view, img, readErr := s.client.MapView(ctx, session)
			if readErr != nil || !refreshImageMatches(status, view) {
				continue
			}
			s.clearLocationRefresh(id)
			s.applyRefreshedLocation(view, img)
			return
		}
	}()
}
func refreshImageMatches(status contract.LocationRefreshStatus, view contract.MapView) bool {
	return status.State == "ready" && refreshObservationMatches(status, view) && status.ImageID == view.ImageID && status.ImageVersion == view.ImageVersion
}

func refreshObservationMatches(status contract.LocationRefreshStatus, view contract.MapView) bool {
	return status.ObservationRevision != "" && (view.VehicleRevision == status.ObservationRevision || view.ObservationRevision == status.ObservationRevision)
}
func refreshReason(reason string) string {
	switch reason {
	case "vehicle-busy":
		return "the vehicle is busy. Try again shortly."
	case "refresh-superseded":
		return "a newer location update took over."
	case "refresh-timeout":
		return "the server did not finish in time."
	case "provider-paused":
		return "vehicle access is paused."
	default:
		return "the server reported an unavailable location."
	}
}
func (s *Screen) locationRefreshError(message string) {
	fyne.Do(func() {
		if !s.preview.Load() {
			s.showPagedInfo("Location refresh", []string{message})
		}
	})
}
func (s *Screen) applyRefreshedLocation(view contract.MapView, img image.Image) {
	generation := s.locationRefresh.mapGeneration.Load()
	fyne.Do(func() {
		if generation != s.locationRefresh.mapGeneration.Load() || s.preview.Load() || s.saved.Session.RefreshHandle == "" {
			return
		}
		if !s.mapView.ReceivedAt.IsZero() && view.ReceivedAt.Before(s.mapView.ReceivedAt) {
			return
		}
		s.acceptMap(view, img)
		s.paintHome()
	})
}
func (s *Screen) animateLocationRefresh(ctx context.Context, generation uint64) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var pending atomic.Bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !pending.CompareAndSwap(false, true) {
				continue
			}
			fyne.Do(func() {
				defer pending.Store(false)
				if s.home == nil || s.locationRefresh.generation.Load() != generation {
					return
				}
				s.refreshSceneInput(time.Now())
				s.home.sky.Refresh()
			})
		}
	}
}
