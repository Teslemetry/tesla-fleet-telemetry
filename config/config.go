package config

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed" //Used for default CAs
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	githubairbrake "github.com/airbrake/gobrake/v5"
	githublogrus "github.com/sirupsen/logrus"

	"github.com/teslamotors/fleet-telemetry/datastore/nats"
	"github.com/teslamotors/fleet-telemetry/datastore/simple"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/server/airbrake"
	"github.com/teslamotors/fleet-telemetry/telemetry"
	"github.com/teslamotors/fleet-telemetry/telemetry/tracing"
)

const (
	airbrakeProjectKeyEnv = "AIRBRAKE_PROJECT_KEY"
)

// Config object for server
type Config struct {
	// Host is the telemetry server hostname
	Host string `json:"host,omitempty"`

	// Port is the telemetry server port
	Port int `json:"port,omitempty"`

	// Status Port is used to check whether service is live or not
	StatusPort int `json:"status_port,omitempty"`

	// TLS contains certificates & CA info for the webserver
	TLS *TLS `json:"tls,omitempty"`

	// UseDefaultEngCA overrides default CA to eng
	UseDefaultEngCA bool `json:"use_default_eng_ca"`

	// RateLimit is a configuration for the ratelimit
	RateLimit *RateLimit `json:"rate_limit,omitempty"`

	// ReliableAckSources is a mapping of record types to a dispatcher that will be used for reliable ack
	ReliableAckSources map[string]telemetry.Dispatcher `json:"reliable_ack_sources,omitempty"`

	// Namespace defines a prefix for the NATS subject/logger topic
	Namespace string `json:"namespace,omitempty"`

	// Monitoring defines information for metrics
	Monitoring *metrics.MonitoringConfig `json:"monitoring,omitempty"`

	// LoggerConfig configures the simple logger
	LoggerConfig *simple.Config `json:"logger,omitempty"`

	// LogLevel set the log-level
	LogLevel string `json:"log_level,omitempty"`

	// JSONLogEnable if true log in json format
	JSONLogEnable bool `json:"json_log_enable,omitempty"`

	// Records is a mapping of topics (records type) to a reference dispatch implementation (i,e: nats)
	Records map[string][]telemetry.Dispatcher `json:"records,omitempty"`

	// TransmitDecodedRecords if true decodes proto message before dispatching it to supported datastores
	// when vehicle configuration has prefer_typed set to true, enum fields will have a prefix
	TransmitDecodedRecords bool `json:"transmit_decoded_records,omitempty"`

	VinsSignalTrackingEnabled []string `json:"vins_signal_tracking_enabled"`

	// MetricCollector collects metrics for the application
	MetricCollector metrics.MetricCollector

	// AckChan is a channel used to push acknowledgment from the datastore to connected clients
	AckChan chan (*telemetry.Record)

	// Airbrake config
	Airbrake *Airbrake

	// NATS config
	NATS *nats.Config `json:"nats,omitempty"`
}

// Airbrake config
type Airbrake struct {
	Host        string `json:"host"`
	ProjectKey  string `json:"project_key"`
	Environment string `json:"environment"`
	ProjectID   int64  `json:"project_id"`

	TLS *TLS `json:"tls" yaml:"tls"`
}

// RateLimit config for the service to handle ratelimiting incoming requests
type RateLimit struct {
	// MessageRateLimiterEnabled skip messages if it exceeds the limit
	Enabled bool `json:"enabled,omitempty"`

	// MessageLimit is a rate limiting of the number of messages per client
	MessageLimit int `json:"message_limit,omitempty"`

	// MessageInterval is the rate limit time interval
	MessageInterval int `json:"message_interval_time,omitempty"`

	// MessageIntervalTimeSecond is the rate limit time interval as a duration in second
	MessageIntervalTimeSecond time.Duration
}

//go:embed files/eng_ca.crt
var defaultEngCA []byte

//go:embed files/prod_ca.crt
var defaultProdCA []byte

// TLS config
type TLS struct {
	CAFile     string `json:"ca_file"`
	ServerCert string `json:"server_cert"`
	ServerKey  string `json:"server_key"`
}

// AirbrakeTLSConfig return the TLS config needed for connecting with airbrake server
func (c *Config) AirbrakeTLSConfig() (*tls.Config, error) {
	if c.Airbrake.TLS == nil {
		return nil, nil
	}
	caPath := c.Airbrake.TLS.CAFile
	certPath := c.Airbrake.TLS.ServerCert
	keyPath := c.Airbrake.TLS.ServerKey
	tlsConfig := &tls.Config{}
	if certPath != "" && keyPath != "" {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("can't properly load cert pair (%s, %s): %s", certPath, keyPath, err.Error())
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
		// TODO remove the lint bypass
		// nolint:staticcheck
		tlsConfig.BuildNameToCertificate()
	}

	if caPath != "" {
		clientCACert, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("can't properly load ca cert (%s): %s", caPath, err.Error())
		}
		clientCertPool := x509.NewCertPool()
		clientCertPool.AppendCertsFromPEM(clientCACert)
		tlsConfig.RootCAs = clientCertPool
	}

	return tlsConfig, nil
}

