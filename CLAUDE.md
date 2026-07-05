# CLAUDE.md — WA Lake Fish Prediction

Handoff context for a fish-prediction API for Washington freshwater lakes,
carried over from a design conversation. Treat as current source of truth;
update as decisions land. Keep this file high-signal — it's a behavioral
contract, not documentation.

## What we're building
A public product predicting fishing conditions for WA lakes, delivered as an
API that also backs a consumer app. The consumer app's catch-logging is the
API's training-data pipeline (log EFFORT, not just catches — see flywheel).

Launch scope:
- ~500 WDFW-stocked lowland lakes (v1); high/alpine lakes ALSO ingested
  (lakes.lake_type = 'lowland' | 'high') — the crawl covers both. Alpine bite
  dynamics (ice-off) aren't modeled yet, so treat high lakes as lower-confidence
  until the heuristic accounts for them.
- All species anglers target (stocked trout + warmwater: bass / panfish / walleye)
- Prediction horizon: now + short forecast window (~72h)
- Regulations: link out to WDFW only — do NOT encode seasons/limits

## The three predictions (decomposition)
- **Species presence** = knowledge base, not ML. Stocked species from the
  stocking feed; warmwater + wild species from the FishWA per-species layers
  (IMPLEMENTED — replaced the survey-PDF plan for presence, which was harder and
  no better); corrected over time by user reports.
- **Bite likelihood** = the one real predictor. v1 is a transparent additive
  heuristic (each factor emits contribution + human-readable reason, so the
  "why" IS the computation). Evolves to gradient-boosted trees (XGBoost /
  LightGBM over tabular features — NOT deep learning) once catch-log volume
  justifies it.
- **Lake ranking** = query-time composite: score bite likelihood across
  candidate lakes, filter by species-wanted + drive distance, sort. No
  separate model.

Every prediction returns score + confidence + why. Confidence is driven by
data coverage / kriging variance, and is SEPARATE from the score.

## Data sources
Live (poll hourly):
- **WDFW Fish Plants** — data.wa.gov Socrata `6fex-3r7d`, JSON at
  `https://data.wa.gov/resource/6fex-3r7d.json`. Stocking backbone; dominant
  trout signal ("days since last catchable plant" + decay curve).
- **Open-Meteo** — weather + barometric trend (hourly surface pressure / temp /
  wind / cloud). Free tier is NON-commercial, so SELF-HOST the AGPL server
  (Docker) for this product.
- **NWS stations** (optional nowcast refinement) — api.weather.gov/stations +
  observations for real barometer readings + model-bias correction. REQUIRES a
  User-Agent header or requests 403 silently.

Load once / periodic:
- **WDFW lowland-lake pages** (LAKE SPINE — implemented, `internal/ingest/wdfwlakes`,
  job `lakes`). WDFW's lowland-lake web pages expose `geo_code` alongside
  centroid/acreage/elevation/county/name (server-rendered, no JS). Enumerate via
  `sitemap.xml?page=N` (WDFW 404s past the last page), parse `<strong>Label:</strong>`
  fields, upsert on EXACT geo_code join to the stocking-seeded lakes — no fuzzy
  matching. Crawls BOTH the lowland-lakes (~710 pages) and high-lakes (~996
  pages) sections; ~1677 lakes loaded, covering ~87% of actively-stocked lakes
  with zero false matches. The ~13% remainder are creeks/rivers (excluded) and
  marginal waters (seep/drain lakes, ponds, numbered sub-basins) with no WDFW
  page. This REPLACED the NHD-as-spine plan — NHD lacks geo_code, so it can't
  join deterministically. A FishWA name+county fuzzy fallback could add a few
  more but at false-match risk — declined for v1.
- **NHD Waterbodies** (POLYGON OVERLAY only — geometry, not spine). Load into
  PostGIS; filter FType LakePond=390, Reservoir=436; attach polygon `geom` to
  located lakes by centroid-in-polygon. Cosmetic for v1 (KED + drive-distance
  need only centroids, which the WDFW crawl already provides). Does NOT include
  depth. Retired Oct 2023 (→ 3DHP) but still available.
