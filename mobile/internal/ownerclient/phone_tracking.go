package ownerclient

import (
	"context"
	"errors"
	"math"
	"time"
)

type PhoneSample struct {
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	ObservedAt     time.Time `json:"observedAt"`
	AccuracyMeters float64   `json:"accuracyMeters"`
}

// PhoneTrackingState lives only inside the existing encrypted owner vault.
type PhoneTrackingState struct {
	Sample      PhoneSample `json:"sample"`
	UploadedAt  time.Time   `json:"uploadedAt"`
	AttemptedAt time.Time   `json:"attemptedAt"`
}

func (s PhoneSample) Valid(now time.Time) bool {
	return !math.IsNaN(s.Latitude+s.Longitude+s.AccuracyMeters) && !math.IsInf(s.Latitude+s.Longitude+s.AccuracyMeters, 0) && math.Abs(s.Latitude) <= 85 && math.Abs(s.Longitude) <= 180 && s.AccuracyMeters >= 0 && s.AccuracyMeters <= 30.48 && !s.ObservedAt.IsZero() && !s.ObservedAt.After(now.Add(time.Minute)) && now.Sub(s.ObservedAt) <= 5*time.Minute
}

func PhoneMoved(a, b PhoneSample) bool {
	lat1, lat2 := a.Latitude*math.Pi/180, b.Latitude*math.Pi/180
	dlat, dlon := lat2-lat1, (b.Longitude-a.Longitude)*math.Pi/180
	h := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 2*6371008.8*math.Asin(math.Sqrt(math.Min(1, h))) > 15.24
}

func PhoneUploadDue(previous PhoneTrackingState, sample PhoneSample, now time.Time) bool {
	if !sample.Valid(now) || (!previous.AttemptedAt.IsZero() && now.Sub(previous.AttemptedAt) < 2*time.Minute) {
		return false
	}
	return previous.UploadedAt.IsZero() || sample.ObservedAt.After(previous.Sample.ObservedAt) && PhoneMoved(previous.Sample, sample)
}

// UploadPhone snapshots/rotates authentication under the shared vault lock.
// The upload itself never holds that lock, so app and widgets remain responsive.
func UploadPhone(ctx context.Context, vault Vault, client *Client, sample PhoneSample, now time.Time) (bool, error) {
	var session Session
	due := false
	err := vault.WithLock(ctx, func() error {
		saved, err := vault.Load()
		if err != nil {
			return err
		}
		if saved.Session.RefreshHandle == "" {
			return ErrUnauthorized
		}
		if !PhoneUploadDue(saved.PhoneTracking, sample, now) {
			return nil
		}
		if !saved.Session.AccessExpiresAt.After(now.Add(time.Minute)) {
			saved.Session, err = client.Refresh(ctx, saved.Session)
			if err != nil {
				return err
			}
		}
		saved.PhoneTracking.AttemptedAt = now
		if err = vault.Save(saved); err != nil {
			return err
		}
		session = saved.Session
		due = true
		return nil
	})
	if err != nil || !due {
		return false, err
	}
	var result struct {
		Accepted bool `json:"accepted"`
	}
	if err = client.Request(ctx, "POST", "/api/location/phone", session.AccessHandle, sample, &result); err != nil {
		return false, err
	}
	if !result.Accepted {
		return false, nil
	}
	err = vault.WithLock(ctx, func() error {
		saved, err := vault.Load()
		if err != nil {
			return err
		}
		if saved.Session.RefreshHandle != session.RefreshHandle {
			return errors.New("Phone tracking session changed")
		}
		saved.PhoneTracking.Sample = sample
		saved.PhoneTracking.UploadedAt = now
		return vault.Save(saved)
	})
	return err == nil, err
}
