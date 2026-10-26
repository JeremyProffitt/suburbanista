package locationmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/net/html"
)

const satelliteNoticeRows = 192
const satelliteWorkers = 8
const satelliteMaxTiles = 256

type satelliteSource struct {
	TileSize    int
	Attribution string
}

// Only fixed AWS paths are fetched. URLs in style metadata are never followed.
func (r Renderer) tileResource(ctx context.Context, path string, limit int64, now time.Time) ([]byte, error) {
	base := r.Endpoint
	if base == "" {
		base = "https://maps.geo." + r.Config.Region + ".amazonaws.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
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
	if v4.NewSigner().SignHTTP(ctx, credentials, req, emptySHA256, "geo-maps", r.Config.Region, now) != nil {
		return nil, errors.New("map-source-unavailable")
	}
	client := http.Client{Timeout: 20 * time.Second}
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
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("map-source-http-%d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(data) > int(limit) {
		return nil, errors.New("map-source-read-failed")
	}
	return data, nil
}

func parseSatelliteSource(data []byte) (satelliteSource, error) {
	var descriptor struct {
		Sources map[string]struct {
			Type        string `json:"type"`
			TileSize    *int   `json:"tileSize"`
			Attribution string `json:"attribution"`
		} `json:"sources"`
	}
	if len(data) > 1<<20 || json.Unmarshal(data, &descriptor) != nil {
		return satelliteSource{}, errors.New("map-data-unavailable")
	}
	var selected satelliteSource
	count := 0
	for _, source := range descriptor.Sources {
		if source.Type != "raster" {
			continue
		}
		count++
		size := 512 // MapLibre raster-source default when tileSize is omitted.
		if source.TileSize != nil {
			size = *source.TileSize
		}
		if size != 256 && size != 512 && size != 1024 {
			return selected, errors.New("map-data-unavailable")
		}
		if len(source.Attribution) == 0 || len(source.Attribution) > 8192 {
			return selected, errors.New("map-data-unavailable")
		}
		node, err := html.Parse(strings.NewReader(source.Attribution))
		if err != nil {
			return selected, errors.New("map-data-unavailable")
		}
		var text []string
		var walk func(*html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
				return
			}
			if n.Type == html.TextNode {
				text = append(text, n.Data)
			}
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
		}
		walk(node)
		attribution := strings.Join(strings.Fields(strings.Join(text, " ")), " ")
		if attribution == "" || len(attribution) > 2048 {
			return selected, errors.New("map-data-unavailable")
		}
		selected = satelliteSource{size, attribution}
	}
	if count != 1 {
		return selected, errors.New("map-data-unavailable")
	}
	return selected, nil
}

type tilePart struct {
	X, Y        int64
	Destination image.Rectangle
	Source      image.Point
}
type tileWindow struct {
	Z     int
	Parts []tilePart
}

func satelliteWindow(fix onstar.LocationFix, c RenderConfig, size int) (tileWindow, error) {
	if !validFix(fix) || c.Zoom != math.Trunc(c.Zoom) || c.Width < 64 || c.Height < satelliteNoticeRows+64 || c.Width > contract.MaxMapDimension || c.Height > contract.MaxMapDimension || int64(c.Width)*int64(c.Height) > 3072*2048 {
		return tileWindow{}, errors.New("map-request-invalid")
	}
	z := int(c.Zoom) - int(math.Log2(float64(size)/256))
	if z < 0 || z > 22 {
		return tileWindow{}, errors.New("map-request-invalid")
	}
	n := int64(1) << z
	span := float64(size) * float64(n)
	sin := math.Sin(fix.Latitude * math.Pi / 180)
	cx := (fix.Longitude + 180) / 360 * span
	cy := (.5 - math.Log((1+sin)/(1-sin))/(4*math.Pi)) * span
	left, top := int64(math.Floor(cx-float64(c.Width)/2)), int64(math.Floor(cy-float64(c.Height)/2))
	if int64(c.Width) > int64(span) || top < 0 || top+int64(c.Height) > int64(span) {
		return tileWindow{}, errors.New("map-position-invalid")
	}
	firstX, lastX := int64(math.Floor(float64(left)/float64(size))), int64(math.Floor(float64(left+int64(c.Width)-1)/float64(size)))
	firstY, lastY := top/int64(size), (top+int64(c.Height)-1)/int64(size)
	if (lastX-firstX+1)*(lastY-firstY+1) > satelliteMaxTiles {
		return tileWindow{}, errors.New("map-request-invalid")
	}
	window := tileWindow{Z: z}
	output := image.Rect(0, 0, c.Width, c.Height)
	for y := firstY; y <= lastY; y++ {
		for x := firstX; x <= lastX; x++ {
			x0, y0 := int(x*int64(size)-left), int(y*int64(size)-top)
			dst := image.Rect(x0, y0, x0+size, y0+size).Intersect(output)
			wrapped := (x%n + n) % n
			window.Parts = append(window.Parts, tilePart{wrapped, y, dst, image.Pt(dst.Min.X-x0, dst.Min.Y-y0)})
		}
	}
	return window, nil
}

