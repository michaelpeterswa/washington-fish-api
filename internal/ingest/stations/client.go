// Package stations ingests NWS observation-station metadata (position +
// elevation) into weather_stations. These are the KED conditioning points: the
// confidence field depends only on their geometry, so no observation VALUES are
// fetched here (that's phase 3b-2, bias correction).
package stations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/michaelpeterswa/washington-fish-api/internal/httpx"
)

// NWS requires a User-Agent identifying the app + a contact, or it 403s.
const userAgent = "washington-fish-api/0.1 (michael@michaelpeterswa.com)"

// Station is one observation station.
type Station struct {
	ID    string
	Lon   float64
	Lat   float64
	ElevM float64
}

type Client struct {
	baseURL string
	http    *httpx.Client
}

func NewClient(baseURL string, logger *slog.Logger) *Client {
	return &Client{baseURL: baseURL, http: httpx.New(httpx.WithTimeout(45*time.Second), httpx.WithLogger(logger))}
}

type stationsResp struct {
	Features []struct {
		Properties struct {
			StationIdentifier string `json:"stationIdentifier"`
			Elevation         struct {
				Value *float64 `json:"value"`
			} `json:"elevation"`
		} `json:"properties"`
		Geometry struct {
			Coordinates []float64 `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
	Pagination struct {
		Next string `json:"next"`
	} `json:"pagination"`
}

// FetchWAStations returns all WA stations with a known elevation, following the
// cursor pagination until a page comes back empty.
func (c *Client) FetchWAStations(ctx context.Context) ([]Station, error) {
	url := c.baseURL + "?state=WA&limit=500"
	const maxPages = 40 // safety cap (~20k stations)

	var out []Station
	for page := 0; page < maxPages && url != ""; page++ {
		var resp stationsResp
		if err := c.getJSON(ctx, url, &resp); err != nil {
			return nil, err
		}
		if len(resp.Features) == 0 {
			break
		}
		for _, f := range resp.Features {
			if f.Properties.Elevation.Value == nil || len(f.Geometry.Coordinates) < 2 {
				continue // no elevation or coords -> unusable as a drift anchor
			}
			out = append(out, Station{
				ID:    f.Properties.StationIdentifier,
				Lon:   f.Geometry.Coordinates[0],
				Lat:   f.Geometry.Coordinates[1],
				ElevM: *f.Properties.Elevation.Value,
			})
		}
		url = resp.Pagination.Next
	}
	return out, nil
}

// getJSON fetches with retries — NWS pagination requests are occasionally slow
// or 5xx, and one hung page shouldn't fail the whole ingest.
func (c *Client) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("stations: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/geo+json")

	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("stations: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("stations: status %d: %s", resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("stations: decode: %w", err)
	}
	return nil
}
