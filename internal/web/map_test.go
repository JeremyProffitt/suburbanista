package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/locationmap"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

type mapStub struct {
	data  []byte
	calls int
	err   error
}

type locationRequestsStub struct {
	requests, statuses int
	family, id         string
}

type locationInvokerStub struct {
	output *lambda.InvokeOutput
	input  *lambda.InvokeInput
	calls  int
}

func (s *locationInvokerStub) Invoke(_ context.Context, in *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	s.input = in
	s.calls++
	return s.output, nil
}
func TestIAMLocationRequestOnlyEnqueuesAndValidatesReply(t *testing.T) {
	id := "synthetic-request-identity"
	status := contract.LocationRefreshStatus{RequestID: id, OperationID: strings.Repeat("a", 64), State: "queued", RequestedAt: time.Now(), PollAfterSeconds: 10}
	good, _ := json.Marshal(map[string]any{"status": status})
	for _, tc := range []struct {
		name string
		out  *lambda.InvokeOutput
		want bool
	}{
		{"queued", &lambda.InvokeOutput{StatusCode: 200, Payload: good}, true},
		{"function-error", &lambda.InvokeOutput{StatusCode: 200, Payload: good, FunctionError: aws.String("synthetic")}, false},
		{"malformed", &lambda.InvokeOutput{StatusCode: 200, Payload: []byte(`{}`)}, false},
		{"rate-limited", &lambda.InvokeOutput{StatusCode: 200, Payload: []byte(`{"error":"rate-limited"}`)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoker := &locationInvokerStub{output: tc.out}
			service := IAMLocationRequests{Client: invoker, Function: "synthetic-worker"}
			_, err := service.Request(context.Background(), "synthetic-family", id)
			if (err == nil) != tc.want {
				t.Fatal("wrong reply validation", err)
			}
			var request contract.LocationRefreshRequest
			if json.Unmarshal(invoker.input.Payload, &request) != nil || request.Kind != "location-refresh-request" || request.FamilyID != "synthetic-family" || request.RequestID != id {
				t.Fatal("wrong worker envelope")
			}
			if invoker.calls != 1 {
				t.Fatal("unexpected invoke retry")
			}
			_, _ = service.Status(context.Background(), "synthetic-family", id)
			if invoker.calls != 1 {
				t.Fatal("status invoked worker")
			}
		})
	}
}

func (s *locationRequestsStub) Request(_ context.Context, family, id string) (contract.LocationRefreshStatus, error) {
	s.requests++
	s.family = family
	s.id = id
	return contract.LocationRefreshStatus{RequestID: id, State: "queued", PollAfterSeconds: 10}, nil
}
func (s *locationRequestsStub) Status(_ context.Context, family, id string) (contract.LocationRefreshStatus, error) {
	s.statuses++
	s.family = family
	s.id = id
	return contract.LocationRefreshStatus{RequestID: id, State: "ready", ObservationRevision: "synthetic-revision", PollAfterSeconds: 10}, nil
}
func TestLocationRefreshRoutesAreOwnerOnlyAndSeparateReadFromRequest(t *testing.T) {
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
	for _, tc := range []struct {
		name, path, method, query, body string
		owner                           bool
		want                            int
	}{
		{"request", "/api/map/refresh", "POST", "", `{"requestId":"synthetic-refresh-id"}`, true, 202},
		{"status", "/api/map/refresh/status", "GET", "requestId=synthetic-refresh-id", "", true, 200},
		{"anonymous-request", "/api/map/refresh", "POST", "", `{"requestId":"synthetic-refresh-id"}`, false, 401},
		{"anonymous-status", "/api/map/refresh/status", "GET", "requestId=synthetic-refresh-id", "", false, 401},
		{"map-key-request", "/api/map/refresh", "POST", "key=synthetic", `{"requestId":"synthetic-refresh-id"}`, true, 401},
		{"map-key-status", "/api/map/refresh/status", "GET", "requestId=synthetic-refresh-id&key=synthetic", "", true, 401},
		{"family-spoof", "/api/map/refresh", "POST", "", `{"requestId":"synthetic-refresh-id","familyId":"other"}`, true, 400},
		{"invalid-id", "/api/map/refresh", "POST", "", `{"requestId":"bad"}`, true, 400},
		{"duplicate-id", "/api/map/refresh/status", "GET", "requestId=synthetic-refresh-id&requestId=synthetic-refresh-id", "", true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refresh := &locationRequestsStub{}
			broker := &brokerStub{}
			adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: docs, Owner: "synthetic-owner", Now: func() time.Time { return now }}, Provider: &state.Store{}, Commands: broker, LocationRequests: refresh})
			if err != nil {
				t.Fatal(err)
			}
			event := request(t)
			event.RawPath = tc.path
			event.RawQueryString = tc.query
			event.RequestContext.HTTP.Method = tc.method
			event.Body = tc.body
			event.Cookies = nil
			event.Headers = map[string]string{"content-type": "application/json"}
			if tc.owner {
				event.Headers["authorization"] = "Bearer " + issued.AccessHandle
			}
			result, err := adapter.Handle(context.Background(), event)
			if err != nil || result.StatusCode != tc.want {
				t.Fatal("wrong refresh response", result.StatusCode, err)
			}
			if result.Headers["cache-control"] != "private, no-store" {
				t.Fatal("refresh response can be cached")
			}
			if len(broker.calls) != 0 {
				t.Fatal("location request entered command broker")
			}
			if tc.want >= 400 {
				if refresh.requests+refresh.statuses != 0 {
					t.Fatal("invalid request reached service")
				}
			} else if refresh.family != family.ID || refresh.id != "synthetic-refresh-id" {
				t.Fatal("family not derived from session")
			}
			if tc.method == "GET" && refresh.requests != 0 {
				t.Fatal("poll enqueued work")
			}
		})
	}
}

