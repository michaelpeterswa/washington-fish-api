package wdfwlakes

import (
	"context"
	"log/slog"
	"sync"

	"github.com/michaelpeterswa/washington-fish-api/internal/config"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
)

const (
	acreM2       = 4046.8564224 // 1 acre in square metres
	feetPerMetre = 3.280839895
)

// Load crawls WDFW lowland-lake pages and upserts their geo_code + centroid +
// acreage + elevation + canonical name into lakes (exact geo_code join to the
// stocking-seeded rows). Idempotent; safe to re-run periodically.
func Load(ctx context.Context, c *config.Config, st *store.Store, logger *slog.Logger) error {
	client := NewClient(c.WDFWSitemapURL, logger)

	urls, err := client.LakeURLs(ctx)
	if err != nil {
		return err
	}
	if c.WDFWLakesMax > 0 && len(urls) > c.WDFWLakesMax {
		urls = urls[:c.WDFWLakesMax]
	}
	logger.InfoContext(ctx, "enumerated wdfw lake pages", slog.Int("pages", len(urls)))

	conc := c.WDFWLakesConcurrency
	if conc <= 0 {
		conc = 6
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var upserted, skipped, failed int
	var firstErr error

	for _, u := range urls {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()

			p, err := client.FetchLake(ctx, u)
			if err != nil {
				mu.Lock()
				failed++
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if p == nil {
				mu.Lock()
				skipped++
				mu.Unlock()
				return
			}

			if err := st.UpsertLakeGeometry(ctx, toLakeGeom(p)); err != nil {
				mu.Lock()
				failed++
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			upserted++
			mu.Unlock()
		}(u)
	}
	wg.Wait()

	// Fill estimated depth for newly-located lakes (feeds the water-temp proxy).
	depths, err := st.EstimateLakeDepths(ctx)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "wdfw lakes load complete",
		slog.Int("upserted", upserted),
		slog.Int("skipped", skipped),
		slog.Int("failed", failed),
		slog.Int64("depths_estimated", depths))

	// Tolerate a handful of page failures; only surface an error if nothing landed.
	if upserted == 0 && firstErr != nil {
		return firstErr
	}
	return nil
}

func toLakeGeom(p *LakePage) store.LakeGeom {
	g := store.LakeGeom{
		GeoCode:  p.GeoCode,
		Name:     p.Name,
		Lon:      p.Lon,
		Lat:      p.Lat,
		LakeType: p.LakeType,
	}
	if p.County != "" {
		county := p.County
		g.County = &county
	}
	if p.Acres > 0 {
		area := p.Acres * acreM2
		g.AreaM2 = &area
	}
	if p.ElevFt > 0 {
		elev := p.ElevFt / feetPerMetre
		g.ElevM = &elev
	}
	return g
}
