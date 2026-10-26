// Package telemetryhistory archives one passive diagnostics read per UTC hour.
package telemetryhistory

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/internal/worker"
)

type Provider interface {
	Refresh(context.Context, onstar.Grant) (onstar.Grant, error)
	Exchange(context.Context, onstar.Grant, string, string) (onstar.GMToken, error)
	Diagnostics(context.Context, onstar.GMToken, string) (onstar.HealthStatus, error)
}

type Service struct {
	Docs                                  auth.Documents
	Credentials                           worker.Store
	Provider                              Provider
	Owner, Vehicle, DeviceID, ScheduleARN string
	Paused                                func(context.Context) (bool, error)
	Now                                   func() time.Time
	Wait                                  func(context.Context) bool
}

type Request struct {
	Kind        string    `json:"kind"`
	VehicleKey  string    `json:"vehicleKey"`
	ScheduleARN string    `json:"scheduleArn"`
	ScheduledAt time.Time `json:"scheduledAt"`
}

// Receipt intentionally contains no reading values, identity, credentials or raw body.
type Receipt struct {
	State            string     `json:"state"`
	Reason           string     `json:"reason,omitempty"`
	Hour             time.Time  `json:"hour"`
	CollectedAt      time.Time  `json:"collectedAt,omitempty"`
	SourceObservedAt *time.Time `json:"sourceObservedAt,omitempty"`
	SourceFreshness  string     `json:"sourceFreshness,omitempty"`
	DataCalls        int        `json:"diagnosticCalls"`
	Readings         int        `json:"readings"`
	HasFuel          bool       `json:"hasFuel"`
	HasOdometer      bool       `json:"hasOdometer"`
	ArchiveVerified  bool       `json:"archiveVerified"`
}

