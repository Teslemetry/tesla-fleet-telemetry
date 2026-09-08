package prometheus

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
)

// FloatCounter for Prometheus
type FloatCounter struct {
	counter *prometheus.CounterVec
}

// Add to the counter
func (c *FloatCounter) Add(n float64, labels adapter.Labels) {
	l := make(prometheus.Labels, len(labels))
	for name, value := range labels {
		l[sanitizeName(name)] = value
	}
	c.counter.With(l).Add(n)
}

// sanitizeName maps a metric or label name onto Prometheus' character set. Dotted OpenTelemetry
// names are legal upstream but would make prometheus.MustRegister panic on an invalid descriptor.
func sanitizeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == ':':
			return r
		default:
			return '_'
		}
	}, name)
}
