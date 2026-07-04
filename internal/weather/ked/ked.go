// Package ked implements Kriging with External Drift (universal kriging)
// for interpolating weather fields to Washington lakes, using station
// elevation as the drift covariate.
//
// KEY PROPERTY exploited here: the kriging *weights* depend only on the
// station geometry, the drift terms, and the (frozen) variogram — NOT on
// the observed values. So for a fixed station set + lake you solve the
// bordered system ONCE and reuse the weights for the nowcast AND every
// forecast hour; only the value vector you dot against changes.
//
// In KED the external drift IS the elevation correction: you regress on
// elevation, krige the residual, and evaluate the predictor at the lake's
// elevation — one operation, no separate lapse-rate pass.
package ked

import (
	"errors"
	"math"

	"gonum.org/v1/gonum/mat"
)

// Variogram is a spherical model. Parameters are FROZEN — fit offline once
// per (region, variable, season); spatial structure is not re-estimated at
// serving time. Distances use the same horizontal units as Station.X/Y
// (recommend projected metres, e.g. UTM zone 10N / EPSG:32610).
//
// NOTE: the obs field (nowcast) and the model-residual field (forecast bias)
// have DIFFERENT structure — keep two frozen Variograms. The residual field
// is usually smoother (smaller sill/range).
type Variogram struct {
	Nugget float64
	PSill  float64 // partial sill
	Range  float64
}

// Cov is C(h) = sill - gamma(h) for the spherical model. The diagonal
// (h == 0) returns the full sill (nugget + partial sill); the nugget is the
// discontinuity at the origin, which also helps condition the system.
func (v Variogram) Cov(h float64) float64 {
	if h <= 0 {
		return v.Nugget + v.PSill
	}
	if h >= v.Range {
		return 0
	}
	hr := h / v.Range
	s := 1.5*hr - 0.5*hr*hr*hr
	return v.PSill * (1 - s)
}

// Station is a data location. Elev is the external-drift covariate. Scale
// and centre elevation (e.g. km above a regional mean) before filling this
// in, so the drift block stays O(1) and the bordered system is well
// conditioned relative to the covariance entries.
type Station struct {
	X, Y float64 // projected horizontal coords, metres
	Elev float64 // scaled/centred elevation (drift covariate)
}

// Target is the prediction location (the lake), same units as Station.
type Target struct {
	X, Y float64
	Elev float64
}

// p is the number of external-drift terms: [1, elev, x, y] — universal kriging
// with elevation PLUS a planar horizontal trend. The horizontal trend is what
// makes the drift residual second-order stationary over WA (elevation alone
// leaves a marine→continental gradient, so the residual variogram railed — see
// tools/variogram-fit). x,y are centred + scaled inside Solve.
const p = 4

// Weights holds the solved KED system for one (stations, lake, variogram)
// configuration. Reuse across the nowcast and all forecast hours.
type Weights struct {
	Lambda   []float64 // n station weights
	Variance float64   // kriging variance — depends only on geometry, not values
}

