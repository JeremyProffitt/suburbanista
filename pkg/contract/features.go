package contract

import (
	"errors"
	"time"
)

type FeatureParameter struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Unit    string   `json:"unit,omitempty"`
	Minimum *int     `json:"minimum,omitempty"`
	Maximum *int     `json:"maximum,omitempty"`
	Choices []string `json:"choices,omitempty"`
}
type Feature struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	Category     string             `json:"category"`
	SourceMethod string             `json:"sourceMethod"`
	Action       string             `json:"action,omitempty"`
	ReadID       string             `json:"readId,omitempty"`
	Section      string             `json:"section"`
	Parameters   []FeatureParameter `json:"parameters"`
	Destinations []string           `json:"destinations"`
	ProofState   string             `json:"proofState"`
	Limitation   string             `json:"limitation,omitempty"`
}
type FeatureAvailability struct {
	ID         string `json:"id"`
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
	Eligible   *bool  `json:"eligible,omitempty"`
	ProofState string `json:"proofState"`
}
type FeaturesResponse struct {
	Features     []Feature             `json:"features"`
	Availability []FeatureAvailability `json:"availability"`
	FetchedAt    time.Time             `json:"fetchedAt"`
}
type CommandOptions struct {
	CabinTemperature *int     `json:"cabinTemperature,omitempty"`
	Delay            *int     `json:"delay,omitempty"`
	Duration         *int     `json:"duration,omitempty"`
	Actions          []string `json:"actions,omitempty"`
}
type SnapshotField struct {
	SourceName string     `json:"sourceName,omitempty"`
	Key        string     `json:"key"`
	Label      string     `json:"label"`
	Value      string     `json:"value"`
	Unit       string     `json:"unit,omitempty"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	Freshness  string     `json:"freshness"`
}
type SnapshotItem struct {
	Title  string          `json:"title"`
	Fields []SnapshotField `json:"fields"`
}
type CommandEligibility struct {
	Action   string `json:"action"`
	Eligible *bool  `json:"eligible,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
type VehicleSnapshot struct {
	FeatureID   string               `json:"featureId"`
	State       string               `json:"state"`
	Reason      string               `json:"reason,omitempty"`
	FetchedAt   time.Time            `json:"fetchedAt"`
	ObservedAt  *time.Time           `json:"observedAt,omitempty"`
	Freshness   string               `json:"freshness"`
	Fields      []SnapshotField      `json:"fields"`
	Items       []SnapshotItem       `json:"items"`
	Eligibility []CommandEligibility `json:"eligibility,omitempty"`
}

func intValue(v int) *int { return &v }

// FeatureCatalog is public source metadata, never a claim of vehicle eligibility.
// Every call returns independent slices so one client cannot alter another's list.
func FeatureCatalog() []Feature {
	delay := []FeatureParameter{{Name: "delay", Type: "integer", Unit: "provider-defined", Minimum: intValue(0)}}
	alert := []FeatureParameter{{Name: "actions", Type: "string-list", Choices: []string{"Honk", "Flash"}}, {Name: "delay", Type: "integer", Unit: "provider-defined", Minimum: intValue(0)}, {Name: "duration", Type: "integer", Unit: "provider-defined", Minimum: intValue(1)}}
	action := func(id, title, method string, params []FeatureParameter) Feature {
		return Feature{ID: id, Title: title, Category: "action", SourceMethod: method, Action: id, Section: "controls", Parameters: params, Destinations: []string{"android", "alexa"}, ProofState: "transport-tested"}
	}
	read := func(id, title, method, section string) Feature {
		return Feature{ID: id, Title: title, Category: "read", SourceMethod: method, ReadID: id, Section: section, Parameters: []FeatureParameter{}, Destinations: []string{"android", "alexa"}, ProofState: "source-known"}
	}
	ev := func(id, title, method string, params []FeatureParameter) Feature {
		return Feature{ID: id, Title: title, Category: "ev", SourceMethod: method, Section: "ev", Parameters: params, Destinations: []string{"android", "alexa"}, ProofState: "source-known", Limitation: "Vehicle applicability and separate EV authorization are not established."}
	}
	features := []Feature{
		action("start", "Remote start", "start", []FeatureParameter{{Name: "cabinTemperature", Type: "integer", Unit: "celsius"}}),
		action("cancel-start", "Cancel remote start", "cancelStart", nil), action("lock", "Lock doors", "lockDoor", delay), action("unlock", "Unlock doors", "unlockDoor", delay), action("lock-trunk", "Lock trunk", "lockTrunk", delay), action("unlock-trunk", "Unlock trunk", "unlockTrunk", delay), action("horn-and-lights", "Horn and lights", "alert", alert), action("cancel-alert", "Stop alerts", "cancelAlert", nil), action("flash-lights", "Flash lights", "flashLights", alert), action("stop-lights", "Stop lights", "stopLights", nil),
		read("diagnostics", "Vehicle health", "diagnostics", "diagnostics"), read("garage", "Garage", "getAccountVehicles", "vehicle"), read("vehicle-details", "Vehicle details", "getVehicleDetails", "vehicle"), read("onstar-plan", "OnStar plan", "getOnstarPlan", "plans"), read("recalls", "Recalls", "getVehicleRecallInfo", "vehicle"), read("warranty", "Warranty", "getWarrantyInfo", "vehicle"), read("sxm-subscription", "SiriusXM subscription", "getSxmSubscriptionInfo", "plans"), read("location", "Vehicle location", "location", "location"),
		ev("ev-set-charge-target", "EV charge target", "setChargeLevelTarget", []FeatureParameter{{Name: "chargeTarget", Type: "integer", Unit: "percent", Minimum: intValue(1), Maximum: intValue(100)}}), ev("ev-stop-charging", "Stop EV charging", "stopCharging", nil), ev("ev-charging-metrics", "EV charging metrics", "getEVChargingMetrics", nil), ev("ev-refresh-charging-metrics", "Refresh EV metrics", "refreshEVChargingMetrics", nil),
	}
	for i := range features {
		f := &features[i]
		f.Parameters = append([]FeatureParameter{}, f.Parameters...)
		for j := range f.Parameters {
			f.Parameters[j].Choices = append([]string(nil), f.Parameters[j].Choices...)
		}
		if f.ID == "unlock-trunk" {
			f.Limitation = "Unlock does not imply powered liftgate motion."
		}
		if f.ID == "stop-lights" {
			f.Limitation = "Uses the same cancel-alert endpoint."
		}
		if f.ID == "location" {
			f.Limitation = "Pending status is supported; coordinates and fix timestamp remain unverified."
		}
		if f.ID == "flash-lights" {
			f.Parameters[0].Choices = []string{"Flash"}
		}
	}
	return features
}
func FindFeature(id string) (Feature, bool) {
	for _, f := range FeatureCatalog() {
		if f.ID == id {
			return f, true
		}
	}
	return Feature{}, false
}
func ValidateCommandOptions(action string, options CommandOptions) error {
	feature, ok := FindFeature(action)
	if !ok || feature.Category != "action" {
		return errors.New("unsupported-action")
	}
	door := action == "lock" || action == "unlock" || action == "lock-trunk" || action == "unlock-trunk"
	alert := action == "horn-and-lights" || action == "flash-lights"
	if options.CabinTemperature != nil && action != "start" || options.Delay != nil && !door && !alert || options.Duration != nil && !alert || len(options.Actions) > 0 && !alert {
		return errors.New("invalid-command-options")
	}
	if options.Delay != nil && *options.Delay < 0 || options.Duration != nil && *options.Duration <= 0 {
		return errors.New("invalid-command-options")
	}
	seen := map[string]bool{}
	for _, value := range options.Actions {
		if (value != "Honk" && value != "Flash") || seen[value] || (action == "flash-lights" && value != "Flash") {
			return errors.New("invalid-command-options")
		}
		seen[value] = true
	}
	return nil
}

func FreshnessAt(observed *time.Time, now time.Time) string {
	if observed == nil || observed.IsZero() || observed.After(now) {
		return "unknown"
	}
	if now.Sub(*observed) > 15*time.Minute {
		return "stale"
	}
	return "fresh"
}
