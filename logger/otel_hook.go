package logrus

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// OTelConfig holds configuration for the OpenTelemetry log exporter
type OTelConfig struct {
	// Endpoint is the OTLP endpoint (e.g., "localhost:4317" for gRPC or "localhost:4318" for HTTP)
	Endpoint string `json:"endpoint,omitempty"`

	// ServiceName is the name of the service for resource identification
	ServiceName string `json:"service_name,omitempty"`

	// Protocol specifies the OTLP protocol: "grpc" or "http"
	Protocol string `json:"protocol,omitempty"`

	// Insecure disables TLS for the connection
	Insecure bool `json:"insecure,omitempty"`
}

// OTelHook is a logrus hook that sends logs to OpenTelemetry
type OTelHook struct {
	loggerProvider *sdklog.LoggerProvider
	otelLogger     log.Logger
}

// NewOTelHook creates a new logrus hook for OpenTelemetry logging
func NewOTelHook(cfg *OTelConfig) (*OTelHook, error) {
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
		res = resource.NewSchemaless(semconv.ServiceName(serviceName))
	}

	// Create exporter based on protocol
	var exporter sdklog.Exporter
	switch protocol {
	case "http":
		opts := []otlploghttp.Option{
			otlploghttp.WithEndpoint(cfg.Endpoint),
		}
		if cfg.Insecure {
			opts = append(opts, otlploghttp.WithInsecure())
		}
		exporter, err = otlploghttp.New(ctx, opts...)
	default: // grpc
		opts := []otlploggrpc.Option{
			otlploggrpc.WithEndpoint(cfg.Endpoint),
		}
		if cfg.Insecure {
			opts = append(opts, otlploggrpc.WithInsecure())
		}
		exporter, err = otlploggrpc.New(ctx, opts...)
	}

	if err != nil {
		return nil, err
	}

	// Create logger provider with batch processor
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)

	// Create logger
	otelLogger := loggerProvider.Logger("fleet-telemetry")

	return &OTelHook{
		loggerProvider: loggerProvider,
		otelLogger:     otelLogger,
	}, nil
}

// Fire is called for each log entry
func (h *OTelHook) Fire(entry *logrus.Entry) error {
	if h.otelLogger == nil {
		return nil
	}

	// Convert logrus level to OTel severity
	severity := logrusLevelToOTelSeverity(entry.Level)

	// Build the log record
	var record log.Record
	record.SetTimestamp(entry.Time)
	record.SetSeverity(severity)
	record.SetSeverityText(entry.Level.String())
	record.SetBody(log.StringValue(entry.Message))

	// Convert logrus fields to OTel attributes
	if len(entry.Data) > 0 {
		attrs := make([]log.KeyValue, 0, len(entry.Data))
		for k, v := range entry.Data {
			attrs = append(attrs, convertToKeyValue(k, v))
		}
		record.AddAttributes(attrs...)
	}

	// Emit the log record
	h.otelLogger.Emit(context.Background(), record)

	return nil
}

// Levels returns the logrus levels this hook is interested in
func (h *OTelHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

// Shutdown gracefully shuts down the logger provider
func (h *OTelHook) Shutdown() error {
	if h.loggerProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.loggerProvider.Shutdown(ctx)
	}
	return nil
}

// logrusLevelToOTelSeverity converts logrus levels to OpenTelemetry severity
func logrusLevelToOTelSeverity(level logrus.Level) log.Severity {
	switch level {
	case logrus.TraceLevel:
		return log.SeverityTrace
	case logrus.DebugLevel:
		return log.SeverityDebug
	case logrus.InfoLevel:
		return log.SeverityInfo
	case logrus.WarnLevel:
		return log.SeverityWarn
	case logrus.ErrorLevel:
		return log.SeverityError
	case logrus.FatalLevel:
		return log.SeverityFatal
	case logrus.PanicLevel:
		return log.SeverityFatal4
	default:
		return log.SeverityInfo
	}
}

// convertToKeyValue converts a logrus field to an OTel KeyValue
func convertToKeyValue(key string, value interface{}) log.KeyValue {
	switch v := value.(type) {
	case string:
		return log.String(key, v)
	case int:
		return log.Int(key, v)
	case int64:
		return log.Int64(key, v)
	case float64:
		return log.Float64(key, v)
	case bool:
		return log.Bool(key, v)
	case error:
		return log.String(key, v.Error())
	default:
		// Fall back to string representation
		return log.String(key, fmt.Sprintf("%v", v))
	}
}
