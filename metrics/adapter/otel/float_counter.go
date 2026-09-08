package otel

import (
	"context"

	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
	"go.opentelemetry.io/otel/metric"
)

// FloatCounter for OpenTelemetry
type FloatCounter struct {
	counter metric.Float64Counter
}

// Add to the FloatCounter
func (c *FloatCounter) Add(n float64, labels adapter.Labels) {
	if c.counter == nil {
		return
	}
	attrs := labelsToAttributes(labels)
	c.counter.Add(context.Background(), n, metric.WithAttributes(attrs...))
}
