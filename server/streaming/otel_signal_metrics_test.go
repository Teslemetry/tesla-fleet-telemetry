package streaming_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	collectormetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"

	"github.com/teslamotors/fleet-telemetry/config"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/messages"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter/otel"
	"github.com/teslamotors/fleet-telemetry/protos"
	"github.com/teslamotors/fleet-telemetry/server/streaming"
	"github.com/teslamotors/fleet-telemetry/telemetry"
)

// signalMetricsOTLPSubprocessEnvVar re-execs this test binary to run only the
// export check in its own process. server/streaming's metricsRegistry is
// registered exactly once per process (sync.Once), and every other spec in
// this package's test binary also builds a SocketManager - so sharing a
// process risks binding metricsRegistry to whichever collector wins that
// race first, not the OTLP receiver this test needs to observe. Mirrors the
// subprocess pattern in datastore/nats/nats_close_test.go.
const signalMetricsOTLPSubprocessEnvVar = "FLEET_TELEMETRY_OTEL_SIGNAL_METRICS_SUBPROCESS"

// fakeOTLPMetricsReceiver is a minimal in-process OTLP/gRPC metrics sink -
// the same wire endpoint production's monitoring.otel config points the
// server at (OTLP grpc to a local collector) - so this test observes exactly
// what the real exporter puts on the wire, not a mocked stand-in for it.
type fakeOTLPMetricsReceiver struct {
	collectormetricpb.UnimplementedMetricsServiceServer

	mu       sync.Mutex
	requests []*collectormetricpb.ExportMetricsServiceRequest
}

func (f *fakeOTLPMetricsReceiver) Export(_ context.Context, req *collectormetricpb.ExportMetricsServiceRequest) (*collectormetricpb.ExportMetricsServiceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	return &collectormetricpb.ExportMetricsServiceResponse{}, nil
}

// gaugeValue returns the last observed value and attributes for a gauge
// metric name, or ok=false if it was never exported.
func (f *fakeOTLPMetricsReceiver) gaugeValue(name string) (value int64, attrs map[string]string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		for _, rm := range req.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name != name {
						continue
					}
					gauge := m.GetGauge()
					if gauge == nil || len(gauge.DataPoints) == 0 {
						continue
					}
					dp := gauge.DataPoints[len(gauge.DataPoints)-1]
					value, ok = dp.GetAsInt(), true
					attrs = attrsToMap(dp.Attributes)
				}
			}
		}
	}
	return value, attrs, ok
}

// sumValue returns the last observed double value, attributes and unit for a
// monotonic sum metric name, or ok=false if it was never exported.
func (f *fakeOTLPMetricsReceiver) sumValue(name string) (value float64, attrs map[string]string, unit string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		for _, rm := range req.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name != name {
						continue
					}
					sum := m.GetSum()
					if sum == nil || len(sum.DataPoints) == 0 {
						continue
					}
					dp := sum.DataPoints[len(sum.DataPoints)-1]
					value, unit, ok = dp.GetAsDouble(), m.GetUnit(), true
					attrs = attrsToMap(dp.Attributes)
				}
			}
		}
	}
	return value, attrs, unit, ok
}

func (f *fakeOTLPMetricsReceiver) hasMetric(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		for _, rm := range req.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name == name {
						return true
					}
				}
			}
		}
	}
	return false
}

func attrsToMap(kvs []*commonpb.KeyValue) map[string]string {
	attrs := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	return attrs
}

