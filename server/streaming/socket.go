package streaming

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/beefsack/go-rate"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/teslamotors/fleet-telemetry/config"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
	"github.com/teslamotors/fleet-telemetry/telemetry"
	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type contextKeyType int

// SocketContext is the name of variable holding socket data in the context
const SocketContext contextKeyType = iota + 1

// ReadWriteExitDeadline is the deadline to set when existing the reader/writer
const ReadWriteExitDeadline = 50 * time.Millisecond

// WriteLoopDeadline is the read/write deadline in the main loop
const WriteLoopDeadline = 10 * time.Second

// chunkMaxDuration bounds a single connection "chunk" span's wall-clock lifetime.
// A vehicle can hold one websocket open for many hours (p99 ~8.76h), so instead
// of one span per connection we rotate a fresh chunk span periodically. This keeps
// span durations finite/queryable and bounds crash/restart loss to at most one
// chunk. See the long-spans-judge-k4 design.
const chunkMaxDuration = 30 * time.Minute

// chunkMaxEvents rotates the chunk span before it reaches the OTel SDK's default
// 128-event span limit (which today silently truncates ~13.8% of sessions). Each
// chunk therefore starts with a fresh event budget.
const chunkMaxEvents = 100

// errServerShutdown is recorded as the close_reason when a socket is torn down by
// the graceful-drain path (SIGTERM/SIGINT) rather than a vehicle-initiated close.
var errServerShutdown = errors.New("server_shutdown")

// SocketManager is a struct responsible for managing the socket connection with the clients
type SocketManager struct {
	Ws           *websocket.Conn
	MsgType      int
	RecordsStats map[string]int
	StartTime    time.Time
	UUID         string

	config                 *config.Config
	logger                 *logrus.Logger
	requestIdentity        *telemetry.RequestIdentity
	requestInfo            map[string]interface{}
	metricsCollector       metrics.MetricCollector
	stopChan               chan struct{}
	writeChan              chan SocketMessage
	transmitDecodedRecords bool
	vinsSignalTracking     map[string]struct{}

	// activeLogger holds the logger scoped to the current chunk span's context so
	// log lines carry the live chunk's trace_id/span_id. It is swapped atomically
	// on every chunk rotation because the writer/ack goroutines read it (via log())
	// concurrently with the read loop that rotates chunks. A nil value (e.g. before
	// ProcessTelemetry runs, as in unit tests) falls back to the base logger.
	activeLogger atomic.Pointer[logrus.Logger]

	// Chunk span bookkeeping. These are mutated only by the ProcessTelemetry read
	// loop (and its deferred teardown, which runs on the same goroutine), so they
	// need no locking. The graceful-drain path never touches them directly - it
	// closes the socket, which unblocks the read loop into its normal teardown.
	chunkSpan   trace.Span
	chunkStart  time.Time
	chunkEvents int
	chunkIndex  int

	closeReasonMu sync.Mutex
	closeReason   string
}

// SocketMessage represents incoming socket connection
type SocketMessage struct {
	MsgType int
	Txid    string
	Msg     []byte
}

type socketReadResult struct {
	msgType int
	message []byte
	err     error
}

// Metrics stores metrics reported from this package
type Metrics struct {
	rateLimitExceededCount       adapter.Counter
	recordTooBigCount            adapter.Counter
	unauthorizedSenderCount      adapter.Counter
	unknownMessageTypeErrorCount adapter.Counter
	dispatchCount                adapter.Counter
	unexpectedRecordErrorCount   adapter.Counter
	socketErrorCount             adapter.Counter
	recordSizeBytesTotal         adapter.Counter
	recordCount                  adapter.Counter
	signalsCount                 adapter.Gauge
	vinSignalCount               adapter.Gauge
}

var (
	metricsRegistry Metrics
	metricsOnce     sync.Once
)

