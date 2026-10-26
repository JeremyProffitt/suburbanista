package contract

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxMapImageBytes = 24 * 1024 * 1024
const MaxMapDimension = 4096
const MapTicketMediaType = "application/vnd.suburban.map-ticket+json"

var mapDownloadPath = regexp.MustCompile(`^/[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]/map-cache/([a-f0-9]{32})\.png$`)

// ValidMapDownloadURL confines short-lived credentials to one immutable map
// object on the existing private S3 endpoint. Never include the URL in errors.
func ValidMapDownloadURL(address, imageID string, now time.Time) bool {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != "s3.us-east-1.amazonaws.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	match := mapDownloadPath.FindStringSubmatch(u.Path)
	if match == nil || imageID != "" && match[1] != imageID {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	allowed := map[string]bool{"X-Amz-Algorithm": true, "X-Amz-Credential": true, "X-Amz-Date": true, "X-Amz-Expires": true, "X-Amz-SignedHeaders": true, "X-Amz-Signature": true, "X-Amz-Security-Token": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-SignedHeaders") != "host" || !strings.HasSuffix(q.Get("X-Amz-Credential"), "/us-east-1/s3/aws4_request") || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(q.Get("X-Amz-Signature")) {
		return false
	}
	at, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
	if err != nil {
		return false
	}
	seconds, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	return err == nil && seconds > 0 && seconds <= 120 && !at.After(now.Add(time.Minute)) && now.Before(at.Add(time.Duration(seconds)*time.Second))
}

// MapPlace is what the scoped map key may learn besides the image: the
// geofence name (Home, Work or empty) and the vehicle's position as a fraction
// of the image width and height. It never carries coordinates.
type MapPlace struct {
	Place      string    `json:"place"`
	ImageID    string    `json:"imageId"`
	ObservedAt time.Time `json:"observedAt"`
	PinVisible bool      `json:"pinVisible"`
	PinX       float64   `json:"pinX"`
	PinY       float64   `json:"pinY"`
}

// MapView is returned only to an authenticated owner. Image bytes and position
// are read from one immutable image reference, so separate requests cannot mix
// generations. Coordinates never enter URLs or diagnostic messages.
type MapView struct {
	Source              string    `json:"source,omitempty"`
	OriginRevision      string    `json:"originRevision,omitempty"`
	VehicleRevision     string    `json:"vehicleRevision,omitempty"`
	Place               string    `json:"place,omitempty"`
	ObservationRevision string    `json:"observationRevision"`
	ImageID             string    `json:"imageId"`
	ImageVersion        string    `json:"imageVersion"`
	PNG                 []byte    `json:"png,omitempty"`
	DownloadURL         string    `json:"downloadUrl,omitempty"`
	ImageBytes          int64     `json:"imageBytes,omitempty"`
	ImageSHA256         string    `json:"imageSha256,omitempty"`
	NoticeRows          int       `json:"noticeRows,omitempty"`
	GeneratedAt         time.Time `json:"generatedAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
	ObservedAt          time.Time `json:"observedAt"`
	ReceivedAt          time.Time `json:"receivedAt"`
	Latitude            float64   `json:"latitude"`
	Longitude           float64   `json:"longitude"`
	CenterLatitude      float64   `json:"centerLatitude"`
	CenterLongitude     float64   `json:"centerLongitude"`
	Zoom                float64   `json:"zoom"`
	Width               int       `json:"width"`
	Height              int       `json:"height"`
	Attribution         string    `json:"attribution"`
}
