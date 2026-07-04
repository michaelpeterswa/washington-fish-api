"""Offline variogram fitting for the washington-fish-api KED weather interpolation.

Fits the two frozen spherical variograms that internal/weather/field hardcodes:

  - obs field (nowcast): spatial structure of real station temperature after an
    elevation drift is removed (the same [1, elev] drift KED applies).
  - residual field (forecast bias): spatial structure of (obs - model) at the
    stations, also elevation-detrended.

Coordinates are projected to UTM 10N (EPSG:32610) metres so the fitted Range is
in the SAME units internal/weather/proj produces. The spherical model fit here
is byte-for-byte the one ked.Variogram.Cov implements, so params transfer
directly.

Run:  uv run fit.py [--stations N] [--no-residual]
Output: Go-ready ked.Variogram{...} literals to paste into field.go.
"""

from __future__ import annotations

import argparse
import sys
from concurrent.futures import ThreadPoolExecutor

import numpy as np
import requests
from pyproj import Transformer
from scipy.optimize import curve_fit

UA = {"User-Agent": "washington-fish-api-variogram-fit/0.1 (michael@michaelpeterswa.com)"}
NWS_STATIONS = "https://api.weather.gov/stations"
OPEN_METEO = "https://api.open-meteo.com/v1/forecast"

# EPSG:4326 (lon/lat) -> EPSG:32610 (UTM 10N metres), matching internal/weather/proj.
_to_utm = Transformer.from_crs("EPSG:4326", "EPSG:32610", always_xy=True)


def fetch_stations(cap: int) -> list[dict]:
    """WA NWS stations with an elevation, paginated."""
    url = f"{NWS_STATIONS}?state=WA&limit=500"
    out: list[dict] = []
    for _ in range(40):
        if not url or len(out) >= cap:
            break
        r = requests.get(url, headers=UA, timeout=45)
        r.raise_for_status()
        d = r.json()
        feats = d.get("features", [])
        if not feats:
            break
        for f in feats:
            p, g = f["properties"], f["geometry"]
            elev = (p.get("elevation") or {}).get("value")
            coords = g.get("coordinates") or []
            if elev is None or len(coords) < 2:
                continue
            out.append({"id": p["stationIdentifier"], "lon": coords[0], "lat": coords[1], "elev": elev})
        url = d.get("pagination", {}).get("next", "")
    return out[:cap]


def fetch_temp_series(station_id: str, hours: int) -> dict[str, float]:
    """Recent hourly 2 m temperatures (deg C) for one station, keyed by hour."""
    try:
        r = requests.get(
            f"{NWS_STATIONS}/{station_id}/observations",
            params={"limit": hours},
            headers=UA,
            timeout=30,
        )
        if r.status_code != 200:
            return {}
        out: dict[str, float] = {}
        for f in r.json().get("features", []):
            p = f.get("properties", {})
            t = (p.get("temperature") or {}).get("value")
            ts = p.get("timestamp")
            if t is not None and ts:
                out[ts[:13]] = float(t)  # bucket to the hour (YYYY-MM-DDTHH)
        return out
    except Exception:
        return {}


def fetch_obs(stations: list[dict], hours: int, workers: int) -> None:
    """Attach a 'series' (hour -> temp) and latest 'temp' to each station."""
    with ThreadPoolExecutor(max_workers=workers) as ex:
        series = list(ex.map(lambda s: fetch_temp_series(s["id"], hours), stations))
    for s, ser in zip(stations, series):
        s["series"] = ser
        s["temp"] = ser[max(ser)] if ser else None  # most recent hour


def fetch_model_temp(stations: list[dict]) -> None:
    """Attach a 'model' field: Open-Meteo current 2 m temperature at each station."""
    for i in range(0, len(stations), 100):
        chunk = stations[i : i + 100]
        lats = ",".join(f"{s['lat']:.5f}" for s in chunk)
        lons = ",".join(f"{s['lon']:.5f}" for s in chunk)
        r = requests.get(
            OPEN_METEO,
            params={"latitude": lats, "longitude": lons, "current": "temperature_2m", "timezone": "UTC"},
            headers=UA,
            timeout=60,
        )
        r.raise_for_status()
        data = r.json()
        if isinstance(data, dict):
            data = [data]
        for s, d in zip(chunk, data):
            s["model"] = (d.get("current") or {}).get("temperature_2m")


