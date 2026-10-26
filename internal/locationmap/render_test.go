package locationmap

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestSatelliteTileMosaicPreservesWorldPixelScale(t *testing.T) {
	var descriptorCalls, tileCalls atomic.Int32
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old; old = peak.Load() {
			if peak.CompareAndSwap(old, current) {
				break
			}
		}
		if r.Header.Get("Authorization") == "" || r.URL.RawQuery != "" {
			t.Error("unsigned or unexpected tile query")
		}
		if r.URL.Path == "/v2/styles/Satellite/descriptor" {
			descriptorCalls.Add(1)
			io.WriteString(w, `{"sources":{"satellite":{"type":"raster","attribution":"<a href=\"https://example.invalid\">© Synthetic provider</a>"}}}`)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) != 7 || parts[3] != "raster.satellite" {
			t.Error("unexpected provider path")
			w.WriteHeader(400)
			return
		}
		z, _ := strconv.Atoi(parts[4])
		x, _ := strconv.Atoi(parts[5])
		y, _ := strconv.Atoi(parts[6])
		if z != 18 {
			t.Error("512-pixel tiles must use provider zoom18 for effective zoom19")
		}
		tileCalls.Add(1)
		img := image.NewRGBA(image.Rect(0, 0, 512, 512))
		for py := 0; py < 512; py++ {
			for px := 0; px < 512; px++ {
				img.SetRGBA(px, py, color.RGBA{uint8(x), uint8(y), uint8(z), 255})
			}
		}
		png.Encode(w, img)
	}))
	defer server.Close()
	style := DefaultRenderConfig()
	style.Width = 3072
	style.Height = 2048
	data, err := (Renderer{Config: syntheticConfig(), Style: style, Endpoint: server.URL, HTTP: server.Client()}).Render(context.Background(), onstar.LocationFix{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds() != image.Rect(0, 0, 3072, 2048) {
		t.Fatal("mosaic dimensions", err)
	}
	if descriptorCalls.Load() != 1 || tileCalls.Load() != 24 {
		t.Fatal("unexpected provider request count", descriptorCalls.Load(), tileCalls.Load())
	}
	if peak.Load() > satelliteWorkers {
		t.Fatal("provider parallelism exceeded bound")
	}
	for _, point := range []image.Point{{0, 0}, {511, 511}, {512, 512}, {1536, 1024}, {3071, 1800}} {
		want := color.RGBA{uint8(131069 + point.X/512), uint8(131070 + point.Y/512), 18, 255}
		if color.RGBAModel.Convert(img.At(point.X, point.Y)) != want {
			t.Fatal("mosaic seam or projection mismatch", point)
		}
	}
	if color.RGBAModel.Convert(img.At(0, 2047)) != (color.RGBA{12, 18, 24, 255}) {
		t.Fatal("copyright footer absent")
	}
}

func TestSatelliteMetadataAndPolarBoundaries(t *testing.T) {
	for _, source := range []string{
		`{"sources":{}}`,
		`{"sources":{"x":{"type":"raster","tileSize":0,"attribution":"Synthetic"}}}`,
		`{"sources":{"x":{"type":"raster","tileSize":512}}}`,
		`{"sources":{"x":{"type":"raster","attribution":"<script>invented</script>"}}}`,
		`{"sources":{"x":{"type":"raster","attribution":"one"},"y":{"type":"raster","attribution":"two"}}}`,
	} {
		if _, err := parseSatelliteSource([]byte(source)); err == nil {
			t.Fatal("invalid attribution/source accepted")
		}
	}
	source, err := parseSatelliteSource([]byte(`{"sources":{"x":{"type":"raster","attribution":"<a href=\"https://example.invalid\">© Synthetic</a> &amp; contributor"}}}`))
	if err != nil || source.TileSize != 512 || source.Attribution != "© Synthetic & contributor" {
		t.Fatal("authoritative attribution not preserved", err)
	}
	c := DefaultRenderConfig()
	window, err := satelliteWindow(onstar.LocationFix{Longitude: math.Nextafter(180, 0)}, c, 512)
	if err != nil {
		t.Fatal(err)
	}
	var west, east bool
	for _, p := range window.Parts {
		if p.X < 0 || p.X >= 1<<18 || p.Y < 0 || p.Y >= 1<<18 {
			t.Fatal("tile grid escaped bounds")
		}
		west = west || p.X == 0
		east = east || p.X == (1<<18)-1
	}
	if !west || !east {
		t.Fatal("dateline did not wrap")
	}
	c.Zoom = 0
	if _, err = satelliteWindow(onstar.LocationFix{}, c, 256); err == nil {
		t.Fatal("polar/world overflow accepted")
	}
}

func TestSatelliteRejectsWrongTileSizeAndDoesNotFollowStyleURLs(t *testing.T) {
	var tileCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/styles/") {
			io.WriteString(w, `{"sources":{"x":{"type":"raster","tileSize":512,"tiles":["https://example.invalid/never-fetch"],"attribution":"Synthetic"}}}`)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v2/tiles/raster.satellite/") {
			t.Error("followed untrusted style URL")
		}
		tileCalls.Add(1)
		png.Encode(w, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	}))
	defer server.Close()
	_, err := (Renderer{Config: syntheticConfig(), Style: DefaultRenderConfig(), Endpoint: server.URL, HTTP: server.Client()}).Render(context.Background(), onstar.LocationFix{}, time.Now())
	if err == nil || tileCalls.Load() == 0 {
		t.Fatal("incorrect tile dimensions accepted")
	}
}

