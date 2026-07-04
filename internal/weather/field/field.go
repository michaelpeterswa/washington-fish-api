// Package field turns a station network + a lake into KED-variance-derived
// confidence. It projects to UTM 10N, picks a local nearest-k neighborhood, and
// solves the bordered KED system (ked package) for two frozen variograms — the
// obs field (nowcast) and the residual field (forecast bias). Kriging variance,
// which depends only on geometry + drift + variogram (NOT on any observed
// values), maps to the confidence field.
package field

import (
	"sort"

	"github.com/michaelpeterswa/washington-fish-api/internal/weather/ked"
	"github.com/michaelpeterswa/washington-fish-api/internal/weather/proj"
)

// Frozen variograms (distances in metres, UTM 10N).
var (
	// obsVariogram: DATA-DERIVED (tools/variogram-fit, pooled 48h of NWS obs,
	// planar [1,elev,x,y] drift matching ked). The planar drift makes the
	// residual stationary so this actually settles to a sill/range.
	obsVariogram = ked.Variogram{Nugget: 0.348, PSill: 7.700, Range: 247080}
	// residVariogram (forecast bias): still a PLACEHOLDER — smoother/smaller than
	// the obs field. A stable fit needs Open-Meteo ARCHIVE model at stations to
	// pool obs−model over many hours; the single-snapshot fit is noise-dominated.
	residVariogram = ked.Variogram{Nugget: 0.3, PSill: 3.0, Range: 200000}
)

const (
	neighbors = 25  // local kriging neighborhood size
	elevRefKm = 0.5 // centre elevation (~regional mean, km) for conditioning
)

// Point is a lon/lat/elevation location (WGS84 degrees, metres).
type Point struct {
	Lon   float64
	Lat   float64
	ElevM float64
}

// Confidences is the KED confidence for the nowcast and forecast fields.
type Confidences struct {
	Nowcast  float64
	Forecast float64
}

type pStation struct {
	x, y, elevKm float64
}

// StationSet is a projected, reusable station network — project once, evaluate
// every lake against it.
type StationSet struct {
	s []pStation
}

// NewStationSet projects the station network to UTM 10N and scales elevation.
func NewStationSet(stations []Point) *StationSet {
	ps := make([]pStation, len(stations))
	for i, st := range stations {
		x, y := proj.ToUTM10N(st.Lat, st.Lon)
		ps[i] = pStation{x: x, y: y, elevKm: st.ElevM/1000 - elevRefKm}
	}
	return &StationSet{s: ps}
}

// Len reports how many stations are in the set.
func (ss *StationSet) Len() int { return len(ss.s) }

// Confidence computes nowcast + forecast confidence for a lake from its nearest
// stations. Returns an error if the local KED system is singular (e.g. too few
// elevation-spanning neighbors for the drift to be full rank).
func (ss *StationSet) Confidence(lake Point) (Confidences, error) {
	lx, ly := proj.ToUTM10N(lake.Lat, lake.Lon)
	target := ked.Target{X: lx, Y: ly, Elev: lake.ElevM/1000 - elevRefKm}

	nn := ss.nearest(lx, ly, neighbors)
	kst := make([]ked.Station, len(nn))
	for i, p := range nn {
		kst[i] = ked.Station{X: p.x, Y: p.y, Elev: p.elevKm}
	}

	nowcast, err := confidenceFor(obsVariogram, kst, target)
	if err != nil {
		return Confidences{}, err
	}
	forecast, err := confidenceFor(residVariogram, kst, target)
	if err != nil {
		return Confidences{}, err
	}
	return Confidences{Nowcast: nowcast, Forecast: forecast}, nil
}

// confidenceFor solves KED and maps the kriging variance to [0.05, 0.98]:
// variance near 0 (well-surrounded) -> high confidence; variance approaching or
// exceeding the sill (sparse coverage or drift extrapolation) -> low.
func confidenceFor(v ked.Variogram, st []ked.Station, t ked.Target) (float64, error) {
	w, err := ked.Solve(v, st, t)
	if err != nil {
		return 0, err
	}
	sill := v.Nugget + v.PSill
	return clamp(1-w.Variance/sill, 0.05, 0.98), nil
}

// nearest returns the k stations closest to (x,y).
func (ss *StationSet) nearest(x, y float64, k int) []pStation {
	if k > len(ss.s) {
		k = len(ss.s)
	}
	idx := make([]int, len(ss.s))
	dist := make([]float64, len(ss.s))
	for i, p := range ss.s {
		dx, dy := p.x-x, p.y-y
		idx[i] = i
		dist[i] = dx*dx + dy*dy
	}
	sort.Slice(idx, func(a, b int) bool { return dist[idx[a]] < dist[idx[b]] })
	out := make([]pStation, k)
	for i := 0; i < k; i++ {
		out[i] = ss.s[idx[i]]
	}
	return out
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
