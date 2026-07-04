package store

import (
	"context"
	"fmt"
)

// LakeGeom is a lake location sourced from the WDFW lowland-lakes crawl.
// Coordinates are WGS84 (lon/lat); Area is square metres; Elev is metres.
type LakeGeom struct {
	GeoCode  string
	Name     string
	County   *string
	ElevM    *float64
	AreaM2   *float64
	Lon      float64
	Lat      float64
	LakeType string // "lowland" | "high"
}

// UpsertLakeGeometry inserts or updates a lake keyed by geo_code, setting the
// centroid point (EPSG:4326) plus canonical name/county/elevation/area. The
// WDFW name is authoritative, so it overwrites any provisional stocking name.
// morph_source is left untouched: WDFW pages give area but no depth, so
// morphometry provenance stays unknown until NHD/PDF/estimate fills depth.
// Hand-written SQL because it uses PostGIS constructors (sqlc doesn't type
// ST_MakePoint params).
func (s *Store) UpsertLakeGeometry(ctx context.Context, g LakeGeom) error {
	const q = `
INSERT INTO lakes (geo_code, name, county, elev_m, area_m2, centroid, lake_type, updated_at)
VALUES ($1, $2, $3, $4, $5, ST_SetSRID(ST_MakePoint($6, $7), 4326), $8, now())
ON CONFLICT (geo_code) DO UPDATE SET
    name       = EXCLUDED.name,
    county     = COALESCE(EXCLUDED.county, lakes.county),
    elev_m     = COALESCE(EXCLUDED.elev_m, lakes.elev_m),
    area_m2    = COALESCE(EXCLUDED.area_m2, lakes.area_m2),
    centroid   = EXCLUDED.centroid,
    lake_type  = COALESCE(EXCLUDED.lake_type, lakes.lake_type),
    updated_at = now()`
	_, err := s.Pool.Exec(ctx, q, g.GeoCode, g.Name, g.County, g.ElevM, g.AreaM2, g.Lon, g.Lat, nullStr(g.LakeType))
	if err != nil {
		return fmt.Errorf("store: upsert lake geometry %s: %w", g.GeoCode, err)
	}
	return nil
}

// nullStr maps "" to a NULL string param so the lake_type CHECK isn't violated.
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// EstimateLakeDepths fills depth for lakes that have area but no depth yet, from
// a crude area+elevation heuristic (bigger/higher lakes trend deeper). Flagged
// morph_source='estimated'; real bathymetry/PDF depth upgrades it later. Never
// overwrites an existing depth. Feeds the water-temp proxy's thermal inertia.
func (s *Store) EstimateLakeDepths(ctx context.Context) (int64, error) {
	const q = `
UPDATE lakes SET
    depth_mean_m = LEAST(30, GREATEST(1.5, 1.5 + 2.2*ln(area_m2/10000.0 + 1) + COALESCE(elev_m,0)/700.0)),
    depth_max_m  = LEAST(75, GREATEST(2.0, 2.5*(1.5 + 2.2*ln(area_m2/10000.0 + 1) + COALESCE(elev_m,0)/700.0))),
    morph_source = 'estimated',
    updated_at   = now()
WHERE area_m2 IS NOT NULL AND depth_mean_m IS NULL`
	tag, err := s.Pool.Exec(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("store: estimate lake depths: %w", err)
	}
	return tag.RowsAffected(), nil
}

// LakePoint is a lake's id + centroid coordinates (WGS84 lon/lat) + mean depth.
type LakePoint struct {
	ID         int64
	Lon        float64
	Lat        float64
	DepthMeanM *float64
}

// LakesWithCentroid returns every lake that has a located centroid — the
// candidate set for weather ingestion and prediction.
func (s *Store) LakesWithCentroid(ctx context.Context) ([]LakePoint, error) {
	const q = `SELECT id, ST_X(centroid), ST_Y(centroid), depth_mean_m
	           FROM lakes WHERE centroid IS NOT NULL ORDER BY id`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: query lakes with centroid: %w", err)
	}
	defer rows.Close()

	var out []LakePoint
	for rows.Next() {
		var p LakePoint
		if err := rows.Scan(&p.ID, &p.Lon, &p.Lat, &p.DepthMeanM); err != nil {
			return nil, fmt.Errorf("store: scan lake point: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
