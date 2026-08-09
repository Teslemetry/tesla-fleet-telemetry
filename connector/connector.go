// Package connector configures data connectors: pluggable sources of supplemental
// data an operator can wire up to enhance server behavior, such as checking whether
// a VIN is allowed to connect before a vehicle's websocket is accepted.
package connector

import (
	"fmt"

	"github.com/teslamotors/fleet-telemetry/connector/adapter/file"
	"github.com/teslamotors/fleet-telemetry/connector/adapter/nats"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
)

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
// it admits every vin.
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
	if c.config.File != nil && len(c.config.File.Capabilities) > 0 {
		connector, err := file.NewConnector(*c.config.File, metricsCollector, logger)
		if err == nil {
			c.Connectors.File = connector
			c.configureConnectorCapabilities(connector, c.config.File.Capabilities)
		} else {
			logger.ErrorLog("connector_provider_configure_sources_file_error", err, nil)
		}
	}

	if c.config.Nats != nil && len(c.config.Nats.Capabilities) > 0 {
		connector, err := nats.NewConnector(*c.config.Nats, metricsCollector, logger)
		if err == nil {
			c.Connectors.Nats = connector
			c.configureConnectorCapabilities(connector, c.config.Nats.Capabilities)
		} else {
			logger.ErrorLog("connector_provider_configure_sources_nats_error", err, nil)
		}
	}
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
