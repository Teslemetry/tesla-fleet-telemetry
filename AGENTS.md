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

## CI Notes

The "Build and Test" workflow (`.github/workflows/build.yml`) runs as one job: proto-gen check, format check, `golangci-lint` (via `golangci-lint-action`, separate from the later `make linters` step), unit tests, then `make integration` (docker-compose based, no external secrets needed — all backends are local emulators/containers). A step failing aborts the rest of the job, so a red run can be masking failures in later steps.

`cloud.google.com/go/pubsub` is deprecated in favor of `.../pubsub/v2`; the v1 usages are suppressed with `//nolint:staticcheck` at each import until someone does the v2 migration — don't blanket-disable staticcheck for this, keep the nolint scoped to the pubsub import lines.

`test/integration/Dockerfile`'s base image Go version must track `go.mod`'s `go` directive — the official `golang` images ship with `GOTOOLCHAIN=local`, so a mismatch fails `go mod download` outright instead of auto-fetching the right toolchain.

`docker-compose.yml`'s `kinesis` service is pinned to `localstack/localstack:3.8`: newer `localstack/localstack` tags refuse to start at all without a paid `LOCALSTACK_AUTH_TOKEN`, even to serve community-tier services like Kinesis. Don't float this image back to `:latest`.

`datastore/googlepubsub`'s `Producer.Produce` intentionally publishes **every** record type (V, connectivity, ...) to a single pubsub topic named after `namespace` (not `namespace_<recordtype>` like kafka/mqtt/zmq/kinesis) — see commit "Use custom topic". `test/integration` reflects this: it subscribes once to that shared topic and filters incoming messages by the `txtype` message attribute rather than using per-record-type topics.

`test/integration/config.json`'s `monitoring` block sets `profiler_host`/`prometheus_metrics_host` to `0.0.0.0`. Production defaults these to `127.0.0.1` (see "Fix vehicle identity spoofing and bind monitoring servers to localhost") for security, but the integration test's HTTP checks run from a separate container on the compose network and need to reach `app:4269`/`app:9090`.

Local dev note: this sandbox environment's default `go` is 1.19, too old for this module (`go 1.24.0` in go.mod). Install a matching toolchain before running `make lint`/`make test` locally.

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
  connection's chunk spans (see the connection-span model below) — each publish is its own short
  root trace so consumers (api/cache/webhook) still join a real trace without inflating a
  connection-level span's descendant count. `datastore/nats` has no integration test harness (no
  embedded/dockerized NATS server in this repo) — the header/propagation logic is covered by
  pure-function unit tests instead (`datastore/nats/nats_test.go`); an end-to-end check needs a
  real NATS server.
- Connection spans are **chunked**, not one span per session (`server/streaming/socket.go`
  `ProcessTelemetry`). A vehicle can hold a websocket open for many hours (p99 ~8.76h); a single
  span per connection right-censored every "currently connected" trace query, silently truncated
  ~13.8% of sessions at the OTel default 128-event span limit, and lost the whole session on any
  restart. Instead each connection emits: (a) one short `websocket.connect` span at accept, (b) a
  rolling `websocket.chunk` span that `rotateChunkIfNeeded` ends+restarts every `chunkMaxDuration`
  (30m) **or** `chunkMaxEvents` (100) — whichever comes first, giving each chunk a fresh 128-event
  budget — carrying the `message_received`/`rate_limit_exceeded`/`disconnect` events, and (c) one
  short `websocket.disconnect` span in `Close()` with `duration_sec`, `close_reason`, and total
  bytes. All three are independent `SpanKindServer` roots (never a shared parent — that would
  reintroduce the sampling blast-radius the design rejected); correlate them by the shared
  `connection.socket_id` (= `sm.UUID`) / `vehicle.vin` attributes, not span links. Rotation is
  time/count-gated bookkeeping (one int compare + one `time.Since`) so the per-message hot path
  stays cheap — do not add per-message allocation there. Chunk state fields (`chunkSpan`,
  `chunkStart`, `chunkEvents`, `chunkIndex`) are mutated only by the read-loop goroutine (and its
  deferred teardown, same goroutine), so they need no locking.
- Log/trace correlation: `logger.Logger.WithContext(ctx)` (in `logger/logger.go`) returns a
  logger scoped to `ctx` — this makes the OTel log hook (`logger/otel_hook.go`) pass `ctx` to
  `otelLogger.Emit`, which is what lets the OTel SDK log bridge stamp `trace_id`/`span_id`
  natively. It also adds `trace_id`/`span_id` as plain fields for non-OTel output. Because the
  connection span is chunked, the logger is re-pointed at the **current** chunk span on every
  rotation: `startChunk` stores `sm.logger.WithContext(chunkCtx)` into `sm.activeLogger` (an
  `atomic.Pointer[logrus.Logger]`), and all connection-lifetime log call sites go through
  `sm.log()` (atomic load, falls back to the base logger before any chunk starts / in unit tests).
  The atomic is required because the writer and ack goroutines read `sm.log()` concurrently with
  the read loop's rotation — do not revert to reassigning `sm.logger`, which would race under
  `-race`. Never let logs point at the ended `websocket.connect` span or a stale chunk.
