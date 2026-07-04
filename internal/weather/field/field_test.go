package field

import (
	"math"
	"testing"
)

// stationGrid builds a 6x6 station grid across a WA-ish box. Elevation is a
// non-planar bump (a Gaussian ridge), NOT a linear ramp — a linear ramp would
// make elev collinear with x,y and leave the [1,elev,x,y] drift rank-deficient
// (real terrain isn't a plane).
func stationGrid() []Point {
	var pts []Point
	for i := 0; i < 6; i++ {
		for j := 0; j < 6; j++ {
			di, dj := float64(i)-2.5, float64(j)-2.5
			pts = append(pts, Point{
				Lat:   47.0 + float64(i)*0.15,
				Lon:   -122.0 + float64(j)*0.15,
				ElevM: 200 + 700*math.Exp(-(di*di+dj*dj)/6),
			})
		}
	}
	return pts
}

func TestConfidence_HigherWhenWellSurrounded(t *testing.T) {
	ss := NewStationSet(stationGrid())

	inside, err := ss.Confidence(Point{Lat: 47.4, Lon: -121.6, ElevM: 350})
	if err != nil {
		t.Fatalf("inside: %v", err)
	}
	remote, err := ss.Confidence(Point{Lat: 47.4, Lon: -118.0, ElevM: 350}) // far east of the grid
	if err != nil {
		t.Fatalf("remote: %v", err)
	}
	if inside.Nowcast <= remote.Nowcast {
		t.Errorf("expected inside confidence (%.3f) > remote (%.3f)", inside.Nowcast, remote.Nowcast)
	}
}

func TestConfidence_LowerWhenElevationExtrapolated(t *testing.T) {
	ss := NewStationSet(stationGrid())

	normal, err := ss.Confidence(Point{Lat: 47.4, Lon: -121.6, ElevM: 350})
	if err != nil {
		t.Fatalf("normal: %v", err)
	}
	alpine, err := ss.Confidence(Point{Lat: 47.4, Lon: -121.6, ElevM: 3000}) // far above all stations
	if err != nil {
		t.Fatalf("alpine: %v", err)
	}
	if alpine.Nowcast >= normal.Nowcast {
		t.Errorf("expected alpine/extrapolated confidence (%.3f) < normal (%.3f)", alpine.Nowcast, normal.Nowcast)
	}
}

func TestConfidence_InRange(t *testing.T) {
	ss := NewStationSet(stationGrid())
	c, err := ss.Confidence(Point{Lat: 47.4, Lon: -121.6, ElevM: 350})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, v := range []float64{c.Nowcast, c.Forecast} {
		if v < 0.05 || v > 0.98 {
			t.Errorf("confidence %.3f out of [0.05,0.98]", v)
		}
	}
}
