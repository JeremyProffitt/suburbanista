package commands

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

type ReadProvider interface {
	ReadSnapshot(context.Context, onstar.GMToken, string, string) (contract.VehicleSnapshot, error)
}

func unavailable(id, reason string) contract.VehicleSnapshot {
	return contract.VehicleSnapshot{FeatureID: id, State: "unavailable", Reason: reason, Freshness: "unknown", Fields: []contract.SnapshotField{}, Items: []contract.SnapshotItem{}}
}
func (s *Service) cachedSnapshot(ctx context.Context, id string) (contract.VehicleSnapshot, error) {
	f, ok := contract.FindFeature(id)
	if !ok || f.Category != "read" {
		return unavailable(id, "unsupported-read"), nil
	}
	doc, e := s.Docs.Load(ctx, "snapshot#"+id)
	if errors.Is(e, auth.ErrMissing) {
		return unavailable(id, "not-fetched"), nil
	}
	if e != nil {
		return contract.VehicleSnapshot{}, e
	}
	snapshot, e := decoded[contract.VehicleSnapshot](doc)
	if e != nil || snapshot.FeatureID != id {
		return contract.VehicleSnapshot{}, ErrPersistence
	}
	if !s.now().Before(doc.ExpiresAt) {
		return unavailable(id, "snapshot-expired"), nil
	}
	snapshot.Freshness = contract.FreshnessAt(snapshot.ObservedAt, s.now())
	for i := range snapshot.Fields {
		snapshot.Fields[i].Freshness = contract.FreshnessAt(snapshot.Fields[i].ObservedAt, s.now())
	}
	for i := range snapshot.Items {
		for j := range snapshot.Items[i].Fields {
			field := &snapshot.Items[i].Fields[j]
			field.Freshness = contract.FreshnessAt(field.ObservedAt, s.now())
		}
	}
	return snapshot, nil
}
func (s *Service) newActionEligibility(ctx context.Context, action string) string {
	snapshot, e := s.cachedSnapshot(ctx, "vehicle-details")
	if e != nil {
		return "capability-unavailable"
	}
	return eligibilityReason(snapshot, action, s.now())
}
func eligibilityReason(snapshot contract.VehicleSnapshot, action string, now time.Time) string {
	if snapshot.State != "available" || snapshot.FetchedAt.IsZero() {
		return "capability-unverified"
	}
	if snapshot.FetchedAt.After(now) || now.Sub(snapshot.FetchedAt) > 15*time.Minute {
		return "capability-stale"
	}
	for _, row := range snapshot.Eligibility {
		if row.Action == action {
			if row.Eligible == nil {
				return "capability-unverified"
			}
			if !*row.Eligible {
				return "provider-ineligible"
			}
			return ""
		}
	}
	return "capability-unverified"
}
func (s *Service) features(ctx context.Context) contract.AlexaCommandResponse {
	flags := Flags{ProviderPaused: true}
	if s.Flags == nil {
		return rejected("storage-unavailable")
	}
	var e error
	flags, e = s.Flags.ReadFlags(ctx)
	if e != nil {
		return rejected("storage-unavailable")
	}
	availability := []contract.FeatureAvailability{}
	snapshot, snapshotErr := s.cachedSnapshot(ctx, "vehicle-details")
	for _, feature := range contract.FeatureCatalog() {
		row := contract.FeatureAvailability{ID: feature.ID, ProofState: feature.ProofState}
		switch feature.Category {
		case "ev":
			row.Reason = "vehicle-applicability-unverified"
		case "read":
			row.Available = !flags.ProviderPaused
			if flags.ProviderPaused {
				row.Reason = "provider-paused"
			} else if feature.ID == "location" {
				row.Reason = "location-schema-unverified"
			}
		case "action":
			row.Reason = DisabledReason(flags, feature.Action)
			if feature.ID != "start" && feature.ID != "lock" && feature.ID != "unlock" {
				eligibility := "capability-unavailable"
				if snapshotErr == nil {
					eligibility = eligibilityReason(snapshot, feature.ID, s.now())
				}
				if eligibility != "" {
					row.Reason = eligibility
				} else {
					yes := true
					row.Eligible = &yes
					row.ProofState = "vehicle-eligible"
				}
			} else {
				row.ProofState = "owner-reported"
			}
			row.Available = row.Reason == ""
		}
		availability = append(availability, row)
	}
	return contract.AlexaCommandResponse{State: "succeeded", Availability: availability, UpdatedAt: s.now().Unix()}
}
func (s *Service) read(ctx context.Context, r contract.AlexaCommandRequest, generation uint64) contract.AlexaCommandResponse {
	var capture *onstar.ReadTraceCapture
	feature, known := contract.FindFeature(r.FeatureID)
	if !known {
		return rejected("unsupported-read")
	}
	if feature.Category == "ev" {
		value := unavailable(feature.ID, "ev-special-auth-unimplemented")
		return contract.AlexaCommandResponse{State: "succeeded", Snapshot: &value}
	}
	if feature.Category != "read" {
		return rejected("unsupported-read")
	}
	done := func(snapshot contract.VehicleSnapshot) contract.AlexaCommandResponse {
		out := contract.AlexaCommandResponse{State: "succeeded", Snapshot: &snapshot, UpdatedAt: s.now().Unix()}
		if capture != nil {
			out.Trace = capture.Result()
			if data, err := json.Marshal(out); err != nil || len(data) >= 65536 {
				out.Trace = nil
			}
		}
		return out
	}
	if r.Operation == "cached-read" {
		snapshot, e := s.cachedSnapshot(ctx, feature.ID)
		if e != nil {
			return rejected("storage-unavailable")
		}
		return done(snapshot)
	}
	if s.Flags == nil {
		return done(unavailable(feature.ID, "provider-paused"))
	}
	flags, e := s.Flags.ReadFlags(ctx)
	if e != nil {
		return rejected("storage-unavailable")
	}
	if flags.ProviderPaused {
		return done(unavailable(feature.ID, "provider-paused"))
	}
	provider, ok := s.Provider.(ReadProvider)
	if !ok {
		return done(unavailable(feature.ID, "read-not-implemented"))
	}
	lease, e := s.Credentials.Acquire(ctx, "read-"+hash(r.RequestID), s.now())
	if e != nil {
		return done(unavailable(feature.ID, "provider-busy"))
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = s.Credentials.Release(releaseCtx, lease)
	}()
	record, e := s.Credentials.Load(ctx, lease, s.now())
	if e != nil {
		return done(unavailable(feature.ID, "credential-unavailable"))
	}
	if record.Bootstrap.ValidateDevice(s.Config.DeviceID) != nil || record.ProviderState != "ready" || record.CooldownUntil > s.now().Unix() {
		return done(unavailable(feature.ID, "provider-not-ready"))
	}
	token := onstar.GMToken{AccessToken: record.GMToken, ExpiresAt: record.GMExpiresAt, VehicleLinked: true, VIN: record.Bootstrap.VIN}
	readFail := func(cause error) contract.AlexaCommandResponse {
		reason := "provider-unavailable"
		var problem *onstar.Error
		if errors.As(cause, &problem) {
			switch problem.Kind {
			case "garage-query-failed", "vehicle-data-unavailable", "invalid-vehicle-data", "invalid-location-response", "unsupported-read":
				reason = problem.Kind
			case "reauth-required", "account-mismatch":
				record.ProviderState = "reauth-required"
				reason = "reauth-required"
			case "vehicle-not-linked":
				record.ProviderState = "vehicle-not-linked"
				reason = "vehicle-not-linked"
			}
			if problem.AuthEndpoint && (problem.Status == 401 || problem.Status == 403 || problem.Kind == "access-denied") {
				record.CooldownUntil = s.now().Add(6 * time.Hour).Unix()
				record.CooldownReason = "auth-endpoint-denied"
			}
			if _, saveErr := s.Credentials.Save(ctx, lease, record, s.now()); saveErr != nil {
				return rejected("storage-unavailable")
			}
		}
		return done(unavailable(feature.ID, reason))
	}
	if token.AccessToken == "" || token.ExpiresAt <= s.now().Add(30*time.Second).Unix() {
		if record.Grant.AccessToken == "" || record.Grant.ExpiresAt <= s.now().Add(30*time.Second).Unix() {
			grant, err := s.Provider.Refresh(ctx, record.Grant)
			if err != nil {
				return readFail(err)
			}
			record.Grant = grant
			record, e = s.Credentials.Save(ctx, lease, record, s.now())
			if e != nil {
				return rejected("storage-unavailable")
			}
		}
		token, e = s.Provider.Exchange(ctx, record.Grant, s.Config.DeviceID, record.Bootstrap.VIN)
		if e != nil {
			return readFail(e)
		}
		record.GMToken = token.AccessToken
		record.GMExpiresAt = token.ExpiresAt
		record, e = s.Credentials.Save(ctx, lease, record, s.now())
		if e != nil {
			return rejected("storage-unavailable")
		}
	}
	if _, e = s.ownerDocument(ctx); e != nil {
		return uncertainRequest(r, "owner-binding-unverified")
	}
	_, binding, e := s.user(ctx, hash(r.UserID), true)
	if e != nil {
		return rejected("storage-unavailable")
	}
	if !binding.Active || binding.Generation != generation {
		return uncertainRequest(r, "binding-revoked")
	}
	if r.Source == "mobile" {
		if _, e = s.Families.CommandFamily(ctx, r.FamilyID); e != nil {
			return uncertainRequest(r, "session-revoked")
		}
	}
	flags, e = s.Flags.ReadFlags(ctx)
	if e != nil {
		return rejected("storage-unavailable")
	}
	if flags.ProviderPaused {
		return done(unavailable(feature.ID, "provider-paused"))
	}
	if r.ReadTrace && r.Source == "mobile" {
		ctx, capture = onstar.WithReadTrace(ctx)
	}
	snapshot, e := provider.ReadSnapshot(ctx, token, record.Bootstrap.VIN, feature.ID)
	if e != nil {
		return readFail(e)
	}
	if snapshot.FeatureID != feature.ID {
		return done(unavailable(feature.ID, "invalid-snapshot"))
	}
	doc, e := s.Docs.Load(ctx, "snapshot#"+feature.ID)
	if errors.Is(e, auth.ErrMissing) {
		doc = s.newDoc("snapshot#" + feature.ID)
	} else if e != nil {
		return rejected("storage-unavailable")
	}
	doc.ExpiresAt = s.now().AddDate(0, 0, 90)
	if s.save(ctx, &doc, snapshot) != nil {
		return rejected("storage-unavailable")
	}
	return done(snapshot)
}
