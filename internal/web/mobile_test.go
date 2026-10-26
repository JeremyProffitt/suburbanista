package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/locationmap"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
)

type emptyDocuments struct{}

func (emptyDocuments) Load(context.Context, string) (auth.Document, error) {
	return auth.Document{}, auth.ErrMissing
}

func TestPhoneLocationUploadRequiresOwnerAndStrictBody(t *testing.T) {
	now := time.Now().UTC()
	family, issued, err := auth.NewFamily("synthetic-owner", "synthetic-owner", "synthetic-owner", "synthetic-phone", now)
	if err != nil {
		t.Fatal(err)
	}
	fd, _ := json.Marshal(struct {
		Family      auth.Family
		ValidatedAt time.Time
	}{family, now})
	ad, _ := json.Marshal(struct {
		FamilyID  string
		ExpiresAt time.Time
	}{family.ID, issued.AccessExpiresAt})
	hash := sha256.Sum256([]byte(issued.AccessHandle))
	docs := sessionDocuments{map[string]auth.Document{"family#" + family.ID: {Data: fd, ExpiresAt: family.Deadline}, "access#" + hex.EncodeToString(hash[:]): {Data: ad, ExpiresAt: issued.AccessExpiresAt}}}
	sample := locationmap.PhoneSample{Latitude: 0, Longitude: 0, ObservedAt: now.Add(-time.Second), AccuracyMeters: 5}
	body, _ := json.Marshal(sample)
	for _, tc := range []struct {
		name, body, query string
		owner             bool
		want              int
	}{
		{"owner", string(body), "", true, 200},
		{"anonymous", string(body), "", false, 401},
		{"map-key-only", string(body), "key=" + strings.Repeat("a", 43), false, 401},
		{"owner-query", string(body), "latitude=0", true, 400},
		{"owner-map-key", string(body), "key=" + strings.Repeat("a", 43), true, 400},
		{"family-spoof", strings.TrimSuffix(string(body), "}") + `,"familyId":"other"}`, "", true, 400},
		{"unknown-field", strings.TrimSuffix(string(body), "}") + `,"other":1}`, "", true, 400},
		{"malformed", `{"latitude":`, "", true, 400},
		{"trailing-json", string(body) + `{}`, "", true, 400},
		{"oversize", string(body) + strings.Repeat(" ", 1025), "", true, 400},
		{"null", `null`, "", true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			writer := func(_ context.Context, gotFamily string, got locationmap.PhoneSample) (locationmap.PhoneResult, error) {
				calls++
				if gotFamily != family.ID || got != sample {
					t.Error("phone sample or authenticated family changed")
				}
				return locationmap.PhoneResult{Accepted: true, Synced: true, ObservationRevision: "synthetic-revision"}, nil
			}
			adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: docs, Owner: "synthetic-owner", Now: func() time.Time { return now }}, Provider: &state.Store{}, PhoneLocations: writer})
			if err != nil {
				t.Fatal(err)
			}
			event := request(t)
			event.RawPath = "/api/location/phone"
			event.RawQueryString = tc.query
			event.RequestContext.HTTP.Method = "POST"
			event.Body = tc.body
			event.Cookies = nil
			event.Headers = map[string]string{"content-type": "application/json"}
			if tc.owner {
				event.Headers["authorization"] = "Bearer " + issued.AccessHandle
			}
			result, err := adapter.Handle(context.Background(), event)
			if err != nil || result.StatusCode != tc.want {
				t.Fatalf("phone upload status=%d want=%d error=%v", result.StatusCode, tc.want, err)
			}
			if result.Headers["cache-control"] != "private, no-store" {
				t.Fatal("phone response may be cached")
			}
			if tc.want != 200 && calls != 0 {
				t.Fatal("invalid phone upload reached writer")
			}
			if tc.want == 200 {
				var reply locationmap.PhoneResult
				if calls != 1 || json.Unmarshal([]byte(result.Body), &reply) != nil || !reply.Accepted || !reply.Synced || reply.ObservationRevision != "synthetic-revision" {
					t.Fatal("phone result not returned")
				}
			}
		})
	}
}
func (emptyDocuments) Commit(context.Context, ...auth.Document) error { return nil }
func TestMobileHTTPContract(t *testing.T) {
	service := &auth.MobileService{Store: emptyDocuments{}, Owner: "synthetic-owner", Now: func() time.Time { return time.Unix(1800000000, 0) }}
	adapter, e := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: service, Provider: &state.Store{}})
	if e != nil {
		t.Fatal(e)
	}
	challenge := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	body, _ := json.Marshal(map[string]string{"challenge": challenge, "deviceLabel": "synthetic-phone"})
	event := request(t)
	event.RawPath = "/auth/mobile/start"
	event.RawQueryString = ""
	event.RequestContext.HTTP.Method = "POST"
	event.Body = string(body)
	event.Headers = map[string]string{"content-type": "application/json"}
	event.Cookies = nil
	event.RequestContext.HTTP.SourceIP = "192.0.2.1"
	response, e := adapter.Handle(context.Background(), event)
	if e != nil || response.StatusCode != 200 {
		t.Fatal("mobile start failed", response.StatusCode)
	}
	var start auth.MobileStart
	if json.Unmarshal([]byte(response.Body), &start) != nil || start.PollIntervalSeconds != 3 || len(start.Code) != 43 || start.BrowserURL != "https://suburban.jeremy.ninja/auth/mobile/begin?code="+start.Code || start.ExpiresAt.IsZero() {
		t.Fatal("start contract wrong")
	}
	if len(response.Cookies) != 0 || response.Headers["cache-control"] != "no-store" {
		t.Fatal("mobile start issued cookie or cached response")
	}
	event.Headers["origin"] = "https://attacker.invalid"
	response, _ = adapter.Handle(context.Background(), event)
	if response.StatusCode != 400 {
		t.Fatal("foreign origin accepted")
	}
	delete(event.Headers, "origin")
	event.RawPath = "/auth/session/refresh"
	event.Body = `{"refreshHandle":"invalid"}`
	response, _ = adapter.Handle(context.Background(), event)
	if response.StatusCode != 401 {
		t.Fatal("invalid session not 401")
	}
	event.RawPath = "/api/dashboard"
	event.Body = ""
	event.RequestContext.HTTP.Method = "GET"
	response, _ = adapter.Handle(context.Background(), event)
	if response.StatusCode != 401 {
		t.Fatal("unauthenticated dashboard available")
	}
	event.RawPath = "/auth/lwa/callback"
	event.RawQueryString = "state=" + challenge + "&state=" + challenge + "&code=synthetic"
	response, _ = adapter.Handle(context.Background(), event)
	if response.StatusCode != 400 {
		t.Fatal("duplicate callback state accepted")
	}
}
