package weather

import (
	"testing"
	"time"

	"github.com/michaelpeterswa/washington-fish-api/internal/store"
)

// constAirSeries builds n hourly steps of constant air temperature.
func constAirSeries(n int, airC float64) ([]time.Time, []*float64) {
	base := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	times := make([]time.Time, n)
	air := make([]*float64, n)
	for i := range times {
		times[i] = base.Add(time.Duration(i) * time.Hour)
		v := airC
		air[i] = &v
	}
	return times, air
}

func TestWaterTempSeries_ColdStartSeedsFromAir(t *testing.T) {
	times, air := constAirSeries(24, 25)
	deep := 20.0
	out := waterTempSeries(times, air, &deep, nil)
	if got := *out[times[0]]; got != 25 {
		t.Fatalf("cold start should seed from air (25), got %v", got)
	}
}

func TestWaterTempSeries_ContinuationHoldsState(t *testing.T) {
	times, air := constAirSeries(48, 25) // constant warm air
	deep := 20.0                         // long time constant -> strong damping

	seed := &store.WaterSeed{Temp: 10, At: times[0]}
	out := waterTempSeries(times, air, &deep, seed)

	if got := *out[times[0]]; got != 10 {
		t.Errorf("seeded value at its own time should be exactly the seed (10), got %v", got)
	}
	last := *out[times[47]]
	// A deep lake seeded cool must rise toward the warm air but stay well below
	// it after only ~2 days — this is the whole point of the thermal inertia.
	if last <= 10 || last >= 20 {
		t.Errorf("after 47h a deep lake should rise from 10 but stay well below air 25, got %v", last)
	}
}

func TestWaterTempSeries_ContinuesFromSeedTime(t *testing.T) {
	times, air := constAirSeries(48, 25)
	deep := 20.0
	seed := &store.WaterSeed{Temp: 12, At: times[24]}
	out := waterTempSeries(times, air, &deep, seed)

	if _, ok := out[times[0]]; ok {
		t.Error("no water temp should be produced before the seed time")
	}
	if got, ok := out[times[24]]; !ok || *got != 12 {
		t.Errorf("water temp at seed time should equal the seed (12), got %v (ok=%v)", got, ok)
	}
	if _, ok := out[times[47]]; !ok {
		t.Error("water temp should be produced through the end of the window")
	}
}