// TestSignalMetricsExportOverOTLP proves that both metrics emitted at the
// trackSignalUsage seam - signal_count and the per-VIN tesla.cost - are
// exported over the real OTLP path when a normal, non-rate-limited vehicle
// record is processed, the traffic pattern that makes up virtually all
// production connections.
func TestSignalMetricsExportOverOTLP(t *testing.T) {
	if os.Getenv(signalMetricsOTLPSubprocessEnvVar) == "1" {
		runSignalMetricsOTLPSubprocessBody(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalMetricsExportOverOTLP$", "-test.v") //nolint:gosec
	cmd.Env = append(os.Environ(), signalMetricsOTLPSubprocessEnvVar+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("signal metrics OTLP export check failed in subprocess:\n%s", output)
	}
}

func runSignalMetricsOTLPSubprocessBody(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	receiver := &fakeOTLPMetricsReceiver{}
	grpcServer := grpc.NewServer()
	collectormetricpb.RegisterMetricsServiceServer(grpcServer, receiver)
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	logger, _ := logrus.NoOpLogger()
	collector := otel.NewCollector(&otel.Config{
		Endpoint:       lis.Addr().String(),
		Protocol:       "grpc",
		Insecure:       true,
		ExportInterval: 20, // ms - fast export so the test doesn't wait on production's 30s interval
	}, logger)
	if collector == nil {
		t.Fatal("otel.NewCollector returned nil")
	}
	defer collector.Shutdown()

	requestIdentity := &telemetry.RequestIdentity{DeviceID: "otel-repro-vin", SenderID: "vehicle_device.otel-repro-vin"}
	conf := &config.Config{MetricCollector: collector}
	sm := streaming.NewSocketManager(context.Background(), requestIdentity, nil, conf, logger)

	serializer := telemetry.NewBinarySerializer(requestIdentity, map[string][]telemetry.Producer{"V": nil}, logger)

	payload := &protos.Payload{
		Vin: requestIdentity.DeviceID,
		Data: []*protos.Datum{
			{Key: protos.Field_VehicleName, Value: &protos.Value{Value: &protos.Value_StringValue{StringValue: "repro"}}},
			{Key: protos.Field_BatteryLevel, Value: &protos.Value{Value: &protos.Value_DoubleValue{DoubleValue: 42}}},
		},
		CreatedAt: timestamppb.Now(),
	}
	payloadBytes, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	wantSignals := int64(len(payload.Data))

	streamMsg := messages.StreamMessage{
		TXID:         []byte("otel-repro-txid"),
		SenderID:     []byte(requestIdentity.SenderID),
		DeviceID:     []byte(requestIdentity.DeviceID),
		DeviceType:   []byte("vehicle_device"),
		MessageTopic: []byte("V"),
		Payload:      payloadBytes,
	}
	msgBytes, err := streamMsg.ToBytes()
	if err != nil {
		t.Fatalf("failed to encode stream message: %v", err)
	}

	// A normal, non-rate-limited message - the code path every production
	// vehicle message takes.
	sm.ParseAndProcessRecord(serializer, msgBytes)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if receiver.hasMetric("record_total") && receiver.hasMetric("signal_count") && receiver.hasMetric("tesla.cost") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !receiver.hasMetric("record_total") {
		t.Fatal("record_total never arrived over OTLP - test harness itself is broken, not just signal_count")
	}

	value, attrs, ok := receiver.gaugeValue("signal_count")
	if !ok {
		t.Fatal("signal_count never arrived over OTLP for a normal (non-rate-limited) record - this is the reproduction")
	}
	if value != wantSignals {
		t.Fatalf("signal_count = %d, want %d", value, wantSignals)
	}
	if got := attrs["record_type"]; got != "V" {
		t.Fatalf("signal_count record_type attribute = %q, want %q", got, "V")
	}

	cost, costAttrs, unit, ok := receiver.sumValue("tesla.cost")
	if !ok {
		t.Fatal("tesla.cost never arrived over OTLP")
	}
	if wantCost := float64(wantSignals) / 150000.0; cost != wantCost {
		t.Fatalf("tesla.cost = %v, want %v", cost, wantCost)
	}
	if unit != "USD" {
		t.Fatalf("tesla.cost unit = %q, want %q", unit, "USD")
	}
	wantAttrs := map[string]string{
		"teslemetry.cost.type": "streaming_data",
		"teslemetry.cost.id":   requestIdentity.DeviceID,
	}
	if !reflect.DeepEqual(costAttrs, wantAttrs) {
		t.Fatalf("tesla.cost attributes = %v, want exactly %v", costAttrs, wantAttrs)
	}
}