type record struct {
	Receipt
	SchemaVersion int    `json:"schemaVersion"`
	Source        string `json:"source"`
	Owner         string `json:"owner"`
	Vehicle       string `json:"vehicle"`
	Encoding      string `json:"encoding,omitempty"`
	Parts         int    `json:"parts,omitempty"`
	Bytes         int    `json:"uncompressedBytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
}

const chunkBytes = 128 * 1024
const maxCompressedBytes = 2 * 1024 * 1024
const maxHistoryBytes = 8 * 1024 * 1024

var retained = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *Service) key(hour time.Time) string {
	sum := sha256.Sum256([]byte(s.Owner + "|" + s.Vehicle))
	return fmt.Sprintf("telemetry-hour#%x#%s", sum, hour.UTC().Format("2006-01-02T15"))
}
func withData(doc auth.Document, value any, expiry time.Time) auth.Document {
	doc.Data, _ = json.Marshal(value)
	doc.ExpiresAt = expiry
	return doc
}
func (s *Service) finish(ctx context.Context, doc auth.Document, value record, reason string) (Receipt, error) {
	value.State, value.Reason = "skipped", reason
	if value.DataCalls != 0 {
		value.State = "failed"
	}
	if err := s.Docs.Commit(ctx, withData(doc, value, retained)); err != nil {
		return value.Receipt, auth.ErrUnavailable
	}
	return value.Receipt, nil
}
func (s *Service) wait(ctx context.Context) bool {
	if s.Wait != nil {
		return s.Wait(ctx)
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) Collect(ctx context.Context, input Request) (Receipt, error) {
	if ctx.Err() != nil {
		return Receipt{}, auth.ErrUnavailable
	}
	now := s.now()
	if input.Kind != "hourly-diagnostics" || input.VehicleKey != s.Vehicle || input.ScheduleARN != s.ScheduleARN || s.Owner == "" || s.Vehicle == "" || s.ScheduleARN == "" || input.ScheduledAt.IsZero() || input.ScheduledAt.After(now.Add(time.Minute)) || now.Sub(input.ScheduledAt) > 2*time.Hour {
		return Receipt{}, errors.New("invalid hourly telemetry request")
	}
	hour := input.ScheduledAt.UTC().Truncate(time.Hour)
	key := s.key(hour)
	if _, err := s.Docs.Load(ctx, key); err == nil {
		return s.status(ctx, key)
	} else if !errors.Is(err, auth.ErrMissing) {
		return Receipt{}, auth.ErrUnavailable
	}
	value := record{Receipt: Receipt{State: "started", Hour: hour}, SchemaVersion: 1, Source: "onstar-healthstatus", Owner: s.Owner, Vehicle: s.Vehicle}
	doc := withData(auth.Document{Key: key}, value, retained)
	// Claim before authentication or the data GET. A crashed invocation is not replayed.
	if err := s.Docs.Commit(ctx, doc); errors.Is(err, auth.ErrConflict) {
		return s.status(ctx, key)
	} else if err != nil {
		return Receipt{}, auth.ErrUnavailable
	}
	doc.Version++
	if s.Paused == nil {
		return s.finish(ctx, doc, value, "provider-paused")
	}
	paused, err := s.Paused(ctx)
	if err != nil {
		return s.finish(ctx, doc, value, "provider-state-unavailable")
	}
	if paused {
		return s.finish(ctx, doc, value, "provider-paused")
	}
	var lease state.Lease
	for attempt := 0; attempt < 11; attempt++ {
		lease, err = s.Credentials.Acquire(ctx, key, s.now())
		if err == nil {
			break
		}
		if !errors.Is(err, state.ErrConflict) {
			return s.finish(ctx, doc, value, "credential-unavailable")
		}
		if attempt == 10 || !s.wait(ctx) {
			return s.finish(ctx, doc, value, "provider-busy")
		}
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = s.Credentials.Release(release, lease)
	}()
	credentials, err := s.Credentials.Load(ctx, lease, s.now())
	if err != nil {
		return s.finish(ctx, doc, value, "credential-unavailable")
	}
	if credentials.Bootstrap.ValidateDevice(s.DeviceID) != nil || credentials.ProviderState != "ready" || credentials.CooldownUntil > s.now().Unix() {
		return s.finish(ctx, doc, value, "provider-not-ready")
	}
	deadline := time.Now().Add(55 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Add(-15*time.Second).Before(deadline) {
		deadline = end.Add(-15 * time.Second)
	}
	if time.Until(deadline) < time.Second {
		return s.finish(ctx, doc, value, "insufficient-deadline")
	}
	providerCtx, cancelProvider := context.WithDeadline(ctx, deadline)
	defer cancelProvider()
	fail := func(cause error) (Receipt, error) {
		reason := "provider-unavailable"
		var problem *onstar.Error
		if errors.As(cause, &problem) {
			switch problem.Kind {
			case "reauth-required", "account-mismatch":
				credentials.ProviderState = "reauth-required"
				reason = "reauth-required"
			case "vehicle-not-linked":
				credentials.ProviderState = "vehicle-not-linked"
				reason = "vehicle-not-linked"
			case "rate-limited":
				credentials.CooldownUntil = s.now().Unix() + int64(max(problem.RetryAfterSeconds, 3600))
				credentials.CooldownReason = "diagnostics-rate-limited"
				reason = "rate-limited"
			case "deadline", "invalid-response", "invalid-vehicle-data", "vehicle-data-unavailable", "access-denied":
				reason = problem.Kind
			}
			if problem.AuthEndpoint && (problem.Status == 401 || problem.Status == 403 || problem.Kind == "access-denied") {
				credentials.CooldownUntil = s.now().Add(6 * time.Hour).Unix()
				credentials.CooldownReason = "auth-endpoint-denied"
			}
			if _, e := s.Credentials.Save(ctx, lease, credentials, s.now()); e != nil {
				return value.Receipt, auth.ErrUnavailable
			}
		}
		return s.finish(ctx, doc, value, reason)
	}
	token := onstar.GMToken{AccessToken: credentials.GMToken, ExpiresAt: credentials.GMExpiresAt, VIN: credentials.Bootstrap.VIN, VehicleLinked: true}
	if token.AccessToken == "" || token.ExpiresAt <= s.now().Add(30*time.Second).Unix() {
		if credentials.Grant.AccessToken == "" || credentials.Grant.ExpiresAt <= s.now().Add(30*time.Second).Unix() {
			grant, refreshErr := s.Provider.Refresh(providerCtx, credentials.Grant)
			err = refreshErr
			if err != nil {
				return fail(err)
			}
			credentials.Grant = grant
			credentials, err = s.Credentials.Save(ctx, lease, credentials, s.now())
			if err != nil {
				return value.Receipt, auth.ErrUnavailable
			}
		}
		token, err = s.Provider.Exchange(providerCtx, credentials.Grant, s.DeviceID, credentials.Bootstrap.VIN)
		if err != nil {
			return fail(err)
		}
		credentials.GMToken, credentials.GMExpiresAt = token.AccessToken, token.ExpiresAt
		credentials, err = s.Credentials.Save(ctx, lease, credentials, s.now())
		if err != nil {
			return value.Receipt, auth.ErrUnavailable
		}
	}
	paused, err = s.Paused(ctx)
	if err != nil {
		return s.finish(ctx, doc, value, "provider-state-unavailable")
	}
	if paused {
		return s.finish(ctx, doc, value, "provider-paused")
	}
	if providerCtx.Err() != nil {
		return s.finish(ctx, doc, value, "insufficient-deadline")
	}
	value.State, value.DataCalls = "dispatched", 1
	if err = s.Docs.Commit(ctx, withData(doc, value, retained)); err != nil {
		return value.Receipt, auth.ErrUnavailable
	}
	doc.Version++
	health, err := s.Provider.Diagnostics(providerCtx, token, credentials.Bootstrap.VIN)
	if err != nil {
		return fail(err)
	}
	value.CollectedAt = s.now()
	return s.archive(ctx, doc, value, health)
}

func (s *Service) archive(ctx context.Context, doc auth.Document, value record, health onstar.HealthStatus) (Receipt, error) {
	raw, err := json.Marshal(health)
	if err != nil || len(raw) > maxHistoryBytes {
		return s.finish(ctx, doc, value, "diagnostics-response-too-large")
	}
	defer clear(raw)
	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	if _, err = writer.Write(raw); err != nil {
		return value.Receipt, auth.ErrUnavailable
	}
	if err = writer.Close(); err != nil {
		return value.Receipt, auth.ErrUnavailable
	}
	if zipped.Len() > maxCompressedBytes {
		return s.finish(ctx, doc, value, "diagnostics-response-too-large")
	}
	value.Encoding, value.Bytes = "gzip-json", len(raw)
	sum := sha256.Sum256(raw)
	value.SHA256 = hex.EncodeToString(sum[:])
	value.Parts = (zipped.Len() + chunkBytes - 1) / chunkBytes
	count := func(e onstar.HealthElement) {
		if e.Value != nil {
			value.Readings++
			number, err := strconv.ParseFloat(strings.TrimSpace(*e.Value), 64)
			if err == nil && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 {
				value.HasFuel = value.HasFuel || e.Name == "FUEL_LEVEL" && e.Unit == "%" && number <= 100
				value.HasOdometer = value.HasOdometer || e.Name == "ODO_READ" && (e.Unit == "KM" || e.Unit == "MI")
			}
		}
	}
	count(health.HealthElement)
	for _, d := range health.Diagnostics {
		count(d.HealthElement)
		for _, e := range d.Elements {
			count(e)
		}
	}
	value.State = "collected"
	if value.Readings == 0 {
		value.State, value.Reason = "unavailable", "no-readings"
	}
	display := onstar.DiagnosticsSnapshot(health, value.CollectedAt)
	value.SourceObservedAt, value.SourceFreshness = display.ObservedAt, display.Freshness
	snapshot, err := s.Docs.Load(ctx, "snapshot#diagnostics")
	if errors.Is(err, auth.ErrMissing) {
		snapshot = auth.Document{Key: "snapshot#diagnostics"}
	} else if err != nil {
		return value.Receipt, auth.ErrUnavailable
	}
	latest, err := s.Docs.Load(ctx, "telemetry-latest")
	if errors.Is(err, auth.ErrMissing) {
		latest = auth.Document{Key: "telemetry-latest"}
	} else if err != nil {
		return value.Receipt, auth.ErrUnavailable
	}
	changes := []auth.Document{withData(doc, value, retained), withData(snapshot, display, s.now().AddDate(0, 0, 90)), withData(latest, struct {
		Key string `json:"key"`
	}{doc.Key}, retained)}
	compressed := zipped.Bytes()
	for part := 0; part < value.Parts; part++ {
		data := compressed[part*chunkBytes : min(len(compressed), (part+1)*chunkBytes)]
		changes = append(changes, withData(auth.Document{Key: fmt.Sprintf("%s#part-%02d", doc.Key, part)}, struct {
			Data []byte `json:"data"`
		}{data}, retained))
	}
	if err = s.Docs.Commit(ctx, changes...); err != nil {
		if verified, readErr := s.status(ctx, doc.Key); readErr == nil && verified.ArchiveVerified {
			return verified, nil
		}
		return value.Receipt, auth.ErrUnavailable
	}
	return s.status(ctx, doc.Key)
}

func (s *Service) Latest(ctx context.Context) (Receipt, error) {
	doc, err := s.Docs.Load(ctx, "telemetry-latest")
	if errors.Is(err, auth.ErrMissing) {
		return Receipt{State: "not-collected"}, nil
	}
	if err != nil {
		return Receipt{}, auth.ErrUnavailable
	}
	var pointer struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(doc.Data, &pointer) != nil || pointer.Key == "" {
		return Receipt{}, auth.ErrUnavailable
	}
	return s.status(ctx, pointer.Key)
}

func (s *Service) status(ctx context.Context, key string) (Receipt, error) {
	doc, err := s.Docs.Load(ctx, key)
	if err != nil {
		return Receipt{}, auth.ErrUnavailable
	}
	var value record
	if json.Unmarshal(doc.Data, &value) != nil || value.SchemaVersion != 1 || value.Source != "onstar-healthstatus" || value.Owner != s.Owner || value.Vehicle != s.Vehicle || key != s.key(value.Hour) {
		return Receipt{}, auth.ErrUnavailable
	}
	if value.Parts == 0 {
		return value.Receipt, nil
	}
	if value.Encoding != "gzip-json" || value.Parts > 16 || value.Bytes <= 0 || value.Bytes > maxHistoryBytes {
		return Receipt{}, auth.ErrUnavailable
	}
	var compressed bytes.Buffer
	for part := 0; part < value.Parts; part++ {
		chunk, err := s.Docs.Load(ctx, fmt.Sprintf("%s#part-%02d", key, part))
		var body struct {
			Data []byte `json:"data"`
		}
		if err != nil || json.Unmarshal(chunk.Data, &body) != nil || len(body.Data) == 0 || len(body.Data) > chunkBytes {
			return Receipt{}, auth.ErrUnavailable
		}
		compressed.Write(body.Data)
	}
	reader, err := gzip.NewReader(&compressed)
	if err != nil {
		return Receipt{}, auth.ErrUnavailable
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, int64(value.Bytes)+1))
	defer clear(raw)
	sum := sha256.Sum256(raw)
	if err != nil || len(raw) != value.Bytes || hex.EncodeToString(sum[:]) != value.SHA256 {
		return Receipt{}, auth.ErrUnavailable
	}
	var health onstar.HealthStatus
	if json.Unmarshal(raw, &health) != nil {
		return Receipt{}, auth.ErrUnavailable
	}
	value.ArchiveVerified = true
	return value.Receipt, nil
}
