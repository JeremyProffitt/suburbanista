package locationmap

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
)

const CacheLifetime = 30 * 24 * time.Hour
const MatchRadius = 15.24
const maxCellEntries = 128
const earthRadius = 6371008.8

// RenderConfig contains every input that changes the reusable background.
type RenderConfig struct {
	Provider string  `json:"provider"`
	Style    string  `json:"style"`
	Zoom     float64 `json:"zoom"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Theme    string  `json:"theme"`
	Labels   string  `json:"labels"`
	POIs     string  `json:"pois"`
	Overlays string  `json:"overlays"`
	Version  string  `json:"version"`
}

func DefaultRenderConfig() RenderConfig {
	// Four times the former width and height, at unchanged physical-pixel
	// resolution. A512-pixel provider tile at zoom18 has effective zoom19.
	return RenderConfig{Provider: "amazon-location", Style: "Satellite", Zoom: 19, Width: 3072, Height: 2048, Theme: "Imagery", Labels: "None", POIs: "None", Version: "3"}
}

type Projection struct {
	CenterLatitude  float64 `json:"centerLatitude"`
	CenterLongitude float64 `json:"centerLongitude"`
	Zoom            float64 `json:"zoom"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
}

// CurrentMap is returned only through authenticated delivery. Position and
// projection never appear in identifiers, operational status, or logs.
type CurrentMap struct {
	ImageBytes          int64              `json:"imageBytes,omitempty"`
	ImageSHA256         string             `json:"imageSha256,omitempty"`
	NoticeRows          int                `json:"noticeRows,omitempty"`
	RenderVersion       string             `json:"renderVersion,omitempty"`
	VehicleRevision     string             `json:"vehicleRevision,omitempty"`
	OriginSource        string             `json:"originSource,omitempty"`
	OriginRevision      string             `json:"originRevision,omitempty"`
	Place               string             `json:"place,omitempty"`
	ObservationRevision string             `json:"observationRevision"`
	ImageID             string             `json:"imageId"`
	ImageVersion        string             `json:"imageVersion"`
	GeneratedAt         time.Time          `json:"generatedAt"`
	ExpiresAt           time.Time          `json:"expiresAt"`
	ReceivedAt          time.Time          `json:"receivedAt"`
	Fix                 onstar.LocationFix `json:"fix"`
	Projection          Projection         `json:"projection"`
	Attribution         string             `json:"attribution"`
	CacheHit            bool               `json:"cacheHit"`
}

type Observation struct {
	VehicleRevision string             `json:"vehicleRevision,omitempty"`
	OriginSource    string             `json:"originSource,omitempty"`
	OriginRevision  string             `json:"originRevision,omitempty"`
	Place           string             `json:"place,omitempty"`
	Owner           string             `json:"owner"`
	Vehicle         string             `json:"vehicle"`
	Source          string             `json:"source"`
	SourceID        string             `json:"sourceId"`
	Revision        string             `json:"revision"`
	ReceivedAt      time.Time          `json:"receivedAt"`
	Fix             onstar.LocationFix `json:"fix"`
	Status          string             `json:"status"`
	Reason          string             `json:"reason,omitempty"`
	Map             *CurrentMap        `json:"map,omitempty"`
}

type cacheEntry struct {
	Config RenderConfig `json:"config"`
	Map    CurrentMap   `json:"map"`
}
type cellIndex struct {
	Entries []string `json:"entries"`
}

