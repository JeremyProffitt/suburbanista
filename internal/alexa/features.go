package alexa

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

// Every entry is a pinned upstream product method. These names select catalog
// rows; they never select arbitrary provider URLs or methods.
var featureIntents = map[string]string{
	"StartIntent": "start", "CancelEngineIntent": "cancel-start", "LockIntent": "lock", "UnlockIntent": "unlock", "LockTrunkIntent": "lock-trunk", "UnlockTrunkIntent": "unlock-trunk", "SoundAlertIntent": "horn-and-lights", "StopAlertsIntent": "cancel-alert", "FlashLightsIntent": "flash-lights", "StopLightsIntent": "stop-lights",
	"DiagnosticsIntent": "diagnostics", "GarageIntent": "garage", "VehicleDetailsIntent": "vehicle-details", "OnStarPlanIntent": "onstar-plan", "RecallsIntent": "recalls", "WarrantyIntent": "warranty", "RadioSubscriptionIntent": "sxm-subscription", "LocationIntent": "location",
	"SetChargeTargetIntent": "ev-set-charge-target", "StopChargingIntent": "ev-stop-charging", "ChargingMetricsIntent": "ev-charging-metrics", "RefreshChargingMetricsIntent": "ev-refresh-charging-metrics",
}

// Preview speech is deliberately independent of live/provider output. It is
// reachable without owner authentication and cannot call the broker.
var featurePreviews = map[string]string{
	"start":                       "Demo. A remote start request would be queued. No vehicle request was sent.",
	"cancel-start":                "Demo. An engine stop request would cancel remote start. No vehicle request was sent.",
	"lock":                        "Demo. A door lock request would be queued. No vehicle request was sent.",
	"unlock":                      "Demo. A door unlock request would be queued. No vehicle request was sent.",
	"lock-trunk":                  "Demo. A cargo lock request would be queued. No vehicle request was sent.",
	"unlock-trunk":                "Demo. A cargo unlock request would be queued. This is not liftgate movement. No vehicle request was sent.",
	"horn-and-lights":             "Demo. The example alert uses horn and lights. No vehicle request was sent.",
	"cancel-alert":                "Demo. An alert stop request would be queued. No vehicle request was sent.",
	"flash-lights":                "Demo. A lights-only alert would be queued. No vehicle request was sent.",
	"stop-lights":                 "Demo. A stop lights request would cancel the alert. No vehicle request was sent.",
	"diagnostics":                 "Demo. Example health shows fuel at seventy five percent and tire pressures available. These are sample readings, not your vehicle.",
	"garage":                      "Demo. The garage contains one sample vehicle. No account or vehicle identifiers are spoken.",
	"vehicle-details":             "Demo. The example vehicle supports remote controls, while individual capabilities still require verification. This is sample data.",
	"onstar-plan":                 "Demo. The sample service plan has remote access. Your actual entitlement has not been read.",
	"recalls":                     "Demo. The sample recall list is empty. This is not a recall check for your vehicle.",
	"warranty":                    "Demo. The sample warranty includes basic coverage. Actual coverage and expiry have not been read.",
	"sxm-subscription":            "Demo. The sample radio subscription is active. Your actual subscription has not been read.",
	"location":                    "Demo. The sample vehicle was at Home five minutes ago. This is a fictional named place, not your vehicle location.",
	"ev-set-charge-target":        "Demo. An electric vehicle target would be eighty percent. Live EV support is unverified. No vehicle request was sent.",
	"ev-stop-charging":            "Demo. An electric vehicle charging stop would be requested. Live EV support is unverified. No vehicle request was sent.",
	"ev-charging-metrics":         "Demo. Sample battery charge is eighty percent. This is fictional EV data, not a reading from your Suburban.",
	"ev-refresh-charging-metrics": "Demo. Electric vehicle charging metrics would refresh. Live EV support is unverified. No vehicle request was sent.",
}

