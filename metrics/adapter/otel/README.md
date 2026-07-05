# OpenTelemetry Metrics, Logging, and Tracing

Fleet Telemetry supports exporting metrics, logs, and traces via the [OpenTelemetry Protocol (OTLP)](https://opentelemetry.io/docs/specs/otlp/). This enables integration with OTLP-compatible observability backends such as:

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
      "logging": true,
      "tracing": true,
      "trace_sample_rate": 0.01
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
| `tracing` | bool | Enable trace export via OTLP in addition to metrics | `false` |
| `trace_sample_rate` | float | Ratio of traces to sample, from `0.0` to `1.0` | `0.01` |

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

## Tracing

When `tracing: true` is enabled, Fleet Telemetry exports OTLP spans through the same endpoint, protocol, TLS mode, and service name as metrics. The default sampler exports 1% of root traces; set `trace_sample_rate` to a value between `0.0` and `1.0` to override it.

Spans follow the data path rather than the connection. Dispatchers that support tracing (currently the NATS producer) emit a short span per published record and inject the W3C trace context into the outgoing message headers, so downstream consumers join the same trace. There is intentionally no per-connection span: a websocket can stay open for many hours, which makes a single session span unqueryable and right-censors "currently connected" trace queries. Connection lifecycle is surfaced through the `socket_connected` / `socket_disconnected` logs and the `num_connected_sockets` metric instead.

## Example Configurations

### Local Development with Jaeger
```json
{
  "monitoring": {
    "otel": {
      "endpoint": "localhost:4317",
      "protocol": "grpc",
      "insecure": true,
      "tracing": true,
      "trace_sample_rate": 1.0
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
      "logging": true,
      "tracing": true,
      "trace_sample_rate": 0.01
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
      "service_name": "fleet-telemetry",
      "tracing": true
    }
  }
}
```

## Notes

- The OpenTelemetry metrics adapter is mutually exclusive with Prometheus and StatsD. Only one metrics backend can be active at a time; OTLP logging and tracing are enabled independently by `logging` and `tracing`.
- If the OpenTelemetry metrics exporter connection fails, the server will fall back to a no-op metrics collector and log a warning.
- The `service_name` is attached as a resource attribute to all metrics, logs, and traces, making it easy to filter in your observability backend.
