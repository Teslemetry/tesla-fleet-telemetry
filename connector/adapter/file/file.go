// Package file implements a data connector adapter that reads an allowlist from a
// JSON file and watches it for changes at runtime.
package file

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter"
)

var (
	serverMetricsRegistry ServerMetrics
	serverMetricsOnce     sync.Once
)

// ServerMetrics stores metrics reported from this package
type ServerMetrics struct {
	fileReadErrorCount adapter.Counter
	fileEventCount     adapter.Counter
}

// Config for the file connector
type Config struct {
	Path         string   `json:"path"`
	Capabilities []string `json:"capabilities"`
}

// Data is the JSON schema the watched file is expected to contain
type Data struct {
	AllowedVins []string `json:"allowed_vins,omitempty"`
}

// Connector answers capability checks from a watched file
type Connector struct {
	watcher          *fsnotify.Watcher
	allowedVins      map[string]bool
	LastUpdate       time.Time
	logger           *logrus.Logger
	metricsCollector metrics.MetricCollector
}

// NewConnector reads the file once and starts watching it for changes
func NewConnector(config Config, metricsCollector metrics.MetricCollector, logger *logrus.Logger) (*Connector, error) {
	registerMetricsOnce(metricsCollector)
	connector := &Connector{
		logger:           logger,
		metricsCollector: metricsCollector,
	}

	if err := connector.processFile(config.Path); err != nil {
		return nil, err
	}
	if err := connector.setupWatcher(config.Path); err != nil {
		return nil, err
	}

	return connector, nil
}

// VinAllowed reports whether vin is present in the file's allowlist. With no
// allowlist configured (nil AllowedVins), every vin is allowed.
func (c *Connector) VinAllowed(vin string) (bool, error) {
	if c.allowedVins == nil {
		return true, nil
	}

	return c.allowedVins[vin], nil
}

// Close stops watching the file
func (c *Connector) Close() error {
	return c.watcher.Close()
}

func (c *Connector) processUpdates() {
	for {
		select {
		case event, ok := <-c.watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write {
				err := c.processFile(event.Name)
				serverMetricsRegistry.fileEventCount.Inc(nil)
				if err != nil {
					serverMetricsRegistry.fileReadErrorCount.Inc(nil)
					c.logger.ErrorLog("file_connector_invalid_file_content", err, nil)
				}
			}
		case err, ok := <-c.watcher.Errors:
			if !ok {
				return
			}
			c.logger.ErrorLog("file_connector_watcher_error", err, nil)
		}
	}
}

func (c *Connector) processFile(filepath string) error {
	fileBytes, err := os.ReadFile(filepath)
	if err != nil {
		return err
	}
	fileData := Data{}
	if err := json.Unmarshal(fileBytes, &fileData); err != nil {
		return err
	}

	c.processAllowedVins(fileData)

	c.LastUpdate = time.Now()
	return nil
}

func (c *Connector) processAllowedVins(fileData Data) {
	if fileData.AllowedVins == nil {
		c.allowedVins = nil
		return
	}

	allowedVins := make(map[string]bool, len(fileData.AllowedVins))
	for _, vin := range fileData.AllowedVins {
		allowedVins[vin] = true
	}
	c.allowedVins = allowedVins
}

func (c *Connector) setupWatcher(filepath string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := watcher.Add(filepath); err != nil {
		return err
	}
	c.watcher = watcher
	go c.processUpdates()

	return nil
}

func registerMetricsOnce(metricsCollector metrics.MetricCollector) {
	serverMetricsOnce.Do(func() { registerMetrics(metricsCollector) })
}

func registerMetrics(metricsCollector metrics.MetricCollector) {
	serverMetricsRegistry.fileEventCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "data_connector_file_event_count",
		Help:   "The number of file events processed.",
		Labels: []string{},
	})

	serverMetricsRegistry.fileReadErrorCount = metricsCollector.RegisterCounter(adapter.CollectorOptions{
		Name:   "data_connector_file_read_error_count",
		Help:   "The number of errors while processing file.",
		Labels: []string{},
	})
}
