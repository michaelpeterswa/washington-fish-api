-- name: LastCatchablePlant :one
-- Most recent catchable/legal-size trout plant for a lake — drives the dominant
-- "days since last catchable plant" bite factor.
SELECT plant_date, species
FROM stocking_events
WHERE lake_id = $1 AND size_class IN ('legals', 'adult')
ORDER BY plant_date DESC
LIMIT 1;

-- name: ConditionsForLake :many
-- Full nowcast+forecast series for a lake, ordered by horizon.
SELECT valid_at, horizon_h, air_temp_c, water_temp_c, pressure_hpa, pressure_tendency, wind_mps, cloud_pct, confidence
FROM conditions
WHERE lake_id = $1
ORDER BY horizon_h;

-- name: PrimarySpecies :one
-- The lake's most-confident present species — the default prediction target.
SELECT species FROM species_presence
WHERE lake_id = $1
ORDER BY confidence DESC, species
LIMIT 1;

-- name: SpeciesForLake :many
SELECT species, confidence, source, updated_at
FROM species_presence
WHERE lake_id = $1
ORDER BY confidence DESC, species;

-- name: RebuildStockingPresence :exec
-- Derive species presence from the stocking feed (knowledge base, not ML).
INSERT INTO species_presence (lake_id, species, confidence, source)
SELECT DISTINCT lake_id, species, 0.8, 'stocking'
FROM stocking_events
WHERE lake_id IS NOT NULL
ON CONFLICT (lake_id, species) DO UPDATE SET updated_at = now();