type failingMapTransport struct{ err error }

func (f failingMapTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }
func syntheticConfig() aws.Config {
	return aws.Config{Region: "us-east-1", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
	})}
}
func TestMapSourceTimeoutAndNetworkReasons(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{{"deadline", context.DeadlineExceeded, "map-source-timeout"}, {"network", errors.New("synthetic failure"), "map-source-network"}} {
		t.Run(tc.name, func(t *testing.T) {
			r := Renderer{Config: syntheticConfig(), Style: DefaultRenderConfig(), HTTP: &http.Client{Transport: failingMapTransport{tc.err}}}
			_, err := r.Render(context.Background(), onstar.LocationFix{}, time.Now())
			if err == nil || err.Error() != tc.want {
				t.Fatal("wrong safe reason", err)
			}
		})
	}
}
func TestMapRejectsWrongDimensionsAndUnsupportedHybrid(t *testing.T) {
	style := DefaultRenderConfig()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { png.Encode(w, image.NewRGBA(image.Rect(0, 0, 2, 2))) }))
	defer server.Close()
	r := Renderer{Config: syntheticConfig(), Style: style, Endpoint: server.URL}
	if _, err := r.Render(context.Background(), onstar.LocationFix{}, time.Now()); err == nil {
		t.Fatal("bad dimensions accepted")
	}
	r.Style.Style = "Hybrid"
	if _, err := r.Render(context.Background(), onstar.LocationFix{}, time.Now()); err == nil || err.Error() != "map-request-invalid" {
		t.Fatal("unsupported provider style silently substituted")
	}
}

func TestStandardRetainsSupportedControls(t *testing.T) {
	style := DefaultRenderConfig()
	style.Style, style.Theme, style.Labels, style.POIs = "Standard", "Dark", "Large", "Disabled"
	style.Width, style.Height = 768, 512
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("style") != "Standard" || q.Get("color-scheme") != "Dark" || q.Get("label-size") != "Large" || q.Get("pois") != "Disabled" || q.Get("crop-labels") != "false" {
			t.Error("Standard controls lost")
		}
		jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, style.Width, style.Height)), nil)
	}))
	defer server.Close()
	if _, err := (Renderer{Config: syntheticConfig(), Style: style, Endpoint: server.URL}).Render(context.Background(), onstar.LocationFix{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	style = DefaultRenderConfig()
	style.POIs = "Enabled"
	if _, err := (Renderer{Config: syntheticConfig(), Style: style, Endpoint: server.URL}).Render(context.Background(), onstar.LocationFix{}, time.Now()); err == nil || err.Error() != "map-request-invalid" {
		t.Fatal("unsupported Satellite settings silently ignored")
	}
}

type objectTransport func(*http.Request) (*http.Response, error)

func (f objectTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestImmutableS3ObjectsAndSafeFailures(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := S3Objects{Config: syntheticConfig(), Bucket: "synthetic-map", HTTP: &http.Client{Transport: objectTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" || r.Header.Get("x-amz-server-side-encryption") != "AES256" || r.Header.Get("Cache-Control") != "private,no-store" || r.URL.Path != "/map-cache/"+id+".png" {
			t.Fatal("immutable private write contract missing")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}}
	if err := store.Put(context.Background(), id, []byte("synthetic-image")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status         int
		transportError bool
		want           error
	}{{404, false, auth.ErrMissing}, {403, false, auth.ErrUnavailable}, {500, false, auth.ErrUnavailable}, {0, true, auth.ErrUnavailable}} {
		store.HTTP = &http.Client{Transport: objectTransport(func(*http.Request) (*http.Response, error) {
			if tc.transportError {
				return nil, errors.New("unsafe upstream URL or body")
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(bytes.NewReader([]byte("unsafe provider detail")))}, nil
		})}
		if _, err := store.Get(context.Background(), id); !errors.Is(err, tc.want) {
			t.Fatal("unsafe or incorrect storage error")
		}
	}
}
