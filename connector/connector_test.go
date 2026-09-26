package connector_test

import (
	"encoding/json"
	"errors"
	"os"
	"sync"

	natsclient "github.com/nats-io/nats.go"
	rawlogrus "github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/teslamotors/fleet-telemetry/connector"
	"github.com/teslamotors/fleet-telemetry/connector/adapter/file"
	connectornats "github.com/teslamotors/fleet-telemetry/connector/adapter/nats"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter/noop"
)

var _ = Describe("Provider", func() {
	var (
		config       connector.Config
		logger       *logrus.Logger
		metricsColl  metrics.MetricCollector
		connProvider *connector.Provider
		testFilePath string
	)

	BeforeEach(func() {
		f, err := os.CreateTemp("/tmp", "test-connector-*.json")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = f.Close() }()
		testFilePath = f.Name()

		jsonData, err := json.Marshal(file.Data{AllowedVins: []string{"VIN1"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(testFilePath, jsonData, 0644)).To(Succeed())

		config = connector.Config{
			File: &file.Config{
				Path:         testFilePath,
				Capabilities: []string{"vin_allowed"},
			},
		}

		logger, _ = logrus.NoOpLogger()
		metricsColl = noop.NewCollector()

		connProvider = connector.NewProvider(config, metricsColl, logger)
		Expect(connProvider).NotTo(BeNil())
	})

	AfterEach(func() {
		_ = os.Remove(testFilePath)
	})

	It("gets data using the configured source", func() {
		allowed, err := connProvider.VinAllowed("VIN1")
		Expect(err).To(BeNil())
		Expect(allowed).To(BeTrue())
	})

	Context("configuring sources", func() {
		It("should not initialize unconfigured sources", func() {
			Expect(connProvider.Connectors.Nats).To(BeNil())
		})

		It("should initialize configured source", func() {
			Expect(connProvider.Connectors.File).NotTo(BeNil())
		})
	})

	Context("with no connectors configured", func() {
		BeforeEach(func() {
			connProvider = connector.NewProvider(connector.Config{}, metricsColl, logger)
		})

		It("admits every vin", func() {
			allowed, err := connProvider.VinAllowed("ANY_VIN")
			Expect(err).To(BeNil())
			Expect(allowed).To(BeTrue())
		})
	})

	Context("with a configured connector that fails to construct", func() {
		var (
			hook             *logrustest.Hook
			configureErrors  *countingCounter
			unavailableCount *countingCounter
		)

		BeforeEach(func() {
			connectErr := errors.New("boom")
			original := connectornats.NatsConnect
			connectornats.NatsConnect = func(string, ...natsclient.Option) (*natsclient.Conn, error) {
				return nil, connectErr
			}
			DeferCleanup(func() { connectornats.NatsConnect = original })

			configureErrors = &countingCounter{}
			unavailableCount = &countingCounter{}
			DeferCleanup(connector.SwapMetricsForTest(configureErrors, unavailableCount))

			logger, hook = logrus.NoOpLogger()
			connProvider = connector.NewProvider(connector.Config{
				Nats: &connectornats.Config{URL: "nats://127.0.0.1:1", Capabilities: []string{"vin_allowed"}},
			}, metricsColl, logger)
		})

		It("counts and logs the configure failure", func() {
			Expect(configureErrors.total("nats")).To(Equal(int64(1)))
			entry := findLogEntry(hook, "data_connector_configure_error")
			Expect(entry).NotTo(BeNil())
			Expect(entry.Data["connector"]).To(Equal("nats"))
		})

		It("does not look unconfigured: each check fails open visibly", func() {
			Expect(connProvider.VinAllowedConnector).NotTo(BeNil())

			allowed, err := connProvider.VinAllowed("VIN1")
			Expect(err).To(HaveOccurred())
			Expect(allowed).To(BeTrue())
			Expect(unavailableCount.total("nats")).To(Equal(int64(1)))
			Expect(findLogEntry(hook, "data_connector_unavailable_fail_open")).NotTo(BeNil())
		})
	})
})

// countingCounter totals increments per "connector" label.
type countingCounter struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (c *countingCounter) Add(n int64, labels adapter.Labels) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[string]int64{}
	}
	c.counts[labels["connector"]] += n
}

func (c *countingCounter) Inc(labels adapter.Labels) { c.Add(1, labels) }

func (c *countingCounter) total(connectorName string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[connectorName]
}

func findLogEntry(hook *logrustest.Hook, message string) *rawlogrus.Entry {
	for _, entry := range hook.AllEntries() {
		if entry.Message == message {
			return entry
		}
	}
	return nil
}
