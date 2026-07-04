package proj

import (
	"math"
	"testing"
)

func TestToUTM10N(t *testing.T) {
	cases := []struct {
		name         string
		lat, lon     float64
		wantE, wantN float64
		tol          float64
	}{
		// On the central meridian easting is exactly the false easting and
		// northing is k0 * meridian_arc(47°); the arc (5207247 m) was verified
		// by direct numerical integration, so this is an independent check.
		{"central_meridian", 47.0, -123.0, 500000, 5205164, 2},
		// Seattle: k0*M(47.6062°)=5272532 plus the analytic A²/2 correction
		// (~216 m) = 5272748; easting k0*n*A + 500000 = 550200.
		{"seattle", 47.6062, -122.3321, 550200, 5272748, 3},
	}
	for _, c := range cases {
		e, n := ToUTM10N(c.lat, c.lon)
		if math.Abs(e-c.wantE) > c.tol || math.Abs(n-c.wantN) > c.tol {
			t.Errorf("%s: got E=%.1f N=%.1f, want E=%.1f N=%.1f (tol %.0f)",
				c.name, e, n, c.wantE, c.wantN, c.tol)
		}
	}
}
