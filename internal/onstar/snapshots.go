package onstar

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

var privateSnapshotText = regexp.MustCompile(`(?i:Bearer\s+\S+|(?:access|refresh)[_-]?token\s*[:=]\s*\S+|https?://\S+|[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}|[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|\b[A-HJ-NPR-Z0-9]{17}\b|\+?\b(?:[0-9][ ()-]*){9,}[0-9]\b`)

func safeText(value string, max int) string {
	value = privateSnapshotText.ReplaceAllString(value, "[redacted]")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	var out strings.Builder
	for _, r := range value {
		if out.Len()+utf8.RuneLen(r) > max {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}
func numberText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return safeText(s, 200)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if _, e := n.Float64(); e == nil {
			return n.String()
		}
	}
	return ""
}

type snapshotBuilder struct {
	value     contract.VehicleSnapshot
	count     int
	truncated bool
}

func newSnapshot(id string, now time.Time) *snapshotBuilder {
	return &snapshotBuilder{value: contract.VehicleSnapshot{FeatureID: id, State: "available", FetchedAt: now.UTC(), Freshness: "unknown", Fields: []contract.SnapshotField{}, Items: []contract.SnapshotItem{}}}
}
func observed(value string, now time.Time) *time.Time {
	t, e := time.Parse(time.RFC3339Nano, value)
	if e != nil || t.After(now) {
		return nil
	}
	t = t.UTC()
	return &t
}
func (b *snapshotBuilder) field(key, label, value, unit, stamp string) []contract.SnapshotField {
	if value == "" {
		return nil
	}
	if b.count >= 60 {
		b.truncated = true
		return nil
	}
	b.count++
	at := observed(stamp, b.value.FetchedAt)
	return []contract.SnapshotField{{Key: safeText(key, 60), Label: safeText(label, 60), Value: safeText(value, 200), Unit: safeText(unit, 20), ObservedAt: at, Freshness: contract.FreshnessAt(at, b.value.FetchedAt)}}
}
func (b *snapshotBuilder) add(key, label, value string) {
	b.value.Fields = append(b.value.Fields, b.field(key, label, value, "", "")...)
}
func (b *snapshotBuilder) boolean(key, label string, value *bool) {
	if value != nil {
		b.add(key, label, strconv.FormatBool(*value))
	}
}
func (b *snapshotBuilder) item(title string, fields []contract.SnapshotField) {
	if len(fields) == 0 {
		return
	}
	if len(b.value.Items) >= 20 {
		b.truncated = true
		return
	}
	b.value.Items = append(b.value.Items, contract.SnapshotItem{Title: safeText(title, 100), Fields: fields})
}
func (b *snapshotBuilder) done() contract.VehicleSnapshot {
	if b.count == 0 && len(b.value.Eligibility) == 0 {
		b.value.State = "unavailable"
		b.value.Reason = "provider-fields-unavailable"
	}
	if b.truncated {
		b.value.State = "partial"
		b.value.Reason = "bounded-snapshot"
	}
	return b.value
}
func actionForName(name string) string {
	for _, f := range contract.FeatureCatalog() {
		if f.Category == "action" && (strings.EqualFold(name, f.SourceMethod) || strings.EqualFold(name, f.Action)) {
			return f.Action
		}
	}
	return ""
}

