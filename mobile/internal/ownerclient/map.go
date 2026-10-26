package ownerclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/mapview"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"image"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"sync"
	"time"
)

var ErrMapPending = errors.New("Map is being prepared")

// One immutable decoded image is reused only after the API authorizes fresh
// metadata for that exact object. It is never persisted or shared globally.
type mapImageCache struct {
	sync.Mutex
	id, version, hash string
	bytes             int64
	image             *image.NRGBA
	epoch             uint64
}

func (c *Client) clearMapImage() {
	c.mapCache.Lock()
	defer c.mapCache.Unlock()
	c.mapCache.id, c.mapCache.version, c.mapCache.hash = "", "", ""
	c.mapCache.bytes, c.mapCache.image = 0, nil
	c.mapCache.epoch++
}

// MapView reads a single version-consistent owner-only image/position view.
func (c *Client) MapView(ctx context.Context, session Session) (contract.MapView, image.Image, error) {
	var view contract.MapView
	if session.AccessHandle == "" {
		c.clearMapImage()
		return view, nil, ErrUnauthorized
	}
	c.mapCache.Lock()
	epoch := c.mapCache.epoch
	c.mapCache.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/map/view", nil)
	if err != nil {
		return view, nil, errors.New("Map unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+session.AccessHandle)
	req.Header.Set("Accept", contract.MapTicketMediaType)
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return view, nil, errors.New("Map unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 410 {
		c.clearMapImage()
		return view, nil, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusAccepted {
		return view, nil, ErrMapPending
	}
	if resp.StatusCode != 200 {
		return view, nil, errors.New("Map unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+8193))
	if err != nil || len(raw) > 4*1024*1024+8192 || json.Unmarshal(raw, &view) != nil {
		return contract.MapView{}, nil, errors.New("Map unavailable")
	}
	data := view.PNG
	if view.DownloadURL != "" {
		if len(data) != 0 || view.ImageBytes <= 0 || view.ImageBytes > contract.MaxMapImageBytes || !contract.ValidMapDownloadURL(view.DownloadURL, view.ImageID, time.Now()) {
			return contract.MapView{}, nil, errors.New("Map unavailable")
		}
		c.mapCache.Lock()
		cached := c.mapCache.image
		changed := epoch != c.mapCache.epoch
		match := cached != nil && c.mapCache.id == view.ImageID && c.mapCache.version == view.ImageVersion && c.mapCache.hash == view.ImageSHA256 && c.mapCache.bytes == view.ImageBytes
		c.mapCache.Unlock()
		if changed {
			return contract.MapView{}, nil, ErrUnauthorized
		}
		if match && mapview.Valid(view, cached, time.Now()) {
			view.DownloadURL = ""
			return view, cached, nil
		}
		imageRequest, e := http.NewRequestWithContext(ctx, http.MethodGet, view.DownloadURL, nil)
		if e != nil {
			return contract.MapView{}, nil, errors.New("Map unavailable")
		}
		downloadClient := client
		downloadClient.Jar = nil
		downloadClient.Timeout = 30 * time.Second
		imageResponse, e := downloadClient.Do(imageRequest)
		if e != nil {
			return contract.MapView{}, nil, errors.New("Map unavailable")
		}
		defer imageResponse.Body.Close()
		if imageResponse.StatusCode != http.StatusOK || imageResponse.ContentLength > contract.MaxMapImageBytes || (imageResponse.ContentLength >= 0 && imageResponse.ContentLength != view.ImageBytes) {
			return contract.MapView{}, nil, errors.New("Map unavailable")
		}
		data, e = io.ReadAll(io.LimitReader(imageResponse.Body, view.ImageBytes+1))
		sum := sha256.Sum256(data)
		if e != nil || int64(len(data)) != view.ImageBytes || hex.EncodeToString(sum[:]) != view.ImageSHA256 {
			return contract.MapView{}, nil, errors.New("Map unavailable")
		}
	} else if len(data) > 3*1024*1024 {
		return contract.MapView{}, nil, errors.New("Map unavailable")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != view.Width || cfg.Height != view.Height || cfg.Width > contract.MaxMapDimension || cfg.Height > contract.MaxMapDimension || view.NoticeRows < 0 || view.NoticeRows > 256 || view.NoticeRows >= cfg.Height {
		return contract.MapView{}, nil, errors.New("Map unavailable")
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil || !mapview.Valid(view, decoded, time.Now()) {
		return contract.MapView{}, nil, errors.New("Map unavailable")
	}
	// Normalize the full image once. Gestures reuse these bytes without copying
	// a multi-megabyte RGBA image on every frame.
	img := image.NewNRGBA(decoded.Bounds())
	draw.Draw(img, img.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	view.DownloadURL = ""
	view.PNG = nil
	if view.ImageSHA256 != "" {
		c.mapCache.Lock()
		if epoch != c.mapCache.epoch {
			c.mapCache.Unlock()
			return contract.MapView{}, nil, ErrUnauthorized
		}
		c.mapCache.id, c.mapCache.version, c.mapCache.hash = view.ImageID, view.ImageVersion, view.ImageSHA256
		c.mapCache.bytes, c.mapCache.image = view.ImageBytes, img
		c.mapCache.Unlock()
	}
	return view, img, nil
}

// MapImage reads only the cached private PNG with the existing owner session.
func (c *Client) MapImage(ctx context.Context, session Session) (image.Image, error) {
	if session.AccessHandle == "" {
		return nil, ErrUnauthorized
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/map.png", nil)
	if err != nil {
		return nil, errors.New("Map unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+session.AccessHandle)
	req.Header.Set("Accept", "image/png")
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Map unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 410 {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusFound {
		address := resp.Header.Get("Location")
		if !contract.ValidMapDownloadURL(address, "", time.Now()) {
			return nil, errors.New("Map unavailable")
		}
		follow, e := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if e != nil {
			return nil, errors.New("Map unavailable")
		}
		resp.Body.Close()
		downloadClient := client
		downloadClient.Jar = nil
		downloadClient.Timeout = 30 * time.Second
		resp, e = downloadClient.Do(follow)
		if e != nil {
			return nil, errors.New("Map unavailable")
		}
		defer resp.Body.Close()
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || resp.ContentLength > contract.MaxMapImageBytes {
		return nil, errors.New("Map unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, contract.MaxMapImageBytes+1))
	if err != nil || len(data) > contract.MaxMapImageBytes {
		return nil, errors.New("Map unavailable")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > contract.MaxMapDimension || config.Height > contract.MaxMapDimension {
		return nil, errors.New("Map unavailable")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("Map unavailable")
	}
	return img, nil
}
