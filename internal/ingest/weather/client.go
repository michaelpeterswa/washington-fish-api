// Package weather ingests the Open-Meteo model forecast into the conditions
// table (phase 3a: model-only weather per lake). KED bias-correction against
// real observation stations is layered on in phase 3b.
package weather

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/michaelpeterswa/washington-fish-api/internal/httpx"
)

const userAgent = "washington-fish-api/0.1 (+https://github.com/michaelpeterswa/washington-fish-api)"

// LatLon is a query point (a lake centroid).
type LatLon struct {
	Lat float64
	Lon float64
}

// Current is Open-Meteo's latest values at a point.
type Current struct {
	Time            string   `json:"time"`
	TemperatureC    *float64 `json:"temperature_2m"`
	SurfacePressure *float64 `json:"surface_pressure"`
	WindMs          *float64 `json:"wind_speed_10m"`
	CloudPct        *float64 `json:"cloud_cover"`
}

// Hourly is Open-Meteo's aligned hourly arrays (values may be null).
type Hourly struct {
	Time            []string   `json:"time"`
	TemperatureC    []*float64 `json:"temperature_2m"`
	SurfacePressure []*float64 `json:"surface_pressure"`
	WindMs          []*float64 `json:"wind_speed_10m"`
	CloudPct        []*float64 `json:"cloud_cover"`
}

// Forecast is one location's model output.
type Forecast struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Elevation float64 `json:"elevation"`
	Current   Current `json:"current"`
	Hourly    Hourly  `json:"hourly"`
}

// Client queries the Open-Meteo forecast API.
type Client struct {
	baseURL  string
	pastDays int
	http     *httpx.Client
}

func NewClient(baseURL string, pastDays int, logger *slog.Logger) *Client {
	if pastDays < 0 {
		pastDays = 0
	}
	// Open-Meteo's public tier rate-limits per minute and doesn't send
	// Retry-After, so ramp backoff toward ~a minute and allow a couple windows.
	return &Client{
		baseURL:  baseURL,
		pastDays: pastDays,
		http: httpx.New(
			httpx.WithTimeout(60*time.Second),
			httpx.WithBackoff(2*time.Second, 65*time.Second),
			httpx.WithMaxElapsed(6*time.Minute),
			httpx.WithLogger(logger),
		),
	}
}

const vars = "temperature_2m,surface_pressure,wind_speed_10m,cloud_cover"

// FetchForecast returns one Forecast per input point, in the same order.
// Open-Meteo accepts comma-separated coordinates and returns an array (or a
// single object for one point), so a whole batch of lakes is one HTTP call.
func (c *Client) FetchForecast(ctx context.Context, pts []LatLon) ([]Forecast, error) {
	if len(pts) == 0 {
		return nil, nil
	}
	lats := make([]string, len(pts))
	lons := make([]string, len(pts))
	for i, p := range pts {
		lats[i] = strconv.FormatFloat(p.Lat, 'f', 5, 64)
		lons[i] = strconv.FormatFloat(p.Lon, 'f', 5, 64)
	}

	q := url.Values{}
	q.Set("latitude", strings.Join(lats, ","))
	q.Set("longitude", strings.Join(lons, ","))
	q.Set("current", vars)
	q.Set("hourly", vars)
	q.Set("forecast_days", "4")                  // ensures >=72 future hours after slicing to now
	q.Set("past_days", strconv.Itoa(c.pastDays)) // small bridge window for the water-temp EMA
	q.Set("timezone", "UTC")
	q.Set("wind_speed_unit", "ms")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("weather: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("weather: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("weather: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weather: status %d: %s", resp.StatusCode, truncate(body, 300))
	}

	// One point -> object; many -> array. Normalize to a slice.
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var fs []Forecast
		if err := json.Unmarshal(trimmed, &fs); err != nil {
			return nil, fmt.Errorf("weather: decode array: %w", err)
		}
		return fs, nil
	}
	var f Forecast
	if err := json.Unmarshal(trimmed, &f); err != nil {
		return nil, fmt.Errorf("weather: decode object: %w", err)
	}
	return []Forecast{f}, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
