package streaming

import (
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/messages"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
	"github.com/teslamotors/fleet-telemetry/protos"
	"github.com/teslamotors/fleet-telemetry/telemetry"
)

const costTestVin = "TEST123"

type metricEntry struct {
	labels adapter.Labels
	value  float64
}

// recordingCollector captures every metric write so a spec can assert on values and labels.
type recordingCollector struct {
	mu      sync.Mutex
	entries map[string][]metricEntry
	options map[string]adapter.CollectorOptions
}

func newRecordingCollector() *recordingCollector {
	return &recordingCollector{
		entries: make(map[string][]metricEntry),
		options: make(map[string]adapter.CollectorOptions),
	}
}

func (c *recordingCollector) record(name string, value float64, labels adapter.Labels) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make(adapter.Labels, len(labels))
	for key, label := range labels {
		copied[key] = label
	}
	c.entries[name] = append(c.entries[name], metricEntry{labels: copied, value: value})
}

func (c *recordingCollector) entriesFor(name string) []metricEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]metricEntry(nil), c.entries[name]...)
}

// sumFor totals the values written to name whose labels[key] equals value.
func (c *recordingCollector) sumFor(name, key, value string) float64 {
	total := 0.0
	for _, entry := range c.entriesFor(name) {
		if entry.labels[key] == value {
			total += entry.value
		}
	}
	return total
}

func (c *recordingCollector) remember(options adapter.CollectorOptions) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.options[options.Name] = options
}

func (c *recordingCollector) RegisterCounter(options adapter.CollectorOptions) adapter.Counter {
	c.remember(options)
	return &recordingCounter{collector: c, name: options.Name}
}

func (c *recordingCollector) RegisterFloatCounter(options adapter.CollectorOptions) adapter.FloatCounter {
	c.remember(options)
	return &recordingFloatCounter{collector: c, name: options.Name}
}

func (c *recordingCollector) RegisterGauge(options adapter.CollectorOptions) adapter.Gauge {
	c.remember(options)
	return &recordingGauge{collector: c, name: options.Name}
}

func (c *recordingCollector) RegisterTimer(options adapter.CollectorOptions) adapter.Timer {
	c.remember(options)
	return &recordingTimer{collector: c, name: options.Name}
}

func (c *recordingCollector) Shutdown() {}

type recordingCounter struct {
	collector *recordingCollector
	name      string
}

func (c *recordingCounter) Add(n int64, labels adapter.Labels) {
	c.collector.record(c.name, float64(n), labels)
}

func (c *recordingCounter) Inc(labels adapter.Labels) { c.Add(1, labels) }

type recordingFloatCounter struct {
	collector *recordingCollector
	name      string
}

func (c *recordingFloatCounter) Add(n float64, labels adapter.Labels) {
	c.collector.record(c.name, n, labels)
}

type recordingGauge struct {
	collector *recordingCollector
	name      string
}

func (g *recordingGauge) Add(n int64, labels adapter.Labels) {
	g.collector.record(g.name, float64(n), labels)
}

func (g *recordingGauge) Sub(n int64, labels adapter.Labels) { g.Add(-n, labels) }
func (g *recordingGauge) Inc(labels adapter.Labels)          { g.Add(1, labels) }
func (g *recordingGauge) Set(n int64, labels adapter.Labels) { g.Add(n, labels) }

type recordingTimer struct {
	collector *recordingCollector
	name      string
}

func (t *recordingTimer) Observe(n int64, labels adapter.Labels) {
	t.collector.record(t.name, float64(n), labels)
}

