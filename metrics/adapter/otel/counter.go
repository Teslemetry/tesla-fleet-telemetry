package otel

import (
	"context"

	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
	"go.opentelemetry.io/otel/metric"
)

// Counter for OpenTelemetry
type Counter struct {
	counter metric.Int64Counter
}

// Add to the Counter
func (c *Counter) Add(n int64, labels adapter.Labels) {
	if c.counter == nil {
		return
	}
	attrs := labelsToAttributes(labels)
	c.counter.Add(context.Background(), n, metric.WithAttributes(attrs...))
}

// Inc the Counter
func (c *Counter) Inc(labels adapter.Labels) {
	if c.counter == nil {
		return
	}
	attrs := labelsToAttributes(labels)
	c.counter.Add(context.Background(), 1, metric.WithAttributes(attrs...))
}
