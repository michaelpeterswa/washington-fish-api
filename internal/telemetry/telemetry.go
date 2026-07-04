// Package telemetry centralizes the ootel/OpenTelemetry bootstrap so both the
// wfa-api server and the wfa-worker jobs initialize metrics and tracing the
// same way.
package telemetry

import (
	"context"
	"fmt"
	"time"

	"alpineworks.io/ootel"
	"github.com/michaelpeterswa/washington-fish-api/internal/config"
	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
)

// Init wires up ootel metrics + tracing plus runtime/host instrumentation and
// returns a shutdown func to be deferred by the caller.
func Init(ctx context.Context, c *config.Config) (func(context.Context) error, error) {
	exporterType := ootel.ExporterTypePrometheus
	if c.Local {
		exporterType = ootel.ExporterTypeOTLPGRPC
	}

	ootelClient := ootel.NewOotelClient(
		ootel.WithMetricConfig(
			ootel.NewMetricConfig(
				c.MetricsEnabled,
				exporterType,
				c.MetricsPort,
			),
		),
		ootel.WithTraceConfig(
			ootel.NewTraceConfig(
				c.TracingEnabled,
				c.TracingSampleRate,
				c.TracingService,
				c.TracingVersion,
			),
		),
	)

	shutdown, err := ootelClient.Init(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not init ootel client: %w", err)
	}

	if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(5 * time.Second)); err != nil {
		return nil, fmt.Errorf("could not start runtime metrics: %w", err)
	}

	if err := host.Start(); err != nil {
		return nil, fmt.Errorf("could not start host metrics: %w", err)
	}

	return shutdown, nil
}