// NewSocketManager instantiates a SocketManager
func NewSocketManager(ctx context.Context, requestIdentity *telemetry.RequestIdentity, ws *websocket.Conn, config *config.Config, logger *logrus.Logger) *SocketManager {
	registerMetricsOnce(config.MetricCollector)

	requestLogInfo, socketUUID := buildRequestContext(ctx)

	return &SocketManager{
		Ws:           ws,
		MsgType:      websocket.BinaryMessage,
		RecordsStats: make(map[string]int),
		StartTime:    time.Now(),
		UUID:         socketUUID.String(),

		config:                 config,
		metricsCollector:       config.MetricCollector,
		logger:                 logger,
		requestInfo:            requestLogInfo,
		writeChan:              make(chan SocketMessage, 1000),
		stopChan:               make(chan struct{}),
		requestIdentity:        requestIdentity,
		transmitDecodedRecords: config.TransmitDecodedRecords,
		vinsSignalTracking:     config.VinsToTrack(),
	}
}

func buildRequestContext(ctx context.Context) (logInfo map[string]interface{}, socketUUID uuid.UUID) {
	socketUUID = uuid.New()
	logInfo = make(map[string]interface{})
	if ctx == nil {
		return
	}

	contextInfo, ok := ctx.Value(SocketContext).(map[string]interface{})
	if !ok {
		return
	}

	r, ok := contextInfo["request"].(*http.Request)
	if !ok {
		return
	}

	txid, err := uuid.Parse(r.Header.Get("X-TXID"))
	if err != nil {
		txid = socketUUID
		r.Header.Add("X-TXID", txid.String())
	} else {
		socketUUID = txid
	}

	logInfo["network_interface"] = r.Header.Get("X-Network-Interface")
	logInfo["txid"] = txid
	logInfo["method"] = r.Method
	logInfo["path"] = r.URL.Path
	logInfo["user_agent"] = r.Header.Get("User-Agent")
	logInfo["X-Forwarded-For"] = r.Header.Get("X-Forwarded-For")

	return
}

// GetNetworkInterface returns value from request headers
func (sm *SocketManager) GetNetworkInterface() string {
	networkInterfaceData, ok := sm.requestInfo["network_interface"]
	if !ok {
		return ""
	}
	return networkInterfaceData.(string)
}

// ListenToWriteChannel to the write channel
func (sm *SocketManager) ListenToWriteChannel() SocketMessage {
	msg := <-sm.writeChan
	return msg
}

// isExpectedDisconnect reports whether err is one of the benign network/TLS
// teardown errors seen whenever a vehicle drops its connection (lost signal,
// sleep, reboot, ...), as opposed to an unexpected server-side fault.
func isExpectedDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, websocket.ErrCloseSent) || errors.Is(err, net.ErrClosed) {
		return true
	}
	// crypto/tls wraps the underlying write error with this exact message when
	// the peer already closed the raw TCP connection before we could send our
	// own closeNotify alert - functionally a normal close, not a fault.
	return strings.Contains(err.Error(), "failed to send closeNotify alert (but connection was closed anyway)")
}

// recordCloseReason stores the first teardown error seen across the
// reader/writer goroutines, so socket_disconnected can report a single close_reason
// regardless of which side (read, write, or ws.Close itself) observed it first.
func (sm *SocketManager) recordCloseReason(err error) {
	if err == nil {
		return
	}
	sm.closeReasonMu.Lock()
	defer sm.closeReasonMu.Unlock()
	if sm.closeReason == "" {
		sm.closeReason = err.Error()
	}
}

// log returns the logger scoped to the current chunk span (so log lines correlate
// to the live chunk's trace), falling back to the base logger before any chunk has
// started. Cheap enough for per-message call sites - it's a single atomic load.
func (sm *SocketManager) log() *logrus.Logger {
	if l := sm.activeLogger.Load(); l != nil {
		return l
	}
	return sm.logger
}

// startChunk opens a fresh rolling "chunk" span for the connection and re-points
// log/trace correlation at it. Every chunk carries the same connection.socket_id /
// vehicle.vin so chunks (and the connect/disconnect spans) correlate by attribute
// rather than by a single long-lived parent span.
func (sm *SocketManager) startChunk() {
	ctx, span := otelapi.Tracer("fleet-telemetry").Start(context.Background(), "websocket.chunk",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("vehicle.vin", sm.requestIdentity.DeviceID),
			attribute.String("connection.socket_id", sm.UUID),
			attribute.String("network_interface", sm.GetNetworkInterface()),
			attribute.Int("connection.chunk_index", sm.chunkIndex),
		),
	)
	sm.chunkSpan = span
	sm.chunkStart = time.Now()
	sm.chunkEvents = 0
	sm.activeLogger.Store(sm.logger.WithContext(ctx))
}

