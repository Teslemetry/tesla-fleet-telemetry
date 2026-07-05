package telemetry

import (
	"fmt"

	logrus "github.com/teslamotors/fleet-telemetry/logger"
)

// Dispatcher type of telemetry record dispatcher
type Dispatcher string

const (
	// Logger registers a simple logger
	Logger Dispatcher = "logger"
	// NATS registers a NATS dispatcher
	NATS Dispatcher = "nats"
)

// BuildTopicName creates a topic from a namespace and a recordName
func BuildTopicName(namespace, recordName string) string {
	return fmt.Sprintf("%s_%s", namespace, recordName)
}

// Producer handles dispatching data received from the vehicle
type Producer interface {
	Close() error
	Produce(entry *Record)
	ProcessReliableAck(entry *Record)
	ReportError(message string, err error, logInfo logrus.LogInfo)
}
