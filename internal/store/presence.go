package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PresencePoint is a species observed at a location (from FishWA).
type PresencePoint struct {
	Species string
	Lon     float64
	Lat     float64
}

// Match each FishWA point to the NEAREST lake centroid within maxMeters (KNN via
// the GIST index), and record the species there. Points with no lake in range
// (FishWA waters we didn't crawl) simply insert nothing.
const upsertPresenceNearestSQL = `
INSERT INTO species_presence (lake_id, species, confidence, source)
SELECT l.id, $1, 0.85, 'fishwa'
FROM lakes l
WHERE l.centroid IS NOT NULL
  AND ST_DWithin(l.centroid::geography, ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography, $4)
ORDER BY l.centroid <-> ST_SetSRID(ST_MakePoint($2, $3), 4326)
LIMIT 1
ON CONFLICT (lake_id, species) DO UPDATE SET
    confidence = GREATEST(species_presence.confidence, EXCLUDED.confidence),
    source     = CASE WHEN species_presence.source = 'user_report'
                      THEN species_presence.source ELSE 'fishwa' END,
    updated_at = now()`

// UpsertPresenceFromPoints spatial-joins FishWA species points to lakes and
// upserts presence. Returns how many points matched a lake.
func (s *Store) UpsertPresenceFromPoints(ctx context.Context, pts []PresencePoint, maxMeters float64) (int, error) {
	if len(pts) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, p := range pts {
		b.Queue(upsertPresenceNearestSQL, p.Species, p.Lon, p.Lat, maxMeters)
	}
	br := s.Pool.SendBatch(ctx, b)
	defer func() { _ = br.Close() }()
	matched := 0
	for range pts {
		tag, err := br.Exec()
		if err != nil {
			return matched, fmt.Errorf("store: upsert presence: %w", err)
		}
		if tag.RowsAffected() > 0 {
			matched++
		}
	}
	return matched, nil
}
