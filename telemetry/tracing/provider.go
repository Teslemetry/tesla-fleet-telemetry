package tracing

import (
	"context"
	"time"

	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter/otel"
	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// Provider wraps an OpenTelemetry TracerProvider
type Provider struct {
	tracerProvider *sdktrace.TracerProvider
}

// NewProvider creates an OTLP trace exporter and TracerProvider, sets it as the global provider
func NewProvider(cfg *otel.Config, logger *logrus.Logger) (*Provider, error) {
	ctx := context.Background()

	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "fleet-telemetry"
	}

	protocol := cfg.Protocol
	if protocol == "" {
		protocol = "grpc"
	}

	sampleRate := cfg.TraceSampleRate
	if sampleRate <= 0 {
		sampleRate = 1.0
	}

	// Create resource with explicit service name
	res, err := resource.New(
		ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
		resource.WithProcessRuntimeDescription(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		logger.ErrorLog("otel_trace_resource_creation_failed", err, nil)
		res = resource.NewSchemaless(semconv.ServiceName(serviceName))
	}

	// Create exporter based on protocol
	var exporter sdktrace.SpanExporter
	switch protocol {
	case "http":
		opts := []otlptracehttp.Option{
			otlptracehttp.WithEndpoint(cfg.Endpoint),
		}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		exporter, err = otlptracehttp.New(ctx, opts...)
	default: // grpc
		opts := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(cfg.Endpoint),
		}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		exporter, err = otlptracegrpc.New(ctx, opts...)
	}

	if err != nil {
		return nil, err
	}

	// Create tracer provider with batch span processor and ratio-based sampler
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(sampleRate)),
	)

	// Set as global trace provider
	otelapi.SetTracerProvider(tracerProvider)

	logger.ActivityLog("otel_tracing_enabled", logrus.LogInfo{
		"endpoint":     cfg.Endpoint,
		"protocol":     protocol,
		"service_name": serviceName,
		"sample_rate":  sampleRate,
	})

	return &Provider{
		tracerProvider: tracerProvider,
	}, nil
}

// Shutdown flushes and shuts down the trace provider
func (p *Provider) Shutdown() error {
	if p.tracerProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return p.tracerProvider.Shutdown(ctx)
	}
	return nil
}

