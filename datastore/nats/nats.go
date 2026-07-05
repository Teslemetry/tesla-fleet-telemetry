package nats

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/nats-io/nats.go"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
	"github.com/teslamotors/fleet-telemetry/server/airbrake"
	"github.com/teslamotors/fleet-telemetry/telemetry"
	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// NatsConnect is a variable that holds the function to create a NATS connection
// This allows for testing by replacing it with a mock
var NatsConnect = nats.Connect

// tracer is used for the PRODUCER span emitted on every NATS publish. It resolves
// to the no-op tracer (near-zero overhead) when OTel tracing isn't configured.
var tracer = otelapi.Tracer("fleet-telemetry")

// natsHeaderCarrier adapts nats.Header to propagation.TextMapCarrier so W3C trace
// context can be injected into outgoing message headers.
type natsHeaderCarrier struct {
	header nats.Header
}

func (c natsHeaderCarrier) Get(key string) string {
	return c.header.Get(key)
}

func (c natsHeaderCarrier) Set(key, value string) {
	c.header.Set(key, value)
}

func (c natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c.header))
	for k := range c.header {
		keys = append(keys, k)
	}
	return keys
}

// Producer client to handle NATS interactions
type Producer struct {
	natsConn           *nats.Conn
	namespace          string
	metricsCollector   metrics.MetricCollector
	logger             *logrus.Logger
	airbrakeHandler    *airbrake.Handler
	ackChan            chan (*telemetry.Record)
	reliableAckTxTypes map[string]interface{}
	// closing is set before Close() tears down natsConn, so the async
	// ClosedHandler callback (see NewProducer) can tell an intentional,
	// user-driven shutdown apart from a fatal, unrecoverable connection loss.
	closing *atomic.Bool
}

// Metrics stores metrics reported from this package
type Metrics struct {
	producerCount     adapter.Counter
	bytesTotal        adapter.Counter
	producerAckCount  adapter.Counter
	bytesAckTotal     adapter.Counter
	errorCount        adapter.Counter
	reliableAckCount  adapter.Counter
	producerQueueSize adapter.Gauge
}

var (
	metricsRegistry Metrics
	metricsOnce     sync.Once
)

// Config for NATS producer
type Config struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

// NewProducer establishes the NATS connection and define the dispatch method
func NewProducer(config *Config, namespace string, _ bool, metricsCollector metrics.MetricCollector, airbrakeHandler *airbrake.Handler, ackChan chan (*telemetry.Record), reliableAckTxTypes map[string]interface{}, logger *logrus.Logger) (telemetry.Producer, error) {
	registerMetricsOnce(metricsCollector)

	// Created before the connection so the ClosedHandler closure below can
	// observe an intentional Producer.Close() no matter which goroutine that
	// call races against - see the closing field's doc comment.
	closing := &atomic.Bool{}

	natsConn, err := NatsConnect(
		config.URL,
		nats.Name(config.Name),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ClosedHandler(func(conn *nats.Conn) {
			if closing.Load() {
				logger.ActivityLog("nats_closed", logrus.LogInfo{"message": "NATS connection closed"})
				return
			}
			logger.ErrorLog("nats_closed", conn.LastError(), logrus.LogInfo{"message": "NATS closed with error, shutting down server"})
			panic(fmt.Sprintf("NATS disconnected with error: %v", conn.LastError()))
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			logger.ErrorLog("nats_error", err, logrus.LogInfo{"error": err, "subject": sub.Subject})
		}),
		nats.ConnectHandler(func(_ *nats.Conn) {
			logger.ActivityLog("nats_connected", logrus.LogInfo{})
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			logger.ActivityLog("nats_reconnected", logrus.LogInfo{})
		}),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.ActivityLog("nats_disconnected", logrus.LogInfo{"error": err})
		}),
	)
	if err != nil {
		return nil, err
	}

	producer := &Producer{
		natsConn:           natsConn,
		namespace:          namespace,
		metricsCollector:   metricsCollector,
		logger:             logger,
		airbrakeHandler:    airbrakeHandler,
		ackChan:            ackChan,
		reliableAckTxTypes: reliableAckTxTypes,
		closing:            closing,
	}

	producer.logger.ActivityLog("nats_registered", logrus.LogInfo{"namespace": namespace})
	return producer, nil
}

// normalizeTopic maps a record's TxType to its NATS subject topic segment
func normalizeTopic(txType string) string {
	if txType == "V" {
		return "data"
	}
	return txType
}

