package tracing

import (
	"context"
	"time"

	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter/otel"
	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// defaultTraceSampleRate is used when config leaves TraceSampleRate unset (<=0).
// The NATS producer now emits a PRODUCER span per publish (~5.5M spans/day
// fleet-wide at ratio 1.0) so the default is a head sample, not "trace
// everything" - set trace_sample_rate explicitly to override.
const defaultTraceSampleRate = 0.01

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
		sampleRate = defaultTraceSampleRate
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

	// Create tracer provider with batch span processor and ratio-based sampler.
	// ParentBased so a sampled parent (e.g. a future upstream traceparent, or a
	// chunked session span) always keeps its children even when the ratio would
	// otherwise drop them; today's spans are all roots so this is a no-op for them.
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRate))),
	)

	// Set as global trace provider
	otelapi.SetTracerProvider(tracerProvider)

	// Set as global propagator so producers (e.g. NATS) can inject W3C trace
	// context into outgoing message headers for downstream consumers to join
	otelapi.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

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