type ObjectStore interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func opaqueID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("map-id-unavailable")
	}
	return hex.EncodeToString(b[:]), nil
}
func validFix(f onstar.LocationFix) bool {
	return !math.IsNaN(f.Latitude) && !math.IsNaN(f.Longitude) && !math.IsInf(f.Latitude, 0) && !math.IsInf(f.Longitude, 0) && math.Abs(f.Latitude) <= 85 && math.Abs(f.Longitude) <= 180
}
func Distance(a, b onstar.LocationFix) float64 {
	lat1, lat2 := a.Latitude*math.Pi/180, b.Latitude*math.Pi/180
	dlat, dlon := (b.Latitude-a.Latitude)*math.Pi/180, (b.Longitude-a.Longitude)*math.Pi/180
	h := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return earthRadius * 2 * math.Asin(math.Sqrt(math.Min(1, h)))
}
func mercator(f onstar.LocationFix) (float64, float64) {
	return 6378137 * f.Longitude * math.Pi / 180, 6378137 * math.Log(math.Tan(math.Pi/4+f.Latitude*math.Pi/360))
}
func (p Projection) Pixel(f onstar.LocationFix) (float64, float64, bool) {
	if !validFix(f) {
		return 0, 0, false
	}
	x, y := mercator(f)
	cx, cy := mercator(onstar.LocationFix{Latitude: p.CenterLatitude, Longitude: p.CenterLongitude})
	dx := x - cx
	world := 2 * math.Pi * 6378137
	if dx > world/2 {
		dx -= world
	}
	if dx < -world/2 {
		dx += world
	}
	resolution := world / (256 * math.Pow(2, p.Zoom))
	px, py := float64(p.Width)/2+dx/resolution, float64(p.Height)/2-(y-cy)/resolution
	return px, py, px >= 16 && py >= 16 && px < float64(p.Width-16) && py < float64(p.Height-32)
}
func (r *Resolver) cellKeys(f onstar.LocationFix) []string {
	x, y := mercator(f)
	world := 2 * math.Pi * 6378137
	columns := int64(math.Ceil(world / 256))
	x = math.Mod(x+world/2+world, world)
	cx, cy := int64(math.Floor(x/256)), int64(math.Floor(y/256))
	config, _ := json.Marshal(r.Config)
	scope := r.Owner + "|" + r.Vehicle + "|" + string(config)
	keys := make([]string, 0, 9)
	for dx := int64(-1); dx <= 1; dx++ {
		for dy := int64(-1); dy <= 1; dy++ {
			column := (cx + dx + columns) % columns
			keys = append(keys, "map-cell#"+digest(fmt.Sprintf("%s|%d|%d", scope, column, cy+dy)))
		}
	}
	return keys
}
func (r *Resolver) index(ctx context.Context, key string) (auth.Document, cellIndex, error) {
	q, ok := r.Docs.(interface {
		QueryDocument(context.Context, string) (auth.Document, error)
	})
	if !ok {
		return auth.Document{}, cellIndex{}, errors.New("map-index-unavailable")
	}
	doc, err := q.QueryDocument(ctx, key)
	if errors.Is(err, auth.ErrMissing) {
		return auth.Document{Key: key}, cellIndex{}, nil
	}
	if err != nil {
		return doc, cellIndex{}, err
	}
	var index cellIndex
	if json.Unmarshal(doc.Data, &index) != nil || len(index.Entries) > maxCellEntries {
		return doc, index, errors.New("map-index-invalid")
	}
	return doc, index, nil
}
func (r *Resolver) find(ctx context.Context, fix onstar.LocationFix) (*cacheEntry, error) {
	var candidates []cacheEntry
	seen := map[string]bool{}
	for _, key := range r.cellKeys(fix) {
		_, idx, err := r.index(ctx, key)
		if err != nil {
			return nil, err
		}
		for _, id := range idx.Entries {
			if seen[id] {
				continue
			}
			seen[id] = true
			doc, err := r.Docs.Load(ctx, "map-image#"+id)
			if errors.Is(err, auth.ErrMissing) {
				continue
			}
			if err != nil {
				return nil, err
			}
			var entry cacheEntry
			if json.Unmarshal(doc.Data, &entry) != nil {
				return nil, errors.New("map-cache-invalid")
			}
			if entry.Config != r.Config || !entry.Map.ExpiresAt.After(r.now()) || !doc.ExpiresAt.After(r.now()) {
				continue
			}
			centre := onstar.LocationFix{Latitude: entry.Map.Projection.CenterLatitude, Longitude: entry.Map.Projection.CenterLongitude}
			_, _, fits := entry.Map.Projection.Pixel(fix)
			if Distance(fix, centre) <= MatchRadius+1e-8 && fits {
				candidates = append(candidates, entry)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i].Map, candidates[j].Map
		da := Distance(fix, onstar.LocationFix{Latitude: a.Projection.CenterLatitude, Longitude: a.Projection.CenterLongitude})
		db := Distance(fix, onstar.LocationFix{Latitude: b.Projection.CenterLatitude, Longitude: b.Projection.CenterLongitude})
		if math.Abs(da-db) < 1e-8 {
			return a.GeneratedAt.After(b.GeneratedAt)
		}
		return da < db
	})
	for _, entry := range candidates {
		if _, err := r.Objects.Get(ctx, entry.Map.ImageID); err == nil {
			return &entry, nil
		} else if !errors.Is(err, auth.ErrMissing) {
			return nil, err
		}
	}
	return nil, nil
}

func Current(ctx context.Context, docs auth.Documents, now time.Time) (CurrentMap, error) {
	_, observation, err := CurrentObservation(ctx, docs, now)
	if err != nil {
		return CurrentMap{}, err
	}
	if observation.Status != "ready" || observation.Map == nil || !observation.Map.ExpiresAt.After(now) {
		return CurrentMap{}, auth.ErrMissing
	}
	current := *observation.Map
	if current.ObservationRevision == "" {
		current.ObservationRevision = observation.Revision
	}
	return current, nil
}
