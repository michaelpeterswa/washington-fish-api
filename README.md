# washington-fish-api

A public API that predicts freshwater **fishing conditions** for Washington
lakes and ranks them. For any lake it produces a **bite-likelihood** score over
a 72-hour window, a **confidence** measure, and a plain-language **"why"** — and
it can rank nearby lakes by target species and distance.

Every prediction is a transparent, additive heuristic: each factor emits a
signed contribution *and* a human-readable reason, so the explanation *is* the
computation. (It's designed to evolve into gradient-boosted trees once catch-log
volume justifies it, without changing the interface.)

## What it predicts

- **Species presence** — a knowledge base, not ML. Stocked species come from the
  WDFW stocking feed; warmwater and wild species from WDFW's FishWA layers.
- **Bite likelihood** — an additive heuristic (base 50, factors adjust it):
  days since the last catchable trout plant (exponential decay), barometric
  tendency, wind, cloud cover, **solunar** (dawn/dusk + moon overhead/underfoot +
  moonrise/set, moon-phase amplified), and a **species-aware water-temperature**
  term (warm water rewards bass, penalizes trout).
- **Lake ranking** — query-time: score bite across candidate lakes, filter by
  species and radius, sort.
- **Confidence** — derived from the **kriging variance** of the weather-station
  network geometry around each lake (see *Weather interpolation* below). It is
  reported separately from the score: a great score at a poorly-covered lake is
  honestly flagged as low-confidence.

## Architecture

One image, two entrypoints (deployed on a self-hosted k8s / Talos homelab):

- **`wfa-api`** — the HTTP server (chi): the `/v1` API, `/healthz` + `/readyz`,
  and Prometheus metrics / OTLP tracing on the metrics port (default `:8081`).
- **`wfa-worker <job>`** — ingestion jobs, run as k8s **CronJobs** (no in-process
  scheduler, no leader election).

Storage is **Postgres + PostGIS**; **gonum** does the kriging linear algebra;
persistence is **pgx + sqlc** (typed query codegen) with **goose** migrations —
raw SQL for the PostGIS/spatial queries.

### Worker jobs

Run as `wfa-worker <job>` (one-shot: run, exit).

| job        | what it does                                                              | cadence   |
|------------|--------------------------------------------------------------------------|-----------|
| `migrate`  | apply DB migrations (goose, embedded in the binary)                      | on deploy |
| `stocking` | WDFW Fish Plants (Socrata) → `stocking_events` + derived species presence | hourly    |
| `lakes`    | crawl WDFW lowland+high lake pages → coords / acreage / elevation / depth, joined on `geo_code` | periodic  |
| `stations` | ingest NWS stations → recompute per-lake KED **confidence**             | periodic  |
| `fishwa`   | WDFW FishWA per-species layers → warmwater/wild species presence         | periodic  |
| `weather`  | Open-Meteo per lake → `conditions` (nowcast + 72h; temp/pressure/wind/cloud + modeled **water temp**) | hourly    |

## HTTP API

Base path `/v1`. Temperatures are rendered in the server default (`TEMP_UNIT`,
default Fahrenheit) or a per-request `?units=metric|f`.

| method & path                     | description                                                                 |
|-----------------------------------|-----------------------------------------------------------------------------|
| `GET /v1/lakes`                   | search — filters: `q` (name), `county`, `species`, `lake_type`, `lat`+`lon`+`radius_km`, `limit` |
| `GET /v1/lakes/{id}`              | lake detail + species presence                                              |
| `GET /v1/lakes/{id}/species`      | species present at the lake                                                 |
| `GET /v1/lakes/{id}/prediction`   | bite nowcast + 72h forecast; `?species=` (thermal target), `?units=`        |
| `GET /v1/rank`                    | rank lakes near a point — `lat`, `lon` (required), `radius_km`, `species`, `limit` |

Example:

```console
$ curl 'localhost:8080/v1/lakes/221/prediction?species=Rainbow'
{
  "lake_id": 221,
  "target_species": "Rainbow Trout",
  "temp_unit": "F",
  "days_since_catchable_plant": 3,
  "nowcast": {
    "score": 84, "confidence": 0.88, "water_temp": 60.3,
    "factors": [
      {"name":"catchable_plant","contribution":24,"reason":"Catchable trout stocked 3 days ago — fish are concentrated and biting"},
      {"name":"wind","contribution":8,"reason":"Light chop (7 mph) — a ripple breaks up the surface and fish feed"},
      {"name":"water_temp","contribution":10,"reason":"Water ~60°F — in the ideal range for Rainbow Trout"}
    ]
  },
  "forecast": [ /* 72 hourly {valid_at, horizon_h, score, confidence} */ ]
}
```

