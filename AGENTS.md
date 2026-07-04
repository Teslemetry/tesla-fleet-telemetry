# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Tesla Fleet Telemetry is a Go server reference implementation for Tesla's telemetry protocol. Vehicles connect via WebSocket with TLS client certificates, send Flatbuffers-encoded telemetry, and the server dispatches data to configurable backends (Kafka, Kinesis, Google Pub/Sub, MQTT, NATS, ZMQ, or logger).

## Build & Development Commands

```bash
# Build binary (outputs to $GOPATH/bin/fleet-telemetry)
make build

# Run unit tests (excludes integration tests)
make test

# Run unit tests with race detection
make test-race

# Run linters (golangci-lint)
make linters

# Format code
make format

# Run go vet
make vet

# Run integration tests (requires Docker)
make integration

# Regenerate protobuf code (Go, Python, Ruby)
make generate-protos
```

### Running Individual Tests

```bash
# Run tests for a specific package
go test ./telemetry -v

# Run a specific test by name
go test ./telemetry -run TestRecordName -v

# Run with coverage
go test -cover ./config
```

### System Dependencies (macOS)

```bash
brew install librdkafka pkg-config libsodium zmq
```

If you see libcrypto errors, set PKG_CONFIG_PATH to include your OpenSSL pkgconfig directory.

## Architecture

### Data Flow

```
Vehicles (WebSocket/TLS) → server/streaming → telemetry/record → datastore/* dispatchers → Backends
```

### Key Packages

- **cmd/main.go**: Entry point - loads config, initializes TLS, starts server
- **config/**: Central configuration handling for all dispatchers and server settings
- **server/streaming/**: WebSocket server and per-vehicle connection handling (`socket.go` manages individual connections)
- **telemetry/**: Core types - `Producer` interface, `Record` structure, serialization
- **datastore/**: Dispatcher implementations (kafka/, kinesis/, googlepubsub/, mqtt/, nats/, zmq/, simple/)
- **messages/**: Protocol definitions - Flatbuffers schemas, identity handling
- **protos/**: Protocol Buffer definitions for vehicle data types
- **metrics/**: Prometheus and StatsD metric adapters

### Record Types

Records are configured in config.json to route to specific dispatchers:
- **V**: Vehicle telemetry data
- **alerts**: Vehicle alerts
- **errors**: Error conditions
- **connectivity**: Vehicle connection state changes

### Adding a New Dispatcher

1. Implement `telemetry.Producer` interface (Close, Produce, ProcessReliableAck, ReportError)
2. Add configuration handling in `config/config.go`
3. Create package in `datastore/[name]/`
4. Add integration tests

## Testing

Uses **Ginkgo v2** test framework with **Gomega** assertions. Tests use `Describe/Context/It` blocks.

Integration tests require Docker and spin up Kafka, Kinesis (localstack), Google Pub/Sub emulator, MQTT, Errbit, and monitoring services.

## Post-Change Checks

Run these after every code change (mirrors the CI in `.github/workflows/build.yml`):

```bash
make format           # Format code (must produce no diff)
make linters          # Run golangci-lint
make test             # Run unit tests
```

## Configuration

Server config example: `examples/server_config.json`

Key configuration fields:
- `records`: Maps record types to dispatcher arrays
- `reliable_ack_sources`: Maps record types to single dispatcher for ack confirmation
- `namespace`: Topic prefix for message routing
- `transmit_decoded_records`: true for JSON output, false for protobuf

## Go toolchain

`go.mod` requires `go 1.24.0`. Sandboxes/CI images can ship an older system Go (this repo has
been built here with a stock `go1.19`, which cannot even parse the `go 1.24.0` directive since
Go's toolchain auto-switch feature didn't exist before 1.21). If `go build`/`go test` fails with
`invalid go version`, download and extract a matching toolchain instead of assuming the repo is
broken, e.g.:
```bash
curl -sL -o /tmp/go1.24.tar.gz https://go.dev/dl/go1.24.0.linux-amd64.tar.gz
tar -C /tmp/goroot -xzf /tmp/go1.24.tar.gz   # -> /tmp/goroot/go/bin/go
export PATH=/tmp/goroot/go/bin:$PATH
```
`golangci-lint` isn't preinstalled either; CI pins `v1.64.8` (`.github/workflows/build.yml`) —
`go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8` matches it exactly.

## Upstream fork relationship

This is Teslemetry's fork of `teslamotors/fleet-telemetry` (see `git remote -v` / recent merge
commits from `teslamotors/main`). Changes that diverge structurally from upstream (e.g. anything
that would conflict with pulling a future `teslamotors/main` merge) are worth calling out
explicitly in PR descriptions so a human can judge the tradeoff.

## OpenTelemetry instrumentation conventions

- All spans/tracers use the single instrumentation scope name `"fleet-telemetry"` (see
  `otelapi.Tracer("fleet-telemetry")` in `server/streaming/socket.go` and `datastore/nats/nats.go`).
  Reuse that name for new instrumentation rather than introducing per-package scope names.
- Tracing and the global `TextMapPropagator` (W3C `traceparent`/`tracestate` +
  baggage) are both configured once in `telemetry/tracing.NewProvider` (gated by
  `Monitoring.OpenTelemetry.Tracing` in config), which runs before producers are constructed
  (`config/config_initializer.go`). Producers can therefore just call
  `otelapi.GetTextMapPropagator().Inject(ctx, carrier)` and get a real propagator when tracing is
  on, or a safe no-op when it's off — no need to thread config through each dispatcher.
- The NATS producer (`datastore/nats/nats.go`) creates a PRODUCER span per publish and injects
  trace context into NATS message headers via `nats.Msg.Header` (a `natsHeaderCarrier` adapts it
  to `propagation.TextMapCarrier`). It intentionally does NOT parent these spans under the
  long-lived `websocket_connection` span (which is a separate, out-of-scope judgement item on
  whether multi-hour spans are workable) — each publish is its own short root trace so consumers
  (api/cache/webhook) still join a real trace without inflating the connection-level span's
  descendant count. `datastore/nats` has no integration test harness (no embedded/dockerized NATS
  server in this repo) — the header/propagation logic is covered by pure-function unit tests
  instead (`datastore/nats/nats_test.go`); an end-to-end check needs a real NATS server.
- Log/trace correlation: `logger.Logger.WithContext(ctx)` (in `logger/logger.go`) returns a
  logger scoped to `ctx` — this makes the OTel log hook (`logger/otel_hook.go`) pass `ctx` to
  `otelLogger.Emit`, which is what lets the OTel SDK log bridge stamp `trace_id`/`span_id`
  natively. It also adds `trace_id`/`span_id` as plain fields for non-OTel output. Call it once
  per unit of work that has an active span (e.g. `server/streaming/socket.go` calls it right
  after starting the `websocket_connection` span, before spawning the writer goroutine, so every
  log for that connection's lifetime correlates) rather than threading `context.Context` through
  every log call site.
- `isExpectedDisconnect` in `server/streaming/socket.go` downgrades known-benign
  connection-teardown errors (`websocket.ErrCloseSent`, `net.ErrClosed`, and the
  `crypto/tls` "failed to send closeNotify alert (but connection was closed anyway)" message) from
  `ErrorLog` to `ActivityLog`. These accounted for ~99.8% of this service's ERROR-level logs in a
  ClickStack sample (`socket_err` / `websocket_close_err`) and are normal vehicle disconnects, not
  faults — extend this allowlist rather than reverting to blanket `ErrorLog` if new benign
  teardown error strings show up.
