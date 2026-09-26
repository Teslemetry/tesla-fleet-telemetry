package connector

import "github.com/teslamotors/fleet-telemetry/metrics/adapter"

// SwapMetricsForTest points the package's counters at the given ones and
// returns a func restoring the originals, since registration is process-global
// and one-shot.
func SwapMetricsForTest(configureErrorCount, unavailableCount adapter.Counter) func() {
	previous := serverMetricsRegistry
	serverMetricsRegistry.configureErrorCount = configureErrorCount
	serverMetricsRegistry.unavailableCount = unavailableCount
	return func() { serverMetricsRegistry = previous }
}