// ReadSnapshot makes only the requested read and exposes typed selected fields.
// It never returns provider URLs, account IDs, VINs, radio IDs, or raw bodies.
func (c *Client) ReadSnapshot(ctx context.Context, token GMToken, vin, id string) (contract.VehicleSnapshot, error) {
	b := newSnapshot(id, c.now())
	if e := c.commandToken(token, vin); e != nil {
		return b.value, e
	}
	switch id {
	case "garage":
		vehicles, e := c.Vehicles(ctx, token)
		if e != nil {
			return b.value, e
		}
		for _, v := range vehicles {
			if v.VIN != vin {
				continue
			}
			b.add("make", "Make", v.Make)
			b.add("model", "Model", v.Model)
			b.add("year", "Model year", numberText(v.Year))
		}
	case "vehicle-details":
		v, e := c.VehicleDetails(ctx, token, vin)
		if e != nil {
			return b.value, e
		}
		b.add("make", "Make", v.Make)
		b.add("model", "Model", v.Model)
		b.add("year", "Model year", numberText(v.Year))
		b.add("exterior-color", "Exterior color", v.Color.Exterior)
		b.add("interior-color", "Interior color", v.Color.Interior)
		b.add("fuel-category", "Fuel category", v.Metadata.Fuel.Category)
		b.add("propulsion-type", "Propulsion", v.Metadata.Fuel.Propulsion)
		b.add("body-style", "Body style", v.Metadata.BodyStyle)
		b.add("configured-country", "Country", v.Metadata.Country)
		b.add("onstar-status", "OnStar status", v.OnstarInfo.Status)
		b.boolean("onstar-capable", "OnStar capable", v.OnstarCapable)
		b.boolean("shared-vehicle", "Shared vehicle", v.OnstarInfo.Shared)
		seen := map[string]int{}
		for _, row := range v.Commands {
			action := actionForName(row.Name)
			if action == "" {
				continue
			}
			if index, exists := seen[action]; exists {
				b.value.Eligibility[index].Eligible = nil
				b.value.Eligibility[index].Reason = "eligibility-ambiguous"
				continue
			}
			seen[action] = len(b.value.Eligibility)
			reason := ""
			if row.Eligible == nil {
				reason = "eligibility-unknown"
			} else if !*row.Eligible {
				reason = "provider-ineligible"
			}
			b.value.Eligibility = append(b.value.Eligibility, contract.CommandEligibility{Action: action, Eligible: row.Eligible, Reason: reason})
		}
	case "diagnostics":
		health, e := c.Diagnostics(ctx, token, vin)
		if e != nil {
			return b.value, e
		}
		return DiagnosticsSnapshot(health, b.value.FetchedAt), nil
	case "onstar-plan":
		plan, e := c.OnstarPlan(ctx, token, vin)
		if e != nil {
			return b.value, e
		}
		if plan.Partial {
			b.value.State = "partial"
			b.value.Reason = "partial-provider-response"
		}
		items := append(append([]PlanItem{}, plan.Expiry...), plan.Plans...)
		items = append(items, plan.Active...)
		for _, p := range items {
			title := p.Name
			if title == "" {
				title = "Plan"
			}
			fields := b.field("plan-status", "Status", p.Status, "", "")
			fields = append(fields, b.field("start-date", "Start date", p.StartDate, "", "")...)
			fields = append(fields, b.field("end-date", "End date", p.EndDate, "", "")...)
			fields = append(fields, b.field("expiry-date", "Expiry date", p.ExpiryDate, "", "")...)
			fields = append(fields, b.field("billing-cadence", "Billing cadence", p.Cadence, "", "")...)
			if p.Trial != nil {
				fields = append(fields, b.field("trial", "Trial", strconv.FormatBool(*p.Trial), "", "")...)
			}
			for _, f := range p.Features {
				fields = append(fields, b.field("plan-feature", "Feature", f.Name, "", "")...)
			}
			b.item(title, fields)
		}
		for _, offer := range plan.Offers {
			fields := b.field("offer-name", "Offer", offer.Name, "", "")
			fields = append(fields, b.field("retail-price", "Listed price", numberText(offer.Price), "", "")...)
			fields = append(fields, b.field("billing-cadence", "Billing cadence", offer.Cadence, "", "")...)
			b.item("Offer", fields)
		}
	case "recalls":
		recalls, e := c.Recalls(ctx, token, vin)
		if e != nil {
			return b.value, e
		}
		if recalls != nil {
			b.add("recall-count", "Returned recalls", strconv.Itoa(len(recalls)))
		}
		for _, r := range recalls {
			fields := b.field("recall-status", "Recall status", r.Status, "", "")
			for _, f := range []struct{ key, label, value string }{{"type", "Type", r.Type}, {"description", "Description", r.Description}, {"repair-status", "Repair status", r.RepairStatus}, {"repair-description", "Repair", r.Repair}, {"safety-risk", "Safety information", r.Safety}, {"completed-date", "Completed date", r.Completed}} {
				fields = append(fields, b.field(f.key, f.label, f.value, "", "")...)
			}
			b.item(r.Title, fields)
		}
	case "warranty":
		warranties, e := c.Warranty(ctx, token, vin)
		if e != nil {
			return b.value, e
		}
		if warranties != nil {
			b.add("warranty-count", "Returned warranties", strconv.Itoa(len(warranties)))
		}
		for _, w := range warranties {
			fields := b.field("warranty-status", "Status", w.Status, "", "")
			fields = append(fields, b.field("start-mileage", "Start mileage", numberText(w.StartMileage), w.Unit, "")...)
			fields = append(fields, b.field("end-mileage", "End mileage", numberText(w.EndMileage), w.Unit, "")...)
			fields = append(fields, b.field("start-date", "Start date", w.Start, "", "")...)
			fields = append(fields, b.field("expiration-date", "Expiration date", w.Expires, "", "")...)
			fields = append(fields, b.field("notes", "Notes", w.Notes, "", "")...)
			b.item(w.Type, fields)
		}
	case "sxm-subscription":
		sxm, e := c.SxmSubscription(ctx, token, vin)
		if e != nil {
			return b.value, e
		}
		b.add("sxm-status", "Subscription status", sxm.Status)
		b.boolean("suspended", "Suspended", sxm.Suspended)
		b.add("deactivation-date", "Deactivation date", sxm.DeactivationDate)
		for _, sub := range sxm.Subscriptions {
			fields := b.field("subscription-name", "Subscription", sub.Name, "", "")
			fields = append(fields, b.field("start-date", "Start date", sub.Start, "", "")...)
			fields = append(fields, b.field("end-date", "End date", sub.End, "", "")...)
			for _, service := range sub.Services {
				fields = append(fields, b.field("service", "Service", service, "", "")...)
			}
			b.item(sub.Name, fields)
		}
	case "location":
		status, e := c.Location(ctx, token, vin, false)
		if e != nil {
			return b.value, e
		}
		b.add("update-pending", "Location update pending", strconv.FormatBool(status.Pending))
		b.value.State = "partial"
		b.value.Reason = "fix-schema-unverified"
	default:
		return b.value, failure("unsupported-read")
	}
	return b.done(), nil
}

