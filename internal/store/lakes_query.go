package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// LakeSummary is a lake row for list/search/rank responses. Nullable columns
// are pointers; DistanceKm is set only when the query supplies an origin.
type LakeSummary struct {
	ID                 int64
	GeoCode            *string
	Name               string
	County             *string
	LakeType           *string
	Lon                *float64
	Lat                *float64
	AreaM2             *float64
	ElevM              *float64
	DepthMaxM          *float64
	DepthMeanM         *float64
	ConfidenceNowcast  *float64
	ConfidenceForecast *float64
	DistanceKm         *float64
}

// LakeSearch holds the optional filters for QueryLakes. An origin (Lat+Lon)
// enables distance output; adding RadiusKm restricts to that radius.
type LakeSearch struct {
	Name     string
	County   string
	Species  string
	LakeType string
	Lat      *float64
	Lon      *float64
	RadiusKm float64
	Limit    int
}

const lakeCols = `id, geo_code, name, county, lake_type,
	ST_X(centroid), ST_Y(centroid), area_m2, elev_m, depth_max_m, depth_mean_m,
	confidence_nowcast, confidence_forecast`

func scanLake(row pgx.Row, l *LakeSummary) error {
	return row.Scan(&l.ID, &l.GeoCode, &l.Name, &l.County, &l.LakeType,
		&l.Lon, &l.Lat, &l.AreaM2, &l.ElevM, &l.DepthMaxM, &l.DepthMeanM,
		&l.ConfidenceNowcast, &l.ConfidenceForecast, &l.DistanceKm)
}

// QueryLakes runs a filtered lake search. Placeholders are built dynamically so
// unused filters don't appear in the SQL at all.
func (s *Store) QueryLakes(ctx context.Context, f LakeSearch) ([]LakeSummary, error) {
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }

	distSelect := "NULL::float8"
	orderBy := " ORDER BY name"
	where := "WHERE TRUE"

	if f.Lat != nil && f.Lon != nil {
		pt := fmt.Sprintf("ST_SetSRID(ST_MakePoint(%s, %s), 4326)::geography", arg(*f.Lon), arg(*f.Lat))
		distSelect = fmt.Sprintf("ST_Distance(centroid::geography, %s) / 1000.0", pt)
		orderBy = " ORDER BY dist_km NULLS LAST"
		if f.RadiusKm > 0 {
			where += fmt.Sprintf(" AND centroid IS NOT NULL AND ST_DWithin(centroid::geography, %s, %s)", pt, arg(f.RadiusKm*1000.0))
		}
	}
	if f.Name != "" {
		where += " AND name ILIKE " + arg("%"+f.Name+"%")
	}
	if f.County != "" {
		where += " AND county ILIKE " + arg(f.County)
	}
	if f.LakeType != "" {
		where += " AND lake_type = " + arg(f.LakeType)
	}
	if f.Species != "" {
		where += " AND EXISTS (SELECT 1 FROM species_presence sp WHERE sp.lake_id = l.id AND sp.species ILIKE " + arg(f.Species) + ")"
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	q := fmt.Sprintf(`SELECT %s, %s AS dist_km FROM lakes l %s%s LIMIT %s`,
		lakeCols, distSelect, where, orderBy, arg(limit))

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query lakes: %w", err)
	}
	defer rows.Close()

	var out []LakeSummary
	for rows.Next() {
		var l LakeSummary
		if err := scanLake(rows, &l); err != nil {
			return nil, fmt.Errorf("store: scan lake: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ErrLakeNotFound is returned by LakeByID when no lake has the given id.
var ErrLakeNotFound = errors.New("store: lake not found")

// LakeByID returns one lake's full detail.
func (s *Store) LakeByID(ctx context.Context, id int64) (*LakeSummary, error) {
	q := fmt.Sprintf(`SELECT %s, NULL::float8 AS dist_km FROM lakes l WHERE id = $1`, lakeCols)
	var l LakeSummary
	err := scanLake(s.Pool.QueryRow(ctx, q, id), &l)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLakeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: lake by id: %w", err)
	}
	return &l, nil
}
