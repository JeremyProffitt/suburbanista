package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"time"

	xdraw "golang.org/x/image/draw"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/locationmap"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/gofiber/fiber/v3"
)

// Keep the base64 Lambda response below the synchronous response limit.
const maxMapBytes = 3 * 1024 * 1024

type MapStore interface {
	PNG(context.Context) ([]byte, error)
}

// S3Map serves a cached object or requests an image for the stored position.
// It cannot contact the vehicle. Large images use scoped, short-lived S3 tickets.
type S3Map struct {
	Bucket      string
	Credentials aws.CredentialsProvider
	HTTP        *http.Client
	Documents   auth.Documents
	Demand      func(context.Context) error
}

var ErrMapPending = errors.New("map pending")

func (s S3Map) pending(ctx context.Context, err error) error {
	if s.Demand != nil && s.Demand(ctx) == nil {
		return ErrMapPending
	}
	return err
}

func (s S3Map) PNG(ctx context.Context) ([]byte, error) {
	current, err := s.current(ctx)
	if err != nil {
		return nil, s.pending(ctx, err)
	}
	data, err := s.read(ctx, current.ImageID)
	if err != nil {
		return nil, s.pending(ctx, err)
	}
	return mapThumbnail(data)
}

func (s S3Map) current(ctx context.Context) (locationmap.CurrentMap, error) {
	if s.Documents == nil {
		return locationmap.CurrentMap{}, auth.ErrUnavailable
	}
	return locationmap.Current(ctx, s.Documents, time.Now().UTC())
}

func (s S3Map) View(ctx context.Context) (contract.MapView, error) {
	current, err := s.current(ctx)
	if err != nil {
		return contract.MapView{}, s.pending(ctx, err)
	}
	data, err := s.read(ctx, current.ImageID)
	if err != nil {
		return contract.MapView{}, s.pending(ctx, err)
	}
	thumb, err := mapThumbnail(data)
	if err != nil {
		return contract.MapView{}, err
	}
	config, err := png.DecodeConfig(bytes.NewReader(thumb))
	if err != nil || current.Projection.Width <= 0 || current.Projection.Height <= 0 {
		return contract.MapView{}, auth.ErrUnavailable
	}
	view := mapViewMetadata(current)
	scale := float64(config.Width) / float64(current.Projection.Width)
	view.PNG, view.Width, view.Height = thumb, config.Width, config.Height
	view.Zoom += math.Log2(scale)
	view.NoticeRows = int(math.Ceil(float64(current.NoticeRows) * scale))
	view.ImageBytes = int64(len(thumb))
	sum := sha256.Sum256(thumb)
	view.ImageSHA256 = hex.EncodeToString(sum[:])
	return view, nil
}

func (s S3Map) LargeView(ctx context.Context) (contract.MapView, error) {
	current, err := s.current(ctx)
	if err != nil {
		return contract.MapView{}, s.pending(ctx, err)
	}
	address, err := s.downloadCurrent(ctx, current)
	if err != nil {
		return contract.MapView{}, s.pending(ctx, err)
	}
	view := mapViewMetadata(current)
	view.DownloadURL = address
	return view, nil
}

func mapViewMetadata(current locationmap.CurrentMap) contract.MapView {
	observed := time.Time{}
	if current.Fix.ReportedAt != nil {
		observed = *current.Fix.ReportedAt
	}
	p := current.Projection
	return contract.MapView{Source: current.OriginSource, OriginRevision: current.OriginRevision, VehicleRevision: current.VehicleRevision, Place: current.Place, ObservationRevision: current.ObservationRevision, ImageID: current.ImageID, ImageVersion: current.ImageVersion, ImageBytes: current.ImageBytes, ImageSHA256: current.ImageSHA256, NoticeRows: current.NoticeRows, GeneratedAt: current.GeneratedAt, ExpiresAt: current.ExpiresAt, ObservedAt: observed, ReceivedAt: current.ReceivedAt, Latitude: current.Fix.Latitude, Longitude: current.Fix.Longitude, CenterLatitude: p.CenterLatitude, CenterLongitude: p.CenterLongitude, Zoom: p.Zoom, Width: p.Width, Height: p.Height, Attribution: current.Attribution}
}

