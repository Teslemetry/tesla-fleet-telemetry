package config

import (
	"io"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	githublogrus "github.com/sirupsen/logrus"

	"github.com/teslamotors/fleet-telemetry/datastore/nats"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/server/airbrake"
	"github.com/teslamotors/fleet-telemetry/telemetry"
)

var _ = Describe("Test full application config", func() {

	var (
		config    *Config
		producers map[string][]telemetry.Producer
		log       *logrus.Logger
	)

	BeforeEach(func() {
		log, _ = logrus.NoOpLogger()
		config = &Config{
			Host:       "127.0.0.1",
			Port:       443,
			StatusPort: 8080,
			Namespace:  "tesla_telemetry",
			TLS:        &TLS{CAFile: "tesla.ca", ServerCert: "your_own_cert.crt", ServerKey: "your_own_key.key"},
			RateLimit:  &RateLimit{Enabled: true, MessageLimit: 1000, MessageInterval: 30},
			NATS: &nats.Config{
				URL:  "nats://some.broker:4222",
				Name: "fleet-telemetry",
			},
			Monitoring:    &metrics.MonitoringConfig{PrometheusMetricsPort: 9090, ProfilerPort: 4269, ProfilingPath: "/tmp/fleet-telemetry/profile/"},
			LogLevel:      "info",
			JSONLogEnable: true,
			Records:       map[string][]telemetry.Dispatcher{"V": {"nats"}},
		}
	})

	AfterEach(func() {
		os.Clearenv()
		type Closer interface {
			Close() error
		}
		for _, typeProducers := range producers {
			for _, producer := range typeProducers {
				if closer, ok := producer.(Closer); ok {
					err := closer.Close()
					Expect(err).NotTo(HaveOccurred())
				}
			}
		}
	})

	Context("ExtractServiceTLSConfig", func() {
		It("fails when TLS is nil ", func() {
			config = &Config{}
			_, err := config.ExtractServiceTLSConfig(log)
			Expect(err).To(MatchError("tls config is empty - telemetry server is mTLS only, make sure to provide certificates in the config"))
		})

		It("fails when files are missing", func() {
			_, err := config.ExtractServiceTLSConfig(log)
			Expect(err).To(MatchError("open tesla.ca: no such file or directory"))
		})

		It("fails when pem file is invalid", func() {
			tmpCA, err := os.CreateTemp(GinkgoT().TempDir(), "tmpCA")
			Expect(err).NotTo(HaveOccurred())

			_, err = io.WriteString(tmpCA, "-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----")
			Expect(err).NotTo(HaveOccurred())
			config.TLS.CAFile = tmpCA.Name()

			_, err = config.ExtractServiceTLSConfig(log)
			Expect(err).To(MatchError(MatchRegexp("custom ca not properly loaded: .*tmpCA.*")))
		})

		It("uses prod CA", func() {
			config.TLS.CAFile = ""

			tls, err := config.ExtractServiceTLSConfig(log)
			Expect(err).NotTo(HaveOccurred())
			Expect(tls).NotTo(BeNil())
			Expect(tls.ClientCAs).NotTo(BeNil())
			Expect(tls.ClientCAs.Subjects()).To(HaveLen(14)) //nolint:staticcheck
		})

		It("uses eng CA", func() {
			config.TLS.CAFile = ""
			config.UseDefaultEngCA = true

			tls, err := config.ExtractServiceTLSConfig(log)
			Expect(err).NotTo(HaveOccurred())
			Expect(tls).NotTo(BeNil())
			Expect(tls.ClientCAs).NotTo(BeNil())
			Expect(tls.ClientCAs.Subjects()).To(HaveLen(8)) //nolint:staticcheck
		})
	})

	Context("basic config", func() {
		It("use correct ports", func() {
			config, err := loadTestApplicationConfig(TestSmallConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(config.Port).To(BeEquivalentTo(443))
			Expect(config.StatusPort).To(BeEquivalentTo(8080))
		})

		It("transmitrecords disabled by default", func() {
			config, err := loadTestApplicationConfig(TestSmallConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(config.TransmitDecodedRecords).To(BeFalse())
		})

		It("transmitrecords enabled", func() {
			config, err := loadTestApplicationConfig(TestTransmitDecodedRecords)
			Expect(err).NotTo(HaveOccurred())
			Expect(config.TransmitDecodedRecords).To(BeTrue())
		})
	})

	Context("configure nats", func() {
		It("returns an error if nats isn't included", func() {
			config.Records = map[string][]telemetry.Dispatcher{"V": {"nats"}}
			config.NATS = nil

			var err error
			_, producers, err = config.ConfigureProducers(airbrake.NewAirbrakeHandler(nil), log)
			Expect(err).To(MatchError("expected NATS to be configured"))
			Expect(producers).To(BeNil())
		})
	})

	Context("configure airbrake", func() {
		It("gets config from file", func() {
			config, err := loadTestApplicationConfig(TestAirbrakeConfig)
			Expect(err).NotTo(HaveOccurred())

			_, options, err := config.CreateAirbrakeNotifier(log)
			Expect(err).NotTo(HaveOccurred())
			Expect(options.ProjectKey).To(Equal("test1"))
		})

		It("gets config from env variable", func() {
			projectKey := "environmentProjectKey"
			err := os.Setenv("AIRBRAKE_PROJECT_KEY", projectKey)
			Expect(err).NotTo(HaveOccurred())
			config, err := loadTestApplicationConfig(TestAirbrakeConfig)
			Expect(err).NotTo(HaveOccurred())

			_, options, err := config.CreateAirbrakeNotifier(log)
			Expect(err).NotTo(HaveOccurred())
			Expect(options.ProjectKey).To(Equal(projectKey))
		})
	})

	Context("configure reliable acks", func() {
		It("configures each datasource", func() {
			config, err := loadTestApplicationConfig(TestMultipleTxTypeReliableAckConfig)
			Expect(err).NotTo(HaveOccurred())

			reliableAcks, err := config.configureReliableAckSources()
			Expect(err).ToNot(HaveOccurred())
			Expect(reliableAcks["nats"]).To(HaveLen(3))
			Expect(reliableAcks["nats"]["V"]).To(BeTrue())
			Expect(reliableAcks["nats"]["errors"]).To(BeTrue())
			Expect(reliableAcks["nats"]["alerts"]).To(BeTrue())
		})

		DescribeTable("fails",
			func(configInput string, errMessage string) {

				config, err := loadTestApplicationConfig(configInput)
				Expect(err).NotTo(HaveOccurred())

				_, producers, err = config.ConfigureProducers(airbrake.NewAirbrakeHandler(nil), log)
				Expect(err).To(MatchError(errMessage))
				Expect(producers).To(BeNil())
			},
			Entry("when reliable ack is mapped incorrectly", TestBadReliableAckConfig, "some_unconfigured_dispatcher cannot be configured as reliable ack for record: V. Valid datastores configured [nats]"),
			Entry("when logger is configured as reliable ack", TestLoggerAsReliableAckConfig, "logger cannot be configured as reliable ack for record: V"),
			Entry("when reliable ack is configured for unmapped txtype", TestUnusedTxTypeAsReliableAckConfig, "nats cannot be configured as reliable ack for record: error since no record mapping exists"),
			Entry("when reliable ack is mapped with unsupported txtype", TestBadTxTypeReliableAckConfig, "reliable ack not needed for txType: connectivity"),
		)

	})

	Context("VinsToTrack", func() {

		AfterEach(func() {
			maxVinsToTrack = 20
		})

		It("empty vins to track", func() {
			config, err := loadTestApplicationConfig(TestSmallConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(config.VinsToTrack()).To(BeEmpty())
		})

		It("valid vins to track", func() {
			config, err := loadTestApplicationConfig(TestVinsToTrackConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(config.VinsToTrack()).To(HaveLen(2))
		})

		It("returns an error when `vins_signal_tracking_enabled` exceeds limit", func() {
			maxVinsToTrack = 2
			_, err := loadTestApplicationConfig(BadVinsConfig)
			Expect(err).To(MatchError("set the value of `vins_signal_tracking_enabled` less than 2 unique vins"))
		})
	})

	Context("configureMetricsCollector", func() {
		It("does not fail when TLS is nil ", func() {
			log, _ := logrus.NoOpLogger()
			config = &Config{}
			config.configureMetricsCollector(log)

			Expect(config.Monitoring).To(BeNil())
		})

		It("fails if not reachable", func() {
			log, _ := logrus.NoOpLogger()
			config.configureMetricsCollector(log)
			Expect(config.MetricCollector).NotTo(BeNil())
		})
	})

	Context("configureLogger", func() {
		It("Should properly configure logger", func() {
			log, _ := logrus.NoOpLogger()
			config.configureLogger(log)

			Expect(githublogrus.GetLevel().String()).To(Equal("info"))
		})
	})
})
