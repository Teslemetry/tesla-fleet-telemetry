package otel

import (
	"context"
	"time"

	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// Config holds configuration for the OpenTelemetry collector
type Config struct {
	// Endpoint is the OTLP endpoint (e.g., "localhost:4317" for gRPC or "localhost:4318" for HTTP)
	Endpoint string `json:"endpoint,omitempty"`

	// ServiceName is the name of the service for resource identification
	ServiceName string `json:"service_name,omitempty"`

	// Protocol specifies the OTLP protocol: "grpc" or "http"
	Protocol string `json:"protocol,omitempty"`

	// ExportInterval is the interval for metric exports in milliseconds (default: 60000)
	ExportInterval int `json:"export_interval,omitempty"`

	// Insecure disables TLS for the connection
	Insecure bool `json:"insecure,omitempty"`
}

// Collector is an OpenTelemetry based implementation of the stats collector
type Collector struct {
	meter         metric.Meter
	meterProvider *sdkmetric.MeterProvider
	logger        *logrus.Logger
}

// NewCollector creates a metric collector which sends data via OpenTelemetry
func NewCollector(cfg *Config, logger *logrus.Logger) *Collector {
	ctx := context.Background()

	// Set defaults
	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "fleet-telemetry"
	}

	protocol := cfg.Protocol
	if protocol == "" {
		protocol = "grpc"
	}

	exportInterval := time.Duration(cfg.ExportInterval) * time.Millisecond
	if cfg.ExportInterval <= 0 {
		exportInterval = 60 * time.Second
	}

	// Create resource with explicit service name
	// Note: We avoid resource.Merge with resource.Default() because the default
	// process detector sets "unknown_service:binary_name" which can override our service name
	res, err := resource.New(
		ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
		resource.WithProcessRuntimeDescription(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		logger.ErrorLog("otel_resource_creation_failed", err, nil)
		res = resource.NewSchemaless(semconv.ServiceName(serviceName))
	}

	// Create exporter based on protocol
	var exporter sdkmetric.Exporter
	switch protocol {
	case "http":
		opts := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpoint(cfg.Endpoint),
		}
		if cfg.Insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		exporter, err = otlpmetrichttp.New(ctx, opts...)
	default: // grpc
		opts := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
		}
		if cfg.Insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}
		exporter, err = otlpmetricgrpc.New(ctx, opts...)
	}

	if err != nil {
		logger.ErrorLog("otel_exporter_creation_failed", err, logrus.LogInfo{"protocol": protocol})
		return nil
	}

	// Create meter provider with periodic reader
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(
				exporter,
				sdkmetric.WithInterval(exportInterval),
			),
		),
	)

	// Set as global provider
	otel.SetMeterProvider(meterProvider)

	// Create meter
	meter := meterProvider.Meter("fleet-telemetry")

	logger.ActivityLog("new_otel_client", logrus.LogInfo{
		"endpoint":        cfg.Endpoint,
		"protocol":        protocol,
		"service_name":    serviceName,
		"export_interval": exportInterval.String(),
	})

	return &Collector{
		meter:         meter,
		meterProvider: meterProvider,
		logger:        logger,
	}
}

// RegisterTimer creates a new timer for OpenTelemetry
func (c *Collector) RegisterTimer(options adapter.CollectorOptions) adapter.Timer {
	histogram, err := c.meter.Int64Histogram(
		options.Name,
		metric.WithDescription(options.Help),
		metric.WithUnit("ms"),
	)
	if err != nil {
		c.logger.ErrorLog("otel_timer_registration_failed", err, logrus.LogInfo{"name": options.Name})
		return &Timer{histogram: nil}
	}
	return &Timer{histogram: histogram}
}

// RegisterCounter creates a new counter for OpenTelemetry
func (c *Collector) RegisterCounter(options adapter.CollectorOptions) adapter.Counter {
	counter, err := c.meter.Int64Counter(
		options.Name,
		metric.WithDescription(options.Help),
	)
	if err != nil {
		c.logger.ErrorLog("otel_counter_registration_failed", err, logrus.LogInfo{"name": options.Name})
		return &Counter{counter: nil}
	}
	return &Counter{counter: counter}
}

// RegisterGauge creates a new gauge for OpenTelemetry
func (c *Collector) RegisterGauge(options adapter.CollectorOptions) adapter.Gauge {
	return NewGauge(c.meter, options.Name, options.Help)
}

// Shutdown gracefully shuts down the OpenTelemetry meter provider
func (c *Collector) Shutdown() {
	if c.meterProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.meterProvider.Shutdown(ctx); err != nil {
			c.logger.ErrorLog("otel_shutdown_failed", err, nil)
		}
	}
}