func drawSatelliteAttribution(out *image.RGBA, text string) error {
	ttf, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return errors.New("map-encoding-failed")
	}
	face, err := opentype.NewFace(ttf, &opentype.FaceOptions{Size: 64, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return errors.New("map-encoding-failed")
	}
	defer face.Close()
	d := font.Drawer{Dst: out, Src: image.White, Face: face}
	maxWidth := out.Bounds().Dx() - 32 - 256 // Preserve the app's fixed AWS-link corner.
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if d.MeasureString(candidate).Ceil() > maxWidth {
			if line == "" {
				return errors.New("map-data-unavailable")
			}
			lines = append(lines, line)
			line = word
			if d.MeasureString(line).Ceil() > maxWidth {
				return errors.New("map-data-unavailable")
			}
		} else {
			line = candidate
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 || len(lines) > 2 {
		return errors.New("map-data-unavailable")
	}
	top := out.Bounds().Dy() - satelliteNoticeRows
	draw.Draw(out, image.Rect(0, top, out.Bounds().Dx(), out.Bounds().Dy()), image.NewUniform(color.RGBA{12, 18, 24, 255}), image.Point{}, draw.Src)
	for i, line := range lines {
		d.Dot = fixed.P(16, top+72+i*80)
		d.DrawString(line)
	}
	return nil
}

func (r Renderer) renderSatellite(ctx context.Context, fix onstar.LocationFix, now time.Time) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	c := r.Style
	if c.Provider != "amazon-location" || c.Style != "Satellite" || c.Theme != "Imagery" || c.Labels != "None" || c.POIs != "None" || c.Overlays != "" {
		return nil, errors.New("map-request-invalid")
	}
	descriptor, err := r.tileResource(ctx, "/v2/styles/Satellite/descriptor", 1<<20, now)
	if err != nil {
		return nil, err
	}
	source, err := parseSatelliteSource(descriptor)
	if err != nil {
		return nil, err
	}
	window, err := satelliteWindow(fix, c, source.TileSize)
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA(image.Rect(0, 0, c.Width, c.Height))
	jobs := make(chan tilePart, len(window.Parts))
	for _, part := range window.Parts {
		jobs <- part
	}
	close(jobs)
	var workers sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	for i := 0; i < min(satelliteWorkers, len(window.Parts)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for part := range jobs {
				if ctx.Err() != nil {
					return
				}
				data, e := r.tileResource(ctx, fmt.Sprintf("/v2/tiles/raster.satellite/%d/%d/%d", window.Z, part.X, part.Y), 6<<20, now)
				if e == nil {
					var cfg image.Config
					var kind string
					cfg, kind, e = image.DecodeConfig(bytes.NewReader(data))
					if e != nil || (kind != "jpeg" && kind != "png") || cfg.Width != source.TileSize || cfg.Height != source.TileSize {
						e = errors.New("map-data-unavailable")
					}
				}
				var tile image.Image
				if e == nil {
					tile, _, e = image.Decode(bytes.NewReader(data))
					if e != nil {
						e = errors.New("map-data-unavailable")
					}
				}
				if e == nil {
					if opaque, ok := tile.(interface{ Opaque() bool }); !ok || !opaque.Opaque() {
						e = errors.New("map-data-unavailable")
					}
				}
				mu.Lock()
				if e != nil {
					failures = append(failures, e.Error())
					cancel()
				} else {
					draw.Draw(out, part.Destination, tile, part.Source, draw.Src)
				}
				mu.Unlock()
				if e != nil {
					return
				}
			}
		}()
	}
	workers.Wait()
	if len(failures) > 0 {
		sort.Strings(failures)
		return nil, errors.New(failures[0])
	}
	if ctx.Err() != nil {
		return nil, errors.New("map-source-timeout")
	}
	if err = drawSatelliteAttribution(out, source.Attribution); err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	if png.Encode(&encoded, out) != nil {
		return nil, errors.New("map-encoding-failed")
	}
	if encoded.Len() > contract.MaxMapImageBytes {
		return nil, errors.New("map-encoding-failed")
	}
	return encoded.Bytes(), nil
}