// Solve builds and solves the bordered KED system:
//
//	[ K  F ] [ lambda ]   [ k0 ]
//	[ F' 0 ] [  mu    ] = [ f0 ]
//
// K  : station-station covariance           (n x n)
// F  : drift matrix, columns [1, elev, x, y] (n x p)
// k0 : station-target covariance            (n)
// f0 : target drift [1, elev, x, y]_lake    (p)
//
// The system is symmetric indefinite (saddle point); gonum's general LU
// (SolveVec) handles it. It is nonsingular when the variogram is valid and
// the drift has full column rank (stations must SPAN elevation AND not be
// collinear in the horizontal plane — a handful of well-spread stations
// suffice).
func Solve(v Variogram, st []Station, t Target) (*Weights, error) {
	n := len(st)
	if n < p+1 {
		return nil, errors.New("ked: need at least p+1 stations")
	}

	// Centre + scale the horizontal coords for the DRIFT only (distances still
	// use raw metres). Raw UTM eastings/northings are ~1e6, which would swamp the
	// O(sill) covariance entries and wreck conditioning of the bordered system.
	// Kriging is invariant to affine reparametrization of the drift basis, so
	// this changes conditioning, not the result.
	var x0, y0 float64
	for i := range st {
		x0 += st[i].X
		y0 += st[i].Y
	}
	x0 /= float64(n)
	y0 /= float64(n)
	const driftScale = 1e5 // 100 km -> O(1) drift coordinates
	driftRow := func(x, y, elev float64) [p]float64 {
		return [p]float64{1, elev, (x - x0) / driftScale, (y - y0) / driftScale}
	}

	m := n + p
	A := mat.NewDense(m, m, nil)
	b := mat.NewVecDense(m, nil)

	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			h := math.Hypot(st[i].X-st[j].X, st[i].Y-st[j].Y)
			A.Set(i, j, v.Cov(h))
		}
		fi := driftRow(st[i].X, st[i].Y, st[i].Elev)
		for k := 0; k < p; k++ {
			A.Set(i, n+k, fi[k]) // F
			A.Set(n+k, i, fi[k]) // F'
		}
		h0 := math.Hypot(st[i].X-t.X, st[i].Y-t.Y)
		b.SetVec(i, v.Cov(h0)) // k0
	}
	ft := driftRow(t.X, t.Y, t.Elev)
	for k := 0; k < p; k++ {
		b.SetVec(n+k, ft[k]) // f0
	}

	var w mat.VecDense
	if err := w.SolveVec(A, b); err != nil {
		return nil, err // singular: check drift rank / duplicate stations
	}

	lambda := make([]float64, n)
	for i := 0; i < n; i++ {
		lambda[i] = w.AtVec(i)
	}
	// kriging variance = C(0) - w . b
	var wb float64
	for i := 0; i < m; i++ {
		wb += w.AtVec(i) * b.AtVec(i)
	}
	variance := v.Cov(0) - wb
	if variance < 0 {
		variance = 0 // numerical guard near duplicate points
	}
	return &Weights{Lambda: lambda, Variance: variance}, nil
}

// Predict dots the solved weights against a value vector. len(vals) must
// equal the number of stations passed to Solve, in the same order.
//   - nowcast: vals = current observed temperatures
//   - forecast bias: vals = residuals (obs - model) at the stations
func (w *Weights) Predict(vals []float64) float64 {
	var y float64
	for i, l := range w.Lambda {
		y += l * vals[i]
	}
	return y
}

// ----------------------------------------------------------------------
// Usage
// ----------------------------------------------------------------------
//
// Nowcast (observed temperature -> lake, using the OBS-field variogram):
//
//	w, _ := Solve(obsVario, stations, lake)
//	tempNow := w.Predict(obsTemps)   // °C at the lake, now
//	conf    := w.Variance            // -> confidence field (wide = honest low)
//
// Forecast bias-correction (using the RESIDUAL-field variogram):
//
//	wb, _ := Solve(residVario, stations, lake)
//	bias  := wb.Predict(resid)       // resid[i] = obs[i] - model_station[i]
//	for h := range modelLakeHourly { // one dot; same weights every hour
//	    lakeForecast[h] = modelLakeHourly[h] + bias
//	}
//
// Diurnal bias: build resid PER clock-hour from a rolling window and call
// Predict on each — weights are unchanged (geometry is fixed), so it stays
// one solve + a dot per hour.

// ----------------------------------------------------------------------
// Deferred / future work (intentionally out of scope for v1)
// ----------------------------------------------------------------------
//
// v1 uses a SINGLE frozen variogram per field (obs, residual). Known
// limitation: the spatial correlation structure is weather-regime
// dependent, so one variogram is a compromise — least accurate in the
// stable/inversion conditions that matter most.
//
//  1. Regime-stratified variograms. Freeze 3-4 variograms keyed by regime
//     (stable/calm, mixed/windy, transitional); classify each hour from
//     data already pulled (wind, cloud cover, pressure tendency); pick the
//     matching one. Weights are still reused WITHIN a regime, so re-solve
//     only when the regime flips. Also fixes the confidence field, which is
//     currently flat across regimes. Highest-value follow-up, and it pays
//     off most on the residual/bias path since model bias is itself
//     regime-dependent (models blow inversions).
//
//  2. Anisotropic / advective kriging. Let wind advect the field so an
//     upwind station counts more than a downwind one at equal distance.
//     Bigger lift (directional covariance); revisit only if residuals show
//     a clear flow-aligned pattern the isotropic model misses.
//
// IMPORTANT: this is NOT "ignoring weather variation." The value vectors
// carry all hour-to-hour weather; Predict is linear in the data and
// re-fits the elevation slope from each hour's obs (inversions included).
// These items only make the spatial *stencil* itself condition-aware.