// DiagnosticsSnapshot derives the bounded display cache from an already collected response.
// Hourly history retains the complete HealthStatus separately, without these UI limits.
func DiagnosticsSnapshot(health HealthStatus, now time.Time) contract.VehicleSnapshot {
	b := newSnapshot("diagnostics", now)
	b.value.ObservedAt = observed(health.ObservedAt, b.value.FetchedAt)
	b.value.Freshness = contract.FreshnessAt(b.value.ObservedAt, b.value.FetchedAt)
	b.value.Fields = append(b.value.Fields, b.field("health-status", "Vehicle health", health.Status, "", health.ObservedAt)...)
	b.add("recommended-action", "Recommended action", health.RecommendedAction)
	for _, d := range health.Diagnostics {
		title := d.DisplayName
		if title == "" {
			title = d.Name
		}
		fields := b.field("diagnostic-status", "Status", d.Status, "", d.ObservedAt)
		if d.Value != nil {
			values := b.field("diagnostic-value", d.DisplayName, *d.Value, d.Unit, d.ObservedAt)
			for i := range values {
				values[i].SourceName = safeText(d.Name, 60)
			}
			fields = append(fields, values...)
		}
		fields = append(fields, b.field("recommended-action", "Recommended action", d.RecommendedAction, "", "")...)
		for _, v := range d.Elements {
			if v.Value != nil {
				values := b.field("diagnostic-value", v.DisplayName, *v.Value, v.Unit, v.ObservedAt)
				for i := range values {
					values[i].SourceName = safeText(v.Name, 60)
				}
				fields = append(fields, values...)
			} else {
				fields = append(fields, b.field("diagnostic-status", v.DisplayName, v.Status, v.Unit, v.ObservedAt)...)
			}
		}
		b.item(title, fields)
	}
	return b.done()
}