type Slot struct {
	Value       string `json:"value"`
	Resolutions struct {
		Authorities []struct {
			Status struct {
				Code string `json:"code"`
			} `json:"status"`
			Values []struct {
				Value struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"value"`
			} `json:"values"`
		} `json:"resolutionsPerAuthority"`
	} `json:"resolutions"`
}

func slotValue(slot Slot) (string, error) {
	matched := ""
	for _, authority := range slot.Resolutions.Authorities {
		if authority.Status.Code != "ER_SUCCESS_MATCH" {
			continue
		}
		if len(authority.Values) != 1 {
			return "", errors.New("ambiguous slot")
		}
		value := authority.Values[0].Value.ID
		if value == "" {
			value = authority.Values[0].Value.Name
		}
		if matched != "" && matched != value {
			return "", errors.New("ambiguous slot")
		}
		matched = value
	}
	if matched != "" {
		return matched, nil
	}
	return strings.TrimSpace(slot.Value), nil
}
func previewFeature(slots map[string]Slot) string {
	value, err := slotValue(slots["Feature"])
	if err != nil {
		return "Demo. Please choose one feature to preview."
	}
	if _, ok := contract.FindFeature(value); !ok {
		for _, feature := range contract.FeatureCatalog() {
			if strings.EqualFold(value, feature.Title) || strings.EqualFold(value, feature.SourceMethod) {
				value = feature.ID
				break
			}
		}
	}
	if _, ok := contract.FindFeature(value); !ok {
		return "Demo. Say preview followed by a feature, such as vehicle health or remote start."
	}
	if text, ok := featurePreviews[value]; ok {
		return text
	}
	return "Demo. This feature has no example yet. No vehicle request was sent."
}
func parseOptions(action string, slots map[string]Slot) (*contract.CommandOptions, error) {
	options := contract.CommandOptions{}
	provided := false
	for name, slot := range slots {
		value, err := slotValue(slot)
		if err != nil {
			return nil, err
		}
		if value == "" {
			continue
		}
		provided = true
		switch name {
		case "Temperature", "Delay", "Duration":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, errors.New("integer parameter required")
			}
			switch name {
			case "Temperature":
				options.CabinTemperature = &number
			case "Delay":
				options.Delay = &number
			case "Duration":
				options.Duration = &number
			}
		case "AlertAction":
			switch strings.ToLower(value) {
			case "honk", "horn":
				options.Actions = []string{"Honk"}
			case "flash", "lights":
				options.Actions = []string{"Flash"}
			case "both", "horn and lights":
				options.Actions = []string{"Honk", "Flash"}
			default:
				return nil, errors.New("unsupported alert action")
			}
		default:
			return nil, errors.New("unsupported command parameter")
		}
	}
	if err := contract.ValidateCommandOptions(action, options); err != nil {
		return nil, err
	}
	if !provided {
		return nil, nil
	}
	return &options, nil
}
func chargeTargetValid(value string) bool {
	number, err := strconv.Atoi(value)
	if err != nil {
		return false
	}
	feature, ok := contract.FindFeature("ev-set-charge-target")
	if !ok {
		return false
	}
	for _, parameter := range feature.Parameters {
		if parameter.Name == "chargeTarget" {
			return (parameter.Minimum == nil || number >= *parameter.Minimum) && (parameter.Maximum == nil || number <= *parameter.Maximum)
		}
	}
	return false
}
func snapshotSpeech(snapshot *contract.VehicleSnapshot, featureID string, now time.Time) string {
	if snapshot == nil || snapshot.FeatureID != featureID {
		return "That vehicle information is unavailable. Check the app before trying again."
	}
	feature, ok := contract.FindFeature(featureID)
	if !ok {
		return "That feature is unavailable."
	}
	if snapshot.State != "available" && snapshot.State != "partial" {
		return feature.Title + " is unavailable. " + snapshotAge(snapshot, now) + " Check the app for the reason."
	}
	var text string
	switch featureID {
	case "diagnostics":
		text = diagnosticSummary(snapshot, now)
	case "garage":
		text = "The enrolled vehicle's garage information is available."
		makeName := map[string]string{"chevrolet": "Chevrolet", "chevy": "Chevrolet", "gmc": "GMC", "buick": "Buick", "cadillac": "Cadillac", "gm": "GM"}[strings.ToLower(fieldValue(snapshot.Fields, "make"))]
		model := map[string]string{"suburban": "Suburban", "suburban 1500": "Suburban 1500", "suburban 2500": "Suburban 2500"}[strings.ToLower(fieldValue(snapshot.Fields, "model"))]
		year, err := strconv.Atoi(fieldValue(snapshot.Fields, "year"))
		if err == nil && year >= 1900 && year <= 2100 && makeName != "" && model != "" {
			text = fmt.Sprintf("The enrolled vehicle is a %d %s %s.", year, makeName, model)
		}
	case "vehicle-details":
		if len(snapshot.Eligibility) == 0 {
			text = "Vehicle details are available, but command eligibility was not reported."
			break
		}
		eligible := 0
		for _, row := range snapshot.Eligibility {
			if row.Eligible != nil && *row.Eligible {
				eligible++
			}
		}
		text = fmt.Sprintf("Vehicle details report %d eligible command types. Eligibility does not confirm command completion.", eligible)
	case "onstar-plan":
		text = coverageSummary(snapshot, "plan", "plan-status", []string{"expiry-date", "end-date"})
	case "recalls":
		text = "The returned recall count is unavailable."
		if count, ok := safeCount(fieldValue(snapshot.Fields, "recall-count")); ok {
			text = fmt.Sprintf("The response contains %d recall entries. This is not a safety clearance.", count)
		}
	case "warranty":
		text = coverageSummary(snapshot, "warranty", "warranty-status", []string{"expiration-date"})
		if count, ok := safeCount(fieldValue(snapshot.Fields, "warranty-count")); ok {
			text = fmt.Sprintf("The response contains %d warranty entries. ", count) + text
		}
	case "sxm-subscription":
		text = "Satellite radio subscription information is available."
		if status := safeStatus(fieldValue(snapshot.Fields, "sxm-status")); status != "" {
			text = "Radio subscription status: " + status + "."
		}
		switch fieldValue(snapshot.Fields, "suspended") {
		case "true":
			text += " The subscription is suspended."
		case "false":
			text += " The subscription is not suspended."
		}
		for _, item := range snapshot.Items {
			if date := safeDate(fieldValue(item.Fields, "end-date")); date != "" {
				text += " One listed subscription ends " + date + "."
				break
			}
		}
	case "location":
		text = "A current location fix has not been verified. No location or address can be reported."
	default:
		return "That vehicle information is unavailable."
	}
	if snapshot.State == "partial" {
		text = "Partial response. " + text
	}
	return text + " " + snapshotAge(snapshot, now) + " Check the app for details."
}

// Voice uses fixed labels, bounded numbers, enumerated statuses and parsed dates.
// Provider names, descriptions, identifiers and arbitrary unit strings stay silent.
var spokenNumber = regexp.MustCompile(`^[0-9]{1,7}(?:\.[0-9]{1,2})?$`)

func fieldValue(fields []contract.SnapshotField, key string) string {
	for _, field := range fields {
		if field.Key == key {
			return strings.TrimSpace(field.Value)
		}
	}
	return ""
}
func safeCount(value string) (int, bool) {
	if !regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(value) {
		return 0, false
	}
	n, e := strconv.Atoi(value)
	return n, e == nil
}
func safeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "good", "ok", "normal", "active", "inactive", "expired", "pending", "trial", "suspended", "cancelled", "canceled", "warning", "critical", "attention", "poor", "fair", "bad":
		return strings.ToLower(strings.TrimSpace(value))
	case "requires_attention", "requires attention":
		return "attention required"
	}
	return ""
}
func safeDate(value string) string {
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if date, e := time.Parse(layout, value); e == nil && date.Year() >= 1900 && date.Year() <= 2100 {
			return date.Format("January 2, 2006")
		}
	}
	return ""
}
func spokenMeasurement(label, value, unit string) (string, bool) {
	label = strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(label))
	unit = strings.ToLower(strings.TrimSpace(unit))
	name := map[string]string{"fuel": "fuel level", "fuellevel": "fuel level", "fuelremaining": "fuel level", "oillife": "oil life", "remainingoillife": "oil life", "odometer": "odometer", "mileage": "odometer", "battery": "battery", "batterylevel": "battery", "batterycharge": "battery", "batteryvoltage": "battery", "tirepressure": "tire pressure", "frontleft": "front left tire pressure", "frontlefttirepressure": "front left tire pressure", "leftfronttirepressure": "front left tire pressure", "frontright": "front right tire pressure", "frontrighttirepressure": "front right tire pressure", "rightfronttirepressure": "front right tire pressure", "rearleft": "rear left tire pressure", "rearlefttirepressure": "rear left tire pressure", "leftreartirepressure": "rear left tire pressure", "rearright": "rear right tire pressure", "rearrighttirepressure": "rear right tire pressure", "rightreartirepressure": "rear right tire pressure"}[label]
	if name == "" || !spokenNumber.MatchString(value) {
		return "", false
	}
	number, e := strconv.ParseFloat(value, 64)
	if e != nil {
		return "", false
	}
	spokenUnit := ""
	limit := 0.0
	switch {
	case name == "fuel level" || name == "oil life" || name == "battery":
		if unit == "%" || unit == "percent" {
			spokenUnit = "percent"
			limit = 100
		} else if name == "battery" && (unit == "v" || unit == "volts") {
			spokenUnit = "volts"
			limit = 60
		}
	case name == "odometer":
		switch unit {
		case "mi", "mile", "miles":
			spokenUnit = "miles"
		case "km", "kilometers", "kilometres":
			spokenUnit = "kilometers"
		}
		limit = 2000000
	case strings.HasSuffix(name, "tire pressure"):
		if unit == "psi" {
			spokenUnit = "psi"
			limit = 200
		} else if unit == "kpa" {
			spokenUnit = "kilopascals"
			limit = 1500
		}
	}
	if spokenUnit == "" || number > limit {
		return "", false
	}
	return name + " " + strconv.FormatFloat(number, 'f', -1, 64) + " " + spokenUnit, true
}
func diagnosticSummary(snapshot *contract.VehicleSnapshot, now time.Time) string {
	parts := []string{}
	if status := safeStatus(fieldValue(snapshot.Fields, "health-status")); status != "" {
		parts = append(parts, "Reported health: "+status+".")
	}
	readings := 0
	seen := map[string]bool{}
	for _, item := range snapshot.Items {
		for _, field := range item.Fields {
			if field.Key != "diagnostic-value" || readings >= 3 {
				continue
			}
			label := field.Label
			if label == "" || strings.EqualFold(label, "value") || strings.EqualFold(label, "level") {
				label = item.Title
			}
			if reading, ok := spokenMeasurement(label, field.Value, field.Unit); ok && !seen[reading] {
				age := "This reading's source age is unknown."
				if field.ObservedAt != nil && !field.ObservedAt.IsZero() && !field.ObservedAt.After(now) {
					age = "This reading was observed " + ageWords(now.Sub(*field.ObservedAt)) + " ago."
				}
				parts = append(parts, "Reported "+reading+". "+age)
				seen[reading] = true
				readings++
			}
		}
	}
	if len(parts) == 0 {
		return "No recognized health readings are available for voice."
	}
	return strings.Join(parts, " ")
}
func coverageSummary(snapshot *contract.VehicleSnapshot, kind, statusKey string, dateKeys []string) string {
	for _, item := range snapshot.Items {
		parts := []string{}
		if status := safeStatus(fieldValue(item.Fields, statusKey)); status != "" {
			parts = append(parts, "One returned "+kind+" is "+status+".")
		}
		for _, key := range dateKeys {
			if date := safeDate(fieldValue(item.Fields, key)); date != "" {
				parts = append(parts, "Listed end date "+date+".")
				break
			}
		}
		if kind == "warranty" {
			for _, field := range item.Fields {
				if field.Key == "end-mileage" {
					if text, ok := spokenMeasurement("odometer", field.Value, field.Unit); ok {
						parts = append(parts, "Listed mileage limit: "+strings.TrimPrefix(text, "odometer ")+".")
					}
					break
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	return "No recognized " + kind + " status or dates are available for voice."
}

// These source names and units are verified in the owner's passive diagnostics
// and upstream ICE fixtures. Command history, trip counters, labels and engine
// health fields never stand in for current physical lock or engine state.
func vehicleStatusSpeech(snapshot *contract.VehicleSnapshot, now time.Time) string {
	if snapshot == nil || snapshot.FeatureID != "diagnostics" || (snapshot.State != "available" && snapshot.State != "partial") {
		return "Vehicle status is unavailable right now."
	}
	type reading struct {
		value  float64
		at     *time.Time
		found  bool
		native bool
	}
	var readings [3]reading
	fields := append([]contract.SnapshotField{}, snapshot.Fields...)
	for _, item := range snapshot.Items {
		fields = append(fields, item.Fields...)
	}
	for _, field := range fields {
		if field.Key != "diagnostic-value" {
			continue
		}
		index, unit := -1, ""
		switch field.SourceName {
		case "FUEL_LEVEL":
			index, unit = 0, "%"
		case "FUEL_RANGE":
			index, unit = 1, "KM"
		case "FUEL_RANGE_MI":
			index, unit = 1, "MI"
		case "ODO_READ":
			index, unit = 2, "KM"
		case "ODO_READ_MI":
			index, unit = 2, "MI"
		}
		if index < 0 || field.Unit != unit {
			continue
		}
		value, err := strconv.ParseFloat(field.Value, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			continue
		}
		if unit == "KM" {
			value /= 1.609344
		}
		if value > []float64{100, 2000, 2000000}[index] {
			continue
		}
		at := field.ObservedAt
		if at != nil && (at.IsZero() || at.After(now)) {
			at = nil
		}
		previous := readings[index]
		// Prefer newer observations; for equal times prefer provider miles over
		// conversion. Never replace a dated reading with one of unknown age.
		newer := at != nil && (previous.at == nil || at.After(*previous.at))
		sameTime := at == nil && previous.at == nil || at != nil && previous.at != nil && at.Equal(*previous.at)
		if previous.found && !newer && !(sameTime && unit == "MI" && !previous.native) {
			continue
		}
		readings[index] = reading{value, at, true, unit == "MI"}
	}
	parts := []string{}
	for i, label := range []string{"Fuel", "Estimated range", "Odometer"} {
		r := readings[i]
		if !r.found {
			parts = append(parts, label+" unavailable.")
			continue
		}
		value, unit := strconv.FormatFloat(math.Round(r.value), 'f', 0, 64), "miles"
		if i == 0 {
			value, unit = strconv.FormatFloat(math.Round(r.value*10)/10, 'f', -1, 64), "percent"
		}
		age := "source age unknown"
		if r.at != nil {
			duration := now.Sub(*r.at)
			words := ageWords(duration)
			if hours := int(duration / time.Hour); hours > 0 {
				words = fmt.Sprintf("%d hours", hours)
				if hours == 1 {
					words = "1 hour"
				}
			}
			age = "reported " + words + " ago"
		}
		parts = append(parts, label+" "+value+" "+unit+", "+age+".")
	}
	return strings.Join(parts, " ")
}

func snapshotAge(snapshot *contract.VehicleSnapshot, now time.Time) string {
	if snapshot.ObservedAt != nil && !snapshot.ObservedAt.IsZero() && !snapshot.ObservedAt.After(now) {
		return "Source observed " + ageWords(now.Sub(*snapshot.ObservedAt)) + " ago."
	}
	if !snapshot.FetchedAt.IsZero() && !snapshot.FetchedAt.After(now) {
		return "Retrieved " + ageWords(now.Sub(snapshot.FetchedAt)) + " ago; source age is unknown."
	}
	return "Source age is unknown."
}
func ageWords(age time.Duration) string {
	seconds := int64(age / time.Second)
	if seconds == 1 {
		return "1 second"
	}
	if seconds < 60 {
		return fmt.Sprintf("%d seconds", seconds)
	}
	if seconds/60 == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", seconds/60)
}
func availabilitySpeech(rows []contract.FeatureAvailability) string {
	if len(rows) == 0 {
		return "Your owner account was checked, but current feature availability is unavailable. Check the app."
	}
	actions, reads := 0, 0
	for _, row := range rows {
		feature, ok := contract.FindFeature(row.ID)
		if !ok || !row.Available {
			continue
		}
		if feature.Category == "action" {
			actions++
		}
		if feature.Category == "read" {
			reads++
		}
	}
	return fmt.Sprintf("Your owner account was checked. %d command types and %d read features are currently available. This does not prove the vehicle is online.", actions, reads)
}
