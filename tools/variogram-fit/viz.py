"""Visualize the variogram fit and the kriged field / confidence surface.

Produces two PNGs in this directory:
  - variogram.png : experimental + fitted spherical for [1,elev] vs [1,elev,x,y]
                    drift — the railing-vs-settling result, made visual.
  - kriged_map.png: ordinary kriging of the (planar+elev)-detrended temperature
                    residual over WA, plus the kriging-variance confidence field,
                    with the station network overlaid.

Reuses fit.py's data + variogram functions. Kriging here is hand-rolled numpy
(same spirit as the Go ked package), so no extra geostat dependency.

Run:  uv run viz.py [--stations 350] [--hours 48]
"""

from __future__ import annotations

import argparse
import os
import sys

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np
import seaborn as sns

import requests

import fit

WA_LON = (-124.85, -116.9)
WA_LAT = (45.5, 49.05)

# Public US-states GeoJSON (Leaflet examples data); lon/lat, properties.name.
STATES_GEOJSON = "https://raw.githubusercontent.com/PublicaMundi/MappingAPI/master/data/geojson/us-states.json"


def wa_boundary():
    """Washington's polygon geometry (lon/lat), or None if the fetch fails."""
    try:
        r = requests.get(STATES_GEOJSON, timeout=30)
        r.raise_for_status()
        for feat in r.json()["features"]:
            if feat["properties"].get("name") == "Washington":
                return feat["geometry"]
    except Exception as e:  # noqa: BLE001
        print(f"  boundary overlay skipped: {e}", file=sys.stderr)
    return None


def draw_boundary(ax, geom):
    if not geom:
        return
    polys = geom["coordinates"] if geom["type"] == "MultiPolygon" else [geom["coordinates"]]
    for poly in polys:
        for ring in poly:
            arr = np.asarray(ring)
            ax.plot(arr[:, 0], arr[:, 1], color="black", lw=1.3, zorder=5)


def ordinary_krige(xs, ys, vals, gx, gy, params):
    """Ordinary kriging of `vals` at station (xs, ys) onto flat grid points
    (gx, gy). Returns (estimate, variance). params = (nugget, psill, range)."""
    nugget, psill, rng = params
    sill = nugget + psill

    def cov(h):  # spherical covariance C(h) = sill - gamma(h)
        return sill - fit.spherical(h, nugget, psill, rng)

    n = len(xs)
    dxx = np.hypot(xs[:, None] - xs[None, :], ys[:, None] - ys[None, :])
    A = np.ones((n + 1, n + 1))
    A[:n, :n] = cov(dxx)
    A[n, n] = 0.0
    Ainv = np.linalg.inv(A)

    d0 = np.hypot(gx[:, None] - xs[None, :], gy[:, None] - ys[None, :])  # G x n
    b = np.ones((len(gx), n + 1))
    b[:, :n] = cov(d0)
    W = b @ Ainv.T  # G x (n+1): kriging weights + lagrange multiplier
    est = W[:, :n] @ vals
    var = sill - np.einsum("gi,gi->g", b, W)
    return est, np.clip(var, 0, None)


def dedup(xs, ys, *arrays, tol=200.0):
    """Drop near-coincident stations (would make the kriging matrix singular)."""
    keep, seen = [], set()
    for i in range(len(xs)):
        key = (round(xs[i] / tol), round(ys[i] / tol))
        if key not in seen:
            seen.add(key)
            keep.append(i)
    keep = np.array(keep)
    return (xs[keep], ys[keep], *(a[keep] for a in arrays))


def plot_variograms(obs, max_dist, path):
    sns.set_theme(style="whitegrid", context="talk")
    fig, ax = plt.subplots(figsize=(10, 6))
    hgrid = np.linspace(0, max_dist, 200)

    for planar, color, label in [
        (False, "#d1495b", "[1, elev] drift  (current — rails)"),
        (True, "#2e86ab", "[1, elev, x, y] drift  (fix — settles)"),
    ]:
        centers, gammas, _counts, hours = fit.pooled_obs_variogram(obs, max_dist, planar=planar)
        nugget, psill, rng = fit.fit_spherical(centers, gammas, max_dist)
        ax.scatter(np.array(centers) / 1000, gammas, color=color, s=60, zorder=3)
        ax.plot(hgrid / 1000, fit.spherical(hgrid, nugget, psill, rng), color=color, lw=2.5,
                label=f"{label}\n  nugget={nugget:.2f} psill={psill:.2f} range={rng/1000:.0f}km")
        ax.axhline(nugget + psill, color=color, ls=":", lw=1, alpha=0.6)

    ax.set_xlabel("lag distance (km)")
    ax.set_ylabel("semivariance  γ(h)  (°C²)")
    ax.set_title(f"Obs-field variogram — pooled {hours}h, elevation vs planar drift")
    ax.legend(fontsize=11, loc="lower right")
    fig.tight_layout()
    fig.savefig(path, dpi=130)
    plt.close(fig)