def detrend(elev, values, x=None, y=None) -> np.ndarray:
    """Remove an OLS drift and return the residual. Drift is [1, elev], or
    [1, elev, x, y] when coords are supplied (planar universal-kriging trend) —
    must match the drift KED regresses out."""
    cols = [np.ones_like(elev), elev]
    if x is not None and y is not None:
        cols += [x, y]
    A = np.column_stack(cols)
    coef, *_ = np.linalg.lstsq(A, values, rcond=None)
    return values - A @ coef


def experimental_variogram(x, y, vals, n_bins=15, max_dist=250_000.0):
    """Isotropic experimental semivariogram (metres) up to max_dist."""
    dx = x[:, None] - x[None, :]
    dy = y[:, None] - y[None, :]
    dist = np.hypot(dx, dy)
    semiv = 0.5 * (vals[:, None] - vals[None, :]) ** 2
    iu = np.triu_indices(len(vals), 1)
    d, g = dist[iu], semiv[iu]

    bins = np.linspace(0, max_dist, n_bins + 1)
    centers, gammas, counts = [], [], []
    for i in range(n_bins):
        m = (d >= bins[i]) & (d < bins[i + 1])
        if m.sum() >= 10:
            centers.append(0.5 * (bins[i] + bins[i + 1]))
            gammas.append(g[m].mean())
            counts.append(int(m.sum()))
    return np.array(centers), np.array(gammas), counts


def pooled_obs_variogram(stations, max_dist, planar=False, n_bins=12, min_stations=30):
    """Pool elevation-detrended semivariance across every observed hour.

    Each hour's field carries its own transient synoptic gradient; pooling many
    hours averages those out and stabilizes the short-range structure that local
    (nearest-k) kriging actually depends on. Stations must already carry x/y.
    """
    bins = np.linspace(0, max_dist, n_bins + 1)
    sum_g = np.zeros(n_bins)
    cnt = np.zeros(n_bins)

    hours: set[str] = set()
    for s in stations:
        hours.update(s.get("series", {}).keys())

    used = 0
    for h in sorted(hours):
        sub = [s for s in stations if h in s.get("series", {})]
        if len(sub) < min_stations:
            continue
        used += 1
        x = np.array([s["x"] for s in sub])
        y = np.array([s["y"] for s in sub])
        e = np.array([s["elev"] for s in sub])
        v = np.array([s["series"][h] for s in sub])
        r = detrend(e, v, x, y) if planar else detrend(e, v)
        dx = x[:, None] - x[None, :]
        dy = y[:, None] - y[None, :]
        dist = np.hypot(dx, dy)
        sv = 0.5 * (r[:, None] - r[None, :]) ** 2
        iu = np.triu_indices(len(sub), 1)
        which = np.digitize(dist[iu], bins) - 1
        gg = sv[iu]
        for b in range(n_bins):
            m = which == b
            if m.any():
                sum_g[b] += gg[m].sum()
                cnt[b] += m.sum()

    centers, gammas, counts = [], [], []
    for b in range(n_bins):
        if cnt[b] >= 30:
            centers.append(0.5 * (bins[b] + bins[b + 1]))
            gammas.append(sum_g[b] / cnt[b])
            counts.append(int(cnt[b]))
    return np.array(centers), np.array(gammas), counts, used


def spherical(h, nugget, psill, rng):
    """Identical to ked.Variogram.Cov's spherical structure (gamma form)."""
    h = np.asarray(h, float)
    s = np.where(h < rng, 1.5 * (h / rng) - 0.5 * (h / rng) ** 3, 1.0)
    return nugget + psill * s


def fit_spherical(centers, gammas, max_dist):
    sill0 = float(np.max(gammas))
    p0 = [sill0 * 0.1, sill0 * 0.9, min(120_000.0, 0.5 * max_dist)]
    bounds = ([0, 0, 5_000.0], [sill0 * 2 + 1e-9, sill0 * 3 + 1e-9, max_dist])
    popt, _ = curve_fit(spherical, centers, gammas, p0=p0, bounds=bounds, maxfev=20000)
    return tuple(float(v) for v in popt)  # nugget, psill, range