var _ = Describe("Streaming signal cost", func() {
	var (
		collector  *recordingCollector
		saved      Metrics
		manager    *SocketManager
		serializer *telemetry.BinarySerializer
	)

	// Builds a record through the real decode + transform path, as a vehicle message would.
	buildRecord := func(topic string, message proto.Message) *telemetry.Record {
		payloadBytes, err := proto.Marshal(message)
		Expect(err).NotTo(HaveOccurred())

		streamMessage := messages.StreamMessage{
			TXID:         []byte("txid"),
			SenderID:     []byte("vehicle_device." + costTestVin),
			MessageTopic: []byte(topic),
			Payload:      payloadBytes,
		}
		msgBytes, err := streamMessage.ToBytes()
		Expect(err).NotTo(HaveOccurred())

		record, err := telemetry.NewRecord(serializer, msgBytes, "socket-1", true)
		Expect(err).NotTo(HaveOccurred())
		return record
	}

	dataRecord := func(signals int) *telemetry.Record {
		payload := &protos.Payload{Vin: costTestVin, CreatedAt: timestamppb.Now()}
		for i := 0; i < signals; i++ {
			payload.Data = append(payload.Data, &protos.Datum{
				Key:   protos.Field_BatteryLevel,
				Value: &protos.Value{Value: &protos.Value_FloatValue{FloatValue: float32(i)}},
			})
		}
		return buildRecord("V", payload)
	}

	BeforeEach(func() {
		collector = newRecordingCollector()
		saved = metricsRegistry
		registerMetrics(collector)

		logger, _ := logrus.NoOpLogger()
		serializer = telemetry.NewBinarySerializer(
			&telemetry.RequestIdentity{DeviceID: costTestVin, SenderID: "vehicle_device." + costTestVin},
			map[string][]telemetry.Producer{},
			logger,
		)
		manager = &SocketManager{vinsSignalTracking: map[string]struct{}{}}
	})

	AfterEach(func() {
		metricsRegistry = saved
	})

	It("registers api.client.cost in credits", func() {
		Expect(collector.options["api.client.cost"].Unit).To(Equal("{credit}"))
	})

	It("charges a data record its signal count over the Tesla signals-per-credit rate", func() {
		manager.trackSignalUsage(dataRecord(4))

		entries := collector.entriesFor("api.client.cost")
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].value).To(BeNumerically("~", 4.0/150.0, 1e-12))
		Expect(entries[0].labels).To(Equal(adapter.Labels{
			"teslemetry.cost.charged_as":  "streaming_signal",
			"teslemetry.cost.endpoint":    "fleet_telemetry",
			"teslemetry.cost.record_type": "data",
			"vehicle.vin":                 costTestVin,
		}))
	})

	It("charges an alerts record its alert count", func() {
		record := buildRecord("alerts", &protos.VehicleAlerts{
			Vin: costTestVin,
			Alerts: []*protos.VehicleAlert{
				{Name: "Alert1", StartedAt: timestamppb.Now()},
				{Name: "Alert2", StartedAt: timestamppb.Now()},
				{Name: "Alert3", StartedAt: timestamppb.Now()},
			},
		})
		Expect(record.SignalsCount()).To(Equal(3))

		manager.trackSignalUsage(record)

		entries := collector.entriesFor("api.client.cost")
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].value).To(BeNumerically("~", 3.0/150.0, 1e-12))
		Expect(entries[0].labels["teslemetry.cost.record_type"]).To(Equal("alerts"))
		Expect(entries[0].labels["vehicle.vin"]).To(Equal(costTestVin))
	})

	It("counts the elements of an errors record", func() {
		record := buildRecord("errors", &protos.VehicleErrors{
			Vin: costTestVin,
			Errors: []*protos.VehicleError{
				{Name: "Error1", CreatedAt: timestamppb.Now()},
				{Name: "Error2", CreatedAt: timestamppb.Now()},
			},
		})
		Expect(record.SignalsCount()).To(Equal(2))

		manager.trackSignalUsage(record)

		entries := collector.entriesFor("api.client.cost")
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].value).To(BeNumerically("~", 2.0/150.0, 1e-12))
		Expect(entries[0].labels["teslemetry.cost.record_type"]).To(Equal("errors"))
	})

	It("charges nothing for a connectivity record", func() {
		record := buildRecord("connectivity", &protos.VehicleConnectivity{
			Vin:          costTestVin,
			ConnectionId: "connection-1",
			Status:       protos.ConnectivityEvent_CONNECTED,
			CreatedAt:    timestamppb.Now(),
		})
		Expect(record.SignalsCount()).To(Equal(0))

		manager.trackSignalUsage(record)

		Expect(collector.entriesFor("api.client.cost")).To(BeEmpty())
	})

	It("reconciles the data cost back to signal_count", func() {
		for _, signals := range []int{1, 7, 32} {
			manager.trackSignalUsage(dataRecord(signals))
		}
		manager.trackSignalUsage(buildRecord("alerts", &protos.VehicleAlerts{
			Vin:    costTestVin,
			Alerts: []*protos.VehicleAlert{{Name: "Alert1", StartedAt: timestamppb.Now()}},
		}))

		cost := collector.sumFor("api.client.cost", "teslemetry.cost.record_type", "data")
		signals := collector.sumFor("signal_count", "record_type", "V")
		Expect(cost * teslaSignalsPerCredit).To(BeNumerically("~", signals, 1e-9))
		Expect(signals).To(BeNumerically("==", 40))
	})
})
