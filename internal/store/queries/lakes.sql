-- name: UpsertLakeByGeoCode :one
-- Seed/refresh a lake from the stocking feed, keyed by WDFW geo_code. Name is
-- set once on insert (NHD supplies the canonical GNIS name in phase 2b); on
-- conflict we only refresh county/elevation, never clobber an existing name.
INSERT INTO lakes (geo_code, name, county, elev_m, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (geo_code) DO UPDATE
SET county     = COALESCE(EXCLUDED.county, lakes.county),
    elev_m     = COALESCE(EXCLUDED.elev_m, lakes.elev_m),
    updated_at = now()
RETURNING id;

-- name: CountLakes :one
SELECT count(*) FROM lakes;
