-- +goose Up
-- geo_code is WDFW's stable water-body key (e.g. "L3277"), present on ~99% of
-- stocking rows. It becomes the lake spine: stocking seeds lakes keyed by it,
-- NHD geometry attaches to it later (phase 2b).
ALTER TABLE lakes ADD COLUMN geo_code text;
ALTER TABLE lakes ADD CONSTRAINT lakes_geo_code_key UNIQUE (geo_code);

-- Lakes seeded from the stocking feed have no morphometry yet, so morph_source
-- must be allowed to be unknown until NHD/PDF fills it in.
ALTER TABLE lakes ALTER COLUMN morph_source DROP NOT NULL;
ALTER TABLE lakes ALTER COLUMN morph_source DROP DEFAULT;

-- +goose Down
ALTER TABLE lakes ALTER COLUMN morph_source SET DEFAULT 'nhd';
ALTER TABLE lakes DROP CONSTRAINT IF EXISTS lakes_geo_code_key;
ALTER TABLE lakes DROP COLUMN IF EXISTS geo_code;
