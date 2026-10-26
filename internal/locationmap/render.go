package locationmap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Renderer builds the Satellite canvas from AWS raster tiles. The retained
// Standard renderer uses a static background. Neither adds a position marker.
type Renderer struct {
	HTTP     *http.Client
	Endpoint string
	Config   aws.Config
	Style    RenderConfig
}

func (r Renderer) Render(ctx context.Context, fix onstar.LocationFix, now time.Time) ([]byte, error) {
	if !validFix(fix) {
		return nil, errors.New("map-position-invalid")
	}
	if r.Style.Style == "Satellite" {
		return r.renderSatellite(ctx, fix, now)
	}
	c := r.Style
	if c.Provider != "amazon-location" || c.Style != "Standard" || c.Width < 64 || c.Width > 1400 || c.Height < 64 || c.Height > 1400 || c.Zoom < 0 || c.Zoom > 20 || c.Overlays != "" {
		return nil, errors.New("map-request-invalid")
	}
	if c.Style == "Standard" && ((c.Theme != "Light" && c.Theme != "Dark") || (c.Labels != "Small" && c.Labels != "Large") || (c.POIs != "Enabled" && c.POIs != "Disabled")) {
		return nil, errors.New("map-request-invalid")
	}
	endpoint := r.Endpoint
	if endpoint == "" {
		endpoint = "https://maps.geo." + r.Config.Region + ".amazonaws.com/v2/static/map"
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("map-request-invalid")
	}
	q := url.Values{"center": {fmt.Sprintf("%.10f,%.10f", fix.Longitude, fix.Latitude)}, "zoom": {strconv.FormatFloat(c.Zoom, 'f', -1, 64)}, "width": {strconv.Itoa(c.Width)}, "height": {strconv.Itoa(c.Height)}, "style": {c.Style}}
	// Standard's static-map controls are not sent to the Satellite tile API.
	if c.Style == "Standard" {
		q.Set("color-scheme", c.Theme)
		q.Set("label-size", c.Labels)
		q.Set("pois", c.POIs)
		q.Set("crop-labels", "false")
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("map-request-invalid")
	}
	if r.Config.Credentials == nil {
		return nil, errors.New("map-source-unavailable")
	}
	credentials, err := r.Config.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, errors.New("map-source-unavailable")
	}
	if err = v4.NewSigner().SignHTTP(ctx, credentials, req, emptySHA256, "geo-maps", r.Config.Region, now); err != nil {
		return nil, errors.New("map-source-unavailable")
	}
	client := http.Client{Timeout: 30 * time.Second}
	if r.HTTP != nil {
		client = *r.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		var network net.Error
		if errors.As(err, &network) && network.Timeout() {
			return nil, errors.New("map-source-timeout")
		}
		return nil, errors.New("map-source-network")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("map-source-http-%d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 3*1024*1024+1))
	if err != nil || len(data) > 3*1024*1024 {
		return nil, errors.New("map-source-read-failed")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != c.Width || cfg.Height != c.Height {
		return nil, errors.New("map-data-unavailable")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("map-data-unavailable")
	}
	var out bytes.Buffer
	if err = png.Encode(&out, img); err != nil {
		return nil, errors.New("map-encoding-failed")
	}
	return out.Bytes(), nil
}
