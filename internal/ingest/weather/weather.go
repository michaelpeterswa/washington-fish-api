package weather

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/michaelpeterswa/washington-fish-api/internal/config"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
)

const (
	maxHorizon   = 72            // hours ahead to store
	tendencyLag  = 3 * time.Hour // barometric tendency window
	omTimeLayout = "2006-01-02T15:04"
)

// Poll fetches the Open-Meteo model forecast for every located lake and writes
// nowcast (horizon 0) + hourly forecast (1..72) rows into conditions. Idempotent
// via the (lake_id, valid_at, horizon_h) unique key.
func Poll(ctx context.Context, c *config.Config, st *store.Store, logger *slog.Logger) error {
	lakes, err := st.LakesWithCentroid(ctx)
	if err != nil {
		return err
	}
	if c.WeatherMaxLakes > 0 && len(lakes) > c.WeatherMaxLakes {
		lakes = lakes[:c.WeatherMaxLakes]
	}
	batchSize := c.WeatherBatchSize
	if batchSize <= 0 {
		batchSize = 100
	}

	client := NewClient(c.OpenMeteoURL, c.WeatherPastDays, logger)
	now := time.Now().UTC()

	// Prior water-temp state — the EMA continues from this instead of re-spinning
	// up from a long history each poll (keeps Open-Meteo requests light).
	seeds, err := st.PriorWaterTemps(ctx)
	if err != nil {
		return err
	}

	var lakesDone, rowsWritten int

	for i := 0; i < len(lakes); i += batchSize {
		end := min(i+batchSize, len(lakes))
		batch := lakes[i:end]

		pts := make([]LatLon, len(batch))
		for j, l := range batch {
			pts[j] = LatLon{Lat: l.Lat, Lon: l.Lon}
		}
		fcs, err := client.FetchForecast(ctx, pts)
		if err != nil {
			return err
		}
		if len(fcs) != len(batch) {
			logger.WarnContext(ctx, "open-meteo returned unexpected count",
				slog.Int("want", len(batch)), slog.Int("got", len(fcs)))
			continue
		}

		var conds []store.Condition
		for j, l := range batch {
			var seed *store.WaterSeed
			if s, ok := seeds[l.ID]; ok {
				seed = &s
			}
			conds = append(conds, buildConditions(l.ID, fcs[j], now, l.DepthMeanM, seed)...)
		}
		if err := st.UpsertConditions(ctx, conds); err != nil {
			return err
		}
		lakesDone += len(batch)
		rowsWritten += len(conds)
	}

	// Drop past rows so the table stays bounded to current hour + forecast window.
	pruned, err := st.DeleteStaleConditions(ctx, now.Truncate(time.Hour))
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "weather poll complete",
		slog.Int("lakes", lakesDone),
		slog.Int("condition_rows", rowsWritten),
		slog.Int64("pruned_stale", pruned))
	return nil
}