// VinsToTrack to track incoming signals in promemetheus
func (c *Config) VinsToTrack() map[string]struct{} {
	output := make(map[string]struct{}, 0)
	if len(c.VinsSignalTrackingEnabled) == 0 {
		return output
	}
	for _, vin := range c.VinsSignalTrackingEnabled {
		output[vin] = struct{}{}
	}
	return output
}

// ExtractServiceTLSConfig return the TLS config needed for stating the mTLS Server
func (c *Config) ExtractServiceTLSConfig(logger *logrus.Logger) (*tls.Config, error) {
	if c.TLS == nil {
		return nil, errors.New("tls config is empty - telemetry server is mTLS only, make sure to provide certificates in the config")
	}

	var caFileBytes []byte
	var caEnv string
	if c.UseDefaultEngCA {
		caEnv = "eng"
		caFileBytes = make([]byte, len(defaultEngCA))
		copy(caFileBytes, defaultEngCA)
	} else {
		caEnv = "prod"
		caFileBytes = make([]byte, len(defaultProdCA))
		copy(caFileBytes, defaultProdCA)
	}
	caCertPool := x509.NewCertPool()
	ok := caCertPool.AppendCertsFromPEM(caFileBytes)
	if !ok {
		return nil, fmt.Errorf("tls ca not properly loaded for %s environment", caEnv)
	}
	if c.TLS.CAFile != "" {
		customCaFileBytes, err := os.ReadFile(c.TLS.CAFile)
		if err != nil {
			return nil, err
		}
		ok := caCertPool.AppendCertsFromPEM(customCaFileBytes)
		if !ok {
			return nil, fmt.Errorf("custom ca not properly loaded: %s", c.TLS.CAFile)
		}
		logger.ActivityLog("custom_ca_file_appened", logrus.LogInfo{"ca_file_path": c.TLS.CAFile})
	}

	return &tls.Config{
		ClientCAs:  caCertPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}, nil
}

func (c *Config) configureLogger(logger *logrus.Logger) {
	level, err := githublogrus.ParseLevel(c.LogLevel)
	if err != nil {
		logger.ErrorLog("invalid_level", err, nil)
	} else {
		githublogrus.SetLevel(level)
	}
	logger.SetJSONFormatter(c.JSONLogEnable)
}

func (c *Config) configureMetricsCollector(logger *logrus.Logger) {
	c.MetricCollector = metrics.NewCollector(c.Monitoring, logger)
}

// ConfigureOTelLogging sets up the OpenTelemetry logging hook if enabled
// Returns the hook's shutdown function (or nil if not enabled)
func (c *Config) ConfigureOTelLogging(logger *logrus.Logger) func() error {
	if c.Monitoring == nil || c.Monitoring.OpenTelemetry == nil || !c.Monitoring.OpenTelemetry.Logging {
		return nil
	}

	otelCfg := c.Monitoring.OpenTelemetry
	hookCfg := &logrus.OTelConfig{
		Endpoint:    otelCfg.Endpoint,
		ServiceName: otelCfg.ServiceName,
		Protocol:    otelCfg.Protocol,
		Insecure:    otelCfg.Insecure,
	}

	hook, err := logrus.NewOTelHook(hookCfg)
	if err != nil {
		logger.ErrorLog("otel_logging_hook_creation_failed", err, nil)
		return nil
	}

	logger.AddHook(hook)
	logger.ActivityLog("otel_logging_enabled", logrus.LogInfo{
		"endpoint":     otelCfg.Endpoint,
		"protocol":     otelCfg.Protocol,
		"service_name": otelCfg.ServiceName,
	})

	return hook.Shutdown
}

// ConfigureOTelTracing sets up the OpenTelemetry tracing provider if enabled
// Returns the provider's shutdown function (or nil if not enabled)
func (c *Config) ConfigureOTelTracing(logger *logrus.Logger) func() error {
	if c.Monitoring == nil || c.Monitoring.OpenTelemetry == nil || !c.Monitoring.OpenTelemetry.Tracing {
		return nil
	}

	provider, err := tracing.NewProvider(c.Monitoring.OpenTelemetry, logger)
	if err != nil {
		logger.ErrorLog("otel_tracing_provider_creation_failed", err, nil)
		return nil
	}

	return provider.Shutdown
}

func (c *Config) prometheusEnabled() bool {
	if c.Monitoring != nil && c.Monitoring.PrometheusMetricsPort > 0 {
		return true
	}
	return false
}

