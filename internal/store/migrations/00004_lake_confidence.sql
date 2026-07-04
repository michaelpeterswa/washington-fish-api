-- +goose Up
-- KED-variance-derived confidence per lake. Time-invariant (depends only on the
-- station network geometry + lake position), so it's cached on the lake and
-- recomputed when the station network changes. Two fields: the obs-field
-- variogram drives the nowcast, the residual-field variogram the forecast.
ALTER TABLE lakes ADD COLUMN confidence_nowcast double precision;
ALTER TABLE lakes ADD COLUMN confidence_forecast double precision;

-- +goose Down
ALTER TABLE lakes DROP COLUMN IF EXISTS confidence_nowcast;
ALTER TABLE lakes DROP COLUMN IF EXISTS confidence_forecast;