## Running locally

```console
# Postgres/PostGIS + api + grafana-otel-lgtm
docker compose up

# from the host (compose maps postgres to :5432):
export DATABASE_URL='postgres://wfa:wfa@localhost:5432/wfa?sslmode=disable'

go run ./cmd/wfa-worker migrate     # schema
go run ./cmd/wfa-worker lakes       # lake coords / depth / elevation
go run ./cmd/wfa-worker stocking    # stocking events + trout presence
go run ./cmd/wfa-worker fishwa      # warmwater / wild species presence
go run ./cmd/wfa-worker stations    # KED confidence per lake
go run ./cmd/wfa-worker weather     # weather + modeled water temp

go run ./cmd/wfa-api                # serve on :8080
```

> **Open-Meteo:** the `weather` job defaults to the public API, which is
> **rate-limited and non-commercial**. For production, self-host the AGPL
> Open-Meteo server (Docker) and point `OPENMETEO_URL` at it — no rate limits.

## Configuration

Environment variables (defaults in parentheses):

| var | purpose |
|-----|---------|
| `DATABASE_URL` | pgx Postgres/PostGIS connection string (required) |
| `API_PORT` (`8080`) | HTTP API port |
| `TEMP_UNIT` (`f`) | default temperature unit (`f`/`c`); `?units=` overrides per request |
| `LOG_LEVEL` (`error`) | `debug`/`info`/`warn`/`error` |
| `METRICS_PORT` (`8081`), `METRICS_ENABLED` (`true`) | Prometheus/OTLP metrics |
| `TRACING_*`, `LOCAL` | ootel tracing (OTLP when `LOCAL=true`, else Prometheus) |
| `SOCRATA_STOCKING_URL`, `SOCRATA_APP_TOKEN`, `STOCKING_LOOKBACK_DAYS` (`365`) | stocking ingest |
| `WDFW_SITEMAP_URL`, `WDFW_LAKES_MAX` (`0`=all), `WDFW_LAKES_CONCURRENCY` (`6`) | lake crawl |
| `NWS_STATIONS_URL`, `FISHWA_SERVICE_URL` | stations / FishWA endpoints |
| `OPENMETEO_URL`, `WEATHER_BATCH_SIZE` (`100`), `WEATHER_MAX_LAKES` (`0`), `WEATHER_PAST_DAYS` (`2`) | weather ingest |

All outbound ingest requests go through `internal/httpx` — a shared retry client
(cenkalti/backoff with jitter) that retries transient failures and honors
rate-limit headers (`Retry-After`, `RateLimit-Reset`, `X-RateLimit-Reset`).

## Weather interpolation (KED)

Weather is interpolated to each lake with **Kriging with External Drift**
(`internal/weather/ked`), drift `[1, elev, x, y]` — elevation is the lapse
correction and the planar `x,y` term removes WA's marine→continental gradient so
the residual is stationary. The kriging **variance** (which depends only on
station geometry, not values) becomes the **confidence field**. Water temperature
is a depth-damped exponential moving average of the air-temp history. The two
frozen variograms are fit **offline** in `tools/variogram-fit` (uv/Python), which
also renders the confidence-map / variogram figures in its `docs/`.

## Project layout

```
cmd/wfa-api/            HTTP server
cmd/wfa-worker/         ingestion job dispatch
internal/
  api/                 chi router, handlers, DTOs
  store/               pgx + sqlc repo layer; goose migrations
  ingest/{stocking,wdfwlakes,stations,fishwa,weather}
  predict/{bite,species}   bite heuristic + species thermal catalog
  weather/{ked,proj,field} kriging, UTM projection, confidence
  astro/               solunar / moon / dawn-dusk (via suncalc)
  httpx/               shared retry+backoff HTTP client
  config, logging, telemetry
tools/variogram-fit/   offline variogram fitting + visualization (Python/uv)
```

`CLAUDE.md` is the design/handoff contract with the phased build plan and
open decisions.

## Development

```console
go test ./...
go vet ./...
golangci-lint run --enable=bodyclose
```

Commits follow Conventional Commits (enforced by commitlint in CI).

## Data & attribution

WDFW Fish Plants (data.wa.gov Socrata `6fex-3r7d`), WDFW lake pages + FishWA
ArcGIS, NWS stations, and Open-Meteo (CC BY 4.0; self-host the AGPL server for
commercial use). Regulations are intentionally **not** encoded — the app links
out to WDFW for seasons and limits.
