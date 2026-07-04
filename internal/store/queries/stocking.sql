-- name: InsertStockingEvent :execrows
-- Idempotent insert keyed on the Socrata row id (a content hash), so re-polling
-- the same rows is a no-op. Returns rows affected: 1 = new, 0 = already seen.
INSERT INTO stocking_events
    (lake_id, species, count, size_class, plant_date, hatchery, socrata_row_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (socrata_row_id) DO NOTHING;

-- name: CountStockingEvents :one
SELECT count(*) FROM stocking_events;
