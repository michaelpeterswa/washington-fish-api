// Package fishwa ingests species presence from WDFW's FishWA ArcGIS service.
// Each per-species point layer lists the lakes where that species is present;
// we spatial-join those points to our lakes to seed species_presence — this is
// what unlocks warmwater species (bass/panfish/walleye) that the stocking feed
// doesn't cover.
package fishwa

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/michaelpeterswa/washington-fish-api/internal/config"
	"github.com/michaelpeterswa/washington-fish-api/internal/httpx"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
)

const userAgent = "washington-fish-api/0.1 (+https://github.com/michaelpeterswa/washington-fish-api)"

// matchMeters is how close a FishWA point must be to a lake centroid to count as
// that lake (both derive from WDFW, so they're near-coincident).
const matchMeters = 1200

// speciesLayers maps FishWA MapServer layer ids to the species name we store.
// Trout layers are included too (they add wild/non-stocked presence).
var speciesLayers = map[int]string{
	4: "Rainbow", 5: "Brown Trout", 6: "Brook Trout", 7: "Tiger Trout",
	8: "Cutthroat", 9: "Kokanee", 10: "Largemouth Bass", 11: "Smallmouth Bass",
	12: "Walleye", 13: "Tiger Muskie", 14: "Brown Bullhead", 15: "Yellow Perch",
	16: "Black Crappie", 17: "Pumpkinseed", 18: "Bluegill",
}

type queryResp struct {
	Features []struct {
		Geometry struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"geometry"`
	} `json:"features"`
	ExceededTransferLimit bool `json:"exceededTransferLimit"`
}

// Load pulls every species layer and upserts presence by spatial join.
func Load(ctx context.Context, c *config.Config, st *store.Store, logger *slog.Logger) error {
	client := httpx.New(httpx.WithTimeout(45*time.Second), httpx.WithLogger(logger))
	base := c.FishWAServiceURL

	var totalPts, totalMatched int
	for layer, sp := range speciesLayers {
		pts, err := fetchLayer(ctx, client, base, layer, sp)
		if err != nil {
			return fmt.Errorf("fishwa: layer %d (%s): %w", layer, sp, err)
		}
		matched, err := st.UpsertPresenceFromPoints(ctx, pts, matchMeters)
		if err != nil {
			return err
		}
		totalPts += len(pts)
		totalMatched += matched
		logger.InfoContext(ctx, "fishwa species layer",
			slog.String("species", sp), slog.Int("points", len(pts)), slog.Int("matched", matched))
	}
	logger.InfoContext(ctx, "fishwa presence load complete",
		slog.Int("points", totalPts), slog.Int("matched", totalMatched))
	return nil
}

func fetchLayer(ctx context.Context, client *httpx.Client, base string, layer int, sp string) ([]store.PresencePoint, error) {
	var out []store.PresencePoint
	const page = 1000
	for offset := 0; ; offset += page {
		q := url.Values{}
		q.Set("where", "1=1")
		q.Set("returnGeometry", "true")
		q.Set("outSR", "4326")
		q.Set("f", "json")
		q.Set("resultRecordCount", strconv.Itoa(page))
		q.Set("resultOffset", strconv.Itoa(offset))

		reqURL := fmt.Sprintf("%s/%d/query?%s", base, layer, q.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(ctx, req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}
		var qr queryResp
		if err := json.Unmarshal(body, &qr); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		for _, f := range qr.Features {
			if f.Geometry.X != 0 && f.Geometry.Y != 0 {
				out = append(out, store.PresencePoint{Species: sp, Lon: f.Geometry.X, Lat: f.Geometry.Y})
			}
		}
		if !qr.ExceededTransferLimit || len(qr.Features) == 0 {
			break
		}
	}
	return out, nil
}
