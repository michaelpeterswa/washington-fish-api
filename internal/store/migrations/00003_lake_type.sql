-- +goose Up
-- Distinguish lowland vs high (alpine) lakes. Alpine lakes have different bite
-- dynamics (ice-off timing) and sparser weather-station coverage, so the
-- predictor and confidence field will want to treat them differently.
ALTER TABLE lakes ADD COLUMN lake_type text CHECK (lake_type IN ('lowland', 'high'));

-- +goose Down
ALTER TABLE lakes DROP COLUMN IF EXISTS lake_type;