// endChunk closes the current chunk span, if any. Idempotent.
func (sm *SocketManager) endChunk() {
	if sm.chunkSpan == nil {
		return
	}
	sm.chunkSpan.SetAttributes(attribute.Int("connection.chunk_events", sm.chunkEvents))
	sm.chunkSpan.End()
	sm.chunkSpan = nil
	sm.activeLogger.Store(nil)
}

// rotateChunkIfNeeded rotates the chunk span once it exceeds the time or event
// budget. This is time/count-gated bookkeeping (one int compare + one time.Since)
// so it stays cheap on the per-message hot path.
func (sm *SocketManager) rotateChunkIfNeeded() {
	if sm.chunkEvents < chunkMaxEvents && time.Since(sm.chunkStart) < chunkMaxDuration {
		return
	}
	sm.endChunk()
	sm.chunkIndex++
	sm.startChunk()
}

// addChunkEvent records a span event on the current chunk and counts it toward the
// rotation budget.
func (sm *SocketManager) addChunkEvent(name string, opts ...trace.EventOption) {
	if sm.chunkSpan == nil {
		return
	}
	sm.chunkSpan.AddEvent(name, opts...)
	sm.chunkEvents++
}

func (sm *SocketManager) chunkRotationDelay() time.Duration {
	remaining := time.Until(sm.chunkStart.Add(chunkMaxDuration))
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (sm *SocketManager) readMessages(reads chan<- socketReadResult) {
	defer close(reads)
	for {
		msgType, message, err := sm.Ws.ReadMessage()
		reads <- socketReadResult{msgType: msgType, message: message, err: err}
		if err != nil || msgType != sm.MsgType {
			return
		}
	}
}

// RequestClose unblocks the read loop so ProcessTelemetry's normal teardown runs
// (ending the current chunk, emitting the disconnect span, logging
// socket_disconnected). Used by the graceful-drain path on SIGTERM/SIGINT. Safe to
// call from another goroutine: it only records a close reason and closes the
// underlying connection, which is what wakes ReadMessage.
func (sm *SocketManager) RequestClose() {
	sm.recordCloseReason(errServerShutdown)
	_ = sm.Ws.Close()
}

// Close shuts down a socket connection for a single client and log metrics
func (sm *SocketManager) Close() {
	if err := sm.Ws.Close(); err != nil {
		sm.recordCloseReason(err)
		if !isExpectedDisconnect(err) {
			sm.log().ErrorLog("websocket_close_err", err, nil)
		}
	}

	socketMetrics := sm.RecordsStatsToLogInfo()
	durationSec := int(time.Since(sm.StartTime) / time.Second) // Result is in nanosecond, converting it to seconds
	socketMetrics["duration_sec"] = durationSec
	sm.closeReasonMu.Lock()
	closeReason := sm.closeReason
	sm.closeReasonMu.Unlock()
	if closeReason != "" {
		socketMetrics["close_reason"] = closeReason
	}
	sm.log().ActivityLog("socket_disconnected", socketMetrics)

	// Short disconnect span (correlated to the connect/chunk spans by
	// connection.socket_id) carrying the final duration, close reason and total
	// bytes so session-level teardown is queryable without a long-lived span.
	totalBytes, _ := socketMetrics["total"].(int)
	disconnectAttrs := []attribute.KeyValue{
		attribute.String("vehicle.vin", sm.requestIdentity.DeviceID),
		attribute.String("connection.socket_id", sm.UUID),
		attribute.Int("duration_sec", durationSec),
		attribute.Int("records.total_bytes", totalBytes),
	}
	if closeReason != "" {
		disconnectAttrs = append(disconnectAttrs, attribute.String("close_reason", closeReason))
	}
	_, disconnectSpan := otelapi.Tracer("fleet-telemetry").Start(context.Background(), "websocket.disconnect",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(disconnectAttrs...),
	)
	disconnectSpan.End()
}

// RecordsStatsToLogInfo converts the stats map into a loggable map, keeping values int-typed
func (sm *SocketManager) RecordsStatsToLogInfo() map[string]interface{} {
	total := 0
	logInfo := make(map[string]interface{})
	for key, value := range sm.RecordsStats {
		logInfo[key] = value
		total += value
	}
	logInfo["total"] = total
	return logInfo
}

// ProcessTelemetry uses the serializer to dispatch telemetry records
func (sm *SocketManager) ProcessTelemetry(serializer *telemetry.BinarySerializer) {
	defer func() {
		sm.Close()
		sm.endChunk()
		close(sm.stopChan)
	}()

	tracer := otelapi.Tracer("fleet-telemetry")

	// (a) Short connect span at accept. Server kind, one-shot, exported immediately.
	_, connectSpan := tracer.Start(context.Background(), "websocket.connect",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("vehicle.vin", sm.requestIdentity.DeviceID),
			attribute.String("connection.socket_id", sm.UUID),
			attribute.String("network_interface", sm.GetNetworkInterface()),
		),
	)
	connectSpan.End()

	// (b) First rolling chunk span. rotateChunkIfNeeded rotates it on the read loop
	// as time/event budgets are exceeded; the deferred endChunk closes the last one.
	// startChunk also re-points log/trace correlation at the current chunk span, so
	// every log line for this connection carries the live chunk's trace_id/span_id.
	sm.startChunk()

	sm.log().ActivityLog("socket_connected", sm.requestInfo)
	go sm.writer()
	reads := make(chan socketReadResult, 1)
	go sm.readMessages(reads)
	var rl *rate.RateLimiter

	if sm.config.RateLimit == nil {
		// No rate limit config - apply default
		rl = rate.New(100, 60*time.Second)
	} else if sm.config.RateLimit.Enabled {
		// Rate limiting explicitly enabled with custom values
		rl = rate.New(sm.config.RateLimit.MessageLimit, sm.config.RateLimit.MessageIntervalTimeSecond)
	}
	// else: RateLimit config exists but Enabled is false - no rate limiting

	var rateLimitStartTime time.Time
	messagesRateLimited := 0
	chunkTimer := time.NewTimer(time.Hour)
	if !chunkTimer.Stop() {
		<-chunkTimer.C
	}
	defer chunkTimer.Stop()

	// infinite loop until the client disconnects (keep accepting new messages)
	for {
		delay := sm.chunkRotationDelay()
		if delay <= 0 {
			sm.rotateChunkIfNeeded()
			continue
		}
		chunkTimer.Reset(delay)

		var read socketReadResult
		select {
		case readResult, ok := <-reads:
			if !ok {
				return
			}
			read = readResult
			if !chunkTimer.Stop() {
				select {
				case <-chunkTimer.C:
				default:
				}
			}
		case <-chunkTimer.C:
			sm.rotateChunkIfNeeded()
			continue
		}

		msgType, message, err := read.msgType, read.message, read.err
		if err != nil || msgType != sm.MsgType {
			if err != nil {
				sm.recordCloseReason(err)
				if isExpectedDisconnect(err) {
					sm.addChunkEvent("disconnect", trace.WithAttributes(
						attribute.String("disconnect.reason", err.Error()),
					))
				} else if sm.chunkSpan != nil {
					sm.chunkSpan.RecordError(err)
					sm.chunkSpan.SetStatus(codes.Error, "read failure")
				}
			}
			return
		}

		// rotate the chunk span once it exceeds its time/event budget
		sm.rotateChunkIfNeeded()

		// check rate limit
		if rl != nil {
			if ok, _ := rl.Try(); !ok {
				if messagesRateLimited == 0 {
					rateLimitStartTime = time.Now()
				}
				// client exceeded the rate limit
				messagesRateLimited++
				record, _ := telemetry.NewRecord(serializer, message, sm.UUID, sm.transmitDecodedRecords)
				sm.trackSignalUsage(record)
				metricsRegistry.rateLimitExceededCount.Inc(map[string]string{"device_id": sm.requestIdentity.DeviceID, "txtype": record.TxType})
				sm.addChunkEvent("rate_limit_exceeded", trace.WithAttributes(
					attribute.String("record.tx_type", record.TxType),
					attribute.Int("messages_dropped", messagesRateLimited),
				))
				continue
			}
			if messagesRateLimited > 0 {
				parts := bytes.Split(message, []byte(","))
				if len(parts) > 2 {
					duration := time.Since(rateLimitStartTime) / time.Second

					sm.log().ErrorLog("rate_limit_exceeded", nil, logrus.LogInfo{"txid": parts[2], "duration_sec": duration, "messages_rate_limited": messagesRateLimited})
				}
				messagesRateLimited = 0
			}
		}
		record := sm.ParseAndProcessRecord(serializer, message)
		if record != nil {
			sm.addChunkEvent("message_received", trace.WithAttributes(
				attribute.String("record.tx_type", record.TxType),
				attribute.String("record.txid", record.Txid),
				attribute.Int("record.size_bytes", len(message)),
			))
		}
	}
}