def fit_field(name: str, x, y, elev, values, max_dist: float, planar: bool = False):
    resid = detrend(elev, values, x, y) if planar else detrend(elev, values)
    centers, gammas, counts = experimental_variogram(x, y, resid, max_dist=max_dist)
    if len(centers) < 4:
        print(f"  [{name}] too few distance bins ({len(centers)}) — need more/denser stations", file=sys.stderr)
        return None
    nugget, psill, rng = fit_spherical(centers, gammas, max_dist)
    print(f"  [{name}] n={len(values)}  bins={len(centers)}  pairs/bin~{min(counts)}-{max(counts)}  "
          f"nugget={nugget:.3f}  psill={psill:.3f}  range={rng:.0f} m{range_note(rng, max_dist)}", file=sys.stderr)
    return nugget, psill, rng


def range_note(rng: float, max_dist: float) -> str:
    """Temperature is long-correlated, so the range typically pins to the fit
    window; that's fine — it's set to the local kriging-neighborhood scale, and
    nugget+psill (the short-range structure) are the data-derived values."""
    if rng > 0.95 * max_dist:
        return "  [range pinned to fit window — set --max-dist to your kriging-neighborhood scale]"
    return ""


def go_literal(var_name: str, params) -> str:
    n, p, r = params
    return f"\t{var_name} = ked.Variogram{{Nugget: {n:.3f}, PSill: {p:.3f}, Range: {r:.0f}}}"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--stations", type=int, default=300, help="max stations to sample")
    ap.add_argument("--hours", type=int, default=48, help="recent hours to pool per station")
    ap.add_argument("--workers", type=int, default=12, help="concurrent NWS obs fetches")
    ap.add_argument("--max-dist", type=float, default=100_000.0,
                    help="max lag (m) — set to your kriging-neighborhood scale; the range pins here")
    ap.add_argument("--no-residual", action="store_true", help="skip the obs-model residual variogram")
    ap.add_argument("--planar-drift", action=argparse.BooleanOptionalAction, default=True,
                    help="detrend by [1, elev, x, y] to match KED's drift (default on; --no-planar-drift for elev only)")
    args = ap.parse_args()

    print(f"fetching up to {args.stations} WA stations...", file=sys.stderr)
    stations = fetch_stations(args.stations)
    print(f"  {len(stations)} stations", file=sys.stderr)

    print(f"fetching {args.hours}h observation series...", file=sys.stderr)
    fetch_obs(stations, args.hours, args.workers)
    obs = [s for s in stations if s.get("series")]
    print(f"  {len(obs)} stations report temperature", file=sys.stderr)
    if len(obs) < 30:
        print("not enough temperature observations to fit a variogram", file=sys.stderr)
        return 1

    # Project once; attach x/y so pooling can reuse them.
    xs, ys = _to_utm.transform([s["lon"] for s in obs], [s["lat"] for s in obs])
    for s, xv, yv in zip(obs, xs, ys):
        s["x"], s["y"] = float(xv), float(yv)

    print("\nfitting pooled obs-field variogram (nowcast)...", file=sys.stderr)
    obs_params = None
    centers, gammas, counts, used_hours = pooled_obs_variogram(obs, args.max_dist, planar=args.planar_drift)
    if len(centers) >= 4:
        obs_params = fit_spherical(centers, gammas, args.max_dist)
        n_, p_, r_ = obs_params
        print(f"  [obs] hours_pooled={used_hours}  bins={len(centers)}  pairs/bin~{min(counts)}-{max(counts)}  "
              f"nugget={n_:.3f}  psill={p_:.3f}  range={r_:.0f} m{range_note(r_, args.max_dist)}", file=sys.stderr)
    else:
        print("  [obs] too few bins to fit", file=sys.stderr)

    resid_params = None
    if not args.no_residual:
        print("fetching Open-Meteo model at stations...", file=sys.stderr)
        try:
            fetch_model_temp(obs)
            m = [s for s in obs if s.get("model") is not None]
            if len(m) >= 30:
                mlon = np.array([s["lon"] for s in m]); mlat = np.array([s["lat"] for s in m])
                melev = np.array([s["elev"] for s in m])
                bias = np.array([s["temp"] - s["model"] for s in m])
                mx, my = _to_utm.transform(mlon, mlat)
                print("fitting residual-field variogram (forecast bias)...", file=sys.stderr)
                resid_params = fit_field("residual", np.asarray(mx), np.asarray(my), melev, bias, args.max_dist, args.planar_drift)
        except Exception as e:  # noqa: BLE001
            print(f"  residual fit skipped: {e}", file=sys.stderr)

    print("\n// ---- paste into internal/weather/field/field.go ----")
    if obs_params is not None:
        print(go_literal("obsVariogram", obs_params))
    if resid_params is not None:
        print(go_literal("residVariogram", resid_params))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