// buildConditions turns one Open-Meteo forecast into a nowcast row plus one row
// per future hour (up to maxHorizon), with a 3-hour barometric tendency and a
// depth-damped water-temperature proxy.
func buildConditions(lakeID int64, f Forecast, now time.Time, depthM *float64, seed *store.WaterSeed) []store.Condition {
	nowHour := now.Truncate(time.Hour)

	// Index hourly pressure by time for tendency lookups.
	pressureAt := make(map[time.Time]*float64, len(f.Hourly.Time))
	times := make([]time.Time, len(f.Hourly.Time))
	for k, ts := range f.Hourly.Time {
		t, err := time.ParseInLocation(omTimeLayout, ts, time.UTC)
		if err != nil {
			continue
		}
		times[k] = t
		if k < len(f.Hourly.SurfacePressure) {
			pressureAt[t] = f.Hourly.SurfacePressure[k]
		}
	}

	// Water temperature: an exponential moving average of the air-temp series,
	// with a time constant that grows with depth (thermal inertia). Continues
	// from the prior stored value (seed) when available, so only a short bridge
	// of history is needed; otherwise it spins up from the fetched window.
	waterAt := waterTempSeries(times, f.Hourly.TemperatureC, depthM, seed)

	var out []store.Condition

	// Nowcast (horizon 0) from the fresh `current` block, snapped to the top of
	// the hour so re-runs within an hour upsert the same row (not a new one).
	out = append(out, store.Condition{
		LakeID:           lakeID,
		ValidAt:          nowHour,
		HorizonH:         0,
		AirTempC:         f.Current.TemperatureC,
		WaterTempC:       waterAt[nowHour],
		PressureHpa:      f.Current.SurfacePressure,
		PressureTendency: ptrDiff(f.Current.SurfacePressure, pressureAt[nowHour.Add(-tendencyLag)]),
		WindMps:          f.Current.WindMs,
		CloudPct:         f.Current.CloudPct,
	})

	// Forecast rows for future hours.
	for k, t := range times {
		if t.IsZero() || !t.After(nowHour) {
			continue // past hour; nowcast already covers the current hour
		}
		horizon := int32(t.Sub(nowHour) / time.Hour)
		if horizon > maxHorizon {
			break
		}
		out = append(out, store.Condition{
			LakeID:           lakeID,
			ValidAt:          t,
			HorizonH:         horizon,
			AirTempC:         at(f.Hourly.TemperatureC, k),
			WaterTempC:       waterAt[t],
			PressureHpa:      at(f.Hourly.SurfacePressure, k),
			PressureTendency: ptrDiff(at(f.Hourly.SurfacePressure, k), pressureAt[t.Add(-tendencyLag)]),
			WindMps:          at(f.Hourly.WindMs, k),
			CloudPct:         at(f.Hourly.CloudPct, k),
		})
	}
	return out
}

// waterTempSeries models water temperature as an exponential moving average of
// the hourly air-temp series. The time constant grows with depth (deep lakes
// lag and buffer air swings more), so a shallow pond tracks the air closely
// while a deep lake stays cooler through a summer heat spike.
//
// When a seed (the prior poll's water temp + time) is given, the EMA continues
// from it — carrying the accumulated thermal state forward, so only a short
// bridge of history is needed. Without a seed (cold start / new lake) it spins
// up from the start of the fetched window. Returns a map keyed by hour.
func waterTempSeries(times []time.Time, airTemp []*float64, depthM *float64, seed *store.WaterSeed) map[time.Time]*float64 {
	depth := 4.0 // default when unknown
	if depthM != nil && *depthM > 0 {
		depth = *depthM
	}
	// tau (hours): ~1.5 days shallow up to ~12 days deep.
	tauDays := math.Max(1.5, math.Min(12, 1.5+0.6*depth))
	alpha := 1 - math.Exp(-1.0/(tauDays*24)) // per 1-hour step

	out := make(map[time.Time]*float64, len(times))

	// Start index + initial water. With a seed, begin at the first hour >= the
	// seed's time and carry the seed value forward (don't re-advance that hour).
	start := 0
	var water float64
	seeded := false
	if seed != nil {
		water, seeded = seed.Temp, true
		for i, t := range times {
			if !t.IsZero() && !t.Before(seed.At) {
				start = i
				break
			}
		}
		if start < len(times) {
			w := water
			out[times[start]] = &w
			start++
		}
	}

	for i := start; i < len(times); i++ {
		t := times[i]
		if t.IsZero() || i >= len(airTemp) || airTemp[i] == nil {
			if seeded {
				w := water
				out[t] = &w
			}
			continue
		}
		air := *airTemp[i]
		if !seeded {
			water, seeded = air, true
		} else {
			water += alpha * (air - water)
		}
		w := water
		out[t] = &w
	}
	return out
}

func at(s []*float64, i int) *float64 {
	if i < 0 || i >= len(s) {
		return nil
	}
	return s[i]
}

func ptrDiff(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	d := *a - *b
	return &d
}
