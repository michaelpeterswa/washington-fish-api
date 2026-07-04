package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	LogLevel string `env:"LOG_LEVEL" envDefault:"error"`

	// APIPort is the HTTP port for the public API server (wfa-api).
	APIPort int `env:"API_PORT" envDefault:"8080"`

	// TempUnit is the default temperature unit in responses ("f" or "c"); a
	// per-request ?units= overrides it. Defaults to Fahrenheit (US anglers).
	TempUnit string `env:"TEMP_UNIT" envDefault:"f"`

	// DatabaseURL is the pgx-compatible Postgres/PostGIS connection string.
	DatabaseURL string `env:"DATABASE_URL"`

	// Socrata stocking feed (WDFW Fish Plants, dataset 6fex-3r7d).
	StockingURL          string `env:"SOCRATA_STOCKING_URL" envDefault:"https://data.wa.gov/resource/6fex-3r7d.json"`
	SocrataAppToken      string `env:"SOCRATA_APP_TOKEN"`
	StockingLookbackDays int    `env:"STOCKING_LOOKBACK_DAYS" envDefault:"365"`

	// WDFW lowland-lakes crawl (geo_code -> coordinates/acres/elevation).
	WDFWSitemapURL       string `env:"WDFW_SITEMAP_URL" envDefault:"https://wdfw.wa.gov/sitemap.xml"`
	WDFWLakesMax         int    `env:"WDFW_LAKES_MAX" envDefault:"0"` // 0 = all; >0 caps for testing
	WDFWLakesConcurrency int    `env:"WDFW_LAKES_CONCURRENCY" envDefault:"6"`

	// Open-Meteo model forecast. Default is the public API (non-commercial);
	// set OPENMETEO_URL to the self-hosted AGPL server for production.
	OpenMeteoURL     string `env:"OPENMETEO_URL" envDefault:"https://api.open-meteo.com/v1/forecast"`
	WeatherBatchSize int    `env:"WEATHER_BATCH_SIZE" envDefault:"100"` // lakes per Open-Meteo call
	WeatherMaxLakes  int    `env:"WEATHER_MAX_LAKES" envDefault:"0"`    // 0 = all; >0 caps for testing
	// WeatherPastDays is the air-temp history window the water-temp EMA fetches.
	// Small (default 2) because the EMA now continues from the last stored water
	// temp — the window is only a bridge across missed polls. Bigger = a better
	// cold-start spin-up at the cost of much heavier Open-Meteo requests.
	WeatherPastDays int `env:"WEATHER_PAST_DAYS" envDefault:"2"`

	// NWS observation stations — KED conditioning points (positions/elevation).
	NWSStationsURL string `env:"NWS_STATIONS_URL" envDefault:"https://api.weather.gov/stations"`

	// WDFW FishWA ArcGIS service — per-species presence layers (warmwater seed).
	FishWAServiceURL string `env:"FISHWA_SERVICE_URL" envDefault:"https://geodataservices.wdfw.wa.gov/arcgis/rest/services/ApplicationServices/FishWA_2014_AllLakes_PROD/MapServer"`

	MetricsEnabled bool `env:"METRICS_ENABLED" envDefault:"true"`
	MetricsPort    int  `env:"METRICS_PORT" envDefault:"8081"`

	Local bool `env:"LOCAL" envDefault:"false"`

	TracingEnabled    bool    `env:"TRACING_ENABLED" envDefault:"false"`
	TracingSampleRate float64 `env:"TRACING_SAMPLERATE" envDefault:"0.01"`
	TracingService    string  `env:"TRACING_SERVICE" envDefault:"washington-fish-api"`
	TracingVersion    string  `env:"TRACING_VERSION"`
}

func NewConfig() (*Config, error) {
	var cfg Config

	err := env.Parse(&cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &cfg, nil
}