// buildSubject constructs the per-vehicle NATS subject for a record
func buildSubject(namespace, vin, txType string) string {
	return fmt.Sprintf("%s.%s.%s", namespace, vin, normalizeTopic(txType))
}

// publishSpanName builds a low-cardinality span name (VIN replaced with a wildcard)
// matching the naming convention consumers use for their own spans on this subject
func publishSpanName(namespace, txType string) string {
	return fmt.Sprintf("publish %s.*.%s", namespace, normalizeTopic(txType))
}

// Produce asynchronously sends the record payload to NATS
func (p *Producer) Produce(entry *telemetry.Record) {
	subject := buildSubject(p.namespace, entry.Vin, entry.TxType)

	ctx, span := tracer.Start(context.Background(), publishSpanName(p.namespace, entry.TxType),
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", subject),
			attribute.String("messaging.operation", "publish"),
			attribute.String("vehicle.vin", entry.Vin),
			attribute.String("record.tx_type", entry.TxType),
			attribute.String("record.txid", entry.Txid),
		),
	)
	defer span.End()

	msg := &nats.Msg{Subject: subject, Data: entry.Payload()}
	// Only allocate/inject headers when tracing is actually configured (valid
	// SpanContext); with the default no-op provider this is skipped entirely.
	if span.SpanContext().IsValid() {
		msg.Header = make(nats.Header)
		otelapi.GetTextMapPropagator().Inject(ctx, natsHeaderCarrier{msg.Header})
	}

	if err := p.natsConn.PublishMsg(msg); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		p.logError(err)
		return
	}
	p.ProcessReliableAck(entry)
	metricsRegistry.producerCount.Inc(map[string]string{"record_type": entry.TxType})
	metricsRegistry.bytesTotal.Add(int64(entry.Length()), map[string]string{"record_type": entry.TxType})
}

// Close the producer
func (p *Producer) Close() error {
	p.closing.Store(true)
	p.natsConn.Close()
	return nil
}

// ProcessReliableAck sends to ackChan if reliable ack is configured
func (p *Producer) ProcessReliableAck(entry *telemetry.Record) {
	_, ok := p.reliableAckTxTypes[entry.TxType]
	if ok {
		p.ackChan <- entry
		metricsRegistry.reliableAckCount.Inc(map[string]string{"record_type": entry.TxType})
	}
}

// ReportError to airbrake and logger
func (p *Producer) ReportError(message string, err error, logInfo logrus.LogInfo) {
	p.airbrakeHandler.ReportLogMessage(logrus.ERROR, message, err, logInfo)
	p.logger.ErrorLog(message, err, logInfo)
}

func (p *Producer) logError(err error) {
	p.ReportError("nats_err", err, nil)
	metricsRegistry.errorCount.Inc(map[string]string{})
}

func registerMetricsOnce(metricsCollector metrics.MetricCollector) {
	metricsOnce.Do(func() { registerMetrics(metricsCollector) })
}

func registerMetrics(metricsCollector metrics.MetricCollector) {
	metricsRegistry.producerCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "nats_produce_total",
		Help:   "The number of records produced to NATS.",
		Labels: []string{"record_type"},
	})

	metricsRegistry.bytesTotal = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "nats_produce_total_bytes",
		Help:   "The number of bytes produced to NATS.",
		Labels: []string{"record_type"},
	})

	metricsRegistry.producerAckCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "nats_produce_ack_total",
		Help:   "The number of records produced to NATS for which we got an ACK.",
		Labels: []string{"record_type"},
	})

	metricsRegistry.reliableAckCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "nats_reliable_ack_total",
		Help:   "The number of records produced to NATS for which we sent a reliable ACK.",
		Labels: []string{"record_type"},
	})

	metricsRegistry.bytesAckTotal = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "nats_produce_ack_total_bytes",
		Help:   "The number of bytes produced to NATS for which we got an ACK.",
		Labels: []string{"record_type"},
	})

	metricsRegistry.errorCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "nats_err",
		Help:   "The number of errors while producing to NATS.",
		Labels: []string{},
	})

	metricsRegistry.producerQueueSize = metricsCollector.RegisterGauge(adapter.CollectorOptions{
		Name:   "nats_produce_queue_size",
		Help:   "Total pending messages to produce",
		Labels: []string{"type"},
	})
}