func (sm *SocketManager) trackSignalUsage(record *telemetry.Record) {
	metricsRegistry.signalsCount.Add(int64(record.SignalsCount()), map[string]string{"record_type": record.TxType})
	vin := record.Vin
	if _, ok := sm.vinsSignalTracking[vin]; !ok {
		return
	}
	metricsRegistry.vinSignalCount.Add(int64(record.SignalsCount()), map[string]string{"vin": vin, "record_type": record.TxType})
}

// ParseAndProcessRecord reads incoming client message and dispatches to relevant producer
func (sm *SocketManager) ParseAndProcessRecord(serializer *telemetry.BinarySerializer, message []byte) *telemetry.Record {
	record, err := telemetry.NewRecord(serializer, message, sm.UUID, sm.transmitDecodedRecords)
	logInfo := logrus.LogInfo{"txid": record.Txid, "record_type": record.TxType}

	if err != nil {
		if err == telemetry.ErrMessageTooBig {
			sm.respondToVehicle(record, err)
			metricsRegistry.recordTooBigCount.Inc(map[string]string{})
			return record
		}

		switch typedError := err.(type) {
		case *telemetry.UnauthorizedSenderIDError:
			logInfo["sender_id"] = typedError.ReceivedSenderID
			logInfo["expected_sender_id"] = typedError.ExpectedSenderID
			sm.log().ErrorLog("unauthorized_sender_id", nil, logInfo)
			metricsRegistry.unauthorizedSenderCount.Inc(map[string]string{})
			sm.respondToVehicle(record, nil) // respond to the client message was accepted so they are not resending it over and over
			return record
		case *telemetry.UnknownMessageType:
			logInfo["msg_txid"] = typedError.Txid
			logInfo["msg_type"] = string(typedError.GuessedType)
			sm.log().ErrorLog("unknown_message_type_error", err, logInfo)
			metricsRegistry.unknownMessageTypeErrorCount.Inc(map[string]string{"msg_type": string(typedError.GuessedType)})
			sm.respondToVehicle(record, nil) // respond to the client message was accepted so they are not resending it over and over
		default:
			sm.respondToVehicle(record, err)
			return record
		}
	}

	// write the record out to kafka
	sm.ReportMetricBytesPerRecords(record.TxType, record.Length())
	sm.processRecord(record)

	// respond instantly to the client if we are not doing reliable ACKs
	if !sm.reliableAck(record) {
		sm.respondToVehicle(record, nil)
	}
	return record
}

