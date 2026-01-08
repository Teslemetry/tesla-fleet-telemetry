package otel

import (
	"context"
	"sync"

	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Gauge for OpenTelemetry using observable gauge for proper Set support
type Gauge struct {
	mu     sync.RWMutex
	values map[string]int64 // key is serialized label set
	labels map[string][]attribute.KeyValue
}

// NewGauge creates a new gauge and registers it with the meter
func NewGauge(meter metric.Meter, name, help string) *Gauge {
	g := &Gauge{
		values: make(map[string]int64),
		labels: make(map[string][]attribute.KeyValue),
	}

	// Register an observable gauge with callback
	_, _ = meter.Int64ObservableGauge(
		name,
		metric.WithDescription(help),
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
			g.mu.RLock()
			defer g.mu.RUnlock()
			for key, value := range g.values {
				attrs := g.labels[key]
				observer.Observe(value, metric.WithAttributes(attrs...))
			}
			return nil
		}),
	)

	return g
}

// labelsKey creates a unique key for a label set
func labelsKey(labels adapter.Labels) string {
	// Simple serialization - for more complex cases, consider sorted keys
	key := ""
	for k, v := range labels {
		key += k + "=" + v + ";"
	}
	return key
}

// Add to the Gauge
func (g *Gauge) Add(n int64, labels adapter.Labels) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := labelsKey(labels)
	g.values[key] += n
	if _, exists := g.labels[key]; !exists {
		g.labels[key] = labelsToAttributes(labels)
	}
}

// Sub from the Gauge
func (g *Gauge) Sub(n int64, labels adapter.Labels) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := labelsKey(labels)
	g.values[key] -= n
	if _, exists := g.labels[key]; !exists {
		g.labels[key] = labelsToAttributes(labels)
	}
}

// Inc the Gauge
func (g *Gauge) Inc(labels adapter.Labels) {
	g.Add(1, labels)
}

// Set the Gauge to an absolute value
func (g *Gauge) Set(n int64, labels adapter.Labels) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := labelsKey(labels)
	g.values[key] = n
	g.labels[key] = labelsToAttributes(labels)
}