- `isExpectedDisconnect` in `server/streaming/socket.go` classifies known-benign
  connection-teardown errors (`websocket.ErrCloseSent`, `net.ErrClosed`, and the
  `crypto/tls` "failed to send closeNotify alert (but connection was closed anyway)" message).
  Extend this allowlist rather than reverting to blanket `ErrorLog` if new benign teardown error
  strings show up.
- Teardown logging is deduplicated onto the single `socket_disconnected` line rather than emitted
  separately per source: `sm.recordCloseReason(err)` (mutex-guarded, first-error-wins, since the
  read loop, the writer goroutine, and `Close()`'s own `sm.Ws.Close()` can each observe a teardown
  error) records the teardown error string, and `Close()` attaches it as
  `close_reason` on `socket_disconnected` instead of also logging a standalone `socket_err` /
  `websocket_close_err` line for the expected case. Genuinely unexpected errors still get their own
  `ErrorLog` (`socket_err` / `websocket_close_err`) in addition to feeding `close_reason` — this
  cut ~55-63% of the service's total log volume (`request_start`/`request_end` were also deleted
  as redundant with `socket_disconnected`'s `duration_sec`/`RecordsStats`). `RecordsStatsToLogInfo`
  emits int values (not `strconv.Itoa` strings) so ClickHouse can aggregate them without casts.
- The same `isExpectedDisconnect` classifier gates span hygiene in `ProcessTelemetry`'s read-error
  path, now applied to the **current chunk span**: an expected disconnect gets a `disconnect` span
  event (with the close reason as an attribute) and no error status; anything else calls
  `sm.chunkSpan.RecordError(err)` and `sm.chunkSpan.SetStatus(codes.Error, "read failure")` (guarded
  by a `sm.chunkSpan != nil` nil-check). Before this, every read error — expected or not — called
  `span.RecordError`, producing ~31.8k `exception` events/day that were ~100% benign teardown noise
  while `StatusCode` stayed `Unset` on all spans. Keep the log-side and span-side classification
  using the same function so the two stay consistent as the allowlist grows.
- Graceful drain on SIGTERM/SIGINT (`cmd/main.go`): `signal.NotifyContext` cancels a context that
  `startServer` selects on alongside the serve error. On signal, `gracefulShutdown` calls
  `server.Shutdown` (stops accepting; hijacked websockets are **not** tracked by net/http so this
  returns fast), then `registry.CloseAllSockets()` — which snapshots the sockets under the read lock
  and calls `sm.RequestClose()` on each outside the lock (closing the ws unblocks that socket's read
  loop into its normal deferred teardown, so chunk/disconnect spans End and `socket_disconnected`
  logs). `waitForSocketsDrain` polls `NumConnectedSockets()` until 0 or `shutdownDrainTimeout` (25s).
  `RequestClose` records `errServerShutdown` (`"server_shutdown"`) as the first close reason so the
  drain shows up as `close_reason` on `socket_disconnected`. A signal-driven shutdown returns nil
  from `startServer` (main returns normally, deferred `shutdownFuncs`/`provider.Shutdown()` flush the
  batched spans); only genuine serve faults `panic` so airbrake's `NotifyOnPanic` still fires. Do
  not restore the old unconditional `panic(startServer(...))` — that dropped every open span on any
  real termination and never ran the shutdown funcs.
- VIN-spoof observability: `telemetry.Record.applyProtoRecordTransforms` always overwrites a
  payload's claimed `Vin` with the connection-authenticated `record.Vin` (the `V`, `alerts`,
  `errors`, and `connectivity` arms all do `message.Vin = record.Vin`) — this is a silent
  correction, not a drop. The `connectivity` arm additionally calls `record.logVinMismatch(...)`
  (`telemetry/record.go`) to emit a `WARN "unexpected_vin"` log (fields: `socket_id`, `txid`,
  `record_type`, `claimed_vin`, `connection_vin`) when the claimed VIN is non-empty and differs
  from the authenticated one — added so a future decision to actually DROP spoofed messages can be
  backed by real incidence data. It's rate-capped to once per connection via
  `BinarySerializer.ShouldLogVinMismatch()` (an `atomic.Bool` on the per-connection
  `BinarySerializer`, which is constructed once per socket in `server/streaming/server.go`) so a
  misbehaving firmware repeatedly sending a mismatched VIN can't flood the logs — this mirrors the
  log-volume-reduction lesson above. If this warning is extended to the `V`/`alerts`/`errors` arms
  too, reuse the same `logVinMismatch` helper and per-connection cap rather than adding parallel
  logic.
