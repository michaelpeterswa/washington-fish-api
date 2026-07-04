package stations

import (
	"context"
	"log/slog"

	"github.com/michaelpeterswa/washington-fish-api/internal/config"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
	"github.com/michaelpeterswa/washington-fish-api/internal/weather/field"
)

// Load ingests the NWS station network, then recomputes each lake's KED
// confidence from the updated network geometry. Run when the station network
// changes (periodically) — the confidence is time-invariant otherwise.
func Load(ctx context.Context, c *config.Config, st *store.Store, logger *slog.Logger) error {
	client := NewClient(c.NWSStationsURL, logger)
	found, err := client.FetchWAStations(ctx)
	if err != nil {
		return err
	}

	ups := make([]store.StationUpsert, len(found))
	for i, s := range found {
		ups[i] = store.StationUpsert{Source: "nws", ExternalID: s.ID, Lon: s.Lon, Lat: s.Lat, ElevM: s.ElevM}
	}
	if err := st.UpsertStations(ctx, ups); err != nil {
		return err
	}
	logger.InfoContext(ctx, "ingested nws stations", slog.Int("stations", len(ups)))

	return computeConfidences(ctx, st, logger)
}

// computeConfidences solves KED variance per lake against the current station
// network and stores the confidence fields.
func computeConfidences(ctx context.Context, st *store.Store, logger *slog.Logger) error {
	pts, err := st.AllStationPoints(ctx)
	if err != nil {
		return err
	}
	if len(pts) < 3 {
		logger.WarnContext(ctx, "too few stations for KED confidence", slog.Int("stations", len(pts)))
		return nil
	}
	fpts := make([]field.Point, len(pts))
	for i, p := range pts {
		fpts[i] = field.Point{Lon: p.Lon, Lat: p.Lat, ElevM: p.ElevM}
	}
	ss := field.NewStationSet(fpts)

	lakes, err := st.LakesForConfidence(ctx)
	if err != nil {
		return err
	}

	updates := make([]store.LakeConfidence, 0, len(lakes))
	var failed int
	for _, l := range lakes {
		cf, err := ss.Confidence(field.Point{Lon: l.Lon, Lat: l.Lat, ElevM: l.ElevM})
		if err != nil {
			failed++
			continue
		}
		updates = append(updates, store.LakeConfidence{LakeID: l.ID, Nowcast: cf.Nowcast, Forecast: cf.Forecast})
	}
	if err := st.UpdateLakeConfidences(ctx, updates); err != nil {
		return err
	}

	logger.InfoContext(ctx, "computed KED confidence",
		slog.Int("stations", len(pts)),
		slog.Int("lakes_scored", len(updates)),
		slog.Int("failed", failed))
	return nil
}
