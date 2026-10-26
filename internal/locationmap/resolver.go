package locationmap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-lambda-go/events"
)

type Resolver struct {
	Docs           auth.Documents
	Objects        ObjectStore
	Owner, Vehicle string
	Config         RenderConfig
	Render         func(context.Context, onstar.LocationFix, time.Time) ([]byte, error)
	Now            func() time.Time
}

var safeRenderFailure = regexp.MustCompile(`^map-(position-invalid|request-invalid|source-unavailable|source-timeout|source-network|source-read-failed|source-http-[1-5][0-9]{2}|data-unavailable|encoding-failed)$`)

func renderFailureReason(err error) string {
	if err != nil && safeRenderFailure.MatchString(err.Error()) {
		return err.Error()
	}
	return "map-render-unavailable"
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// ResolveCurrent requests the same effective stored position as map delivery.
// It may derive the configured place center but never contacts the vehicle.
func (r *Resolver) ResolveCurrent(ctx context.Context) (Status, error) {
	key, _, err := CurrentObservation(ctx, r.Docs, r.now())
	if err != nil {
		return Status{}, errors.New("location-fix-unavailable")
	}
	if err = r.Resolve(ctx, key); err != nil {
		return Status{}, err
	}
	return (&Service{Docs: r.Docs, Now: r.Now}).Status(ctx)
}
func encoded(doc auth.Document, value any, expiry time.Time) auth.Document {
	doc.Data, _ = json.Marshal(value)
	doc.ExpiresAt = expiry
	return doc
}

// Resolve uses a fenced, per-owner/vehicle lease. It serializes overlapping
// spatial cells without a process-local lock and never refreshes vehicle data.
func (r *Resolver) Resolve(ctx context.Context, key string) error {
	if !strings.HasPrefix(key, "location-observation#") || len(key) != len("location-observation#")+64 {
		return errors.New("map-observation-invalid")
	}
	doc, err := r.Docs.Load(ctx, key)
	if err != nil {
		return err
	}
	var observation Observation
	if json.Unmarshal(doc.Data, &observation) != nil || observation.Owner != r.Owner || observation.Vehicle != r.Vehicle || (observation.Source != "onstar" && observation.Source != "phone" && observation.Source != "geofence") || observation.Revision == "" || !validFix(observation.Fix) {
		return errors.New("map-observation-invalid")
	}
	if observation.Status == "ready" && observation.Map != nil && observation.Map.ExpiresAt.After(r.now()) && observation.Map.RenderVersion == r.Config.Version && observation.Map.Projection.Width == r.Config.Width && observation.Map.Projection.Height == r.Config.Height && observation.Map.Projection.Zoom == r.Config.Zoom && observation.Map.ImageBytes > 0 && len(observation.Map.ImageSHA256) == 64 {
		if _, err = r.Objects.Get(ctx, observation.Map.ImageID); err == nil {
			return nil
		} else if !errors.Is(err, auth.ErrMissing) {
			return err
		}
	}
	leaseKey := "map-generation#" + digest(r.Owner+"|"+r.Vehicle)
	lease, err := r.Docs.Load(ctx, leaseKey)
	if errors.Is(err, auth.ErrMissing) {
		lease = auth.Document{Key: leaseKey}
	} else if err != nil {
		return err
	} else if lease.ExpiresAt.After(r.now()) {
		return errors.New("map-generation-busy")
	}
	lease = encoded(lease, struct {
		Observation string `json:"observation"`
	}{key}, r.now().Add(2*time.Minute))
	if err = r.Docs.Commit(ctx, lease); err != nil {
		return err
	}
	lease.Version++
	// All final writes include the acquired lease version. A superseded worker
	// cannot publish after a newer generation owner takes over.
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		lease.ExpiresAt = r.now()
		_ = r.Docs.Commit(release, lease)
	}()
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	entry, err := r.find(ctx, observation.Fix)
	if err != nil {
		return r.fail(ctx, doc, observation, lease, "map-cache-unavailable")
	}
	hit := entry != nil
	var changes []auth.Document
	if entry == nil {
		id, idErr := opaqueID()
		if idErr != nil {
			return idErr
		}
		data, renderErr := r.Render(ctx, observation.Fix, r.now())
		if renderErr != nil {
			return r.fail(ctx, doc, observation, lease, renderFailureReason(renderErr))
		}
		if len(data) == 0 || len(data) > contract.MaxMapImageBytes {
			return r.fail(ctx, doc, observation, lease, "map-encoding-failed")
		}
		sum := sha256.Sum256(data)
		noticeRows := 20
		if r.Config.Style == "Satellite" {
			noticeRows = satelliteNoticeRows
		}
		if err = r.Objects.Put(ctx, id, data); err != nil {
			return r.fail(ctx, doc, observation, lease, "map-publish-unavailable")
		}
		if _, err = r.Objects.Get(ctx, id); err != nil {
			return r.fail(ctx, doc, observation, lease, "map-publish-unavailable")
		}
		generated := r.now()
		entry = &cacheEntry{Config: r.Config, Map: CurrentMap{ImageID: id, ImageVersion: r.Config.Version, ImageBytes: int64(len(data)), ImageSHA256: hex.EncodeToString(sum[:]), NoticeRows: noticeRows, RenderVersion: r.Config.Version, GeneratedAt: generated, ExpiresAt: generated.Add(CacheLifetime), Projection: Projection{CenterLatitude: observation.Fix.Latitude, CenterLongitude: observation.Fix.Longitude, Zoom: r.Config.Zoom, Width: r.Config.Width, Height: r.Config.Height}, Attribution: "https://docs.aws.amazon.com/location/latest/developerguide/data-attribution.html"}}
		// The middle of the nine queried cells is the image's immutable cell.
		cellKey := r.cellKeys(observation.Fix)[4]
		cellDoc, index, indexErr := r.index(ctx, cellKey)
		if indexErr != nil {
			return indexErr
		}
		var retained []string
		for _, prior := range index.Entries {
			old, e := r.Docs.Load(ctx, "map-image#"+prior)
			if errors.Is(e, auth.ErrMissing) {
				continue
			}
			if e != nil {
				return e
			}
			if old.ExpiresAt.After(generated) {
				// A one-time image purge leaves encrypted metadata intact. Drop
				// missing objects from the index so they cannot exhaust its bound.
				if _, objectErr := r.Objects.Get(ctx, prior); errors.Is(objectErr, auth.ErrMissing) {
					continue
				} else if objectErr != nil {
					return objectErr
				}
				retained = append(retained, prior)
			}
		}
		if len(retained) >= maxCellEntries {
			return r.fail(ctx, doc, observation, lease, "map-cache-capacity")
		}
		index.Entries = append(retained, id)
		changes = append(changes, encoded(auth.Document{Key: "map-image#" + id}, entry, entry.Map.ExpiresAt), encoded(cellDoc, index, entry.Map.ExpiresAt))
	}
	current := entry.Map
	current.OriginSource = observation.OriginSource
	if current.OriginSource == "" {
		current.OriginSource = observation.Source
	}
	current.OriginRevision = observation.OriginRevision
	current.VehicleRevision = observation.VehicleRevision
	if current.VehicleRevision == "" && observation.Source == "onstar" {
		current.VehicleRevision = observation.Revision
	}
	if current.OriginRevision == "" {
		current.OriginRevision = observation.Revision
	}
	current.Place = observation.Place
	current.ObservationRevision = observation.Revision
	current.Fix = observation.Fix
	current.ReceivedAt = observation.ReceivedAt
	current.CacheHit = hit
	observation.Map = &current
	observation.Status = "ready"
	observation.Reason = ""
	changes = append(changes, encoded(doc, observation, doc.ExpiresAt))
	lease.ExpiresAt = r.now()
	changes = append(changes, lease)
	// Current delivery follows location-latest's observation identity. Finishing
	// an older historical row never changes that pointer.
	return r.Docs.Commit(ctx, changes...)
}
func (r *Resolver) fail(ctx context.Context, doc auth.Document, o Observation, lease auth.Document, reason string) error {
	o.Status = "failed"
	o.Reason = reason
	o.Map = nil
	lease.ExpiresAt = r.now()
	if err := r.Docs.Commit(ctx, encoded(doc, o, doc.ExpiresAt), lease); err != nil {
		return err
	}
	return errors.New(reason)
}