func (s *mapStub) PNG(context.Context) ([]byte, error) { s.calls++; return s.data, s.err }
func mapHash(data []byte) string                       { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func mapPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestMapOwnerKeyAuthorizationAndRevocation(t *testing.T) {
	now := time.Now().UTC()
	key := strings.Repeat("synthetic", 8)
	hash := sha256.Sum256([]byte(key))
	digest := hex.EncodeToString(hash[:])
	for _, tc := range []struct {
		name, session, query, digest, method string
		want                                 int
	}{
		{"owner", "owner", "", digest, "GET", 200},
		{"owner-key-disabled", "owner", "", "", "GET", 200},
		{"key", "", "key=" + key, digest, "GET", 200},
		{"pending-key", "", "key=" + key, digest, "GET", 202},
		{"pending-head-key", "", "key=" + key, digest, "HEAD", 202},
		{"head-key", "", "key=" + key, digest, "HEAD", 200},
		{"head-missing", "", "", digest, "HEAD", 401},
		{"missing", "", "", digest, "GET", 401},
		{"wrong-owner", "wrong-owner", "", digest, "GET", 401},
		{"expired", "expired", "", digest, "GET", 401},
		{"revoked", "revoked", "", digest, "GET", 401},
		{"invalid-key", "", "key=" + strings.Repeat("wrong", 10), digest, "GET", 401},
		{"revoked-key", "", "key=" + key, "", "GET", 401},
		{"rotated-key", "", "key=" + key, strings.Repeat("a", 64), "GET", 401},
		{"duplicate-key", "owner", "key=" + key + "&key=" + key, digest, "GET", 401},
		{"malformed-key", "owner", "key=%zz", digest, "GET", 401},
		{"empty-key", "owner", "key=", digest, "GET", 401},
		{"invalid-key-with-owner", "owner", "key=" + strings.Repeat("wrong", 10), digest, "GET", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			family, issued, err := auth.NewFamily("synthetic-owner", "synthetic-owner", "synthetic-owner", "synthetic-device", now)
			if err != nil {
				t.Fatal(err)
			}
			if tc.session == "wrong-owner" {
				family.Owner = "other-owner"
			}
			if tc.session == "revoked" {
				family.Revoked = true
			}
			expiry := issued.AccessExpiresAt
			if tc.session == "expired" {
				expiry = now.Add(-time.Second)
			}
			fdata, _ := json.Marshal(struct {
				Family      auth.Family
				ValidatedAt time.Time
			}{family, now})
			adata, _ := json.Marshal(struct {
				FamilyID  string
				ExpiresAt time.Time
			}{family.ID, expiry})
			hash := sha256.Sum256([]byte(issued.AccessHandle))
			docs := sessionDocuments{map[string]auth.Document{"family#" + family.ID: {Data: fdata, ExpiresAt: family.Deadline}, "access#" + hex.EncodeToString(hash[:]): {Data: adata, ExpiresAt: expiry}}}
			maps := &mapStub{data: mapPNG(t)}
			if strings.HasPrefix(tc.name, "pending-") {
				maps.err = ErrMapPending
			}
			broker := &brokerStub{}
			adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: docs, Owner: "synthetic-owner", Now: func() time.Time { return now }}, Provider: &state.Store{}, Commands: broker, Map: maps, MapKeySHA256: tc.digest})
			if err != nil {
				t.Fatal(err)
			}
			event := request(t)
			event.RawPath = "/api/map.png"
			event.RawQueryString = tc.query
			event.RequestContext.HTTP.Method = tc.method
			event.Body = ""
			event.Headers = map[string]string{"if-none-match": "synthetic-etag"}
			event.Cookies = nil
			if tc.session != "" {
				event.Headers["authorization"] = "Bearer " + issued.AccessHandle
			}
			out, err := adapter.Handle(context.Background(), event)
			if err != nil || out.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d err=%v", out.StatusCode, tc.want, err)
			}
			if out.Headers["cache-control"] != "private, no-store" || out.Headers["referrer-policy"] != "no-referrer" {
				t.Fatal("unsafe map headers")
			}
			if len(broker.calls) != 0 {
				t.Fatal("map called command/provider broker")
			}
			if tc.want != 200 && tc.want != 202 && maps.calls != 0 {
				t.Fatal("unauthorized storage read")
			}
			if tc.want == 200 && tc.method == "GET" {
				raw, _ := base64.StdEncoding.DecodeString(out.Body)
				if !out.IsBase64Encoded || !bytes.Equal(raw, maps.data) || out.Headers["content-type"] != "image/png" {
					t.Fatal("PNG not returned exactly")
				}
			}
			if tc.method == "HEAD" && out.Body != "" {
				t.Fatal("HEAD body not empty")
			}
			if tc.want == 202 && (maps.calls != 1 || out.Headers["retry-after"] != "10") {
				t.Fatal("keyed pending read lost polling contract")
			}
			if strings.Contains(out.Body, key) {
				t.Fatal("credential in response")
			}
		})
	}
}

