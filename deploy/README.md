# Deployment runbook — washington.fish

Production topology:

```
              Cloudflare DNS (washington.fish)
                        │  CNAME api  ->  washington-fish-api.fly.dev  (DNS-only)
                        ▼
   ┌─────────────────────────────────────────┐        ┌──────────────────────┐
   │  Fly app: washington-fish-api   (sjc)    │        │  Neon Postgres+PostGIS│
   │  ENTRYPOINT /wfa-api  → :8080 (HTTPS 443) │◄──────►│  project washington-  │
   │  metrics :8081/metrics (private, scraped) │  SQL   │  fish (aws-us-east-1) │
   │  release_command: /wfa-worker migrate     │        └──────────────────────┘
   └─────────────────────────────────────────┘                  ▲
                        ▲  spawns one-shot Machines INTO          │ inherit
                        │  this app (inherit its secrets)         │ DATABASE_URL
   ┌─────────────────────────────────────────┐                   │
   │  Fly app: washington-fish-cron  (sjc)    │  /wfa-worker <job>│
   │  cron-manager → schedules.json           │───────────────────┘
   └─────────────────────────────────────────┘
```

**One image, two entrypoints.** The Docker image ships both `/wfa-api` (server)
and `/wfa-worker` (jobs). The API app runs the server; cron-manager spawns
ephemeral Machines *inside the API app* that run `/wfa-worker <job>` and then
auto-destroy — so they inherit the API app's Fly secrets (`DATABASE_URL`, …)
with zero extra wiring. This mirrors the old k8s Deployment + CronJob split.

---

## 0. Prerequisites

```bash
fly auth login          # flyctl is installed; this session was NOT logged in
fly auth whoami         # confirm
```

You also need the Neon DB password (see below) and access to the Cloudflare
zone `washington.fish`.

---

## 1. Neon database (DONE — provisioning already ran)

Already created and migrated by the setup session:

| | |
|---|---|
| Project | `washington-fish` (`autumn-credit-66295829`) |
| Region | `aws-us-east-1` |
| Branch / DB / role | `main` / `neondb` / `neondb_owner` |
| Schema | goose migrations `00001`–`00005` applied; PostGIS 3.5 enabled |

**Connection strings** (grab the password from the Neon console →
_washington-fish → Connection Details_, or via the Neon integration — it is NOT
committed here):

- **Pooled** (use for the app — survives Machine autoscaling):
  ```
  postgres://neondb_owner:<PASSWORD>@ep-fragrant-morning-at7n1ibq-pooler.c-9.us-east-1.aws.neon.tech/neondb?sslmode=require&channel_binding=require
  ```
- **Direct** (unpooled — only if a migration ever needs it): same host without
  the `-pooler` segment.

Migrations are re-applied automatically on every deploy via `release_command`,
so you normally never run goose by hand.

> **Latency note:** DB is us-east-1, app is `sjc` (~60 ms/query). If `/v1/rank`
> feels slow, add a **us-west-2 read replica** in Neon (Oregon — right next to
> `sjc`) and point read traffic at its endpoint. Neon supports cross-region
> replicas. Tracked as a follow-up.

---

## 2. Deploy the API app

```bash
# From the repo root.
fly apps create washington-fish-api

# Secrets (NOT in fly.toml). Use the POOLED Neon string from step 1.
fly secrets set -a washington-fish-api \
  DATABASE_URL='postgres://neondb_owner:<PASSWORD>@ep-fragrant-morning-at7n1ibq-pooler.c-9.us-east-1.aws.neon.tech/neondb?sslmode=require&channel_binding=require'
# Optional — lifts the WDFW Socrata rate limit (get one at data.wa.gov):
# fly secrets set -a washington-fish-api SOCRATA_APP_TOKEN='...'

# Deploy. `--image-label latest` pins the registry tag that schedules.json
# references (registry.fly.io/washington-fish-api:latest), so cron Machines
# always pull the current code. Re-run this exact command for every deploy.
fly deploy --image-label latest

# The release_command runs `/wfa-worker migrate` first (no-op if current).
```

Smoke-test:

```bash
fly status -a washington-fish-api
curl https://washington-fish-api.fly.dev/healthz            # -> 200
curl https://washington-fish-api.fly.dev/readyz             # -> 200 (DB reachable)
curl 'https://washington-fish-api.fly.dev/v1/lakes?q=green&limit=3'
```