func (sm *SocketManager) reliableAck(record *telemetry.Record) bool {
	_, ok := sm.config.ReliableAckSources[record.TxType]
	return ok
}

func (sm *SocketManager) processRecord(record *telemetry.Record) {
	record.Dispatch()
	metricsRegistry.dispatchCount.Inc(map[string]string{"record_type": record.TxType})
}

// respondToVehicle sends an ack message to the client to acknowledge that the records have been transmitted
func (sm *SocketManager) respondToVehicle(record *telemetry.Record, err error) {
	var response []byte

	logInfo := logrus.LogInfo{"txid": record.Txid, "record_type": record.TxType, "device_id": sm.requestIdentity.DeviceID}

	if err != nil {
		sm.log().ErrorLog("unexpected_record", err, logInfo)
		metricsRegistry.unexpectedRecordErrorCount.Inc(map[string]string{})
		response = record.Error(errors.New("incorrect message format"))
		logInfo["response_type"] = "error"
	} else {
		logInfo["response_type"] = "ack"
		response = record.Ack()
	}

	sm.log().Log(logrus.DEBUG, "message_respond", logInfo)
	sm.writeChan <- SocketMessage{sm.MsgType, record.Txid, response}
}

func (sm *SocketManager) writer() {
	defer func() {

		sm.log().Log(logrus.DEBUG, "writer_done", nil)
		_ = sm.Ws.SetReadDeadline(time.Now().Add(ReadWriteExitDeadline))
	}()

	for {
		select {
		case <-sm.stopChan:
			sm.log().Log(logrus.DEBUG, "return_stop_chan", nil)
			return
		case msg := <-sm.writeChan:
			err := sm.writeMessage(msg.MsgType, msg.Msg)
			if err != nil {
				metricsRegistry.socketErrorCount.Inc(map[string]string{})
				sm.recordCloseReason(err)
				if !isExpectedDisconnect(err) {
					sm.log().ErrorLog("socket_err", err, logrus.LogInfo{"txid": msg.Txid, "device_id": sm.requestIdentity.DeviceID})
				}
				return
			}
		}
	}
}