func TestMapKeyCannotReadHistoryOrSendCommands(t *testing.T) {
	key := strings.Repeat("synthetic", 8)
	sum := sha256.Sum256([]byte(key))
	broker := &brokerStub{}
	maps := &mapStub{data: mapPNG(t)}
	adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: emptyDocuments{}, Owner: "synthetic-owner"}, Provider: &state.Store{}, Commands: broker, Map: maps, MapKeySHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, method, body string
		want               int
	}{{"/api/commands", "POST", `{"action":"lock","requestId":"synthetic-intent"}`, 401}, {"/api/vehicle/read", "POST", `{"featureId":"location"}`, 401}, {"/api/vehicle/read/status", "GET", "", 401}, {"/api/map.png", "POST", "", 405}, {"/api/history", "GET", "", 404}} {
		event := request(t)
		event.RawPath = tc.path
		event.RawQueryString = "key=" + key + "&featureId=location"
		event.RequestContext.HTTP.Method = tc.method
		event.Body = tc.body
		event.Headers = map[string]string{"content-type": "application/json"}
		event.Cookies = nil
		out, err := adapter.Handle(context.Background(), event)
		if err != nil || out.StatusCode != tc.want {
			t.Fatalf("%s status=%d want=%d", tc.path, out.StatusCode, tc.want)
		}
	}
	if len(broker.calls) != 0 || maps.calls != 0 {
		t.Fatal("map key escaped read-only scope")
	}
}

type mapTransport func(*http.Request) (*http.Response, error)

func (f mapTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestS3MapBoundedPrivateRead(t *testing.T) {
	data := mapPNG(t)
	for _, tc := range []struct {
		name   string
		status int
		data   []byte
		length int64
		want   bool
	}{{"png", 200, data, int64(len(data)), true}, {"missing", 404, data, 0, false}, {"redirect", 302, data, 0, false}, {"oversize", 200, bytes.Repeat([]byte{'x'}, maxMapBytes+1), -1, false}, {"invalid", 200, []byte("not png"), 7, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := S3Map{Bucket: "synthetic-map", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "synthetic-id", SecretAccessKey: "synthetic-secret"}, nil
			}), HTTP: &http.Client{Transport: mapTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.String() != "https://s3.us-east-1.amazonaws.com/synthetic-map/map-cache/"+strings.Repeat("a", 32)+".png" || r.Header.Get("Authorization") == "" {
					t.Fatal("wrong object/signature")
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("missing deadline")
				}
				return &http.Response{StatusCode: tc.status, ContentLength: tc.length, Body: io.NopCloser(bytes.NewReader(tc.data)), Header: http.Header{"Location": []string{"https://untrusted.invalid"}}}, nil
			})}}
			got, err := s.read(context.Background(), strings.Repeat("a", 32))
			if (err == nil) != tc.want {
				t.Fatalf("success=%v want=%v", err == nil, tc.want)
			}
			if tc.want && !bytes.Equal(got, data) {
				t.Fatal("PNG altered")
			}
		})
	}
}

type mapViewStub struct {
	mapStub
	view      contract.MapView
	viewCalls int
	viewErr   error
}

func (s *mapViewStub) View(context.Context) (contract.MapView, error) {
	s.viewCalls++
	return s.view, s.viewErr
}

