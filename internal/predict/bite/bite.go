// Package bite is the v1 bite-likelihood predictor: a transparent additive
// heuristic. Each factor emits a signed contribution AND a human-readable
// reason, so the "why" IS the computation. It evolves to gradient-boosted trees
// once catch-log volume justifies it — but the interface (Inputs -> Result)
// stays.
package bite

import (
	"fmt"
	"math"
	"strings"
)

// baseScore is the neutral starting point before factors adjust it.
const baseScore = 50.0

// TempUnit selects how temperatures are rendered in factor reasons. The score
// itself is unit-agnostic; this is presentation only.
type TempUnit int

const (
	Celsius TempUnit = iota // zero value
	Fahrenheit
)

// ParseTempUnit reads a units string; unknown values fall back to Celsius (the
// caller supplies its own default before this is reached).
func ParseTempUnit(s string) TempUnit {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "f", "fahrenheit", "imperial", "us":
		return Fahrenheit
	default:
		return Celsius
	}
}

// ConvertTemp converts a Celsius value to the given unit's numeric value.
func ConvertTemp(c float64, u TempUnit) float64 {
	if u == Fahrenheit {
		return c*9/5 + 32
	}
	return c
}

func (u TempUnit) label() string {
	if u == Fahrenheit {
		return "°F"
	}
	return "°C"
}

func formatTemp(c float64, u TempUnit) string {
	return fmt.Sprintf("%.0f%s", ConvertTemp(c, u), u.label())
}

// Factor is one additive term in the score with its rationale.
type Factor struct {
	Name         string  `json:"name"`
	Contribution float64 `json:"contribution"`
	Reason       string  `json:"reason"`
}

// Inputs are the observed/derived signals for one lake at one time. Pointer
// fields are nil when the signal is unavailable (and that factor is skipped).
type Inputs struct {
	DaysSinceCatchablePlant *int // days since the last catchable/legal plant
	AirTempC                *float64
	PressureTendency        *float64 // hPa change over the last 3h
	WindMps                 *float64
	CloudPct                *float64
	Solunar                 *Solunar // sun/moon timing for this instant; nil to skip
	Thermal                 *Thermal // water temp vs the target species' window; nil to skip
	TempUnit                TempUnit // how to render temperatures in reasons
}

// Thermal carries the modeled water temperature and the target species' comfort
// bands (degrees C). The same temp is prime for one species and poor for
// another, so this is where the prediction becomes species-aware.
type Thermal struct {
	WaterTempC  float64
	SpeciesName string
	OptLo       float64
	OptHi       float64
	TolLo       float64
	TolHi       float64
}

// Solunar is the time-of-day signal for one instant: minutes to the nearest
// sun/moon feeding peak, plus moon illumination. Populated from internal/astro.
type Solunar struct {
	MinsToSunEvent  float64 // dawn/dusk
	MinsToMoonMajor float64 // moon overhead/underfoot
	MinsToMoonMinor float64 // moonrise/moonset
	MoonIllum       float64 // 0..1
}

// Result is the score plus the factors that produced it.
type Result struct {
	Score   float64  `json:"score"` // 0..100
	Factors []Factor `json:"factors"`
}

// Score runs every factor and sums the contributions onto the base, clamped to
// [0,100]. Factors with no data are omitted entirely (not zeroed), so the
// reason list reflects only what actually informed the score.
func Score(in Inputs) Result {
	factors := make([]Factor, 0, 6)
	add := func(f *Factor) {
		if f != nil {
			factors = append(factors, *f)
		}
	}

	add(catchablePlantFactor(in.DaysSinceCatchablePlant))
	add(pressureFactor(in.PressureTendency))
	add(windFactor(in.WindMps))
	add(cloudFactor(in.CloudPct))
	add(solunarFactor(in.Solunar))
	add(thermalFactor(in.Thermal, in.TempUnit))

	score := baseScore
	for _, f := range factors {
		score += f.Contribution
	}
	score = math.Max(0, math.Min(100, score))
	return Result{Score: math.Round(score*10) / 10, Factors: factors}
}

// catchablePlantFactor is the dominant trout signal: recently-stocked catchable
// fish are concentrated and eager. Exponential decay with a ~2-week scale.
func catchablePlantFactor(days *int) *Factor {
	if days == nil {
		return &Factor{
			Name:         "catchable_plant",
			Contribution: 0,
			Reason:       "No catchable trout plant on record for this lake",
		}
	}
	d := float64(*days)
	contribution := math.Round(30 * math.Exp(-d/14))

	var reason string
	switch {
	case *days <= 3:
		reason = fmt.Sprintf("Catchable trout stocked %d days ago — fish are concentrated and biting", *days)
	case *days <= 10:
		reason = fmt.Sprintf("Recent catchable plant (%d days ago) — good numbers still present", *days)
	case *days <= 21:
		reason = fmt.Sprintf("Catchable plant %d days ago — fish dispersing but present", *days)
	case *days <= 45:
		reason = fmt.Sprintf("Catchable plant ~%d weeks ago — holdovers remain", *days/7)
	default:
		reason = fmt.Sprintf("Last catchable plant was %d days ago — little stocking boost left", *days)
	}
	return &Factor{Name: "catchable_plant", Contribution: contribution, Reason: reason}
}