// ConfigureProducers validates and establishes connections to the producers (nats/logger)
func (c *Config) ConfigureProducers(airbrakeHandler *airbrake.Handler, logger *logrus.Logger) (map[telemetry.Dispatcher]telemetry.Producer, map[string][]telemetry.Producer, error) {
	reliableAckSources, err := c.configureReliableAckSources()
	if err != nil {
		return nil, nil, err
	}

	producers := make(map[telemetry.Dispatcher]telemetry.Producer)
	producers[telemetry.Logger] = simple.NewProtoLogger(c.LoggerConfig, logger)

	requiredDispatchers := make(map[telemetry.Dispatcher][]string)
	for recordName, dispatchRules := range c.Records {
		for _, dispatchRule := range dispatchRules {
			requiredDispatchers[dispatchRule] = append(requiredDispatchers[dispatchRule], recordName)
		}
	}

	if _, ok := requiredDispatchers[telemetry.NATS]; ok {
		if c.NATS == nil {
			return nil, nil, errors.New("expected NATS to be configured")
		}
		natsProducer, err := nats.NewProducer(c.NATS, c.Namespace, c.prometheusEnabled(), c.MetricCollector, airbrakeHandler, c.AckChan, reliableAckSources[telemetry.NATS], logger)
		if err != nil {
			return nil, nil, err
		}
		producers[telemetry.NATS] = natsProducer
	}

	dispatchProducerRules := make(map[string][]telemetry.Producer)
	for recordName, dispatchRules := range c.Records {
		var dispatchFuncs []telemetry.Producer
		for _, dispatchRule := range dispatchRules {
			dispatchFuncs = append(dispatchFuncs, producers[dispatchRule])
		}
		dispatchProducerRules[recordName] = dispatchFuncs

		if len(dispatchProducerRules[recordName]) == 0 {
			return nil, nil, fmt.Errorf("unknown_dispatch_rule record: %v, dispatchRule:%v", recordName, dispatchRules)
		}
	}

	return producers, dispatchProducerRules, nil
}

func (c *Config) configureReliableAckSources() (map[telemetry.Dispatcher]map[string]interface{}, error) {
	reliableAckSources := make(map[telemetry.Dispatcher]map[string]interface{}, 0)
	for txType, dispatchRule := range c.ReliableAckSources {
		if txType == "connectivity" {
			return nil, fmt.Errorf("reliable ack not needed for txType: %s", txType)
		}
		if dispatchRule == telemetry.Logger {
			return nil, fmt.Errorf("logger cannot be configured as reliable ack for record: %s", txType)
		}
		dispatchers, ok := c.Records[txType]
		if !ok {
			return nil, fmt.Errorf("%s cannot be configured as reliable ack for record: %s since no record mapping exists", dispatchRule, txType)
		}
		dispatchRuleFound := false
		validDispatchers := parseValidDispatchers(dispatchers)
		for _, dispatcher := range validDispatchers {
			if dispatcher == dispatchRule {
				dispatchRuleFound = true
				if _, ok := reliableAckSources[dispatchRule]; !ok {
					reliableAckSources[dispatchRule] = make(map[string]interface{}, 1)
				}
				reliableAckSources[dispatchRule][txType] = true
				break
			}
		}
		if !dispatchRuleFound {
			return nil, fmt.Errorf("%s cannot be configured as reliable ack for record: %s. Valid datastores configured %v", dispatchRule, txType, validDispatchers)
		}
	}
	return reliableAckSources, nil
}

// parseValidDispatchers removes no-op dispatcher from the input i.e. Logger
func parseValidDispatchers(input []telemetry.Dispatcher) []telemetry.Dispatcher {
	var result []telemetry.Dispatcher
	for _, v := range input {
		if v != telemetry.Logger {
			result = append(result, v)
		}
	}
	return result
}

// CreateAirbrakeNotifier intializes an airbrake notifier with standard configs
func (c *Config) CreateAirbrakeNotifier(logger *logrus.Logger) (*githubairbrake.Notifier, *githubairbrake.NotifierOptions, error) {
	if c.Airbrake == nil {
		return nil, nil, nil
	}
	tlsConfig, err := c.AirbrakeTLSConfig()
	if err != nil {
		return nil, nil, err
	}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}
	errbitHost := c.Airbrake.Host
	projectKey, ok := os.LookupEnv(airbrakeProjectKeyEnv)
	logInfo := logrus.LogInfo{}
	if ok {
		logInfo["source"] = "environment_variable"
		logInfo["env_key"] = airbrakeProjectKeyEnv

	} else {
		projectKey = c.Airbrake.ProjectKey
		logInfo["source"] = "config_file"
	}
	logger.ActivityLog("airbrake_configured", logInfo)
	options := &githubairbrake.NotifierOptions{
		Host:                errbitHost,
		RemoteConfigHost:    errbitHost,
		DisableRemoteConfig: true,
		APMHost:             errbitHost,
		DisableAPM:          true,
		ProjectId:           c.Airbrake.ProjectID,
		ProjectKey:          projectKey,
		Environment:         c.Airbrake.Environment,
		HTTPClient:          httpClient,
	}
	return githubairbrake.NewNotifierWithOptions(options), options, nil
}
