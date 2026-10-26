package locationmap

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
)

const phoneCadence = 2 * time.Minute
const phoneLifetime = 5 * time.Minute
const syncRadius = 30.48

type PhoneSample struct {
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	ObservedAt     time.Time `json:"observedAt"`
	AccuracyMeters float64   `json:"accuracyMeters"`
}

type PhoneResult struct {
	Accepted            bool   `json:"accepted"`
	Reason              string `json:"reason,omitempty"`
	Synced              bool   `json:"synced"`
	ObservationRevision string `json:"observationRevision,omitempty"`
}

type Geofence struct {
	Name         string  `json:"name"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	RadiusMeters float64 `json:"radiusMeters"`
}

type GeofenceConfig struct {
	Fences []Geofence `json:"fences"`
}

type phoneLatest struct {
	Observation string      `json:"observation"`
	Sample      PhoneSample `json:"sample"`
	ReceivedAt  time.Time   `json:"receivedAt"`
}

type phoneTracking struct {
	FamilyHash       string `json:"familyHash"`
	Synced           bool   `json:"synced"`
	SyncRevision     string `json:"syncRevision"`
	ComparedRevision string `json:"comparedRevision"`
}

func trackingDocument(ctx context.Context, docs auth.Documents, key string, now time.Time, value any) (auth.Document, error) {
	doc, err := docs.Load(ctx, key)
	if errors.Is(err, auth.ErrMissing) {
		return auth.Document{Key: key}, nil
	}
	if err != nil {
		return doc, err
	}
	if doc.ExpiresAt.After(now) && json.Unmarshal(doc.Data, value) != nil {
		return doc, auth.ErrUnavailable
	}
	return doc, nil
}

func validPhone(sample PhoneSample, now time.Time) bool {
	return validFix(onstar.LocationFix{Latitude: sample.Latitude, Longitude: sample.Longitude}) &&
		!sample.ObservedAt.IsZero() && !sample.ObservedAt.After(now.Add(time.Minute)) &&
		now.Sub(sample.ObservedAt) <= phoneLifetime && sample.AccuracyMeters >= 0 &&
		sample.AccuracyMeters <= syncRadius && !math.IsNaN(sample.AccuracyMeters)
}

func phoneFix(sample PhoneSample) onstar.LocationFix {
	return onstar.LocationFix{Latitude: sample.Latitude, Longitude: sample.Longitude, ReportedAt: &sample.ObservedAt}
}

func phoneMatches(sample PhoneSample, observation Observation) bool {
	if observation.Fix.ReportedAt == nil {
		return false
	}
	// The vehicle is sampled every thirty minutes. Phone freshness is checked
	// independently; a recent phone can synchronize with the latest known fix
	// without waiting for another scheduled vehicle poll.
	return Distance(phoneFix(sample), observation.Fix) <= syncRadius
}

// RecordPhone accepts a family supplied only by authenticated server code.
// Keys contain no coordinates or raw family identifiers. Documents/history use
// the existing encrypted store and commit atomically with the accepted pointer.
func RecordPhone(ctx context.Context, docs auth.Documents, family string, sample PhoneSample, now time.Time) (PhoneResult, error) {
	if family == "" || len(family) > 256 || !validPhone(sample, now) {
		return PhoneResult{}, auth.ErrInvalid
	}
	familyHash := digest(family)
	var previous phoneLatest
	phoneDoc, err := trackingDocument(ctx, docs, "phone-latest#"+familyHash, now, &previous)
	if err != nil {
		return PhoneResult{}, err
	}
	var track phoneTracking
	trackDoc, err := trackingDocument(ctx, docs, "location-tracking", now, &track)
	if err != nil {
		return PhoneResult{}, err
	}
	result := PhoneResult{Synced: track.Synced && track.FamilyHash == familyHash}
	previousTrack := track
	if !previous.Sample.ObservedAt.IsZero() {
		if !sample.ObservedAt.After(previous.Sample.ObservedAt) {
			return result, auth.ErrInvalid
		}
		if now.Sub(previous.ReceivedAt) < phoneCadence {
			result.Reason = "phone-update-too-soon"
			return result, nil
		}
		if Distance(phoneFix(sample), phoneFix(previous.Sample)) <= MatchRadius {
			result.Reason = "phone-not-moved"
		}
	}
	_, raw, rawErr := RawCurrentObservation(ctx, docs, now)
	if rawErr != nil && !errors.Is(rawErr, auth.ErrMissing) {
		return result, rawErr
	}
	// A different handset may take over only after a new vehicle observation
	// independently proves proximity. Matching an old parked fix is not enough.
	if rawErr == nil && phoneMatches(sample, raw) && (track.FamilyHash == familyHash || !track.Synced || track.ComparedRevision != raw.Revision) {
		track = phoneTracking{FamilyHash: familyHash, Synced: true, SyncRevision: raw.Revision, ComparedRevision: raw.Revision}
	} else if rawErr == nil && track.FamilyHash == familyHash && track.ComparedRevision != raw.Revision {
		track.Synced = false
		track.ComparedRevision = raw.Revision
	}
	if result.Reason == "phone-not-moved" {
		// A stationary sample may prove proximity to a new vehicle observation,
		// but it must not write another phone position or refresh its timestamp.
		if track != previousTrack {
			if err = docs.Commit(ctx, encoded(trackDoc, track, now.Add(90*24*time.Hour))); err != nil {
				return result, err
			}
		}
		result.Synced = track.Synced && track.FamilyHash == familyHash
		return result, nil
	}
	id, err := opaqueID()
	if err != nil {
		return result, err
	}
	id = digest(id)
	key := "location-observation#" + id
	observation := Observation{Owner: raw.Owner, Vehicle: raw.Vehicle, Source: "phone", SourceID: familyHash, Revision: id, ReceivedAt: now, Fix: phoneFix(sample), Status: "pending", OriginSource: "phone", OriginRevision: id}
	phoneDoc = encoded(phoneDoc, phoneLatest{Observation: key, Sample: sample, ReceivedAt: now}, now.Add(90*24*time.Hour))
	trackDoc = encoded(trackDoc, track, now.Add(90*24*time.Hour))
	if err = docs.Commit(ctx, encoded(auth.Document{Key: key}, observation, now.Add(90*24*time.Hour)), phoneDoc, trackDoc); err != nil {
		return result, err
	}
	return PhoneResult{Accepted: true, Synced: track.Synced && track.FamilyHash == familyHash, ObservationRevision: id}, nil
}

// TrackObservation selects a fresh synchronized phone observation, then applies
// configured place centers. Original OnStar and phone observations are never
// rewritten; a derived observation gets its own immutable identity and map.
func TrackObservation(ctx context.Context, docs auth.Documents, now time.Time, rawKey string, raw Observation) (string, Observation, error) {
	selectedKey, selected := rawKey, raw
	var track phoneTracking
	trackDoc, err := trackingDocument(ctx, docs, "location-tracking", now, &track)
	if err != nil {
		return "", Observation{}, err
	}
	if track.FamilyHash != "" {
		var latestPhone phoneLatest
		_, err = trackingDocument(ctx, docs, "phone-latest#"+track.FamilyHash, now, &latestPhone)
		if err != nil {
			return "", Observation{}, err
		}
		fresh := validPhone(latestPhone.Sample, now) && !latestPhone.ReceivedAt.After(now) && now.Sub(latestPhone.ReceivedAt) <= phoneLifetime
		if track.ComparedRevision != raw.Revision {
			track.Synced = fresh && phoneMatches(latestPhone.Sample, raw)
			track.ComparedRevision = raw.Revision
			if track.Synced {
				track.SyncRevision = raw.Revision
			}
			if err = docs.Commit(ctx, encoded(trackDoc, track, now.Add(90*24*time.Hour))); err != nil {
				return "", Observation{}, err
			}
		}
		if track.Synced && fresh && latestPhone.Observation != "" {
			doc, loadErr := docs.Load(ctx, latestPhone.Observation)
			if loadErr != nil {
				return "", Observation{}, loadErr
			}
			if !doc.ExpiresAt.After(now) || json.Unmarshal(doc.Data, &selected) != nil || selected.Source != "phone" || selected.SourceID != track.FamilyHash {
				return "", Observation{}, auth.ErrUnavailable
			}
			selectedKey = latestPhone.Observation
		}
	}
	fix, place, err := GeofencedFix(selected.Fix, currentGeofences())
	if err != nil {
		return "", Observation{}, err
	}
	if place == "" && selected.Source == "onstar" && selected.Owner == raw.Owner && selected.Vehicle == raw.Vehicle {
		return selectedKey, selected, nil
	}
	identity, _ := json.Marshal(struct {
		Key, Place, Owner, Vehicle, VehicleRevision string
		Fix                                         onstar.LocationFix
	}{selectedKey, place, raw.Owner, raw.Vehicle, raw.Revision, fix})
	id := digest(string(identity))
	key := "location-observation#" + id
	doc, loadErr := docs.Load(ctx, key)
	if loadErr == nil && doc.ExpiresAt.After(now) {
		var existing Observation
		if json.Unmarshal(doc.Data, &existing) != nil {
			return "", Observation{}, auth.ErrUnavailable
		}
		return key, existing, nil
	}
	if loadErr != nil && !errors.Is(loadErr, auth.ErrMissing) {
		return "", Observation{}, loadErr
	}
	if errors.Is(loadErr, auth.ErrMissing) {
		doc = auth.Document{Key: key}
	}
	derived := selected
	derived.Owner, derived.Vehicle = raw.Owner, raw.Vehicle
	derived.VehicleRevision = raw.Revision
	derived.OriginSource, derived.OriginRevision = selected.Source, selected.Revision
	derived.Revision, derived.Fix, derived.Place, derived.Map = id, fix, place, nil
	derived.Status, derived.Reason = "pending", ""
	if place != "" {
		derived.Source = "geofence"
	}
	if err = docs.Commit(ctx, encoded(doc, derived, now.Add(90*24*time.Hour))); err != nil {
		return "", Observation{}, err
	}
	return key, derived, nil
}

// GeofencedFix uses configured centers only within the requested radii. The
// source timestamp is retained. Configuration comes from LoadGeofences.
func GeofencedFix(fix onstar.LocationFix, config GeofenceConfig) (onstar.LocationFix, string, error) {
	if len(config.Fences) > 2 || !validFix(fix) {
		return fix, "", auth.ErrInvalid
	}
	seen := map[string]bool{}
	selected := -1
	nearest := math.Inf(1)
	for i, fence := range config.Fences {
		if seen[fence.Name] || (fence.Name != "Home" && fence.Name != "Work") || !validFix(onstar.LocationFix{Latitude: fence.Latitude, Longitude: fence.Longitude}) || (fence.Name == "Home" && fence.RadiusMeters != 30.48) || (fence.Name == "Work" && fence.RadiusMeters != 60.96) {
			return fix, "", auth.ErrInvalid
		}
		seen[fence.Name] = true
		distance := Distance(fix, onstar.LocationFix{Latitude: fence.Latitude, Longitude: fence.Longitude})
		if distance <= fence.RadiusMeters && distance < nearest {
			selected, nearest = i, distance
		}
	}
	if selected < 0 {
		return fix, "", nil
	}
	fence := config.Fences[selected]
	fix.Latitude, fix.Longitude = fence.Latitude, fence.Longitude
	return fix, fence.Name, nil
}
