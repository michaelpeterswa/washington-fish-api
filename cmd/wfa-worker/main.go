// Command wfa-worker runs ingestion jobs. It is invoked as a k8s CronJob with
// the job name as the first argument, e.g. `wfa-worker stocking`. Each job is a
// one-shot: run, exit. No in-process scheduler.
//
// Jobs are implemented in later build phases (internal/ingest/*); phase 1 wires
// dispatch and config/telemetry plumbing.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sort"

	"github.com/michaelpeterswa/washington-fish-api/internal/config"
	"github.com/michaelpeterswa/washington-fish-api/internal/ingest/fishwa"
	"github.com/michaelpeterswa/washington-fish-api/internal/ingest/stations"
	"github.com/michaelpeterswa/washington-fish-api/internal/ingest/stocking"
	"github.com/michaelpeterswa/washington-fish-api/internal/ingest/wdfwlakes"
	"github.com/michaelpeterswa/washington-fish-api/internal/ingest/weather"
	"github.com/michaelpeterswa/washington-fish-api/internal/logging"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
	"github.com/michaelpeterswa/washington-fish-api/internal/telemetry"
)

// job is a single ingestion task. It receives an already-connected store.
type job func(ctx context.Context, c *config.Config, st *store.Store, logger *slog.Logger) error

// jobs is the dispatch table. Register implementations here as phases land.
var jobs = map[string]job{
	"migrate":  migrateJob,
	"stocking": stocking.Poll,
	"lakes":    wdfwlakes.Load,
	"weather":  weather.Poll,
	"stations": stations.Load,
	"fishwa":   fishwa.Load,
	// "nhd":      nhd.Load,       // phase 2b polygon overlay
}

// migrateJob applies pending database migrations.
func migrateJob(_ context.Context, c *config.Config, _ *store.Store, _ *slog.Logger) error {
	return store.Migrate(c.DatabaseURL)
}

func main() {
	if err := run(); err != nil {
		slog.Error("job failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: wfa-worker <job>; known jobs: %v", knownJobs())
	}
	name := os.Args[1]

	j, ok := jobs[name]
	if !ok {
		return fmt.Errorf("unknown job %q; known jobs: %v", name, knownJobs())
	}

	c, err := config.NewConfig()
	if err != nil {
		return fmt.Errorf("could not create config: %w", err)
	}

	slogLevel, err := logging.LogLevelToSlogLevel(c.LogLevel)
	if err != nil {
		log.Fatalf("could not convert log level: %s", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel}))
	slog.SetDefault(logger)

	ctx := context.Background()

	shutdown, err := telemetry.Init(ctx, c)
	if err != nil {
		return fmt.Errorf("could not init telemetry: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	st, err := store.New(ctx, c.DatabaseURL)
	if err != nil {
		return fmt.Errorf("could not connect store: %w", err)
	}
	defer st.Close()

	logger.Info("running job", slog.String("job", name))
	if err := j(ctx, c, st, logger); err != nil {
		return fmt.Errorf("job %q: %w", name, err)
	}
	logger.Info("job complete", slog.String("job", name))
	return nil
}

func knownJobs() []string {
	names := make([]string, 0, len(jobs))
	for n := range jobs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
