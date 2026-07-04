// Command wfa-api is the public HTTP server for the Washington fish-prediction
// API. It serves the /v1 surface plus ops probes, and exposes metrics on the
// separate metrics port via ootel.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/michaelpeterswa/washington-fish-api/internal/api"
	"github.com/michaelpeterswa/washington-fish-api/internal/config"
	"github.com/michaelpeterswa/washington-fish-api/internal/logging"
	"github.com/michaelpeterswa/washington-fish-api/internal/predict"
	"github.com/michaelpeterswa/washington-fish-api/internal/predict/bite"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
	"github.com/michaelpeterswa/washington-fish-api/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
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

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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

	handler := api.NewRouter(&api.Server{
		Store:       st,
		Predict:     predict.New(st),
		Logger:      logger,
		DefaultUnit: bite.ParseTempUnit(c.TempUnit),
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", c.APIPort),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("starting api server", slog.Int("port", c.APIPort))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	return nil
}