func TestMapViewOwnerOnlyAndCoherentPayload(t *testing.T) {
	now := time.Now().UTC()
	key := strings.Repeat("synthetic", 8)
	sum := sha256.Sum256([]byte(key))
	for _, tc := range []struct {
		name, session, query string
		want                 int
	}{{"owner", "owner", "", 200}, {"pending", "owner", "", 202}, {"no-session", "", "", 401}, {"key-only", "", "key=" + key, 401}, {"owner-with-key", "owner", "key=" + key, 401}, {"wrong-owner", "wrong-owner", "", 401}, {"expired", "expired", "", 401}, {"revoked", "revoked", "", 401}, {"unknown-query", "owner", "unused=true", 401}} {
		t.Run(tc.name, func(t *testing.T) {
			family, issued, err := auth.NewFamily("synthetic-owner", "synthetic-owner", "synthetic-owner", "synthetic-device", now)
			if err != nil {
				t.Fatal(err)
			}
			if tc.session == "wrong-owner" {
				family.Owner = "other-owner"
			}
			if tc.session == "revoked" {
				family.Revoked = true
			}
			expiry := issued.AccessExpiresAt
			if tc.session == "expired" {
				expiry = now.Add(-time.Second)
			}
			fd, _ := json.Marshal(struct {
				Family      auth.Family
				ValidatedAt time.Time
			}{family, now})
			ad, _ := json.Marshal(struct {
				FamilyID  string
				ExpiresAt time.Time
			}{family.ID, expiry})
			accessHash := sha256.Sum256([]byte(issued.AccessHandle))
			docs := sessionDocuments{map[string]auth.Document{"family#" + family.ID: {Data: fd, ExpiresAt: family.Deadline}, "access#" + hex.EncodeToString(accessHash[:]): {Data: ad, ExpiresAt: expiry}}}
			maps := &mapViewStub{view: contract.MapView{ImageID: strings.Repeat("a", 32), ImageVersion: "1", PNG: mapPNG(t), GeneratedAt: now, ExpiresAt: now.Add(locationmap.CacheLifetime), ReceivedAt: now, Width: 8, Height: 8, Zoom: 18}}
			if tc.name == "pending" {
				maps.viewErr = ErrMapPending
			}
			broker := &brokerStub{}
			adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: docs, Owner: "synthetic-owner", Now: func() time.Time { return now }}, Provider: &state.Store{}, Commands: broker, Map: maps, MapKeySHA256: hex.EncodeToString(sum[:])})
			if err != nil {
				t.Fatal(err)
			}
			event := request(t)
			event.RawPath = "/api/map/view"
			event.RawQueryString = tc.query
			event.RequestContext.HTTP.Method = "GET"
			event.Body = ""
			event.Cookies = nil
			event.Headers = map[string]string{}
			if tc.session != "" {
				event.Headers["authorization"] = "Bearer " + issued.AccessHandle
			}
			out, err := adapter.Handle(context.Background(), event)
			if err != nil || out.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d error=%v", out.StatusCode, tc.want, err)
			}
			if out.Headers["cache-control"] != "private, no-store" || out.Headers["referrer-policy"] != "no-referrer" {
				t.Fatal("unsafe metadata headers")
			}
			if len(broker.calls) != 0 || maps.calls != 0 {
				t.Fatal("metadata invoked provider or separate PNG retrieval")
			}
			if tc.want == 200 {
				var view contract.MapView
				if json.Unmarshal([]byte(out.Body), &view) != nil || view.ImageID != maps.view.ImageID || !bytes.Equal(view.PNG, maps.view.PNG) || !view.ExpiresAt.Equal(maps.view.ExpiresAt) || maps.viewCalls != 1 {
					t.Fatal("image and metadata payload mismatch")
				}
			} else if tc.want == 202 {
				if out.Headers["retry-after"] != "10" || maps.viewCalls != 1 || !strings.Contains(out.Body, "pending") {
					t.Fatal("pending map did not specify bounded polling")
				}
			} else if maps.viewCalls != 0 {
				t.Fatal("unauthorized metadata storage access")
			}
		})
	}
}

func mapDocuments(t *testing.T, current locationmap.CurrentMap, status string) sessionDocuments {
	t.Helper()
	key := "location-observation#" + strings.Repeat("a", 64)
	latest, _ := json.Marshal(map[string]string{"observationId": key})
	observation, _ := json.Marshal(locationmap.Observation{Owner: "synthetic-owner", Vehicle: "synthetic-vehicle", Source: "onstar", Revision: "synthetic", Status: status, Map: &current})
	return sessionDocuments{map[string]auth.Document{"location-latest": {Data: latest, ExpiresAt: time.Now().Add(90 * 24 * time.Hour)}, key: {Data: observation, ExpiresAt: time.Now().Add(90 * 24 * time.Hour)}}}
}

func TestMapViewStorageExpiryMissingAndAtomicReference(t *testing.T) {
	now := time.Now().UTC()
	data := mapPNG(t)
	observed := now.Add(-time.Hour)
	current := locationmap.CurrentMap{ImageBytes: int64(len(data)), ImageSHA256: mapHash(data), ImageID: strings.Repeat("a", 32), ImageVersion: "1", GeneratedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ReceivedAt: now, Fix: onstar.LocationFix{ReportedAt: &observed}, Projection: locationmap.Projection{Width: 8, Height: 8, Zoom: 18}, Attribution: "https://docs.aws.amazon.com/location/latest/developerguide/data-attribution.html"}
	for _, tc := range []struct {
		name, state      string
		expired, missing bool
		want             bool
	}{{"ready", "ready", false, false, true}, {"expired", "ready", true, false, false}, {"pending", "pending", false, false, false}, {"failed", "failed", false, false, false}, {"missing-image", "ready", false, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			m := current
			if tc.expired {
				m.ExpiresAt = now.Add(-time.Second)
			}
			docs := mapDocuments(t, m, tc.state)
			calls := 0
			s := S3Map{Bucket: "synthetic-map", Documents: docs, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "synthetic-id", SecretAccessKey: "synthetic-secret"}, nil
			}), HTTP: &http.Client{Transport: mapTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "HEAD" || req.URL.Path != "/synthetic-map/map-cache/"+current.ImageID+".png" {
					t.Fatal("read wrong immutable image")
				}
				delete(docs.items, "location-latest")
				status := 200
				if tc.missing {
					status = 404
				}
				return &http.Response{StatusCode: status, ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}}, nil
			})}}
			view, err := s.LargeView(context.Background())
			if (err == nil) != tc.want {
				t.Fatal("incorrect map readiness", err)
			}
			if tc.want && (view.ImageID != m.ImageID || len(view.PNG) != 0 || !contract.ValidMapDownloadURL(view.DownloadURL, m.ImageID, time.Now()) || view.ImageSHA256 != mapHash(data) || !view.ObservedAt.Equal(observed) || !view.ReceivedAt.Equal(m.ReceivedAt) || calls != 1) {
				t.Fatal("immutable snapshot was mixed with newer reference")
			}
			if !tc.want && !tc.missing && calls != 0 {
				t.Fatal("unready map read storage")
			}
		})
	}
	if _, err := (S3Map{}).PNG(context.Background()); !errors.Is(err, auth.ErrUnavailable) {
		t.Fatal("missing storage configuration accepted")
	}
}

