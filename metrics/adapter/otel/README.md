# OpenTelemetry Metrics and Logging

Fleet Telemetry supports exporting metrics and logs via the [OpenTelemetry Protocol (OTLP)](https://opentelemetry.io/docs/specs/otlp/). This enables integration with OTLP-compatible observability backends such as:

- Grafana (via Tempo, Loki, Mimir)
- Datadog
- New Relic
- Honeycomb
- Jaeger
- Any OTLP-compatible collector

## Configuration

Add the `otel` section under `monitoring` in your config.json:

```json
{
  "monitoring": {
    "otel": {
      "endpoint": "localhost:4317",
      "service_name": "fleet-telemetry",
      "protocol": "grpc",
      "export_interval": 60000,
      "insecure": false,
      "logging": true
    }
  }
}
```

## Configuration Options

| Field | Type | Description | Default |
|-------|------|-------------|---------|
| `endpoint` | string | OTLP collector endpoint. Use port `4317` for gRPC or `4318` for HTTP | **required** |
| `service_name` | string | Service name used for resource identification in traces and metrics | `fleet-telemetry` |
| `protocol` | string | OTLP transport protocol: `grpc` or `http` | `grpc` |
| `export_interval` | int | Interval between metric exports in milliseconds | `60000` (1 minute) |
| `insecure` | bool | Disable TLS for the connection (use for local development) | `false` |
| `logging` | bool | Enable log export via OTLP in addition to metrics | `false` |

## Protocol Selection

### gRPC (default)
- Default port: `4317`
- More efficient for high-volume data
- Requires HTTP/2 support

### HTTP
- Default port: `4318`
- Works through HTTP/1.1 proxies
- Easier to debug with standard HTTP tools

## Metrics Exported

The OpenTelemetry adapter exports all Fleet Telemetry metrics including:

- **Counters**: Message counts, error counts, dispatch events
- **Gauges**: Active connections, memory usage, goroutine counts
- **Histograms**: Request latencies, processing times

Standard server metrics:
- `num_goroutine` - Active Go routines
- `memory_allocated_bytes` - Memory allocated by Go
- `memory_heap` - Heap memory usage
- `memory_stack` - Stack memory usage
- `gc_total_pause` - Total GC pause time
- `gc_pause_per_second` - GC pause time per second
- `gc_per_second` - GC runs per second

## Logging

When `logging: true` is enabled, all application logs are exported via OTLP with:

- **Severity levels**: Mapped from logrus levels (trace, debug, info, warn, error, fatal)
- **Timestamps**: Original log timestamps preserved
- **Structured fields**: All log fields converted to OTLP attributes

Log severity mapping:
| Logrus Level | OTLP Severity |
|--------------|---------------|
| trace | TRACE |
| debug | DEBUG |
| info | INFO |
| warn | WARN |
| error | ERROR |
| fatal | FATAL |
| panic | FATAL4 |

## Example Configurations

### Local Development with Jaeger
```json
{
  "monitoring": {
    "otel": {
      "endpoint": "localhost:4317",
      "protocol": "grpc",
      "insecure": true
    }
  }
}
```

### Production with Grafana Cloud
```json
{
  "monitoring": {
    "otel": {
      "endpoint": "otlp-gateway-prod-us-central-0.grafana.net:443",
      "service_name": "fleet-telemetry-prod",
      "protocol": "grpc",
      "export_interval": 30000,
      "logging": true
    }
  }
}
```

### HTTP Protocol with Datadog
```json
{
  "monitoring": {
    "otel": {
      "endpoint": "localhost:4318",
      "protocol": "http",
      "service_name": "fleet-telemetry"
    }
  }
}
```

## Notes

- OpenTelemetry is mutually exclusive with Prometheus and StatsD. Only one metrics backend can be active at a time.
- If the OpenTelemetry collector connection fails, the server will fall back to a no-op metrics collector and log a warning.
- The `service_name` is attached as a resource attribute to all metrics and logs, making it easy to filter in your observability backend.
