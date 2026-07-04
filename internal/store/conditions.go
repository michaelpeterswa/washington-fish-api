package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Condition is one cached weather record for a lake at a (valid_at, horizon).
// horizon_h = 0 is the nowcast. Nullable fields are pointers.
type Condition struct {
	LakeID           int64
	ValidAt          time.Time
	HorizonH         int32
	AirTempC         *float64
	WaterTempC       *float64
	PressureHpa      *float64
	PressureTendency *float64
	WindMps          *float64
	CloudPct         *float64
}

// upsertConditionSQL writes the weather columns plus the modeled water temp.
// solunar_state and confidence are owned by other passes and left untouched on
// conflict, so re-running the weather poll never clobbers them.
const upsertConditionSQL = `
INSERT INTO conditions
    (lake_id, valid_at, horizon_h, air_temp_c, water_temp_c, pressure_hpa, pressure_tendency, wind_mps, cloud_pct, computed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
ON CONFLICT (lake_id, valid_at, horizon_h) DO UPDATE SET
    air_temp_c        = EXCLUDED.air_temp_c,
    water_temp_c      = EXCLUDED.water_temp_c,
    pressure_hpa      = EXCLUDED.pressure_hpa,
    pressure_tendency = EXCLUDED.pressure_tendency,
    wind_mps          = EXCLUDED.wind_mps,
    cloud_pct         = EXCLUDED.cloud_pct,
    computed_at       = now()`

// UpsertConditions writes a batch of condition rows in one round trip.
func (s *Store) UpsertConditions(ctx context.Context, cs []Condition) error {
	if len(cs) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, c := range cs {
		b.Queue(upsertConditionSQL, c.LakeID, c.ValidAt, c.HorizonH,
			c.AirTempC, c.WaterTempC, c.PressureHpa, c.PressureTendency, c.WindMps, c.CloudPct)
	}
	br := s.Pool.SendBatch(ctx, b)
	defer br.Close()
	for range cs {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("store: upsert conditions batch: %w", err)
		}
	}
	return nil
}

// WaterSeed is a lake's last modeled water temperature and the time it was for
// — the state the next poll's EMA continues from.
type WaterSeed struct {
	Temp float64
	At   time.Time
}

// PriorWaterTemps returns each lake's most recent nowcast water temperature, so
// the water-temp EMA can continue from stored state instead of re-fetching a
// long air-temp history every poll.
func (s *Store) PriorWaterTemps(ctx context.Context) (map[int64]WaterSeed, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT DISTINCT ON (lake_id) lake_id, valid_at, water_temp_c
FROM conditions
WHERE horizon_h = 0 AND water_temp_c IS NOT NULL
ORDER BY lake_id, valid_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: prior water temps: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]WaterSeed)
	for rows.Next() {
		var id int64
		var at time.Time
		var temp float64
		if err := rows.Scan(&id, &at, &temp); err != nil {
			return nil, fmt.Errorf("store: scan water seed: %w", err)
		}
		out[id] = WaterSeed{Temp: temp, At: at}
	}
	return out, rows.Err()
}

// DeleteStaleConditions removes rows whose valid_at is before `before`, keeping
// the conditions table bounded (the current hour + the forecast window).
func (s *Store) DeleteStaleConditions(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM conditions WHERE valid_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("store: delete stale conditions: %w", err)
	}
	return tag.RowsAffected(), nil
}