func TestS3MapDemandsOnlyUnavailableImages(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name         string
		status       string
		objectStatus int
		pending      bool
	}{
		{"cached", "ready", 200, false}, {"unresolved", "pending", 200, true}, {"purged", "ready", 404, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := mapPNG(t)
			current := locationmap.CurrentMap{ImageBytes: int64(len(data)), ImageSHA256: mapHash(data), ImageID: strings.Repeat("a", 32), ImageVersion: "1", GeneratedAt: now, ExpiresAt: now.Add(time.Hour)}
			demands := 0
			s := S3Map{Bucket: "synthetic-map", Documents: mapDocuments(t, current, tc.status), Demand: func(context.Context) error { demands++; return nil }, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "synthetic-id", SecretAccessKey: "synthetic-secret"}, nil
			}), HTTP: &http.Client{Transport: mapTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.objectStatus, ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}}, nil
			})}}
			_, err := s.LargeView(context.Background())
			if errors.Is(err, ErrMapPending) != tc.pending || demands != map[bool]int{true: 1, false: 0}[tc.pending] {
				t.Fatal("map demand did not match cache availability")
			}
		})
	}
}

type redirectMapStub struct {
	mapStub
	address   string
	downloads int
}

func (m *redirectMapStub) Download(context.Context) (string, error) {
	m.downloads++
	return m.address, nil
}

func TestMapImageContractsPreserveKeyScopeAndBoundedBodies(t *testing.T) {
	now := time.Now().UTC()
	id := strings.Repeat("a", 32)
	store := S3Map{Bucket: "synthetic-map", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "synthetic-id", SecretAccessKey: "synthetic-secret", SessionToken: "synthetic-token"}, nil
	}), HTTP: &http.Client{Transport: mapTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "HEAD" || r.URL.Path != "/synthetic-map/map-cache/"+id+".png" || r.Header.Get("Authorization") == "" {
			t.Fatal("ticket did not verify immutable object")
		}
		return &http.Response{StatusCode: 200, ContentLength: 10 << 20, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}}
	address, err := store.downloadCurrent(context.Background(), locationmap.CurrentMap{ImageID: id, ImageBytes: 10 << 20, ImageSHA256: strings.Repeat("a", 64)})
	if err != nil || !contract.ValidMapDownloadURL(address, id, now) || contract.ValidMapDownloadURL(address, id, now.Add(3*time.Minute)) {
		t.Fatal("invalid scoped ticket", err)
	}
	key := strings.Repeat("synthetic", 8)
	sum := sha256.Sum256([]byte(key))
	for _, method := range []string{"GET", "HEAD"} {
		for _, authorized := range []bool{false, true} {
			maps := &redirectMapStub{mapStub: mapStub{data: mapPNG(t)}, address: address}
			adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: emptyDocuments{}, Owner: "synthetic-owner"}, Provider: &state.Store{}, Map: maps, MapKeySHA256: hex.EncodeToString(sum[:])})
			if err != nil {
				t.Fatal(err)
			}
			event := request(t)
			event.RawPath = "/api/map.png"
			event.RequestContext.HTTP.Method = method
			event.Headers = map[string]string{}
			event.Cookies = nil
			event.Body = ""
			event.RawQueryString = ""
			if authorized {
				event.RawQueryString = "key=" + key
			}
			out, err := adapter.Handle(context.Background(), event)
			if err != nil {
				t.Fatal(err)
			}
			if authorized {
				wantDownloads, wantReads := 0, 1
				if method == http.MethodHead {
					wantDownloads, wantReads = 1, 0
				}
				if out.StatusCode != http.StatusOK || out.Headers["location"] != "" || maps.downloads != wantDownloads || maps.calls != wantReads || out.Headers["cache-control"] != "private, no-store" || out.Headers["content-type"] != "image/png" {
					t.Fatal("legacy map response or owner ticket contract changed")
				}
				if method == http.MethodGet {
					data, e := base64.StdEncoding.DecodeString(out.Body)
					if e != nil || !out.IsBase64Encoded || !bytes.Equal(data, maps.data) {
						t.Fatal("legacy PNG body changed")
					}
				}
			} else if out.StatusCode != 401 || maps.downloads != 0 || out.Headers["location"] != "" {
				t.Fatal("unauthorized map ticket exposed")
			}
			if method == "HEAD" && out.Body != "" {
				t.Fatal("HEAD contains body")
			}
		}
	}
}

