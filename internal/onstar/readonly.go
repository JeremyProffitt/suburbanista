package onstar

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Vehicle struct {
	VIN              string          `json:"vin"`
	VehicleID        string          `json:"vehicleId"`
	Make             string          `json:"make"`
	Model            string          `json:"model"`
	Year             json.RawMessage `json:"year"`
	OnstarCapable    bool            `json:"onstarCapable"`
	RoleCode         string          `json:"roleCode"`
	OnstarStatusCode json.RawMessage `json:"onstarStatusCode"`
}

func (c *Client) Vehicles(ctx context.Context, token GMToken) ([]Vehicle, error) {
	if token.AccessToken == "" || token.ExpiresAt <= c.now().Unix() {
		return nil, failure("reauth-required")
	}
	const query = "query getVehiclesMBFF {vehicles {vin vehicleId make model nickName year imageUrl onstarCapable vehicleType roleCode onstarStatusCode onstarAccountNumber preDelivery orderNum orderStatus}}"
	var response struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Vehicles []Vehicle `json:"vehicles"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodPost, APIBase+"/mbff/garage/v1", "text/plain; charset=utf-8", token.AccessToken, strings.NewReader(query), &response); err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 {
		return nil, failure("garage-query-failed")
	}
	if response.Data.Vehicles == nil {
		return nil, failure("invalid-garage-response")
	}
	for _, v := range response.Data.Vehicles {
		if !validVIN(v.VIN) {
			return nil, failure("invalid-garage-response")
		}
	}
	return response.Data.Vehicles, nil
}

// LocationFix uses the v3 telemetry.data.position.lat/lng contract documented by
// the upstream consumer. ReportedAt is telemetry occurredTs, not request time.
type LocationFix struct {
	Latitude   float64    `json:"latitude"`
	Longitude  float64    `json:"longitude"`
	ReportedAt *time.Time `json:"reportedAt,omitempty"`
}

type LocationStatus struct {
	Pending        bool
	ProviderStatus string
	FixAvailable   bool
	FixReason      string
	Fix            *LocationFix
}

func (c *Client) Location(ctx context.Context, token GMToken, vin string, wake bool) (LocationStatus, error) {
	if !token.VehicleLinked || token.VIN != vin || !validVIN(vin) {
		return LocationStatus{}, failure("vehicle-not-linked")
	}
	if token.AccessToken == "" || token.ExpiresAt <= c.now().Unix() {
		return LocationStatus{}, failure("reauth-required")
	}
	sms := "false"
	if wake {
		sms = "true"
	}
	var response struct {
		Telemetry struct {
			OccurredTs json.RawMessage `json:"occurredTs"`
			Data       struct {
				Position struct {
					Lat json.RawMessage `json:"lat"`
					Lng json.RawMessage `json:"lng"`
				} `json:"position"`
				Session struct {
					UpdatePending *string `json:"updatePending"`
				} `json:"session"`
			} `json:"data"`
		} `json:"telemetry"`
	}
	if err := c.request(ctx, http.MethodGet, APIBase+"/veh/datadelivery/digitaltwin/v1/vehicles/"+vin+"?sms="+sms+"&region=na", "application/json", token.AccessToken, nil, &response); err != nil {
		return LocationStatus{}, err
	}
	status := response.Telemetry.Data.Session.UpdatePending
	if status == nil || *status == "" {
		return LocationStatus{}, failure("invalid-location-response")
	}
	out := LocationStatus{Pending: *status == "PENDING", ProviderStatus: *status, FixReason: "fix-schema-unverified"}
	if out.Pending {
		return out, nil
	}
	lat, a := locationNumber(response.Telemetry.Data.Position.Lat)
	lng, b := locationNumber(response.Telemetry.Data.Position.Lng)
	if !a || !b || math.Abs(lat) > 90 || math.Abs(lng) > 180 {
		return out, nil
	}
	out.Fix = &LocationFix{Latitude: lat, Longitude: lng, ReportedAt: locationTime(response.Telemetry.OccurredTs, c.now())}
	out.FixAvailable = true
	out.FixReason = ""
	return out, nil
}

func locationNumber(raw json.RawMessage) (float64, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	n, e := strconv.ParseFloat(s, 64)
	return n, e == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
}
func locationTime(raw json.RawMessage, now time.Time) *time.Time {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		value = string(raw)
	}
	t, e := time.Parse(time.RFC3339Nano, value)
	if e != nil {
		return nil
	}
	if t.IsZero() || t.After(now.Add(time.Minute)) {
		return nil
	}
	t = t.UTC()
	return &t
}
