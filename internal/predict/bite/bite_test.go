package bite

import "testing"

func ptrI(i int) *int         { return &i }
func ptrF(f float64) *float64 { return &f }

func TestScore_FreshPlantGoodConditions(t *testing.T) {
	r := Score(Inputs{
		DaysSinceCatchablePlant: ptrI(1),
		PressureTendency:        ptrF(-1.5), // falling
		WindMps:                 ptrF(3),    // light chop
		CloudPct:                ptrF(80),   // overcast
	})
	// base 50 + ~30 + 10 + 8 + 6 = ~104 -> clamped 100
	if r.Score < 90 {
		t.Fatalf("expected high score, got %v", r.Score)
	}
	if len(r.Factors) != 4 {
		t.Fatalf("expected 4 factors, got %d", len(r.Factors))
	}
}

func TestScore_OldPlantBadConditions(t *testing.T) {
	r := Score(Inputs{
		DaysSinceCatchablePlant: ptrI(120),
		PressureTendency:        ptrF(2.0), // rising
		WindMps:                 ptrF(12),  // gale
		CloudPct:                ptrF(5),   // clear
	})
	// base 50 + ~0 - 6 - 8 - 2 = ~34
	if r.Score > 45 {
		t.Fatalf("expected low score, got %v", r.Score)
	}
}

func TestScore_ClampAndMissingData(t *testing.T) {
	r := Score(Inputs{}) // no data at all
	// only the catchable factor emits (with a "no plant" reason, 0 contribution)
	if r.Score != baseScore {
		t.Fatalf("expected base score with no data, got %v", r.Score)
	}
	if len(r.Factors) != 1 || r.Factors[0].Name != "catchable_plant" {
		t.Fatalf("expected only the catchable factor, got %+v", r.Factors)
	}
}

func TestScore_SolunarDawnAddsBonus(t *testing.T) {
	base := Score(Inputs{DaysSinceCatchablePlant: ptrI(30)})
	// Right at dawn, at a moon major period, near full moon.
	peak := Score(Inputs{
		DaysSinceCatchablePlant: ptrI(30),
		Solunar:                 &Solunar{MinsToSunEvent: 0, MinsToMoonMajor: 0, MinsToMoonMinor: 500, MoonIllum: 1.0},
	})
	if peak.Score <= base.Score {
		t.Fatalf("solunar peak (%v) should exceed off-peak (%v)", peak.Score, base.Score)
	}
	var found bool
	for _, f := range peak.Factors {
		if f.Name == "solunar" && f.Contribution > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a positive solunar factor, got %+v", peak.Factors)
	}
}

func TestScore_SolunarDeadTimeNoBonus(t *testing.T) {
	// Midday, no moon periods nearby -> solunar factor present but zero.
	r := Score(Inputs{
		DaysSinceCatchablePlant: ptrI(30),
		Solunar:                 &Solunar{MinsToSunEvent: 300, MinsToMoonMajor: 300, MinsToMoonMinor: 300, MoonIllum: 0.5},
	})
	for _, f := range r.Factors {
		if f.Name == "solunar" && f.Contribution != 0 {
			t.Fatalf("expected zero solunar contribution in dead time, got %v", f.Contribution)
		}
	}
}

func TestScore_ThermalSpeciesAware(t *testing.T) {
	// 24°C water: warm. Bass (opt 20-27) should get a bonus; trout (opt 10-18,
	// tol to 22) should be penalized — same temp, opposite sign.
	bassWin := &Thermal{WaterTempC: 24, SpeciesName: "Largemouth Bass", OptLo: 20, OptHi: 27, TolLo: 15, TolHi: 32}
	troutWin := &Thermal{WaterTempC: 24, SpeciesName: "Rainbow Trout", OptLo: 10, OptHi: 18, TolLo: 4, TolHi: 22}

	bass := factorByName(Score(Inputs{Thermal: bassWin}).Factors, "water_temp")
	trout := factorByName(Score(Inputs{Thermal: troutWin}).Factors, "water_temp")
	if bass == nil || trout == nil {
		t.Fatal("expected water_temp factor for both")
	}
	if bass.Contribution <= 0 {
		t.Errorf("bass in 24°C should be positive, got %v", bass.Contribution)
	}
	if trout.Contribution >= 0 {
		t.Errorf("trout in 24°C should be negative, got %v", trout.Contribution)
	}
}

func TestThermalFactor_TempUnits(t *testing.T) {
	win := Inputs{Thermal: &Thermal{WaterTempC: 20, SpeciesName: "Largemouth Bass", OptLo: 20, OptHi: 27, TolLo: 15, TolHi: 32}}

	win.TempUnit = Celsius
	c := factorByName(Score(win).Factors, "water_temp")
	win.TempUnit = Fahrenheit
	f := factorByName(Score(win).Factors, "water_temp")

	if c == nil || f == nil {
		t.Fatal("expected water_temp factor")
	}
	if !contains(c.Reason, "20°C") {
		t.Errorf("celsius reason should show 20°C: %q", c.Reason)
	}
	if !contains(f.Reason, "68°F") { // 20°C == 68°F
		t.Errorf("fahrenheit reason should show 68°F: %q", f.Reason)
	}
	if c.Contribution != f.Contribution {
		t.Errorf("unit must not change the score: %v vs %v", c.Contribution, f.Contribution)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func factorByName(fs []Factor, name string) *Factor {
	for i := range fs {
		if fs[i].Name == name {
			return &fs[i]
		}
	}
	return nil
}

func TestScore_NeverBelowZeroOrAbove100(t *testing.T) {
	hi := Score(Inputs{DaysSinceCatchablePlant: ptrI(0), PressureTendency: ptrF(-2), WindMps: ptrF(3), CloudPct: ptrF(100)})
	if hi.Score > 100 {
		t.Fatalf("score exceeded 100: %v", hi.Score)
	}
	lo := Score(Inputs{DaysSinceCatchablePlant: ptrI(999), PressureTendency: ptrF(5), WindMps: ptrF(20), CloudPct: ptrF(0)})
	if lo.Score < 0 {
		t.Fatalf("score below 0: %v", lo.Score)
	}
}
