package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// StationUpsert is one observation station to persist.
type StationUpsert struct {
	Source     string
	ExternalID string
	Lon        float64
	Lat        float64
	ElevM      float64
}

// UpsertStations writes the station network (idempotent on source+external_id).
func (s *Store) UpsertStations(ctx context.Context, sts []StationUpsert) error {
	if len(sts) == 0 {
		return nil
	}
	const q = `
INSERT INTO weather_stations (source, external_id, geom, elev_m)
VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326), $5)
ON CONFLICT (source, external_id) DO UPDATE SET
    geom = EXCLUDED.geom, elev_m = EXCLUDED.elev_m`
	b := &pgx.Batch{}
	for _, st := range sts {
		b.Queue(q, st.Source, st.ExternalID, st.Lon, st.Lat, st.ElevM)
	}
	br := s.Pool.SendBatch(ctx, b)
	defer br.Close()
	for range sts {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("store: upsert stations: %w", err)
		}
	}
	return nil
}

// StationPoint is a station's location for KED conditioning.
type StationPoint struct {
	Lon   float64
	Lat   float64
	ElevM float64
}

// AllStationPoints returns every station's lon/lat/elevation.
func (s *Store) AllStationPoints(ctx context.Context) ([]StationPoint, error) {
	rows, err := s.Pool.Query(ctx, `SELECT ST_X(geom), ST_Y(geom), elev_m FROM weather_stations`)
	if err != nil {
		return nil, fmt.Errorf("store: query stations: %w", err)
	}
	defer rows.Close()
	var out []StationPoint
	for rows.Next() {
		var p StationPoint
		if err := rows.Scan(&p.Lon, &p.Lat, &p.ElevM); err != nil {
			return nil, fmt.Errorf("store: scan station: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LakeElevPoint is a lake's id + centroid + elevation, for confidence solving.
type LakeElevPoint struct {
	ID    int64
	Lon   float64
	Lat   float64
	ElevM float64
}

// LakesForConfidence returns lakes that have both a centroid and an elevation
// (both required to place the lake in the KED drift).
func (s *Store) LakesForConfidence(ctx context.Context) ([]LakeElevPoint, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT id, ST_X(centroid), ST_Y(centroid), elev_m
FROM lakes WHERE centroid IS NOT NULL AND elev_m IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("store: query lakes for confidence: %w", err)
	}
	defer rows.Close()
	var out []LakeElevPoint
	for rows.Next() {
		var l LakeElevPoint
		if err := rows.Scan(&l.ID, &l.Lon, &l.Lat, &l.ElevM); err != nil {
			return nil, fmt.Errorf("store: scan lake elev point: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LakeConfidence is a computed confidence pair for one lake.
type LakeConfidence struct {
	LakeID   int64
	Nowcast  float64
	Forecast float64
}

// UpdateLakeConfidences writes the KED confidence fields in one batch.
func (s *Store) UpdateLakeConfidences(ctx context.Context, cs []LakeConfidence) error {
	if len(cs) == 0 {
		return nil
	}
	const q = `UPDATE lakes SET confidence_nowcast = $2, confidence_forecast = $3 WHERE id = $1`
	b := &pgx.Batch{}
	for _, c := range cs {
		b.Queue(q, c.LakeID, c.Nowcast, c.Forecast)
	}
	br := s.Pool.SendBatch(ctx, b)
	defer br.Close()
	for range cs {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("store: update lake confidences: %w", err)
		}
	}
	return nil
}
