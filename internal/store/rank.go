package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// RankRow bundles everything needed to score one candidate lake for ranking:
// its summary + distance, the last catchable-plant date, and the nowcast
// weather — fetched in one query to avoid N+1.
type RankRow struct {
	Lake             LakeSummary
	LastPlantDate    *time.Time
	AirTempC         *float64
	WaterTempC       *float64
	PressureTendency *float64
	WindMps          *float64
	CloudPct         *float64
}

// RankParams filters the candidate set for ranking.
type RankParams struct {
	Lat      float64
	Lon      float64
	RadiusKm float64
	Species  string // optional
	Limit    int
}

// RankCandidates returns lakes within RadiusKm of the origin (optionally
// filtered by species), each joined to its last catchable plant and nowcast
// weather. The caller scores + sorts.
func (s *Store) RankCandidates(ctx context.Context, p RankParams) ([]RankRow, error) {
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }

	pt := fmt.Sprintf("ST_SetSRID(ST_MakePoint(%s, %s), 4326)::geography", arg(p.Lon), arg(p.Lat))
	radius := arg(p.RadiusKm * 1000.0)

	where := fmt.Sprintf("l.centroid IS NOT NULL AND ST_DWithin(l.centroid::geography, %s, %s)", pt, radius)
	if p.Species != "" {
		where += " AND EXISTS (SELECT 1 FROM species_presence sp WHERE sp.lake_id = l.id AND sp.species ILIKE " + arg(p.Species) + ")"
	}

	limit := p.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	q := fmt.Sprintf(`
SELECT l.id, l.geo_code, l.name, l.county, l.lake_type,
       ST_X(l.centroid), ST_Y(l.centroid), l.area_m2, l.elev_m, l.depth_max_m, l.depth_mean_m,
       l.confidence_nowcast, l.confidence_forecast,
       ST_Distance(l.centroid::geography, %s) / 1000.0 AS dist_km,
       cp.last_plant_date,
       c.air_temp_c, c.water_temp_c, c.pressure_tendency, c.wind_mps, c.cloud_pct
FROM lakes l
LEFT JOIN LATERAL (
    SELECT max(plant_date) AS last_plant_date
    FROM stocking_events se
    WHERE se.lake_id = l.id AND se.size_class IN ('legals', 'adult')
) cp ON TRUE
LEFT JOIN LATERAL (
    SELECT air_temp_c, water_temp_c, pressure_tendency, wind_mps, cloud_pct
    FROM conditions
    WHERE lake_id = l.id AND horizon_h = 0
    ORDER BY valid_at DESC
    LIMIT 1
) c ON TRUE
WHERE %s
ORDER BY dist_km
LIMIT %s`, pt, where, arg(limit))

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: rank candidates: %w", err)
	}
	defer rows.Close()

	var out []RankRow
	for rows.Next() {
		var r RankRow
		var lastPlant pgtype.Date
		if err := rows.Scan(
			&r.Lake.ID, &r.Lake.GeoCode, &r.Lake.Name, &r.Lake.County, &r.Lake.LakeType,
			&r.Lake.Lon, &r.Lake.Lat, &r.Lake.AreaM2, &r.Lake.ElevM, &r.Lake.DepthMaxM, &r.Lake.DepthMeanM,
			&r.Lake.ConfidenceNowcast, &r.Lake.ConfidenceForecast,
			&r.Lake.DistanceKm, &lastPlant,
			&r.AirTempC, &r.WaterTempC, &r.PressureTendency, &r.WindMps, &r.CloudPct,
		); err != nil {
			return nil, fmt.Errorf("store: scan rank row: %w", err)
		}
		if lastPlant.Valid {
			t := lastPlant.Time
			r.LastPlantDate = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
