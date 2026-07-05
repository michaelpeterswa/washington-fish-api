// Package predict assembles bite predictions from stored data: it pulls a
// lake's stocking recency + weather, runs the bite heuristic per horizon, and
// attaches a confidence field. Confidence is provisional in v1 (data-coverage
// based); it becomes KED-variance-driven once phase 3b lands.
package predict

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/michaelpeterswa/washington-fish-api/internal/astro"
	"github.com/michaelpeterswa/washington-fish-api/internal/predict/bite"
	"github.com/michaelpeterswa/washington-fish-api/internal/predict/species"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
)

type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service { return &Service{st: st} }

// NowcastPrediction is the current bite estimate with its full reasoning.
type NowcastPrediction struct {
	ValidAt    time.Time
	Score      float64
	Confidence float64
	WaterTempC *float64 // modeled water temperature (canonical °C), nil if unknown
	Factors    []bite.Factor
}

// ForecastPoint is a compact per-horizon score for the timeline.
type ForecastPoint struct {
	ValidAt    time.Time
	HorizonH   int32
	Score      float64
	Confidence float64
}

// LakePrediction is the full prediction payload for one lake.
type LakePrediction struct {
	TargetSpecies           string // species the thermal factor was scored for
	DaysSinceCatchablePlant *int
	LastPlantSpecies        string
	Nowcast                 *NowcastPrediction
	Forecast                []ForecastPoint
}

// RankedLake is one scored candidate in a ranking.
type RankedLake struct {
	Lake       store.LakeSummary
	Score      float64
	Confidence float64
	TopReason  string
}

// LakePrediction builds the nowcast + 72h forecast bite series for a lake,
// scored for targetSpecies (empty -> the lake's primary present species).
func (s *Service) LakePrediction(ctx context.Context, lakeID int64, targetSpecies string, unit bite.TempUnit) (*LakePrediction, error) {
	lake, err := s.st.LakeByID(ctx, lakeID)
	if err != nil {
		return nil, err
	}

	plantDate, lastSp, err := s.lastCatchablePlant(ctx, lakeID)
	if err != nil {
		return nil, err
	}

	if targetSpecies == "" {
		if sp, err := s.st.Q.PrimarySpecies(ctx, lakeID); err == nil {
			targetSpecies = sp
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	sp := species.Lookup(targetSpecies)

	conds, err := s.st.Q.ConditionsForLake(ctx, lakeID)
	if err != nil {
		return nil, err
	}

	out := &LakePrediction{LastPlantSpecies: lastSp, TargetSpecies: sp.Canonical}
	for _, c := range conds {
		ref := c.ValidAt.Time
		ds := daysSincePlant(plantDate, ref)
		res := bite.Score(bite.Inputs{
			DaysSinceCatchablePlant: ds,
			AirTempC:                c.AirTempC,
			PressureTendency:        c.PressureTendency,
			WindMps:                 c.WindMps,
			CloudPct:                c.CloudPct,
			Solunar:                 solunarFor(lake.Lat, lake.Lon, ref),
			Thermal:                 thermalFor(sp, c.WaterTempC),
			Morphometry:             morphometryFor(lake),
			TempUnit:                unit,
		})

		if c.HorizonH == 0 {
			out.DaysSinceCatchablePlant = ds
			conf := pickConfidence(lake.ConfidenceNowcast, lake.LakeType, ds, c.AirTempC != nil)
			out.Nowcast = &NowcastPrediction{ValidAt: ref, Score: res.Score, Confidence: conf, WaterTempC: c.WaterTempC, Factors: res.Factors}
		} else {
			conf := pickConfidence(lake.ConfidenceForecast, lake.LakeType, ds, c.AirTempC != nil)
			out.Forecast = append(out.Forecast, ForecastPoint{
				ValidAt: ref, HorizonH: c.HorizonH, Score: res.Score, Confidence: conf,
			})
		}
	}
	return out, nil
}

// RankLakes scores every candidate within the given radius and returns them
// sorted by bite score (ties broken by proximity).
func (s *Service) RankLakes(ctx context.Context, p store.RankParams, unit bite.TempUnit) ([]RankedLake, error) {
	rows, err := s.st.RankCandidates(ctx, p)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	// When the query names a species, score every candidate's thermal fit for it;
	// otherwise leave thermal out (generic ranking, no cold/warm bias).
	var thermalSp *species.Species
	if p.Species != "" {
		sp := species.Lookup(p.Species)
		thermalSp = &sp
	}

	ranked := make([]RankedLake, 0, len(rows))
	for _, r := range rows {
		ds := daysSincePlant(r.LastPlantDate, now)
		var thermal *bite.Thermal
		if thermalSp != nil {
			thermal = thermalFor(*thermalSp, r.WaterTempC)
		}
		res := bite.Score(bite.Inputs{
			DaysSinceCatchablePlant: ds,
			AirTempC:                r.AirTempC,
			PressureTendency:        r.PressureTendency,
			WindMps:                 r.WindMps,
			CloudPct:                r.CloudPct,
			Solunar:                 solunarFor(r.Lake.Lat, r.Lake.Lon, now),
			Thermal:                 thermal,
			Morphometry:             morphometryFor(&r.Lake),
			TempUnit:                unit,
		})
		ranked = append(ranked, RankedLake{
			Lake:       r.Lake,
			Score:      res.Score,
			Confidence: pickConfidence(r.Lake.ConfidenceNowcast, r.Lake.LakeType, ds, r.AirTempC != nil),
			TopReason:  topReason(res.Factors),
		})
	}

	// Sort by score; break ties deterministically by proximity, then higher
	// confidence, then lake ID so equal-score lakes present in a stable order.
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		di, dj := ranked[i].Lake.DistanceKm, ranked[j].Lake.DistanceKm
		if di != nil && dj != nil && *di != *dj {
			return *di < *dj
		}
		if ranked[i].Confidence != ranked[j].Confidence {
			return ranked[i].Confidence > ranked[j].Confidence
		}
		return ranked[i].Lake.ID < ranked[j].Lake.ID
	})
	return ranked, nil
}

