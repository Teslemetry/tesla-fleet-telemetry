package statsd

import (
	sd "github.com/smira/go-statsd"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
)

// FloatCounter for Statsd
type FloatCounter struct {
	client *sd.Client
	name   string
}

// Add to the FloatCounter
func (s *FloatCounter) Add(n float64, labels adapter.Labels) {
	tags := getTags(labels)
	s.client.FIncr(s.name, n, tags...)
}
