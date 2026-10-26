package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type PhoneFix struct {
	Latitude, Longitude, AccuracyMeters float64
	ObservedAt                          time.Time
	Allowed                             bool
}

func (f PhoneFix) usable(now time.Time) bool {
	return f.Allowed && !math.IsNaN(f.Latitude) && !math.IsNaN(f.Longitude) && math.Abs(f.Latitude) <= 90 && math.Abs(f.Longitude) <= 180 && f.AccuracyMeters >= 0 && f.AccuracyMeters <= 10000 && !f.ObservedAt.IsZero() && !f.ObservedAt.After(now.Add(time.Minute)) && now.Sub(f.ObservedAt) <= 30*time.Minute
}

type Weather struct {
	TemperatureF, HighF, LowF              float64
	Code                                   int
	CloudPercent                           float64
	Sunrise, Sunset, ObservedAt, FetchedAt time.Time
}
type WeatherClient struct {
	HTTP     *http.Client
	Endpoint string
}

func newWeatherClient() *WeatherClient {
	return &WeatherClient{HTTP: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Endpoint: "https://api.open-meteo.com/v1/forecast"}
}
func (c *WeatherClient) Fetch(ctx context.Context, fix PhoneFix, now time.Time) (Weather, error) {
	if !fix.usable(now) {
		return Weather{}, errors.New("Phone location unavailable")
	}
	// Only an approximately one-kilometre cell leaves the device. Coordinates
	// remain in memory and are never logged or included in error messages.
	q := url.Values{"latitude": {approximateComponent(fix.Latitude)}, "longitude": {approximateComponent(fix.Longitude)}, "current": {"temperature_2m,weather_code,cloud_cover"}, "daily": {"temperature_2m_max,temperature_2m_min,sunrise,sunset"}, "temperature_unit": {"fahrenheit"}, "timeformat": {"unixtime"}, "timezone": {"auto"}, "forecast_days": {"1"}}
	request, e := http.NewRequestWithContext(ctx, "GET", c.Endpoint+"?"+q.Encode(), nil)
	if e != nil {
		return Weather{}, errors.New("Weather unavailable")
	}
	request.Header.Set("User-Agent", "Suburban/Android (+https://suburban.jeremy.ninja/privacy)")
	response, e := c.HTTP.Do(request)
	if e != nil {
		return Weather{}, errors.New("Weather unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Weather{}, errors.New("Weather unavailable")
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(raw) > 65536 {
		return Weather{}, errors.New("Weather unavailable")
	}
	var body struct {
		Current struct {
			Time  int64    `json:"time"`
			Temp  *float64 `json:"temperature_2m"`
			Code  *int     `json:"weather_code"`
			Cloud *float64 `json:"cloud_cover"`
		} `json:"current"`
		Daily struct {
			High []float64 `json:"temperature_2m_max"`
			Low  []float64 `json:"temperature_2m_min"`
			Rise []int64   `json:"sunrise"`
			Set  []int64   `json:"sunset"`
		} `json:"daily"`
		Units struct {
			Temp string `json:"temperature_2m"`
		} `json:"current_units"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Current.Temp == nil || body.Current.Code == nil || body.Current.Cloud == nil || body.Units.Temp != "°F" || len(body.Daily.High) != 1 || len(body.Daily.Low) != 1 || len(body.Daily.Rise) != 1 || len(body.Daily.Set) != 1 {
		return Weather{}, errors.New("Weather unavailable")
	}
	w := Weather{TemperatureF: *body.Current.Temp, Code: *body.Current.Code, CloudPercent: *body.Current.Cloud, HighF: body.Daily.High[0], LowF: body.Daily.Low[0], Sunrise: time.Unix(body.Daily.Rise[0], 0), Sunset: time.Unix(body.Daily.Set[0], 0), ObservedAt: time.Unix(body.Current.Time, 0), FetchedAt: now}
	if weatherKind(w.Code) == "unknown" || w.HighF < w.LowF || w.TemperatureF < -150 || w.TemperatureF > 160 || w.CloudPercent < 0 || w.CloudPercent > 100 || !w.Sunset.After(w.Sunrise) || w.ObservedAt.After(now.Add(5*time.Minute)) || now.Sub(w.ObservedAt) > 2*time.Hour {
		return Weather{}, errors.New("Weather unavailable")
	}
	return w, nil
}
func approximateComponent(value float64) string {
	return strconv.FormatFloat(math.Round(value*100)/100, 'f', 2, 64)
}
func weatherKind(code int) string {
	switch code {
	case 0:
		return "clear"
	case 1, 2, 3:
		return "cloud"
	case 45, 48:
		return "fog"
	case 51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 80, 81, 82:
		return "rain"
	case 71, 73, 75, 77, 85, 86:
		return "snow"
	case 95, 96, 99:
		return "thunder"
	}
	return "unknown"
}