func (s *Service) lastCatchablePlant(ctx context.Context, lakeID int64) (*time.Time, string, error) {
	lp, err := s.st.Q.LastCatchablePlant(ctx, &lakeID) // stocking_events.lake_id is nullable
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !lp.PlantDate.Valid {
		return nil, lp.Species, nil
	}
	t := lp.PlantDate.Time
	return &t, lp.Species, nil
}

// thermalFor builds the species-aware water-temp input, or nil if no modeled
// water temperature is available.
func thermalFor(sp species.Species, water *float64) *bite.Thermal {
	if water == nil {
		return nil
	}
	return &bite.Thermal{
		WaterTempC: *water, SpeciesName: sp.Canonical,
		OptLo: sp.OptLo, OptHi: sp.OptHi, TolLo: sp.TolLo, TolHi: sp.TolHi,
	}
}

// morphometryFor builds the intrinsic-shape input from a lake summary, or nil
// if the lake has no morphometry at all.
func morphometryFor(l *store.LakeSummary) *bite.Morphometry {
	if l == nil || (l.AreaM2 == nil && l.DepthMeanM == nil && l.ElevM == nil) {
		return nil
	}
	return &bite.Morphometry{AreaM2: l.AreaM2, DepthMeanM: l.DepthMeanM, ElevM: l.ElevM}
}

// solunarFor computes the solunar signal for a lake at instant t, or nil if the
// lake has no located centroid.
func solunarFor(lat, lon *float64, t time.Time) *bite.Solunar {
	if lat == nil || lon == nil {
		return nil
	}
	s := astro.Solunar(*lat, *lon, t)
	return &bite.Solunar{
		MinsToSunEvent:  s.MinsToSunEvent,
		MinsToMoonMajor: s.MinsToMoonMajor,
		MinsToMoonMinor: s.MinsToMoonMinor,
		MoonIllum:       s.MoonIllum,
	}
}

func daysSincePlant(plant *time.Time, ref time.Time) *int {
	if plant == nil {
		return nil
	}
	d := int(ref.Sub(*plant).Hours() / 24)
	if d < 0 {
		d = 0
	}
	return &d
}

// pickConfidence prefers the KED-variance confidence (phase 3b) when the lake
// has been scored against the station network, and falls back to the coarse
// data-coverage estimate otherwise (e.g. before the stations job has run).
func pickConfidence(ked *float64, lakeType *string, daysSince *int, hasWeather bool) float64 {
	if ked != nil {
		return *ked
	}
	return confidenceFor(lakeType, daysSince, hasWeather)
}

// confidenceFor is a coarse fallback confidence from data coverage, used only
// until KED variance is available for a lake.
func confidenceFor(lakeType *string, daysSince *int, hasWeather bool) float64 {
	c := 0.4
	if hasWeather {
		c += 0.25
	}
	if daysSince != nil && *daysSince <= 60 {
		c += 0.15
	}
	if lakeType != nil && *lakeType == "high" {
		c -= 0.2 // alpine dynamics (ice-off) not modeled yet
	}
	return math.Max(0.05, math.Min(0.9, math.Round(c*100)/100))
}

// topReason returns the reason of the most positively-contributing factor.
func topReason(factors []bite.Factor) string {
	best := ""
	bestC := math.Inf(-1)
	for _, f := range factors {
		if f.Contribution > bestC {
			bestC = f.Contribution
			best = f.Reason
		}
	}
	return best
}