func TestLegacyMapConsumerReceivesSmallFullCanvasPNGWithoutRedirect(t *testing.T) {
	// The existing living-room client refuses redirects and reads at most3MiB.
	// A valid uncompressed source proves the API handles a larger cached PNG.
	source := image.NewNRGBA(image.Rect(0, 0, 3072, 2048))
	draw.Draw(source, source.Bounds(), image.NewUniform(color.NRGBA{40, 40, 40, 255}), image.Point{}, draw.Src)
	colors := []color.NRGBA{{220, 20, 20, 255}, {20, 220, 20, 255}, {20, 20, 220, 255}, {220, 220, 20, 255}, {220, 20, 220, 255}}
	for i, r := range []image.Rectangle{image.Rect(0, 0, 256, 256), image.Rect(2816, 0, 3072, 256), image.Rect(0, 1792, 256, 2048), image.Rect(2816, 1792, 3072, 2048), image.Rect(0, 1856, 3072, 2048)} {
		draw.Draw(source, r, image.NewUniform(colors[i]), image.Point{}, draw.Src)
	}
	var encoded bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() <= maxMapBytes || encoded.Len() > contract.MaxMapImageBytes {
		t.Fatal("fixture does not exercise large source")
	}
	now := time.Now().UTC()
	id := strings.Repeat("a", 32)
	current := locationmap.CurrentMap{ImageID: id, ImageVersion: "3", ImageBytes: int64(encoded.Len()), ImageSHA256: mapHash(encoded.Bytes()), NoticeRows: 192, GeneratedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Projection: locationmap.Projection{Width: 3072, Height: 2048, Zoom: 19}}
	reads := 0
	store := S3Map{Bucket: "synthetic-map", Documents: mapDocuments(t, current, "ready"), Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "synthetic-id", SecretAccessKey: "synthetic-secret"}, nil
	}), HTTP: &http.Client{Transport: mapTransport(func(r *http.Request) (*http.Response, error) {
		reads++
		return &http.Response{StatusCode: 200, ContentLength: int64(encoded.Len()), Body: io.NopCloser(bytes.NewReader(encoded.Bytes())), Header: http.Header{}}, nil
	})}}
	key := strings.Repeat("synthetic", 8)
	sum := sha256.Sum256([]byte(key))
	adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: emptyDocuments{}, Owner: "synthetic-owner"}, Provider: &state.Store{}, Map: store, MapKeySHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	event := request(t)
	event.RawPath = "/api/map.png"
	event.RawQueryString = "key=" + key
	event.RequestContext.HTTP.Method = "GET"
	event.Body = ""
	event.Headers = map[string]string{}
	event.Cookies = nil
	out, err := adapter.Handle(context.Background(), event)
	if err != nil || out.StatusCode != 200 || out.Headers["location"] != "" || out.Headers["content-type"] != "image/png" || !out.IsBase64Encoded {
		t.Fatal("legacy no-redirect consumer cannot read map", out.StatusCode, err)
	}
	data, err := base64.StdEncoding.DecodeString(out.Body)
	if err != nil || len(data) > maxMapBytes {
		t.Fatal("legacy image exceeds response bound")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds() != image.Rect(0, 0, 768, 512) {
		t.Fatal("thumbnail dimensions changed", err)
	}
	for i, p := range []image.Point{{32, 32}, {736, 32}, {32, 455}, {736, 455}, {384, 500}} {
		if color.NRGBAModel.Convert(img.At(p.X, p.Y)) != colors[i] {
			t.Fatal("thumbnail cropped full canvas or provider footer", i)
		}
	}
	place, err := store.Place(context.Background())
	if err != nil || place.ImageID != id || !place.PinVisible || place.PinX != .5 || place.PinY != .5 || reads != 1 {
		t.Fatal("normalized pin or source identity changed", err)
	}
}

func TestOwnerPhoneMapAccessIsIndependentOfSourceIP(t *testing.T) {
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
	maps := &mapViewStub{view: contract.MapView{ImageID: strings.Repeat("a", 32), ImageVersion: "1", PNG: mapPNG(t)}}
	adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: docs, Owner: "synthetic-owner", Now: func() time.Time { return now }}, Provider: &state.Store{}, Map: maps})
	if err != nil {
		t.Fatal(err)
	}
	for _, sourceIP := range []string{"192.0.2.1", "203.0.113.9"} {
		for _, signedIn := range []bool{true, false} {
			event := request(t)
			event.RawPath = "/api/map/view"
			event.RawQueryString = ""
			event.RequestContext.HTTP.Method = "GET"
			event.RequestContext.HTTP.SourceIP = sourceIP
			event.Body = ""
			event.Cookies = nil
			event.Headers = map[string]string{}
			want := 401
			if signedIn {
				want = 200
				event.Headers["authorization"] = "Bearer " + issued.AccessHandle
			}
			out, err := adapter.Handle(context.Background(), event)
			if err != nil || out.StatusCode != want {
				t.Fatal("phone map access changed with network or lost authorization", out.StatusCode, want, err)
			}
		}
	}
	if maps.viewCalls != 2 || maps.calls != 0 {
		t.Fatal("unauthorized map reached storage or valid network switch was denied")
	}
}

