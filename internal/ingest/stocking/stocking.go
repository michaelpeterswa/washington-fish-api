package stocking

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/michaelpeterswa/washington-fish-api/internal/config"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
	"github.com/michaelpeterswa/washington-fish-api/internal/store/db"
)

// Poll fetches recent Trout/Kokanee plants from the Socrata feed and upserts
// them into lakes (keyed by geo_code) + stocking_events (deduped on the Socrata
// :id). Idempotent: re-running inserts only rows not already seen.
func Poll(ctx context.Context, c *config.Config, st *store.Store, logger *slog.Logger) error {
	lookback := c.StockingLookbackDays
	if lookback <= 0 {
		lookback = 365
	}
	since := time.Now().AddDate(0, 0, -lookback)

	client := NewClient(c.StockingURL, c.SocrataAppToken, logger)
	rows, err := client.FetchTroutSince(ctx, since)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "fetched stocking rows",
		slog.Int("rows", len(rows)),
		slog.String("since", since.Format("2006-01-02")))

	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := st.Q.WithTx(tx)

	lakeIDByGeo := make(map[string]int64)
	var skippedNoGeo, newEvents, dupEvents int

	for _, r := range rows {
		// Uppercase so case variants (L1264 vs l1264) map to one lake.
		geo := strings.ToUpper(strings.TrimSpace(r.GeoCode))
		if geo == "" {
			skippedNoGeo++
			continue
		}

		lakeID, ok := lakeIDByGeo[geo]
		if !ok {
			lakeID, err = q.UpsertLakeByGeoCode(ctx, db.UpsertLakeByGeoCodeParams{
				GeoCode: &geo,
				Name:    lakeName(r),
				County:  strPtr(strings.TrimSpace(r.County)),
				ElevM:   parseElev(r.Elevation),
			})
			if err != nil {
				return err
			}
			lakeIDByGeo[geo] = lakeID
		}

		n, err := q.InsertStockingEvent(ctx, db.InsertStockingEventParams{
			LakeID:       &lakeID,
			Species:      strings.TrimSpace(r.Species),
			Count:        parseCount(r.NumberReleased),
			SizeClass:    strPtr(strings.ToLower(strings.TrimSpace(r.Lifestage))),
			PlantDate:    parseDate(r.ReleaseStartDate),
			Hatchery:     strPtr(strings.TrimSpace(r.Facility)),
			SocrataRowID: r.SocrataID,
		})
		if err != nil {
			return err
		}
		if n == 1 {
			newEvents++
		} else {
			dupEvents++
		}
	}

	// Derive species presence (knowledge base) from the freshly-upserted events.
	if err := q.RebuildStockingPresence(ctx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	logger.InfoContext(ctx, "stocking poll complete",
		slog.Int("lakes", len(lakeIDByGeo)),
		slog.Int("new_events", newEvents),
		slog.Int("dup_events", dupEvents),
		slog.Int("skipped_no_geocode", skippedNoGeo))
	return nil
}

// lakeName derives a provisional lake name from the messy release_location.
// NHD's canonical GNIS name supersedes this in phase 2b.
func lakeName(r Row) string {
	name := strings.Join(strings.Fields(r.ReleaseLocation), " ") // collapse whitespace
	if name == "" {
		return strings.TrimSpace(r.GeoCode)
	}
	return name
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseCount(s string) *int32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Socrata delivers numbers as strings, sometimes with a decimal (e.g. "5000.0").
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		v := int32(f)
		return &v
	}
	return nil
}

// feetPerMetre converts the feed's elevation (WDFW reports feet) to metres,
// which is what the KED elevation drift covariate expects.
const feetPerMetre = 3.280839895

func parseElev(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		m := f / feetPerMetre
		return &m
	}
	return nil
}

func parseDate(s string) pgtype.Date {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return pgtype.Date{}
	}
	t, err := time.Parse("2006-01-02", s[:10])
	if err != nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}