- **WDFW FishWA ArcGIS service** (bonus, not yet used) —
  `geodataservices.wdfw.wa.gov/.../FishWA_2014_AllLakes_PROD/MapServer` has
  per-species presence point layers (Rainbow/Kokanee/Bass/Walleye/Perch/…) and a
  bathymetry layer. Candidate feeds for species_presence (phase 5) and depth
  (phase 4/6). Keyed by name+county, NOT geo_code.
- **Warmwater survey PDFs** (WDFW Warmwater Enhancement Program) — one-time LLM
  extraction → seeds warmwater species presence AND lake depth/morphometry
  (physical-parameters tables), which NHD lacks.

Computed locally (no network):
- Solunar periods, moon phase, dawn/dusk (IMPLEMENTED — internal/astro, wraps
  the `sixdouglas/suncalc` library; ephemeris is the library's, the solunar
  SCORING is ours in predict/bite). Deliberately NOT hand-rolled — ephemeris is
  a solved problem, not core IP (cf. ked/proj which ARE).
- Water-temp proxy — modeled from air-temp history + season + depth/area for
  the (nearly all) lakes without a USGS gauge.

Gaps / notes:
- **Water temperature** is the weak link. USGS NWIS (param 00010) covers only
  gauged sites — almost no lowland lakes — so it's mostly modeled. Hits the
  warmwater half hardest.
- KED needs elevation-SPANNING stations: add NRCS SNOTEL + RAWS; do NOT filter
  to lowland airports only (drift extrapolates blind up high).
- Attribution: Open-Meteo CC BY 4.0; respect data.wa.gov / NHD terms.