func TestOwnerMapNegotiatesLegacyInlineAndFullTicket(t *testing.T) {
	now := time.Now().UTC()
	id := strings.Repeat("a", 32)
	img := image.NewNRGBA(image.Rect(0, 0, 3072, 2048))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{30, 80, 120, 255}), image.Point{}, draw.Src)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	observed := now.Add(-time.Hour)
	fix := onstar.LocationFix{ReportedAt: &observed}
	fix.Longitude = float64(1) / 10000
	current := locationmap.CurrentMap{ImageID: id, ImageVersion: "3", ImageBytes: int64(encoded.Len()), ImageSHA256: mapHash(encoded.Bytes()), NoticeRows: 192, Fix: fix, GeneratedAt: now.Add(-time.Minute), ReceivedAt: now, ExpiresAt: now.Add(time.Hour), Projection: locationmap.Projection{Width: 3072, Height: 2048, Zoom: 19}, Attribution: "Synthetic"}
	store := S3Map{Bucket: "synthetic-map", Documents: mapDocuments(t, current, "ready"), Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "synthetic-id", SecretAccessKey: "synthetic-secret"}, nil
	}), HTTP: &http.Client{Transport: mapTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: int64(encoded.Len()), Body: io.NopCloser(bytes.NewReader(encoded.Bytes())), Header: http.Header{}}, nil
	})}}
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
	accessHash := sha256.Sum256([]byte(issued.AccessHandle))
	docs := sessionDocuments{map[string]auth.Document{"family#" + family.ID: {Data: fd, ExpiresAt: family.Deadline}, "access#" + hex.EncodeToString(accessHash[:]): {Data: ad, ExpiresAt: issued.AccessExpiresAt}}}
	adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: docs, Owner: "synthetic-owner", Now: func() time.Time { return now }}, Provider: &state.Store{}, Map: store})
	if err != nil {
		t.Fatal(err)
	}
	for _, accept := range []string{"", "application/json", "*/*", contract.MapTicketMediaType} {
		event := request(t)
		event.RawPath = "/api/map/view"
		event.RawQueryString = ""
		event.RequestContext.HTTP.Method = "GET"
		event.Body = ""
		event.Cookies = nil
		event.Headers = map[string]string{"authorization": "Bearer " + issued.AccessHandle, "accept": accept}
		out, err := adapter.Handle(context.Background(), event)
		if err != nil || out.StatusCode != 200 {
			t.Fatal("owner representation unavailable", out.StatusCode, err)
		}
		var view contract.MapView
		if json.Unmarshal([]byte(out.Body), &view) != nil {
			t.Fatal("invalid map JSON")
		}
		if view.ImageID != id || view.ImageVersion != current.ImageVersion || !view.ObservedAt.Equal(observed) || !view.GeneratedAt.Equal(current.GeneratedAt) || !view.ExpiresAt.Equal(current.ExpiresAt) {
			t.Fatal("representation changed source identity/time")
		}
		if accept == contract.MapTicketMediaType {
			if len(view.PNG) != 0 || !contract.ValidMapDownloadURL(view.DownloadURL, id, now) || view.Width != 3072 || view.Height != 2048 || view.Zoom != 19 || view.NoticeRows != 192 || view.ImageSHA256 != current.ImageSHA256 {
				t.Fatal("full phone map contract changed")
			}
		} else {
			config, e := png.DecodeConfig(bytes.NewReader(view.PNG))
			if e != nil || view.DownloadURL != "" || len(view.PNG) > maxMapBytes || config.Width != 768 || config.Height != 512 || view.Width != 768 || view.Height != 512 || view.Zoom != 17 || view.NoticeRows != 48 || view.ImageBytes != int64(len(view.PNG)) || view.ImageSHA256 != mapHash(view.PNG) {
				t.Fatal("old app inline decoder cannot read coherent map")
			}
			x, y, inside := current.Projection.Pixel(current.Fix)
			px, py, scaledInside := (locationmap.Projection{Width: view.Width, Height: view.Height, Zoom: view.Zoom, CenterLatitude: view.CenterLatitude, CenterLongitude: view.CenterLongitude}).Pixel(current.Fix)
			if !inside || !scaledInside || math.Abs(x/3072-px/768) > 1e-10 || math.Abs(y/2048-py/512) > 1e-10 {
				t.Fatal("inline thumbnail changed geographic projection")
			}
		}
	}
}

type mapPlaceStub struct {
	mapStub
	place      contract.MapPlace
	placeCalls int
	placeErr   error
}

func (s *mapPlaceStub) Place(context.Context) (contract.MapPlace, error) {
	s.placeCalls++
	return s.place, s.placeErr
}

