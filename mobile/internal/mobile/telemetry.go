package mobile

import (
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"math"
	"strconv"
	"time"
)

type TelemetryReading struct {
	Value      float64
	Unit       string
	ObservedAt *time.Time
	Freshness  string
	Available  bool
}
type TelemetryBindings struct{ FuelName, FuelUnit, RangeName, RangeUnit, OdometerName, OdometerUnit string }
type HomeTelemetry struct{ Fuel, Range, Odometer TelemetryReading }

// Verified by the owner's passive diagnostics receipt retained in
// docs/research/alexa-live-status.md. Missing fields remain unavailable; labels,
// warranty mileage and response fetch time are never substituted.
var verifiedTelemetryBindings = TelemetryBindings{
	FuelName: "FUEL_LEVEL", FuelUnit: "%",
	RangeName: "FUEL_RANGE", RangeUnit: "KM",
	OdometerName: "ODO_READ", OdometerUnit: "KM",
}

func telemetryFromSnapshot(snapshot contract.VehicleSnapshot, b TelemetryBindings) HomeTelemetry {
	if snapshot.FeatureID != "diagnostics" {
		return HomeTelemetry{}
	}
	fields := append([]contract.SnapshotField{}, snapshot.Fields...)
	for _, item := range snapshot.Items {
		fields = append(fields, item.Fields...)
	}
	selectValue := func(name, unit string, percent bool) TelemetryReading {
		if name == "" || unit == "" {
			return TelemetryReading{}
		}
		var result TelemetryReading
		matches := 0
		for _, f := range fields {
			if f.SourceName != name || f.Unit != unit {
				continue
			}
			matches++
			value, e := strconv.ParseFloat(f.Value, 64)
			if e != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || (percent && value > 100) {
				return TelemetryReading{}
			}
			displayUnit := unit
			if !percent && unit == "KM" {
				value /= 1.609344
				displayUnit = "mi"
			}
			result = TelemetryReading{Value: value, Unit: displayUnit, ObservedAt: f.ObservedAt, Freshness: f.Freshness, Available: true}
		}
		if matches != 1 {
			return TelemetryReading{}
		}
		return result
	}
	return HomeTelemetry{Fuel: selectValue(b.FuelName, b.FuelUnit, true), Range: selectValue(b.RangeName, b.RangeUnit, false), Odometer: selectValue(b.OdometerName, b.OdometerUnit, false)}
}