func (r *Resolver) Handle(ctx context.Context, batch events.DynamoDBEvent) (events.DynamoDBEventResponse, error) {
	response := events.DynamoDBEventResponse{BatchItemFailures: []events.DynamoDBBatchItemFailure{}}
	for _, record := range batch.Records {
		// Only explicit map demands render. Observations, resolver writes and TTL
		// removals cannot create images even if the stream filter is misconfigured.
		if record.EventSource != "aws:dynamodb" || record.EventName != "INSERT" {
			continue
		}
		pk, ok := record.Change.Keys["pk"]
		if !ok || pk.DataType() != events.DataTypeString {
			continue
		}
		key := strings.TrimPrefix(pk.String(), "auth#")
		if !strings.HasPrefix(key, "map-request#") {
			continue
		}
		if err := r.resolveDemand(ctx, key); err != nil {
			response.BatchItemFailures = append(response.BatchItemFailures, events.DynamoDBBatchItemFailure{ItemIdentifier: record.Change.SequenceNumber})
		}
	}
	return response, nil
}

func (r *Resolver) resolveDemand(ctx context.Context, key string) error {
	doc, err := r.Docs.Load(ctx, key)
	if errors.Is(err, auth.ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	if !doc.ExpiresAt.After(r.now()) {
		return nil
	}
	var demand mapDemand
	if json.Unmarshal(doc.Data, &demand) != nil {
		return errors.New("map-request-invalid")
	}
	current, _, err := CurrentObservation(ctx, r.Docs, r.now())
	if err != nil {
		return err
	}
	if current != demand.Observation {
		return nil
	}
	return r.Resolve(ctx, demand.Observation)
}
