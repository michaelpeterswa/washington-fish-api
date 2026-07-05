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
	Solunar                 *Solunar     // sun/moon timing for this instant; nil to skip
	Thermal                 *Thermal     // water temp vs the target species' window; nil to skip
	Morphometry             *Morphometry // intrinsic lake shape; nil to skip
	TempUnit                TempUnit     // how to render temperatures in reasons
}

// Morphometry carries a lake's intrinsic physical shape. It varies lake-to-lake
// even when weather and stocking are identical, so it breaks up the flat scores
// that clustered, unstocked lakes (e.g. alpine waters sharing a weather grid
// cell) would otherwise share. All fields are nil when unknown.
type Morphometry struct {
	AreaM2     *float64
	DepthMeanM *float64
	ElevM      *float64
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
	factors := make([]Factor, 0, 7)
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
	add(morphometryFactor(in.Morphometry))

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
	// Continuous exponential decay (~2-week scale) — NOT rounded, so every
	// distinct recency yields a distinct contribution instead of snapping to a
	// bucket.
	contribution := 30 * math.Exp(-d/14)

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
// post-front barometer typically slows the bite. A smooth odd response (tanh)
// gives 0 at steady, saturates positive as pressure falls and negative as it
// rises; a mild easing term captures the "spike then shut off" of a violent
// drop. Continuous, so every distinct tendency shifts the score.
func pressureFactor(tend *float64) *Factor {
	if tend == nil {
		return nil
	}
	t := *tend
	drop := -t // positive when the barometer is falling
	main := 8.0 * math.Tanh(drop/2.0)
	easing := math.Max(-6.0, -5.0*math.Max(0, drop-3.0)/3.0)
	contribution := main + easing

	var reason string
	switch {
	case drop >= 3:
		reason = fmt.Sprintf("Pressure dropping fast (%.1f hPa/3h) — a front is moving in; bite can spike then shut off", t)
	case drop > 0.7:
		reason = fmt.Sprintf("Barometer falling (%.1f hPa/3h) — often triggers active feeding", t)
	case drop >= -0.7:
		reason = "Barometric pressure steady"
	default:
		reason = fmt.Sprintf("Pressure rising (%.1f hPa/3h) — post-front lull, bite often slows", t)
	}
	return &Factor{"pressure", contribution, reason}
}

// windFactor: a light ripple ("walleye chop") helps; dead calm makes fish
// spooky and a gale makes lakes unfishable. A Gaussian bump peaking in the chop
// band, a calm penalty near flat water, and a penalty that grows with strong
// wind — all continuous, so a 3 mph and a 5 mph breeze no longer score alike.
func windFactor(wind *float64) *Factor {
	if wind == nil {
		return nil
	}
	w := *wind
	mph := w * 2.2369363
	bump := 8.0 * math.Exp(-math.Pow((w-3.5)/2.6, 2))
	calm := -4.0 * math.Exp(-math.Pow(w/1.6, 2))
	gale := -0.9 * math.Max(0, w-6.0)
	contribution := bump + calm + gale

	var reason string
	switch {
	case w < 1.5:
		reason = fmt.Sprintf("Dead calm (%.0f mph) — flat water, fish are spooky", mph)
	case w <= 5:
		reason = fmt.Sprintf("Light chop (%.0f mph) — a ripple breaks up the surface and fish feed", mph)
	case w <= 8:
		reason = fmt.Sprintf("Moderate wind (%.0f mph) — fishable with some chop", mph)
	default:
		reason = fmt.Sprintf("Strong wind (%.0f mph) — tough to fish and boat", mph)
	}
	return &Factor{"wind", contribution, reason}
}

// cloudFactor: overcast lowers light and extends feeding; bluebird skies push
// the bite to dawn/dusk. A linear ramp from clear (-2) to overcast (+6),
// continuous so partial cover grades smoothly instead of snapping at 30/70%.
func cloudFactor(cloud *float64) *Factor {
	if cloud == nil {
		return nil
	}
	c := *cloud
	contribution := -2.0 + 8.0*(c/100.0)

	var reason string
	switch {
	case c >= 70:
		reason = fmt.Sprintf("Overcast (%.0f%% cloud) — low light, fish feed more freely", c)
	case c >= 30:
		reason = fmt.Sprintf("Partly cloudy (%.0f%%) — decent light conditions", c)
	default:
		reason = fmt.Sprintf("Bright and clear (%.0f%% cloud) — bite may concentrate at dawn/dusk", c)
	}
	return &Factor{"cloud", contribution, reason}
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
	// Not rounded — the linear taper already varies continuously with timing.
	return &Factor{"solunar", contribution, reason}
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

	// Continuous response across the whole temperature range: a gentle interior
	// gradient inside the optimal band (peak +10 at its centre, ~+9 at the
	// edges), then a smooth linear decline through the tolerable margin to 0 at
	// the tolerable edge and on down to a -8 floor beyond it. No flat plateau, so
	// two lakes a couple of degrees apart inside the band still differ.
	var contribution float64
	switch {
	case w >= th.OptLo && w <= th.OptHi:
		mid := (th.OptLo + th.OptHi) / 2
		half := math.Max((th.OptHi-th.OptLo)/2, 0.1)
		contribution = 10.0 - math.Pow((w-mid)/half, 2)
		return &Factor{"water_temp", contribution, fmt.Sprintf("Water ~%s — in the ideal range for %s", temp, sp)}
	default:
		var d, margin float64
		side := "cool"
		if w < th.OptLo {
			d, margin = th.OptLo-w, math.Max(th.OptLo-th.TolLo, 0.1)
		} else {
			d, margin, side = w-th.OptHi, math.Max(th.TolHi-th.OptHi, 0.1), "warm"
		}
		x := d / margin // 0 at the optimal edge, 1 at the tolerable edge
		contribution = math.Max(-8.0, 9.0-9.0*x)
		if w >= th.TolLo && w <= th.TolHi {
			return &Factor{"water_temp", contribution, fmt.Sprintf("Water ~%s — a bit %s for %s but fishable", temp, side, sp)}
		}
		if side == "cool" {
			side = "cold"
		}
		return &Factor{"water_temp", contribution, fmt.Sprintf("Water ~%s — too %s for %s, bite suppressed", temp, side, sp)}
	}
}

// morphometryFactor rewards a lake's intrinsic shape, independent of weather and
// stocking. It is the term that breaks up clustered scores: neighbouring
// unstocked lakes sharing a weather grid cell still differ in area, depth, and
// elevation, so they no longer collapse onto one number. Modest amplitude
// (roughly ±5) — it differentiates without overriding the real predictors.
func morphometryFactor(m *Morphometry) *Factor {
	if m == nil || (m.AreaM2 == nil && m.DepthMeanM == nil && m.ElevM == nil) {
		return nil
	}
	var contribution float64
	var bits []string

	if m.AreaM2 != nil {
		// Productivity peaks for small-to-mid lakes (~20 ha of littoral habitat),
		// tapering for tiny ponds and large, dispersed waters. Bell in log-area.
		ha := *m.AreaM2 / 10000.0
		lp := math.Log10(math.Max(ha, 0.1))
		contribution += 3.0 * math.Exp(-math.Pow((lp-math.Log10(20.0))/1.1, 2))
		switch {
		case ha < 4:
			bits = append(bits, "small water")
		case ha > 400:
			bits = append(bits, "large open water")
		default:
			bits = append(bits, "productive mid-size water")
		}
	}
	if m.DepthMeanM != nil {
		// A moderate mean depth (~5 m) balances thermal refuge and productivity;
		// very shallow (winterkill-prone) and very deep (unproductive) score lower.
		d := *m.DepthMeanM
		contribution += 2.0*math.Exp(-math.Pow((d-5.0)/6.0, 2)) - 0.5
		switch {
		case d < 2.5:
			bits = append(bits, "shallow")
		case d > 15:
			bits = append(bits, "deep")
		}
	}
	if m.ElevM != nil {
		// Higher lakes have a shorter open-water season and colder water — a mild
		// seasonal drag until alpine ice-off is modeled explicitly. ~0 in the
		// lowlands, easing to about -3 by 2000 m.
		e := *m.ElevM
		contribution += -3.0 * math.Min(1.0, math.Max(0, (e-300.0)/1700.0))
		if e > 1200 {
			bits = append(bits, "high elevation")
		}
	}

	reason := "Lake character neutral"
	if len(bits) > 0 {
		reason = strings.ToUpper(bits[0][:1]) + bits[0][1:]
		if len(bits) > 1 {
			reason += ", " + strings.Join(bits[1:], ", ")
		}
	}
	return &Factor{"morphometry", contribution, reason}
}
