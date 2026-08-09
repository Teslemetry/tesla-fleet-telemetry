// Package nats implements a data connector adapter that checks capabilities
// (e.g. vin_allowed) over a NATS request-reply round trip against an
// externally-deployed responder.
package nats

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
)

// vinAllowedSubject is the pinned wire contract's request subject: JSON body
// {"vin":"<vin>"}, JSON reply {"allowed":true|false}.
const vinAllowedSubject = "vin_allowed"

// VinAllowedTimeout bounds the request-reply round trip so a slow or absent
// responder never blocks a vehicle's websocket accept path. Overridable (var,
// not const) so tests can shrink it instead of waiting out the full second.
var VinAllowedTimeout = time.Second

// NatsConnect is replaced in tests to exercise connection-error paths without
// a live NATS server, mirroring datastore/nats's seam of the same name.
var NatsConnect = nats.Connect

var (
	serverMetricsRegistry ServerMetrics
	serverMetricsOnce     sync.Once
)

// ServerMetrics stores metrics reported from this package
type ServerMetrics struct {
	requestErrorCount adapter.Counter
	requestCount      adapter.Counter
	failOpenCount     adapter.Counter
}

// Config for the NATS connector adapter
type Config struct {
	URL          string   `json:"url"`
	Name         string   `json:"name,omitempty"`
	Capabilities []string `json:"capabilities"`
}

// Connector checks capabilities against a NATS request-reply responder over
// its own dedicated connection (see NewConnector for why it doesn't reuse the
// datastore/nats publish producer's connection).
type Connector struct {
	conn   *nats.Conn
	logger *logrus.Logger
}

type vinAllowedRequest struct {
	Vin string `json:"vin"`
}

type vinAllowedResponse struct {
	Allowed bool `json:"allowed"`
}

// NewConnector opens a dedicated NATS connection for data-connector checks.
//
// This intentionally does not reuse datastore/nats.Producer's connection: that
// producer only exists when NATS is configured as a record dispatcher, while
// data_connectors.nats is configured independently and may run without any
// record dispatcher pointed at NATS. Its connection field is also unexported
// with no accessor. Every other connector adapter in this package likewise
// owns an independent client, and keeping short-lived request-reply traffic
// off the fire-and-forget publish connection avoids one workload's backpressure
// affecting the other.
func NewConnector(config Config, metricsCollector metrics.MetricCollector, logger *logrus.Logger) (*Connector, error) {
	registerMetricsOnce(metricsCollector)

	conn, err := NatsConnect(config.URL, nats.Name(config.Name))
	if err != nil {
		return nil, err
	}

	return &Connector{
		conn:   conn,
		logger: logger,
	}, nil
}

// VinAllowed asks the vin_allowed responder whether vin may connect. Any
// failure to get a well-formed reply within the timeout - no responder, a
// timeout, or a malformed body - fails OPEN (admits the vehicle): customer
// telemetry availability outranks enforcement latency here, and cleaning up an
// already-admitted, disallowed vehicle is best-effort.
func (c *Connector) VinAllowed(vin string) (bool, error) {
	serverMetricsRegistry.requestCount.Inc(nil)

	payload, err := json.Marshal(vinAllowedRequest{Vin: vin})
	if err != nil {
		return c.failOpen(vin, err), err
	}

	msg, err := c.conn.Request(vinAllowedSubject, payload, VinAllowedTimeout)
	if err != nil {
		return c.failOpen(vin, err), err
	}

	var reply vinAllowedResponse
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return c.failOpen(vin, err), err
	}

	return reply.Allowed, nil
}

func (c *Connector) failOpen(vin string, err error) bool {
	serverMetricsRegistry.requestErrorCount.Inc(nil)
	serverMetricsRegistry.failOpenCount.Inc(nil)
	c.logger.ErrorLog("nats_connector_vin_allowed_fail_open", err, logrus.LogInfo{"vin": vin})
	return true
}

// Close tears down the connector's NATS connection.
func (c *Connector) Close() error {
	c.conn.Close()
	return nil
}

func registerMetricsOnce(metricsCollector metrics.MetricCollector) {
	serverMetricsOnce.Do(func() { registerMetrics(metricsCollector) })
}

func registerMetrics(metricsCollector metrics.MetricCollector) {
	serverMetricsRegistry.requestCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "data_connector_nats_request_count",
		Help:   "The number of vin_allowed requests sent over NATS.",
		Labels: []string{},
	})

	serverMetricsRegistry.requestErrorCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "data_connector_nats_request_error_count",
		Help:   "The number of vin_allowed requests that errored (timeout, no responder, or a malformed reply).",
		Labels: []string{},
	})

	serverMetricsRegistry.failOpenCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "data_connector_nats_fail_open_count",
		Help:   "The number of vehicles admitted because the vin_allowed check was unavailable (fail-open).",
		Labels: []string{},
	})
}