## Weather interpolation (implemented — see internal/weather/ked)
Kriging with External Drift; drift = [1, elev, x, y] (elevation + a planar
horizontal trend). x,y are centred+scaled inside Solve for conditioning.
- The elevation term IS the lapse correction (regress on elevation, krige
  residual, evaluate at the lake's elevation — one op, no separate lapse pass).
- The x,y term removes WA's marine→continental gradient so the drift residual is
  second-order STATIONARY — without it the obs variogram railed (proven in
  tools/variogram-fit; visualized in its kriged_map.png). p=4 now; needs the
  drift full-rank (elevation not locally collinear with x,y).
- Kriging WEIGHTS depend only on geometry + drift + variogram, NOT on values —
  solve once per (stations, lake, variogram) and reuse across the nowcast and
  every forecast hour; only the value vector changes.
- Nowcast = krige observations. Forecast = krige model residual (obs − model)
  as a bias field added onto the model forecast across horizons.
- TWO frozen variograms (obs field + residual field). Fit OFFLINE in Python
  (GSTools / PyKrige), hardcode params. This is the ONLY Python in the serving
  path — production is Go (gonum solve).
- Project coords to UTM 10N (EPSG:32610) metres; scale/centre elevation (km)
  for conditioning. Kriging variance → the confidence field.
- Deferred (noted in ked.go): regime-stratified variograms; anisotropic /
  advective kriging. NOT yet needed — value vectors already carry the weather
  variation.

## API conventions
- Temperature units: responses render temps in the server default (`TEMP_UNIT`
  env, default `f` — US anglers), overridable per request with `?units=metric|f`.
  Affects the water_temp factor reason + the nowcast `water_temp`/`temp_unit`
  fields. Score is unit-invariant (presentation only). Wind stays mph, pressure
  hPa for now.
- `?species=` on prediction/rank selects the thermal target (default: lake's
  primary present species).

## Stack & conventions
- Go primary. Python for offline modeling only; JS for frontend; C occasionally.
- Postgres + PostGIS. gonum for linear algebra.
- Persistence: pgx + sqlc (typed query codegen) + goose migrations. Raw SQL for
  PostGIS — no ORM (fights spatial queries, hides SQL).
- HTTP: net/http + chi router. Structured slog + ootel already wired. ALL
  outbound ingest requests go through `internal/httpx` — a shared client that
  retries transient failures (429/5xx/408/network) with cenkalti/backoff
  (exponential + jitter) and honors rate-limit headers (Retry-After secs/date,
  RateLimit-Reset, X-RateLimit-Reset epoch). Per-client backoff tuned via options
  (weather ramps to ~65s for Open-Meteo's minutely, header-less limit). No
  per-client retry loops.
- Deployment: self-hosted k8s (Talos) homelab. ONE image, two entrypoints —
  `wfa-api` (server Deployment) and `wfa-worker` (subcommand-dispatched pollers
  run as k8s CronJobs; no in-process scheduler, no leader election).
- Prefer standard-library-first, idiomatic Go.

## Package layout (target)
    cmd/wfa-api/            server: chi router, /healthz /readyz, :8081 metrics
    cmd/wfa-worker/         subcommand dispatch for ingestion jobs
    internal/config,logging existing scaffold (extended)
    internal/api/          handlers, DTOs, middleware
    internal/store/        pgx + sqlc repo layer; migrations (goose)
    internal/ingest/{stocking,weather,nws,nhd}
    internal/predict/{presence,bite,rank}
    internal/weather/{ked,field}   ked.go lives here now
    internal/astro/        solunar / moon / dawn-dusk (pure Go)
    internal/watertemp/    modeled temp proxy
    offline/               python: variogram fitting, one-time PDF extraction

## Build plan (phased — v1 = trout-only, full vertical stack)
1. Foundation — module rename; cmd split; chi server + health; Postgres+PostGIS
   in compose; goose initial migration; sqlc + store skeleton; move ked.go.
2. Ingestion backbone — NHD loader (lakes) + Socrata stocking poller.
3. Weather field — split:
   - 3a (DONE, `internal/ingest/weather`, job `weather`): Open-Meteo MODEL per
     lake → conditions (nowcast horizon 0 + hourly 1..72; temp/pressure/wind/
     cloud + 3h barometric tendency). Batches ~100 lakes/Open-Meteo call;
     nowcast snapped to top-of-hour for idempotency; prunes stale rows. This
     alone gives usable weather for every lake (model-only; confidence still
     null until 3b).
   - 3b-1 confidence (DONE): real KED-variance confidence. `internal/ingest/
     stations` job ingests NWS WA stations (1417, elev 0–2572 m — elevation-
     spanning) → weather_stations. `internal/weather/proj` projects to UTM 10N
     (Snyder TM, unit-tested vs meridian-arc integral). `internal/weather/field`
     picks nearest-25 neighborhood, solves ked.Solve for obs + residual
     variograms, variance → confidence, cached on lakes.confidence_nowcast/
     _forecast (time-invariant; recompute when network changes). predict uses it
     (falls back to provisional for the ~13% unscored). VERIFIED: Seattle lakes
     ~0.88, remote Enchantment alpine ~0.61, Scheelite 0.47 — honest and
     spatially/elevationally sensible. obsVariogram is now DATA-DERIVED
     (`tools/variogram-fit`, nugget 0.348 / psill 7.700 / range 247km) via the
     PLANAR-drift fit — with [1,elev,x,y] the fit SETTLES instead of railing.
     residVariogram still a placeholder (needs Open-Meteo ARCHIVE to pool
     obs−model over hours; single-snapshot is noise). Effect of the data
     variogram + planar drift on the confidence field: higher/flatter (avg 0.89,
     min 0.61) than the old placeholder — HONEST, since temperature is long-
     correlated and WA is densely stationed, so most lakes are well-interpolable;
     the field still correctly floors genuine gaps (south-border Lake Wallula
     0.61, remote N-Cascades alpine 0.64). Alpine confidence tracks local station
     coverage, not a crude elevation rule.
   - 3b-2 bias correction (NEXT): fetch obs VALUES + model-at-station → krige
     nowcast (obs) and forecast residual bias (obs−model) → correct conditions
     temps. Forecast-bias decision (open #1) goes live HERE (confidence didn't
     need it — variance is value-independent).
4. Water-temp proxy (DONE) — `internal/ingest/weather` computes water_temp_c as
   a DEPTH-DAMPED exponential moving average of the Open-Meteo air-temp series
   (tau grows with depth). The EMA CONTINUES from the prior stored water_temp
   (`PriorWaterTemps` seed) each poll rather than re-spinning from a long
   history, so past_days is a small bridge (WEATHER_PAST_DAYS, default 2) — this
   cut Open-Meteo request weight ~5x and is what makes fleet polling viable
   (past_days=15 hammered the public rate limit; the self-hosted server has no
   limit but this saves bandwidth/compute too). Cold start spins up from the
   short window. Depth from area+elevation
   estimate (`EstimateLakeDepths`, morph_source='estimated', run in the lakes
   crawl). Warmwater species presence from the FishWA per-species point layers
   (`internal/ingest/fishwa`, job `fishwa`, spatial-join to nearest centroid —
   1878/1925 points matched, 15 species, ~600 lakes; source='fishwa'). Prediction
   is now SPECIES-AWARE: `internal/predict/species` catalog gives each species a
   thermal window, and the bite `water_temp` factor rewards the target species'
   comfort band (verified: 26°C lake scored +10 for bass, −8 for trout).
   Endpoints take `?species=` (default: lake's primary present species).
5. Prediction + rank (DONE) — `internal/predict/bite` additive heuristic
   (factors: catchable-plant recency decay, barometric tendency, wind, cloud;
   each emits contribution + reason), `internal/predict` service (per-lake
   nowcast+72h series, rank), species presence derived from stocking. Endpoints
   live: GET /v1/lakes (search + geo/species filters), /v1/lakes/{id},
   /v1/lakes/{id}/species, /v1/lakes/{id}/prediction, /v1/rank. Confidence is
   PROVISIONAL (data-coverage based) until KED variance (3b) replaces it.
   Bite factors: catchable-plant recency, barometric tendency, wind, cloud, and
   SOLUNAR (dawn/dusk + moon major/underfoot + moonrise/set, moon-phase
   amplified; via internal/astro). Forecast scores now oscillate with
   time-of-day (verified: dusk/dawn peaks, ~41-pt swing over 72h). Phase 4 added
   the species-aware water-temp factor (see phase 4). All v1 bite factors now in.
   ANTI-CLUSTERING (2026-07-05): nearby unstocked lakes (esp. alpine) collapsed
   onto one score (live check: 100 rank rows → 2 distinct scores). Three causes,
   all addressed: (a) every weather factor was a STEP function that quantized
   real input variation away — pressure/wind/cloud/thermal are now CONTINUOUS
   curves (tanh/Gaussian/linear) and per-factor math.Round is gone; (b) the only
   high-dynamic-range factor (catchable_plant) is 0 across all unstocked lakes —
   added a MORPHOMETRY factor (area/depth/elev, ±~5) that varies lake-to-lake
   even under identical weather; (c) grid-snapping gave neighbours identical
   temps — weather ingest now LAPSE-CORRECTS air temp (6.5°C/km) from the
   Open-Meteo grid-node elevation to the lake's true elevation before the
   water-temp EMA. Verified: 74 alpine lakes under IDENTICAL weather now spread
   over 26 distinct scores (largest cluster 6, was 74). Do NOT re-quantize the
   factors or re-add rounding — the smoothness IS the fix, and no random jitter
   was added (fake variance would be dishonest). Also fixed a /v1/rank JOIN
   fan-out (LEFT JOIN conditions matched all horizon-0 rows per lake → duplicate
   rows, 100 rows = 74 lakes; now a LATERAL picks the latest valid_at).
6. Flywheel + warmwater — catch-log intake (effort mandatory); PDF extraction.

## Decisions (resolved)
- **MVP scope**: trout-only but the FULL vertical stack (stocking → KED weather
  → additive bite → rank). Warmwater presence deferred to phase 6.
- **Water-temp proxy**: BUILD for v1. Dependency: needs depth, but real depth
  comes from warmwater PDFs (phase 6). Resolution: depth FALLBACK — estimate
  mean depth from area + elevation for lakes lacking a survey, flag
  `morph_source='estimated'`, let confidence reflect it. PDF depth upgrades it
  later.
- **DB schema** (first goose migration): lakes / stocking_events /
  species_presence / weather_stations / station_obs / conditions / catch_logs.

## Open decisions (unresolved — ask before assuming)
1. **Forecast bias philosophy**: static spatial bias vs diurnal (clock-hour
   residuals) vs model-value-as-drift. Goes live in phase 3 (weather/field).
   Leaning DIURNAL (clock-hour residuals) — ked.go already documents that path
   and it's free (same weights per hour) — but not locked; confirm at phase 3.

## Catch-log flywheel (design now, train later)
Catch logs are the future ML labels. MANDATORY: capture EFFORT (trip duration +
angler count). Catches without effort are unusable (positives-only trap; the
real target is catch-per-unit-effort). Also log which prediction was shown, to
correct feedback-loop bias later.
