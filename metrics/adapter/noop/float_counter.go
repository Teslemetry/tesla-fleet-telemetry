package noop

import (
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
)

// FloatCounter for noop
type FloatCounter struct {
}

// Add (noop)
func (c *FloatCounter) Add(_ float64, _ adapter.Labels) {
}
