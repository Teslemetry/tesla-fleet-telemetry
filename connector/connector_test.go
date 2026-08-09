package connector_test

import (
	"encoding/json"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/teslamotors/fleet-telemetry/connector"
	"github.com/teslamotors/fleet-telemetry/connector/adapter/file"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter/noop"
)

var _ = Describe("ConnectorProvider", func() {
	var (
		config       connector.Config
		logger       *logrus.Logger
		metricsColl  metrics.MetricCollector
		connProvider *connector.ConnectorProvider
		testFilePath string
	)

	BeforeEach(func() {
		f, err := os.CreateTemp("/tmp", "test-connector-*.json")
		Expect(err).NotTo(HaveOccurred())
		defer f.Close()
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

		connProvider = connector.NewConnectorProvider(config, metricsColl, logger)
		Expect(connProvider).NotTo(BeNil())
	})

	AfterEach(func() {
		os.Remove(testFilePath)
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
			connProvider = connector.NewConnectorProvider(connector.Config{}, metricsColl, logger)
		})

		It("admits every vin", func() {
			allowed, err := connProvider.VinAllowed("ANY_VIN")
			Expect(err).To(BeNil())
			Expect(allowed).To(BeTrue())
		})
	})
})
