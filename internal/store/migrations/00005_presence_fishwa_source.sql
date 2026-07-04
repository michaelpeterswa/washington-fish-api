-- +goose Up
-- Allow species presence sourced from the WDFW FishWA per-species layers.
ALTER TABLE species_presence DROP CONSTRAINT species_presence_source_check;
ALTER TABLE species_presence ADD CONSTRAINT species_presence_source_check
    CHECK (source IN ('stocking', 'survey_pdf', 'user_report', 'fishwa'));

-- +goose Down
ALTER TABLE species_presence DROP CONSTRAINT species_presence_source_check;
ALTER TABLE species_presence ADD CONSTRAINT species_presence_source_check
    CHECK (source IN ('stocking', 'survey_pdf', 'user_report'));
