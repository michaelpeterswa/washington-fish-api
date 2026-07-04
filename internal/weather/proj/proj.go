// Package proj projects WGS84 lon/lat to UTM zone 10N (EPSG:32610) metres, the
// planar coordinate system KED operates in. All of Washington is projected in
// zone 10 (central meridian 123°W) so every station and lake shares one metric
// frame; eastern-WA distortion is acceptable for the local kriging neighborhoods
// used here. Standard Snyder Transverse Mercator forward series (WGS84).
package proj

import "math"

const (
	a  = 6378137.0           // WGS84 semi-major axis (m)
	f  = 1.0 / 298.257223563 // WGS84 flattening
	k0 = 0.9996              // UTM scale factor
	e2 = f * (2 - f)         // first eccentricity squared
	// zone10CM is the central meridian of UTM zone 10 (degrees).
	zone10CM = -123.0
	falseE   = 500000.0 // false easting (m)
)

// ToUTM10N returns easting, northing in metres for a WGS84 lat/lon (degrees).
func ToUTM10N(latDeg, lonDeg float64) (easting, northing float64) {
	rad := math.Pi / 180
	phi := latDeg * rad
	lam := lonDeg * rad
	lam0 := zone10CM * rad
	ep2 := e2 / (1 - e2)

	sinPhi := math.Sin(phi)
	cosPhi := math.Cos(phi)
	tanPhi := math.Tan(phi)

	n := a / math.Sqrt(1-e2*sinPhi*sinPhi)
	t := tanPhi * tanPhi
	c := ep2 * cosPhi * cosPhi
	A := (lam - lam0) * cosPhi

	m := a * ((1-e2/4-3*e2*e2/64-5*e2*e2*e2/256)*phi -
		(3*e2/8+3*e2*e2/32+45*e2*e2*e2/1024)*math.Sin(2*phi) +
		(15*e2*e2/256+45*e2*e2*e2/1024)*math.Sin(4*phi) -
		(35*e2*e2*e2/3072)*math.Sin(6*phi))

	A2 := A * A
	easting = k0*n*(A+(1-t+c)*A*A2/6+(5-18*t+t*t+72*c-58*ep2)*A2*A2*A/120) + falseE
	northing = k0 * (m + n*tanPhi*(A2/2+(5-t+9*c+4*c*c)*A2*A2/24+
		(61-58*t+t*t+600*c-330*ep2)*A2*A2*A2/720))
	return easting, northing
}
