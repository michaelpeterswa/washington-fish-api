-- +goose Up
-- Initial schema for the WA lake fish-prediction API.
-- Geometry stored in EPSG:4326 (lat/lon); kriging math reprojects to UTM 10N
-- (EPSG:32610) metres at compute time (see internal/weather/ked).

CREATE EXTENSION IF NOT EXISTS postgis;

-- Lakes: NHD-loaded polygons + morphometry. Depth is nullable — real depth
-- comes from warmwater survey PDFs (phase 6); until then it's estimated from
-- area + elevation (morph_source = 'estimated'), which the confidence field
-- reflects.
CREATE TABLE lakes (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    wdfw_id      text UNIQUE,
    name         text NOT NULL,
    county       text,
    geom         geometry(MultiPolygon, 4326),
    centroid     geometry(Point, 4326),
    area_m2      double precision,
    elev_m       double precision,
    depth_max_m  double precision,
    depth_mean_m double precision,
    morph_source text NOT NULL DEFAULT 'nhd'
                 CHECK (morph_source IN ('nhd', 'survey_pdf', 'estimated')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX lakes_geom_gist ON lakes USING gist (geom);
CREATE INDEX lakes_centroid_gist ON lakes USING gist (centroid);

-- Stocking events: the dominant trout signal. Dedup on socrata_row_id so the
-- hourly poller is idempotent.
CREATE TABLE stocking_events (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lake_id        bigint REFERENCES lakes (id) ON DELETE CASCADE,
    species        text NOT NULL,
    count          integer,
    size_class     text,
    plant_date     date NOT NULL,
    hatchery       text,
    socrata_row_id text NOT NULL UNIQUE,
    ingested_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX stocking_events_lake_date ON stocking_events (lake_id, plant_date DESC);

-- Species presence: knowledge base (not ML). One row per (lake, species).
CREATE TABLE species_presence (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lake_id    bigint NOT NULL REFERENCES lakes (id) ON DELETE CASCADE,
    species    text NOT NULL,
    confidence double precision NOT NULL DEFAULT 0.5
               CHECK (confidence >= 0 AND confidence <= 1),
    source     text NOT NULL
               CHECK (source IN ('stocking', 'survey_pdf', 'user_report')),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (lake_id, species)
);

-- Weather stations: KED conditioning points. Must SPAN elevation (airports +
-- SNOTEL + RAWS) or drift extrapolates blind up high.
CREATE TABLE weather_stations (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source      text NOT NULL
                CHECK (source IN ('openmeteo', 'nws', 'snotel', 'raws')),
    external_id text NOT NULL,
    geom        geometry(Point, 4326) NOT NULL,
    elev_m      double precision NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source, external_id)
);
CREATE INDEX weather_stations_geom_gist ON weather_stations USING gist (geom);

-- Station observations: the KED value vectors, one row per (station, time, var).
CREATE TABLE station_obs (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    station_id bigint NOT NULL REFERENCES weather_stations (id) ON DELETE CASCADE,
    valid_at   timestamptz NOT NULL,
    var        text NOT NULL,
    value      double precision NOT NULL,
    UNIQUE (station_id, valid_at, var)
);
CREATE INDEX station_obs_valid_at ON station_obs (valid_at);

-- Conditions: cached computed weather field per lake per (valid_at, horizon).
-- horizon_h = 0 is the nowcast. confidence comes from KED variance.
CREATE TABLE conditions (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lake_id           bigint NOT NULL REFERENCES lakes (id) ON DELETE CASCADE,
    valid_at          timestamptz NOT NULL,
    horizon_h         integer NOT NULL,
    air_temp_c        double precision,
    water_temp_c      double precision,
    pressure_hpa      double precision,
    pressure_tendency double precision,
    wind_mps          double precision,
    cloud_pct         double precision,
    solunar_state     text,
    confidence        double precision,
    computed_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (lake_id, valid_at, horizon_h)
);
CREATE INDEX conditions_lake_valid ON conditions (lake_id, valid_at);

-- Catch logs: future ML labels. EFFORT IS MANDATORY (trip window + anglers) —
-- catches without effort are unusable (positives-only trap; target is CPUE).
-- prediction_shown records what the user saw, to correct feedback-loop bias.
CREATE TABLE catch_logs (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lake_id           bigint NOT NULL REFERENCES lakes (id) ON DELETE CASCADE,
    trip_start        timestamptz NOT NULL,
    trip_end          timestamptz NOT NULL,
    angler_count      integer NOT NULL CHECK (angler_count > 0),
    species           text,
    count             integer NOT NULL DEFAULT 0 CHECK (count >= 0),
    prediction_shown  jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CHECK (trip_end > trip_start)
);
CREATE INDEX catch_logs_lake ON catch_logs (lake_id);

-- +goose Down
DROP TABLE IF EXISTS catch_logs;
DROP TABLE IF EXISTS conditions;
DROP TABLE IF EXISTS station_obs;
DROP TABLE IF EXISTS weather_stations;
DROP TABLE IF EXISTS species_presence;
DROP TABLE IF EXISTS stocking_events;
DROP TABLE IF EXISTS lakes;
-- postgis extension left installed intentionally.