---

## 3. Custom domain — api.washington.fish

Fly terminates TLS directly (simplest + most robust), so the records are
**DNS-only (grey cloud)**, not proxied through Cloudflare.

The cert is already created (`fly certs add api.washington.fish` was run). In
**Cloudflare → washington.fish → DNS**, add the records Fly returned:

| Type | Name | Target | Proxy |
|------|------|--------|-------|
| A    | `api` | `66.241.124.125` | **DNS only** |
| AAAA | `api` | `2a09:8280:1::13e:756f:0` | **DNS only** |

(A CNAME `api → washington-fish-api.fly.dev`, DNS-only, also works and is more
robust to IP changes — pick one.)

Then watch validation and verify:
```bash
fly certs check api.washington.fish -a washington-fish-api   # until "Ready"
curl https://api.washington.fish/healthz
```

> If you later want Cloudflare's proxy/WAF/caching (orange cloud): set the
> zone SSL mode to **Full (strict)**, keep the Fly cert on origin, and proxy the
> record. Not needed for launch.

---

## 4. Deploy cron-manager (scheduled ingestion)

Fly has no native CronJob; we use Fly's recommended
[cron-manager](https://github.com/fly-apps/cron-manager) blueprint. It reads
[`schedules.json`](./cron-manager/schedules.json) and spawns a fresh one-shot
Machine per run.

```bash
git clone https://github.com/fly-apps/cron-manager.git /tmp/cron-manager
cp deploy/cron-manager/schedules.json /tmp/cron-manager/schedules.json
cd /tmp/cron-manager

fly apps create washington-fish-cron
fly secrets set -a washington-fish-cron FLY_API_TOKEN="$(fly auth token)"
fly deploy -a washington-fish-cron .
```

Schedules (all UTC, region `sjc`, targeting `washington-fish-api`):

| Job | Cron | Cadence | Timeout |
|-----|------|---------|---------|
| `stocking` | `0 * * * *`  | hourly | 300s |
| `weather`  | `20 * * * *` | hourly (staggered) | 1200s |
| `lakes`    | `0 9 * * 1`  | weekly (Mon) | 2400s |
| `stations` | `0 8 1 * *`  | monthly | 900s |
| `fishwa`   | `0 10 1 * *` | monthly | 900s |

**First run — seed the DB in dependency order** (lakes are the spine; stocking
/ weather / presence hang off them). Trigger manually instead of waiting:

```bash
fly ssh console -a washington-fish-cron
cm schedules list                 # note the schedule IDs
cm jobs trigger <lakes-id>        # wait for it to finish first
cm jobs trigger <stations-id>
cm jobs trigger <stocking-id>
cm jobs trigger <fishwa-id>
cm jobs trigger <weather-id>
cm jobs list <schedule-id>        # inspect run history / exit codes
```

> When you ship new API code, `fly deploy --image-label latest` is enough — cron
> Machines pull `:latest` at spawn, so they track it automatically. Only
> re-deploy cron-manager when you change `schedules.json`.

---

## 5. Operations

```bash
fly logs -a washington-fish-api                 # server logs
fly logs -a washington-fish-cron                # scheduler logs
fly ssh console -a washington-fish-cron -C 'cm jobs list <id>'   # job history
fly scale count 2 -a washington-fish-api        # more API machines
fly secrets list -a washington-fish-api
fly dashboard metrics -a washington-fish-api    # Fly-managed Prometheus (:8081/metrics)
```

Manual one-off worker run (bypassing cron-manager):

```bash
fly machine run registry.fly.io/washington-fish-api:latest \
  -a washington-fish-api --rm --command '/wfa-worker stocking'
```

---

## 6. Follow-ups (not blocking launch)

- **Open-Meteo**: the public API is *non-commercial*. Self-host the AGPL
  Open-Meteo server and set `OPENMETEO_URL` on the API app before real launch
  (see project `CLAUDE.md` → Data sources).
- **Read replica**: add a Neon us-west-2 read replica if DB latency bites.
- **Tracing**: `TRACING_ENABLED=false` today. Point `OTEL_EXPORTER_OTLP_ENDPOINT`
  at a collector and flip it on when there's somewhere to send spans.
- **Frontend** (`washington.fish` apex / `www`) — later; not covered here.
