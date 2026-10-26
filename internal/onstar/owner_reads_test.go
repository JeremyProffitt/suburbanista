package onstar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	synthetic "github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
)

func TestOrdinaryReadWireAndSafeSnapshots(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	stamp := now.Add(-time.Minute).Format(time.RFC3339)
	cases := []struct{ id, query, body string }{
		{"vehicle-details", "getMbffGarageVehicleDetails", `{"data":{"vehicleDetails":{"vin":"` + synthetic.VIN + `","make":"Chevrolet","model":"Suburban","year":2027,"ownerAccount":"synthetic-private-account","vehicleCommands":[{"name":"cancelStart","isEligible":true,"url":"https://private.invalid/provider","serviceId":"synthetic-private-id"}]}}}`},
		{"onstar-plan", "getPlanInfo", `{"data":{"vehicleDetails":{"planExpiryInfo":[{"planName":"Synthetic plan","status":"ACTIVE","startDate":"2026-10-22","endDate":"2027-10-22","additionalInfo":{"radioId":"synthetic-private-radio"}}]}}}`},
		{"recalls", "getVehicleRecallInfo", `{"data":{"vehicleDetails":{"recallInfo":[{"title":"Synthetic recall","recallId":"synthetic-private-id","recallStatus":"OPEN","repairStatus":"PENDING"}]}}}`},
		{"warranty", "getWarrantyInfo", `{"data":{"vehicleDetails":{"warrantyInfo":[{"typeDescription":"Synthetic coverage","startMileage":0,"endMileage":36000,"mileageUnit":"mi","expirationDate":"2027-10-22","status":"ACTIVE"}]}}}`},
		{"sxm-subscription", "getSxmSubscriptionInfo", `{"data":{"vehicleDetails":{"sxmSubscriptionInfo":{"deviceId":"synthetic-private-device","status":"ACTIVE","channelAccountInfo":{"phoneNumber":"555-555-0123","radioId":"synthetic-private-radio"},"subscriptions":[{"subscriptionName":"Synthetic package","startDate":"2026-10-22","endDate":"2027-10-22","services":["Audio"]}]}}}}`},
		{"diagnostics", "", `{"status":"GOOD","cts":"` + stamp + `","diagnostics":[{"name":"OIL","displayName":"Oil","status":"GOOD","diagnosticElements":[{"name":"OIL_LIFE","displayName":"Oil life","value":"75","uom":"%","cts":"` + stamp + `"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			calls := 0
			c := fakeClient(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.query == "" {
					if r.Method != "GET" || r.URL.String() != APIBase+"/api/v1/vh/vehiclehealth/v1/healthstatus/"+synthetic.VIN {
						t.Fatal("wrong diagnostics wire")
					}
				} else {
					body, _ := io.ReadAll(r.Body)
					if r.Method != "POST" || r.URL.String() != APIBase+"/mbff/garage/v1" || r.Header.Get("Content-Type") != "text/plain; charset=utf-8" || !strings.Contains(string(body), "query "+tc.query) || !strings.Contains(string(body), `vehicleDetails(vin: "`+synthetic.VIN+`")`) {
						t.Fatal("wrong pinned GraphQL wire")
					}
				}
				return response(tc.body, 200), nil
			})
			snapshot, e := c.ReadSnapshot(context.Background(), linkedToken(), synthetic.VIN, tc.id)
			if e != nil || snapshot.State != "available" || snapshot.FeatureID != tc.id || !snapshot.FetchedAt.Equal(now) || calls != 1 {
				t.Fatal("read snapshot failed", e)
			}
			encoded, _ := json.Marshal(snapshot)
			for _, private := range []string{synthetic.VIN, "synthetic-private", "private.invalid", "555-555-0123"} {
				if strings.Contains(string(encoded), private) {
					t.Fatal("private read field escaped")
				}
			}
			if tc.id == "diagnostics" {
				if snapshot.ObservedAt == nil || snapshot.Freshness != "fresh" || snapshot.Items[0].Fields[0].Freshness != "unknown" {
					t.Fatal("observation timestamps fabricated")
				}
			} else if snapshot.ObservedAt != nil || snapshot.Freshness != "unknown" {
				t.Fatal("fetch time claimed as observation")
			}
		})
	}
}
func TestReadPartialAbsentAndAmbiguousEligibility(t *testing.T) {
	c := fakeClient(func(*http.Request) (*http.Response, error) {
		return response(`{"errors":[{"message":"synthetic-private-error"}],"data":{"vehicleDetails":{"planExpiryInfo":[{"planName":"Synthetic","status":"ACTIVE"}]}}}`, 200), nil
	})
	snapshot, e := c.ReadSnapshot(context.Background(), linkedToken(), synthetic.VIN, "onstar-plan")
	if e != nil || snapshot.State != "partial" || snapshot.Reason != "partial-provider-response" {
		t.Fatal("partial plan not explicit")
	}
	if _, e = c.ReadSnapshot(context.Background(), linkedToken(), synthetic.VIN, "vehicle-details"); kind(e) != "garage-query-failed" {
		t.Fatal("partial entitlement accepted")
	}
	c = fakeClient(func(*http.Request) (*http.Response, error) {
		return response(`{"data":{"vehicleDetails":{"warrantyInfo":null}}}`, 200), nil
	})
	snapshot, e = c.ReadSnapshot(context.Background(), linkedToken(), synthetic.VIN, "warranty")
	if e != nil || snapshot.State != "unavailable" {
		t.Fatal("absent warranty fabricated")
	}
	for _, rows := range []string{`{"name":"cancelStart","isEligible":true},{"name":"cancelStart","isEligible":false}`, `{"name":"cancelStart","isEligible":false},{"name":"cancelStart","isEligible":true}`, `{"name":"cancelStart","isEligible":true},{"name":"cancelStart"}`} {
		c = fakeClient(func(*http.Request) (*http.Response, error) {
			return response(`{"data":{"vehicleDetails":{"vehicleCommands":[`+rows+`]}}}`, 200), nil
		})
		snapshot, e = c.ReadSnapshot(context.Background(), linkedToken(), synthetic.VIN, "vehicle-details")
		if e != nil || len(snapshot.Eligibility) != 1 || snapshot.Eligibility[0].Eligible != nil || snapshot.Eligibility[0].Reason != "eligibility-ambiguous" {
			t.Fatal("conflicting eligibility became permission")
		}
	}
}
func TestSnapshotPrivacyPreservesDatesAndBounds(t *testing.T) {
	if safeText("2026-10-22", 200) != "2026-10-22" {
		t.Fatal("date redacted as phone")
	}
	for _, value := range []string{synthetic.VIN, "owner@example.invalid", "https://private.invalid/secret", "555-555-0123", strings.Join([]string{"Bearer", "synthetic-token"}, " "), "00000000-0000-4000-8000-000000000001"} {
		if strings.Contains(safeText(value, 200), value) {
			t.Fatal("private text retained")
		}
	}
	b := newSnapshot("diagnostics", time.Unix(1800000000, 0))
	for i := 0; i < 1000; i++ {
		b.add("value", "Value", strings.Repeat("x", 200))
	}
	encoded, _ := json.Marshal(b.done())
	if len(encoded) > 65536 || b.done().State != "partial" {
		t.Fatal("snapshot bound missing")
	}
}
func TestOwnerReadBindingAndLocationNoWake(t *testing.T) {
	calls := 0
	c := fakeClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("sms") != "false" {
			t.Fatal("passive location woke vehicle")
		}
		return response(`{"telemetry":{"data":{"session":{"updatePending":"PENDING"}}}}`, 200), nil
	})
	if _, e := c.ReadSnapshot(context.Background(), linkedToken(), synthetic.OtherVIN, "garage"); e == nil || calls != 0 {
		t.Fatal("wrong vehicle read permitted")
	}
	snap, e := c.ReadSnapshot(context.Background(), linkedToken(), synthetic.VIN, "location")
	if e != nil || snap.State != "partial" || snap.ObservedAt != nil || snap.Reason != "fix-schema-unverified" || calls != 1 {
		t.Fatal("location fix fabricated")
	}
}

func TestLocationCoordinatesRequireCompleteNonPendingResponse(t *testing.T) {
	for _, tc := range []struct {
		pending, lat, lng string
		want              bool
	}{
		{"COMPLETE", `"0"`, `"0"`, true}, {"COMPLETE", `0`, `0`, true},
		{"PENDING", `0`, `0`, false}, {"COMPLETE", `null`, `0`, false},
		{"COMPLETE", `"NaN"`, `0`, false}, {"COMPLETE", `0`, `"Infinity"`, false},
	} {
		c := fakeClient(func(*http.Request) (*http.Response, error) {
			return response(`{"telemetry":{"occurredTs":"2026-01-01T00:00:00Z","data":{"position":{"lat":`+tc.lat+`,"lng":`+tc.lng+`},"session":{"updatePending":"`+tc.pending+`"}}}}`, 200), nil
		})
		got, e := c.Location(context.Background(), linkedToken(), synthetic.VIN, false)
		if e != nil || got.FixAvailable != tc.want || (got.Fix != nil) != tc.want {
			t.Fatal("location validation failed")
		}
	}
}