func TestMapPlaceSharesMapCredentialsWithoutCoordinates(t *testing.T) {
	now := time.Now().UTC()
	key := strings.Repeat("synthetic", 8)
	sum := sha256.Sum256([]byte(key))
	for _, tc := range []struct {
		name, session, query string
		want                 int
	}{{"key", "", "key=" + key, 200}, {"owner", "owner", "", 200}, {"pending", "", "key=" + key, 202}, {"missing", "", "", 401}, {"wrong-key", "", "key=" + strings.Repeat("wrong", 10), 401}, {"duplicate-key", "", "key=" + key + "&key=" + key, 401}, {"wrong-key-with-owner", "owner", "key=" + strings.Repeat("wrong", 10), 401}} {
		t.Run(tc.name, func(t *testing.T) {
			family, issued, err := auth.NewFamily("synthetic-owner", "synthetic-owner", "synthetic-owner", "synthetic-device", now)
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
			accessHash := sha256.Sum256([]byte(issued.AccessHandle))
			docs := sessionDocuments{map[string]auth.Document{"family#" + family.ID: {Data: fd, ExpiresAt: family.Deadline}, "access#" + hex.EncodeToString(accessHash[:]): {Data: ad, ExpiresAt: issued.AccessExpiresAt}}}
			maps := &mapPlaceStub{place: contract.MapPlace{Place: "Work", ImageID: strings.Repeat("b", 32), ObservedAt: now, PinVisible: true, PinX: 0.5, PinY: 0.5}}
			if tc.name == "pending" {
				maps.placeErr = ErrMapPending
			}
			broker := &brokerStub{}
			adapter, err := New(Build{Version: strings.Repeat("a", 40), Built: "2026-10-23T12:00:00Z"}, Services{Mobile: &auth.MobileService{Store: docs, Owner: "synthetic-owner", Now: func() time.Time { return now }}, Provider: &state.Store{}, Commands: broker, Map: maps, MapKeySHA256: hex.EncodeToString(sum[:])})
			if err != nil {
				t.Fatal(err)
			}
			event := request(t)
			event.RawPath = "/api/map/place"
			event.RawQueryString = tc.query
			event.RequestContext.HTTP.Method = "GET"
			event.Body = ""
			event.Cookies = nil
			event.Headers = map[string]string{}
			if tc.session != "" {
				event.Headers["authorization"] = "Bearer " + issued.AccessHandle
			}
			out, err := adapter.Handle(context.Background(), event)
			if err != nil || out.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d error=%v", out.StatusCode, tc.want, err)
			}
			if out.Headers["cache-control"] != "private, no-store" || out.Headers["referrer-policy"] != "no-referrer" {
				t.Fatal("unsafe place headers")
			}
			if len(broker.calls) != 0 || maps.calls != 0 {
				t.Fatal("place invoked provider or image retrieval")
			}
			switch tc.want {
			case 200:
				var place contract.MapPlace
				if json.Unmarshal([]byte(out.Body), &place) != nil || place != maps.place || strings.Contains(strings.ToLower(out.Body), "latitude") || strings.Contains(strings.ToLower(out.Body), "longitude") {
					t.Fatalf("place payload mismatch: %s", out.Body)
				}
			case 202:
				if out.Headers["retry-after"] != "10" || !strings.Contains(out.Body, "pending") {
					t.Fatal("pending place did not specify bounded polling")
				}
			default:
				if maps.placeCalls != 0 {
					t.Fatal("unauthorized place storage access")
				}
			}
		})
	}
}

func TestMapPlaceLabelsOnlyGeofencesAndPinsInsideImage(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	// The synthetic origin keeps fixtures free of real positions; one degree
	// east of it lies far outside a zoom-19 image.
	center := onstar.LocationFix{ReportedAt: &at}
	far := center
	far.Longitude++
	projection := locationmap.Projection{Zoom: 19, Width: 768, Height: 512}
	for _, tc := range []struct {
		label, want string
		fix         onstar.LocationFix
		pin         bool
	}{{"Home", "Home", center, true}, {"Work", "Work", center, true}, {"", "", center, true}, {"Garage", "", center, true}, {"Home", "Home", far, false}} {
		got := mapPlace(locationmap.CurrentMap{Place: tc.label, ImageID: strings.Repeat("c", 32), Fix: tc.fix, Projection: projection})
		if got.Place != tc.want || got.PinVisible != tc.pin || !got.ObservedAt.Equal(at) {
			t.Fatalf("label %q: %+v", tc.label, got)
		}
		if tc.pin && (got.PinX != 0.5 || got.PinY != 0.5) {
			t.Fatalf("centred fix pinned at %v,%v", got.PinX, got.PinY)
		}
	}
	// The place contract describes the full canvas, not the phone's initial
	// fourfold crop. A small offset therefore occupies one fourth the fraction.
	near := center
	near.Longitude = float64(1) / 10000
	small := mapPlace(locationmap.CurrentMap{Fix: near, Projection: projection})
	projection.Width, projection.Height = 3072, 2048
	large := mapPlace(locationmap.CurrentMap{Fix: near, Projection: projection})
	if !small.PinVisible || !large.PinVisible || math.Abs((large.PinX-.5)*4-(small.PinX-.5)) > 1e-10 || large.PinY != .5 {
		t.Fatal("large map place pin no longer uses full canvas fractions")
	}
}
