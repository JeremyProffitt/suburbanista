package locationmap

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
)

// CurrentObservation is the shared pointer used by map delivery and demand.
// Collecting a position does not itself request an image.
func CurrentObservation(ctx context.Context, docs auth.Documents, now time.Time) (string, Observation, error) {
	key, observation, err := RawCurrentObservation(ctx, docs, now)
	if err != nil {
		return key, observation, err
	}
	return TrackObservation(ctx, docs, now, key, observation)
}

func RawCurrentObservation(ctx context.Context, docs auth.Documents, now time.Time) (string, Observation, error) {
	doc, err := docs.Load(ctx, "location-latest")
	if err != nil {
		return "", Observation{}, err
	}
	var value latest
	if !doc.ExpiresAt.After(now) || json.Unmarshal(doc.Data, &value) != nil || value.ObservationID == "" {
		return "", Observation{}, auth.ErrMissing
	}
	doc, err = docs.Load(ctx, value.ObservationID)
	if err != nil {
		return "", Observation{}, err
	}
	var observation Observation
	if !doc.ExpiresAt.After(now) || json.Unmarshal(doc.Data, &observation) != nil || observation.Revision == "" || !validFix(observation.Fix) {
		return "", Observation{}, auth.ErrMissing
	}
	return value.ObservationID, observation, nil
}

type mapDemand struct {
	Observation string `json:"observation"`
}

// RequestMap coalesces repeated authorized reads for one minute. A fresh demand
// row permits bounded recovery after stream retries or a deleted cache image.
// Only the demand INSERT triggers the resolver; position INSERTs do not.
func RequestMap(ctx context.Context, docs auth.Documents, now time.Time) error {
	key, _, err := CurrentObservation(ctx, docs, now)
	if err != nil {
		return err
	}
	gateKey := "map-demand#" + digest(key)
	gate, err := docs.Load(ctx, gateKey)
	if err == nil && gate.ExpiresAt.After(now) {
		return nil
	}
	if errors.Is(err, auth.ErrMissing) {
		gate = auth.Document{Key: gateKey}
	} else if err != nil {
		return err
	}
	id, err := opaqueID()
	if err != nil {
		return err
	}
	demand := encoded(auth.Document{Key: "map-request#" + id}, mapDemand{key}, now.Add(time.Hour))
	gate = encoded(gate, mapDemand{key}, now.Add(time.Minute))
	err = docs.Commit(ctx, demand, gate)
	if errors.Is(err, auth.ErrConflict) {
		return nil
	}
	return err
}
