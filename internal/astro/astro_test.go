package astro

import (
	"math"
	"testing"
	"time"

	"github.com/sixdouglas/suncalc"
)

// Seattle-ish location.
const (
	testLat = 47.6
	testLon = -122.33
)

func TestSolunar_FindsSunEvent(t *testing.T) {
	date := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	sunrise := suncalc.GetTimes(date, testLat, testLon)[suncalc.Sunrise].Value
	if sunrise.IsZero() {
		t.Fatal("no sunrise for reference date")
	}

	// At sunrise, MinsToSunEvent should be ~0.
	s := Solunar(testLat, testLon, sunrise)
	if s.MinsToSunEvent > 1 {
		t.Errorf("at sunrise, MinsToSunEvent = %.1f, want ~0", s.MinsToSunEvent)
	}

	// A couple hours after sunrise, we should be clearly away from an event.
	away := Solunar(testLat, testLon, sunrise.Add(3*time.Hour))
	if away.MinsToSunEvent < 60 {
		t.Errorf("3h after sunrise, MinsToSunEvent = %.1f, want > 60", away.MinsToSunEvent)
	}
}

func TestSolunar_MoonIlluminationInRange(t *testing.T) {
	for _, d := range []time.Time{
		time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 4, 6, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 30, 18, 0, 0, 0, time.UTC),
	} {
		s := Solunar(testLat, testLon, d)
		if s.MoonIllum < 0 || s.MoonIllum > 1 {
			t.Errorf("%s: MoonIllum = %.3f out of [0,1]", d, s.MoonIllum)
		}
	}
}

func TestSolunar_MajorMinorFinite(t *testing.T) {
	s := Solunar(testLat, testLon, time.Date(2026, 7, 4, 20, 0, 0, 0, time.UTC))
	if math.IsInf(s.MinsToMoonMajor, 0) {
		t.Error("MinsToMoonMajor should be finite (transit/underfoot always exist in a 24h window)")
	}
}