def plot_kriged_map(obs, max_dist, path):
    # Latest-hour residual after the planar+elevation drift (the field kriging sees).
    xs = np.array([s["x"] for s in obs])
    ys = np.array([s["y"] for s in obs])
    lon = np.array([s["lon"] for s in obs])
    lat = np.array([s["lat"] for s in obs])
    elev = np.array([s["elev"] for s in obs])
    temp = np.array([s["temp"] for s in obs])
    resid = fit.detrend(elev, temp, xs, ys)

    xs, ys, lon, lat, resid = dedup(xs, ys, lon, lat, resid)

    # Fit the planar variogram to use for the map kriging.
    centers, gammas, _c, _h = fit.pooled_obs_variogram(obs, max_dist, planar=True)
    params = fit.fit_spherical(centers, gammas, max_dist)
    sill = params[0] + params[1]

    # WA grid in lon/lat -> project to UTM for kriging.
    glon = np.linspace(*WA_LON, 90)
    glat = np.linspace(*WA_LAT, 55)
    GLon, GLat = np.meshgrid(glon, glat)
    gx, gy = fit._to_utm.transform(GLon.ravel(), GLat.ravel())
    est, var = ordinary_krige(xs, ys, resid, np.asarray(gx), np.asarray(gy), params)
    conf = np.clip(1 - var / sill, 0.05, 0.98).reshape(GLat.shape)
    est = est.reshape(GLat.shape)

    sns.set_theme(style="white", context="talk")
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(17, 7))
    aspect = 1 / np.cos(np.radians(np.mean(WA_LAT)))

    lim = np.percentile(np.abs(resid), 95)
    c1 = ax1.contourf(GLon, GLat, est, levels=14, cmap="RdBu_r", vmin=-lim, vmax=lim)
    ax1.scatter(lon, lat, c=resid, cmap="RdBu_r", vmin=-lim, vmax=lim,
                s=22, edgecolor="k", linewidth=0.4, zorder=3)
    ax1.set_title("Kriged temperature residual (°C)\n(after elev + planar drift removed)")
    fig.colorbar(c1, ax=ax1, shrink=0.8, label="°C anomaly")

    c2 = ax2.contourf(GLon, GLat, conf, levels=np.linspace(0, 1, 21), cmap="viridis", vmin=0, vmax=1)
    ax2.scatter(lon, lat, c="white", s=10, edgecolor="k", linewidth=0.3, zorder=3)
    ax2.set_title("KED confidence  (1 − kriging_variance / sill)\nhigh near stations, low in gaps/edges")
    fig.colorbar(c2, ax=ax2, shrink=0.8, label="confidence")

    boundary = wa_boundary()
    for ax in (ax1, ax2):
        draw_boundary(ax, boundary)
        ax.set_aspect(aspect)
        ax.set_xlabel("longitude")
        ax.set_ylabel("latitude")
        ax.set_xlim(*WA_LON)
        ax.set_ylim(*WA_LAT)

    fig.suptitle("Ordinary kriging over Washington — interpolation + confidence", fontsize=16)
    fig.tight_layout()
    fig.savefig(path, dpi=130)
    plt.close(fig)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--stations", type=int, default=350)
    ap.add_argument("--hours", type=int, default=48)
    ap.add_argument("--workers", type=int, default=12)
    ap.add_argument("--max-dist", type=float, default=250_000.0)
    args = ap.parse_args()

    print(f"fetching up to {args.stations} stations, {args.hours}h series...", file=sys.stderr)
    stations = fit.fetch_stations(args.stations)
    fit.fetch_obs(stations, args.hours, args.workers)
    obs = [s for s in stations if s.get("series") and s.get("temp") is not None]
    print(f"  {len(obs)} stations with temperature", file=sys.stderr)
    if len(obs) < 30:
        print("not enough data", file=sys.stderr)
        return 1

    xs, ys = fit._to_utm.transform([s["lon"] for s in obs], [s["lat"] for s in obs])
    for s, xv, yv in zip(obs, xs, ys):
        s["x"], s["y"] = float(xv), float(yv)

    os.makedirs("docs", exist_ok=True)
    print("rendering docs/variogram.png...", file=sys.stderr)
    plot_variograms(obs, args.max_dist, "docs/variogram.png")
    print("rendering docs/kriged_map.png...", file=sys.stderr)
    plot_kriged_map(obs, args.max_dist, "docs/kriged_map.png")
    print("wrote docs/variogram.png and docs/kriged_map.png", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
