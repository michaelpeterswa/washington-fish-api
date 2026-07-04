// Package astro is a thin wrapper over suncalc (the Go port of the standard
// SunCalc) that turns sun/moon ephemeris into the signals the solunar bite
// factor needs: how close a given instant is to dawn/dusk, to a moon
// major period (transit/underfoot), and to a moon minor period (moonrise/set),
// plus the moon's illuminated fraction. The astronomy is the library's; the
// fishing interpretation lives in internal/predict/bite.
package astro

import (
	"math"
	"time"

	"github.com/sixdouglas/suncalc"
)

// SolunarState is the proximity of one instant to the solunar peaks (minutes),
// plus moon illumination (0..1). Inf means "no such event nearby / that day".
type SolunarState struct {
	MinsToSunEvent  float64 // nearest sunrise or sunset (crepuscular feeding)
	MinsToMoonMajor float64 // nearest moon transit (overhead) or underfoot
	MinsToMoonMinor float64 // nearest moonrise or moonset
	MoonIllum       float64 // illuminated fraction 0..1
}

// Solunar computes the solunar state for a lake location at instant t.
func Solunar(lat, lon float64, t time.Time) SolunarState {
	t = t.UTC()
	return SolunarState{
		MinsToSunEvent:  nearestSunEvent(lat, lon, t),
		MinsToMoonMajor: nearestMoonMajor(lat, lon, t),
		MinsToMoonMinor: nearestMoonMinor(lat, lon, t),
		MoonIllum:       suncalc.GetMoonIllumination(t).Fraction,
	}
}

func minutesAbs(a, b time.Time) float64 {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d.Minutes()
}

// nearestSunEvent scans the day of t plus its neighbors so events just across a
// midnight boundary are still found.
func nearestSunEvent(lat, lon float64, t time.Time) float64 {
	best := math.Inf(1)
	for _, off := range []int{-1, 0, 1} {
		times := suncalc.GetTimes(t.AddDate(0, 0, off), lat, lon)
		for _, name := range []suncalc.DayTimeName{suncalc.Sunrise, suncalc.Sunset} {
			if dt, ok := times[name]; ok && !dt.Value.IsZero() {
				if m := minutesAbs(t, dt.Value); m < best {
					best = m
				}
			}
		}
	}
	return best
}

func nearestMoonMinor(lat, lon float64, t time.Time) float64 {
	best := math.Inf(1)
	for _, off := range []int{-1, 0, 1} {
		mt := suncalc.GetMoonTimes(t.AddDate(0, 0, off), lat, lon, true)
		for _, e := range []time.Time{mt.Rise, mt.Set} {
			if !e.IsZero() {
				if m := minutesAbs(t, e); m < best {
					best = m
				}
			}
		}
	}
	return best
}

// nearestMoonMajor finds the moon's transit (max altitude) and underfoot (min
// altitude) by scanning a ±12h window, and returns the distance to the nearer.
func nearestMoonMajor(lat, lon float64, t time.Time) float64 {
	var transit, underfoot time.Time
	maxAlt, minAlt := math.Inf(-1), math.Inf(1)
	for τ := t.Add(-12 * time.Hour); !τ.After(t.Add(12 * time.Hour)); τ = τ.Add(15 * time.Minute) {
		alt := suncalc.GetMoonPosition(τ, lat, lon).Altitude
		if alt > maxAlt {
			maxAlt, transit = alt, τ
		}
		if alt < minAlt {
			minAlt, underfoot = alt, τ
		}
	}
	return math.Min(minutesAbs(t, transit), minutesAbs(t, underfoot))
}