// pressureFactor: fish feed on a falling barometer ahead of a front; a rising
// post-front barometer typically slows the bite.
func pressureFactor(tend *float64) *Factor {
	if tend == nil {
		return nil
	}
	t := *tend
	switch {
	case t <= -3:
		return &Factor{"pressure", 3, fmt.Sprintf("Pressure dropping fast (%.1f hPa/3h) — a front is moving in; bite can spike then shut off", t)}
	case t < -0.7:
		return &Factor{"pressure", 10, fmt.Sprintf("Barometer falling (%.1f hPa/3h) — often triggers active feeding", t)}
	case t <= 0.7:
		return &Factor{"pressure", 0, "Barometric pressure steady"}
	default:
		return &Factor{"pressure", -6, fmt.Sprintf("Pressure rising (%.1f hPa/3h) — post-front lull, bite often slows", t)}
	}
}

// windFactor: a light ripple ("walleye chop") helps; dead calm makes fish
// spooky and a gale makes lakes unfishable.
func windFactor(wind *float64) *Factor {
	if wind == nil {
		return nil
	}
	mph := *wind * 2.2369363
	switch {
	case *wind < 1.5:
		return &Factor{"wind", -3, fmt.Sprintf("Dead calm (%.0f mph) — flat water, fish are spooky", mph)}
	case *wind <= 5:
		return &Factor{"wind", 8, fmt.Sprintf("Light chop (%.0f mph) — a ripple breaks up the surface and fish feed", mph)}
	case *wind <= 8:
		return &Factor{"wind", 2, fmt.Sprintf("Moderate wind (%.0f mph) — fishable with some chop", mph)}
	default:
		return &Factor{"wind", -8, fmt.Sprintf("Strong wind (%.0f mph) — tough to fish and boat", mph)}
	}
}

// cloudFactor: overcast lowers light and extends feeding; bluebird skies push
// the bite to dawn/dusk.
func cloudFactor(cloud *float64) *Factor {
	if cloud == nil {
		return nil
	}
	c := *cloud
	switch {
	case c >= 70:
		return &Factor{"cloud", 6, fmt.Sprintf("Overcast (%.0f%% cloud) — low light, fish feed more freely", c)}
	case c >= 30:
		return &Factor{"cloud", 2, fmt.Sprintf("Partly cloudy (%.0f%%) — decent light conditions", c)}
	default:
		return &Factor{"cloud", -2, fmt.Sprintf("Bright and clear (%.0f%% cloud) — bite may concentrate at dawn/dusk", c)}
	}
}

// solunarFactor rewards proximity to feeding peaks: dawn/dusk (the strongest,
// crepuscular), moon major periods (moon overhead/underfoot), and moon minor
// periods (moonrise/set). The moon periods are amplified near a new or full
// moon. Contributions taper linearly to zero at the edge of each window and can
// stack (dawn during a major period is best).
func solunarFactor(s *Solunar) *Factor {
	if s == nil {
		return nil
	}
	// moonForce peaks (1) at new & full moon, ~0 at the quarters.
	moonForce := math.Abs(2*s.MoonIllum - 1)

	var contribution float64
	var reasons []string

	const sunWindow = 75.0 // minutes
	if s.MinsToSunEvent <= sunWindow {
		contribution += 12 * (1 - s.MinsToSunEvent/sunWindow)
		reasons = append(reasons, "prime dawn/dusk feeding window")
	}
	const majorWindow = 60.0
	if s.MinsToMoonMajor <= majorWindow {
		contribution += (5 + 4*moonForce) * (1 - s.MinsToMoonMajor/majorWindow)
		reasons = append(reasons, "major solunar period (moon overhead/underfoot)")
	}
	const minorWindow = 45.0
	if s.MinsToMoonMinor <= minorWindow {
		contribution += (2 + 2*moonForce) * (1 - s.MinsToMoonMinor/minorWindow)
		reasons = append(reasons, "minor solunar period (moonrise/set)")
	}

	if len(reasons) == 0 {
		return &Factor{"solunar", 0, "No solunar feeding peak near this time"}
	}
	reason := strings.ToUpper(reasons[0][:1]) + reasons[0][1:]
	if len(reasons) > 1 {
		reason += "; " + strings.Join(reasons[1:], "; ")
	}
	if moonForce > 0.6 {
		reason += " (near new/full moon)"
	}
	return &Factor{"solunar", math.Round(contribution), reason}
}

// thermalFactor scores the modeled water temperature against the target
// species' comfort bands: prime inside the optimal window, fishable through the
// tolerable band, suppressed outside it. This is the species-aware term — warm
// water rewards bass and penalizes trout.
func thermalFactor(th *Thermal, unit TempUnit) *Factor {
	if th == nil {
		return nil
	}
	w := th.WaterTempC
	sp := th.SpeciesName
	temp := formatTemp(w, unit)
	switch {
	case w >= th.OptLo && w <= th.OptHi:
		return &Factor{"water_temp", 10, fmt.Sprintf("Water ~%s — in the ideal range for %s", temp, sp)}
	case w >= th.TolLo && w <= th.TolHi:
		var d, span float64
		side := "cool"
		if w < th.OptLo {
			d, span = th.OptLo-w, math.Max(th.OptLo-th.TolLo, 0.1)
		} else {
			d, span, side = w-th.OptHi, math.Max(th.TolHi-th.OptHi, 0.1), "warm"
		}
		c := math.Round(6 * (1 - d/span))
		return &Factor{"water_temp", c, fmt.Sprintf("Water ~%s — a bit %s for %s but fishable", temp, side, sp)}
	default:
		side := "cold"
		if w > th.TolHi {
			side = "warm"
		}
		return &Factor{"water_temp", -8, fmt.Sprintf("Water ~%s — too %s for %s, bite suppressed", temp, side, sp)}
	}
}
