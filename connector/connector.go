// Package connector configures data connectors: pluggable sources of supplemental
// data an operator can wire up to enhance server behavior, such as checking whether
// a VIN is allowed to connect before a vehicle's websocket is accepted.
package connector

import (
	"fmt"
	"sync"

	"github.com/teslamotors/fleet-telemetry/connector/adapter/file"
	"github.com/teslamotors/fleet-telemetry/connector/adapter/nats"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
)

var (
	serverMetricsRegistry serverMetrics
	serverMetricsOnce     sync.Once
)

type serverMetrics struct {
	configureErrorCount adapter.Counter
	unavailableCount    adapter.Counter
}

// Connector is a data source that can answer capability checks, e.g. vin_allowed.
type Connector interface {
	VinAllowed(vin string) (bool, error)
	Close() error
}

// Config holds settings for every available data connector adapter.
type Config struct {
	File *file.Config `json:"file,omitempty"`
	Nats *nats.Config `json:"nats,omitempty"`
}

// Connectors holds the instantiated adapter for each configured data connector.
type Connectors struct {
	File *file.Connector
	Nats *nats.Connector
}

// Provider routes each capability check to whichever configured connector
// was assigned that capability.
type Provider struct {
	VinAllowedConnector Connector
	Connectors          Connectors

	config Config
	logger *logrus.Logger
}

// NewProvider configures every data connector listed in config and assigns
// capabilities to them. With no connectors configured, capability checks pass through
// (e.g. VinAllowed admits every vin) - the feature ships default-off.
func NewProvider(config Config, metricsCollector metrics.MetricCollector, logger *logrus.Logger) *Provider {
	provider := &Provider{
		logger: logger,
		config: config,
	}

	provider.configure(metricsCollector, logger)
	return provider
}

// VinAllowed reports whether vin may connect. With no vin_allowed capability configured,
// it admits every vin. A configured-but-failed connector is never nil here (see
// configureFailed), so this pass-through only ever means "not configured".
func (c *Provider) VinAllowed(vin string) (bool, error) {
	if c.VinAllowedConnector == nil {
		return true, nil
	}

	return c.VinAllowedConnector.VinAllowed(vin)
}

// Close tears down every configured connector.
func (c *Provider) Close() {
	if c.Connectors.File != nil {
		_ = c.Connectors.File.Close()
	}
	if c.Connectors.Nats != nil {
		_ = c.Connectors.Nats.Close()
	}
}

func (c *Provider) configure(metricsCollector metrics.MetricCollector, logger *logrus.Logger) {
	serverMetricsOnce.Do(func() { registerMetrics(metricsCollector) })

	if c.config.File != nil && len(c.config.File.Capabilities) > 0 {
		connector, err := file.NewConnector(*c.config.File, metricsCollector, logger)
		if err == nil {
			c.Connectors.File = connector
			c.configureConnectorCapabilities(connector, c.config.File.Capabilities)
		} else {
			c.configureFailed("file", err, c.config.File.Capabilities)
		}
	}

	if c.config.Nats != nil && len(c.config.Nats.Capabilities) > 0 {
		connector, err := nats.NewConnector(*c.config.Nats, metricsCollector, logger)
		if err == nil {
			c.Connectors.Nats = connector
			c.configureConnectorCapabilities(connector, c.config.Nats.Capabilities)
		} else {
			c.configureFailed("nats", err, c.config.Nats.Capabilities)
		}
	}
}

// configureFailed assigns a stand-in to a connector's capabilities when it
// couldn't be built. Leaving them nil would read as "not configured", admitting
// every vin with nothing logged or counted; the stand-in still fails open, but
// visibly on every check.
func (c *Provider) configureFailed(name string, err error, capabilities []string) {
	serverMetricsRegistry.configureErrorCount.Inc(adapter.Labels{"connector": name})
	c.logger.ErrorLog("data_connector_configure_error", err, logrus.LogInfo{"connector": name, "capabilities": capabilities})
	c.configureConnectorCapabilities(&unavailableConnector{name: name, err: err, logger: c.logger}, capabilities)
}

func (c *Provider) configureConnectorCapabilities(connector Connector, capabilities []string) {
	for _, capability := range capabilities {
		c.setCapabilityByName(capability, connector)
	}
}

func (c *Provider) setCapabilityByName(name string, connector Connector) {
	switch name {
	case "vin_allowed":
		if c.VinAllowedConnector != nil {
			c.logger.Log(logrus.WARN, "vin_allowed capability specified multiple times", logrus.LogInfo{})
		}
		c.VinAllowedConnector = connector
	default:
		c.logger.Log(logrus.WARN, fmt.Sprintf("unknown capability %s", name), logrus.LogInfo{})
	}
}

// unavailableConnector stands in for a configured connector that failed to
// construct: every check fails open, logged and counted.
type unavailableConnector struct {
	name   string
	err    error
	logger *logrus.Logger
}

func (u *unavailableConnector) VinAllowed(vin string) (bool, error) {
	serverMetricsRegistry.unavailableCount.Inc(adapter.Labels{"connector": u.name})
	u.logger.ErrorLog("data_connector_unavailable_fail_open", u.err, logrus.LogInfo{"connector": u.name, "vin": vin})
	return true, fmt.Errorf("data connector %s unavailable: %w", u.name, u.err)
}

func (u *unavailableConnector) Close() error {
	return nil
}

func registerMetrics(metricsCollector metrics.MetricCollector) {
	serverMetricsRegistry.configureErrorCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "data_connector_configure_error_count",
		Help:   "The number of configured data connectors that failed to construct.",
		Labels: []string{"connector"},
	})

	serverMetricsRegistry.unavailableCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "data_connector_unavailable_fail_open_count",
		Help:   "The number of vehicles admitted because their capability's configured data connector failed to construct (fail-open).",
		Labels: []string{"connector"},
	})
}