// Place reports the geofence name and the vehicle's position on the current
// image without reading the image or exposing coordinates.
func (s S3Map) Place(ctx context.Context) (contract.MapPlace, error) {
	current, err := s.current(ctx)
	if err != nil {
		return contract.MapPlace{}, s.pending(ctx, err)
	}
	return mapPlace(current), nil
}

func mapPlace(current locationmap.CurrentMap) contract.MapPlace {
	place := contract.MapPlace{ImageID: current.ImageID}
	if current.Place == "Home" || current.Place == "Work" {
		place.Place = current.Place
	}
	if current.Fix.ReportedAt != nil {
		place.ObservedAt = *current.Fix.ReportedAt
	}
	p := current.Projection
	if x, y, inside := p.Pixel(current.Fix); inside && p.Width > 0 && p.Height > 0 {
		place.PinVisible, place.PinX, place.PinY = true, x/float64(p.Width), y/float64(p.Height)
	}
	return place
}

func (s S3Map) read(ctx context.Context, id string) ([]byte, error) {
	if !bucketName.MatchString(s.Bucket) || s.Credentials == nil {
		return nil, auth.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return nil, auth.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://s3.us-east-1.amazonaws.com/"+s.Bucket+"/map-cache/"+id+".png", nil)
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	credentials, err := s.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	req.Header.Set("X-Amz-Content-Sha256", emptyHash)
	if v4.NewSigner().SignHTTP(ctx, credentials, req, emptyHash, "s3", "us-east-1", time.Now().UTC()) != nil {
		return nil, auth.ErrUnavailable
	}
	client := http.Client{Timeout: 3 * time.Second}
	if s.HTTP != nil {
		client = *s.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > contract.MaxMapImageBytes {
		return nil, auth.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, contract.MaxMapImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > contract.MaxMapImageBytes {
		return nil, auth.ErrUnavailable
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > contract.MaxMapDimension || config.Height > contract.MaxMapDimension {
		return nil, auth.ErrUnavailable
	}
	return data, nil
}

// The keyed embed contract remains a small direct PNG. Resize the full canvas
// uniformly so MapPlace fractions and all provider credits keep their meaning.
func mapThumbnail(data []byte) ([]byte, error) {
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	if config.Width <= 768 && config.Height <= 512 && len(data) <= maxMapBytes {
		return data, nil
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	w, h := config.Width, config.Height
	if w > 768 {
		h = max(1, h*768/w)
		w = 768
	}
	if h > 512 {
		w = max(1, w*512/h)
		h = 512
	}
	thumb := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(thumb, thumb.Bounds(), img, img.Bounds(), draw.Src, nil)
	var out bytes.Buffer
	if err = png.Encode(&out, thumb); err != nil || out.Len() > maxMapBytes {
		return nil, auth.ErrUnavailable
	}
	return out.Bytes(), nil
}

func (s S3Map) Download(ctx context.Context) (string, error) {
	current, err := s.current(ctx)
	if err != nil {
		return "", s.pending(ctx, err)
	}
	address, err := s.downloadCurrent(ctx, current)
	if err != nil {
		return "", s.pending(ctx, err)
	}
	return address, nil
}

func (s S3Map) downloadCurrent(ctx context.Context, current locationmap.CurrentMap) (string, error) {
	if !bucketName.MatchString(s.Bucket) || s.Credentials == nil || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(current.ImageID) || current.ImageBytes <= 0 || current.ImageBytes > contract.MaxMapImageBytes || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(current.ImageSHA256) {
		return "", auth.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	address := "https://s3.us-east-1.amazonaws.com/" + s.Bucket + "/map-cache/" + current.ImageID + ".png"
	credentials, err := s.Credentials.Retrieve(ctx)
	if err != nil {
		return "", auth.ErrUnavailable
	}
	head, err := http.NewRequestWithContext(ctx, http.MethodHead, address, nil)
	if err != nil {
		return "", auth.ErrUnavailable
	}
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	head.Header.Set("X-Amz-Content-Sha256", emptyHash)
	if v4.NewSigner().SignHTTP(ctx, credentials, head, emptyHash, "s3", "us-east-1", time.Now().UTC()) != nil {
		return "", auth.ErrUnavailable
	}
	client := http.Client{Timeout: 3 * time.Second}
	if s.HTTP != nil {
		client = *s.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(head)
	if err != nil {
		return "", auth.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength != current.ImageBytes {
		return "", auth.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", auth.ErrUnavailable
	}
	q := req.URL.Query()
	q.Set("X-Amz-Expires", "120")
	req.URL.RawQuery = q.Encode()
	result, _, err := v4.NewSigner().PresignHTTP(ctx, credentials, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now().UTC())
	if err != nil || !contract.ValidMapDownloadURL(result, current.ImageID, time.Now()) {
		return "", auth.ErrUnavailable
	}
	return result, nil
}

func validMapViewDelivery(view contract.MapView) bool {
	if view.DownloadURL == "" {
		return validMapPNG(view.PNG)
	}
	return len(view.PNG) == 0 && view.ImageBytes > 0 && view.ImageBytes <= contract.MaxMapImageBytes && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(view.ImageSHA256) && view.Width > 0 && view.Height > 0 && view.Width <= contract.MaxMapDimension && view.Height <= contract.MaxMapDimension && view.NoticeRows >= 0 && view.NoticeRows <= 256 && view.NoticeRows < view.Height && contract.ValidMapDownloadURL(view.DownloadURL, view.ImageID, time.Now())
}

func validMapPNG(data []byte) bool {
	if len(data) == 0 || len(data) > maxMapBytes {
		return false
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	return err == nil && cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= 4096 && cfg.Height <= 4096
}

var mapKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

func validMapKey(key, expected string) bool {
	if !mapKeyPattern.MatchString(key) || len(expected) != 64 {
		return false
	}
	digest, err := hex.DecodeString(expected)
	if err != nil {
		return false
	}
	actual := sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare(actual[:], digest) == 1
}

func registerMap(app *fiber.App, services Services) {
	registerLocationRefresh(app, services)
	// Position metadata is owner-session only; the scoped map key never gains
	// coordinate access. Fetch one reference and its immutable object together.
	app.Get("/api/map/view", func(c fiber.Ctx) error {
		c.Set("Cache-Control", "private, no-store")
		c.Set("Vary", "Accept")
		c.Set("Referrer-Policy", "no-referrer")
		c.Set("X-Content-Type-Options", "nosniff")
		if len(c.Request().URI().QueryString()) != 0 || services.Mobile == nil {
			return sessionError(c, auth.ErrHandle)
		}
		if _, err := services.Mobile.Authorize(c.Context(), bearer(c)); err != nil {
			return sessionError(c, err)
		}
		store, ok := services.Map.(interface {
			View(context.Context) (contract.MapView, error)
		})
		if !ok {
			return publicError(c, auth.ErrUnavailable)
		}
		readView := store.View
		if c.Get("Accept") == contract.MapTicketMediaType {
			large, ok := services.Map.(interface {
				LargeView(context.Context) (contract.MapView, error)
			})
			if !ok {
				return publicError(c, auth.ErrUnavailable)
			}
			readView = large.LargeView
		}
		ctx, cancel := context.WithTimeout(c.Context(), 4*time.Second)
		defer cancel()
		view, err := readView(ctx)
		if errors.Is(err, ErrMapPending) {
			c.Set("Retry-After", "10")
			return c.Status(http.StatusAccepted).JSON(fiber.Map{"state": "pending", "pollAfterSeconds": 10})
		}
		if err != nil || !validMapViewDelivery(view) {
			return publicError(c, auth.ErrUnavailable)
		}
		return c.JSON(view)
	})
	read := func(c fiber.Ctx) error {
		if c.Method() == http.MethodHead {
			defer c.Response().ResetBody()
		}
		c.Set("Cache-Control", "private, no-store")
		c.Set("Referrer-Policy", "no-referrer")
		c.Set("X-Content-Type-Options", "nosniff")
		if ok, err := authorizeMapRead(c, services); !ok {
			return err
		}
		if services.Map == nil {
			return publicError(c, auth.ErrUnavailable)
		}
		ctx, cancel := context.WithTimeout(c.Context(), 4*time.Second)
		defer cancel()
		if store, ok := services.Map.(interface {
			Download(context.Context) (string, error)
		}); ok && c.Method() == http.MethodHead {
			address, err := store.Download(ctx)
			if errors.Is(err, ErrMapPending) {
				c.Set("Retry-After", "10")
				return c.Status(http.StatusAccepted).JSON(fiber.Map{"state": "pending", "pollAfterSeconds": 10})
			}
			if err != nil || !contract.ValidMapDownloadURL(address, "", time.Now()) {
				return publicError(c, auth.ErrUnavailable)
			}
			c.Set("Content-Type", "image/png")
			return c.SendStatus(http.StatusOK)
		}
		data, err := services.Map.PNG(ctx)
		if errors.Is(err, ErrMapPending) {
			c.Set("Retry-After", "10")
			return c.Status(http.StatusAccepted).JSON(fiber.Map{"state": "pending", "pollAfterSeconds": 10})
		}
		if err != nil || !validMapPNG(data) {
			return publicError(c, auth.ErrUnavailable)
		}
		c.Set("Content-Type", "image/png")
		if c.Method() == http.MethodHead {
			return c.SendStatus(http.StatusOK)
		}
		return c.Send(data)
	}
	app.Get("/api/map.png", read)
	app.Head("/api/map.png", read)
	// The place label and pin share map.png's credentials. The pin is a fraction
	// of the image, so the key still never reads coordinates.
	app.Get("/api/map/place", func(c fiber.Ctx) error {
		c.Set("Cache-Control", "private, no-store")
		c.Set("Referrer-Policy", "no-referrer")
		c.Set("X-Content-Type-Options", "nosniff")
		if ok, err := authorizeMapRead(c, services); !ok {
			return err
		}
		store, ok := services.Map.(interface {
			Place(context.Context) (contract.MapPlace, error)
		})
		if !ok {
			return publicError(c, auth.ErrUnavailable)
		}
		ctx, cancel := context.WithTimeout(c.Context(), 4*time.Second)
		defer cancel()
		place, err := store.Place(ctx)
		if errors.Is(err, ErrMapPending) {
			c.Set("Retry-After", "10")
			return c.Status(http.StatusAccepted).JSON(fiber.Map{"state": "pending", "pollAfterSeconds": 10})
		}
		if err != nil || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(place.ImageID) {
			return publicError(c, auth.ErrUnavailable)
		}
		return c.JSON(place)
	})
}

// authorizeMapRead accepts the scoped map key or an owner session. When it
// reports false, the denial response has already been written.
func authorizeMapRead(c fiber.Ctx, services Services) (bool, error) {
	// Parse the original query strictly: malformed escapes and duplicate keys
	// must not become an accepted key through query normalization.
	query, err := url.ParseQuery(string(c.Request().URI().QueryString()))
	keys, supplied := query["key"]
	if err != nil || supplied && (len(keys) != 1 || !mapKeyPattern.MatchString(keys[0])) {
		return false, sessionError(c, auth.ErrHandle)
	}
	if supplied && validMapKey(keys[0], services.MapKeySHA256) {
		return true, nil
	}
	// A supplied invalid key is rejected, even when another credential is
	// present. Native clients use the owner-session path without a query key.
	if supplied || services.Mobile == nil {
		return false, sessionError(c, auth.ErrHandle)
	}
	if _, err = services.Mobile.Authorize(c.Context(), bearer(c)); err != nil {
		return false, sessionError(c, err)
	}
	return true, nil
}

// ValidateMapDigest permits an empty configuration to revoke only keyed reads.
func ValidateMapDigest(value string) error {
	if value == "" {
		return nil
	}
	if data, err := hex.DecodeString(value); err != nil || len(data) != sha256.Size || len(value) != 64 {
		return errors.New("invalid map credential digest")
	}
	return nil
}
