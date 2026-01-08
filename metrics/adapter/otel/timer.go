package otel

import (
	"context"

	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
	"go.opentelemetry.io/otel/metric"
)

// Timer for OpenTelemetry using histogram
type Timer struct {
	histogram metric.Int64Histogram
}

// Observe records a new timing
func (t *Timer) Observe(n int64, labels adapter.Labels) {
	if t.histogram == nil {
		return
	}
	attrs := labelsToAttributes(labels)
	t.histogram.Record(context.Background(), n, metric.WithAttributes(attrs...))
}