func (sm *SocketManager) writeMessage(msgType int, msg []byte) error {
	_ = sm.Ws.SetWriteDeadline(time.Now().Add(WriteLoopDeadline))
	return sm.Ws.WriteMessage(msgType, msg)
}

// ReportMetricBytesPerRecords records metrics for metric size
func (sm *SocketManager) ReportMetricBytesPerRecords(recordType string, byteSize int) {
	sm.RecordsStats[recordType] += byteSize

	metricsRegistry.recordSizeBytesTotal.Add(int64(byteSize), map[string]string{"record_type": recordType})
	metricsRegistry.recordCount.Inc(map[string]string{"record_type": recordType})
}

func registerMetricsOnce(metricsCollector metrics.MetricCollector) {
	metricsOnce.Do(func() { registerMetrics(metricsCollector) })
}

func registerMetrics(metricsCollector metrics.MetricCollector) {
	metricsRegistry.rateLimitExceededCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "rate_limit_exceeded_total",
		Help:   "The number of times a client has been rate limited.",
		Labels: []string{"device_id", "txtype"},
	})

	metricsRegistry.recordTooBigCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "record_too_big_total",
		Help:   "The number of times the record was too large.",
		Labels: []string{},
	})

	metricsRegistry.unauthorizedSenderCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "unauthorized_sender_id_total",
		Help:   "The number of times the sender was not authorized.",
		Labels: []string{},
	})

	metricsRegistry.unknownMessageTypeErrorCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "unknown_message_type_error_total",
		Help:   "The number of times the message type was not known.",
		Labels: []string{"msg_type"},
	})

	metricsRegistry.dispatchCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "dispatch_total",
		Help:   "The number of records dispatched.",
		Labels: []string{"record_type"},
	})

	metricsRegistry.unexpectedRecordErrorCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "unexpected_record_err_total",
		Help:   "The number of unexpected records received.",
		Labels: []string{},
	})

	metricsRegistry.socketErrorCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "socket_err_total",
		Help:   "The number of socket errors.",
		Labels: []string{},
	})

	metricsRegistry.recordSizeBytesTotal = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "record_size_bytes_total",
		Help:   "The total number of record bytes processed.",
		Labels: []string{"record_type"},
	})

	metricsRegistry.recordCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "record_total",
		Help:   "The number of records processed.",
		Labels: []string{"record_type"},
	})

	metricsRegistry.signalsCount = metricsCollector.RegisterGauge(adapter.CollectorOptions{
		Name:   "signal_count",
		Help:   "Total number of signals received per record type",
		Labels: []string{"record_type"},
	})

	metricsRegistry.vinSignalCount = metricsCollector.RegisterGauge(adapter.CollectorOptions{
		Name:   "vin_signal_count",
		Help:   "Total number of signals received per vin per record type",
		Labels: []string{"record_type", "vin"},
	})

}
