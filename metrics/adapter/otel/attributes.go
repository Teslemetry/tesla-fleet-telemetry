package otel

import (
	"go.opentelemetry.io/otel/attribute"
)

// labelsToAttributes converts adapter.Labels to OpenTelemetry attributes
func labelsToAttributes(labels map[string]string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(labels))
	for key, value := range labels {
		attrs = append(attrs, attribute.String(key, value))
	}
	return attrs
}
