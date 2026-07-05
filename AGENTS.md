# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Tesla Fleet Telemetry is a Go server reference implementation for Tesla's telemetry protocol. Vehicles connect via WebSocket with TLS client certificates, send Flatbuffers-encoded telemetry, and the server dispatches data to configurable backends (Kafka, Kinesis, Google Pub/Sub, MQTT, NATS, ZMQ, or logger).

## Build & Development Commands

```bash
# Build binary (outputs to $GOPATH/bin/fleet-telemetry)
make build

# Run package tests (excludes test/integration; includes embedded NATS e2e)
make test

# Run package tests with race detection
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

`make test` excludes the docker-compose tests under `test/integration`, but includes package-level end-to-end specs such as `datastore/nats/nats_e2e_test.go`, which runs an embedded NATS server in-process. `make integration` requires Docker and spins up Kafka, Kinesis (localstack), Google Pub/Sub emulator, MQTT, Errbit, and monitoring services.

## Post-Change Checks

Run these after every code change (mirrors the CI in `.github/workflows/build.yml`):

```bash
make format           # Format code (must produce no diff)
make linters          # Run golangci-lint
make test             # Run package tests, including embedded NATS e2e
```

## Configuration

Server config example: `examples/server_config.json`

Key configuration fields:
- `records`: Maps record types to dispatcher arrays
- `reliable_ack_sources`: Maps record types to single dispatcher for ack confirmation
- `namespace`: Topic prefix for message routing
- `transmit_decoded_records`: true for JSON output, false for protobuf

## CI Notes

The "Build and Test" workflow (`.github/workflows/build.yml`) runs as one job: proto-gen check, format check, `golangci-lint` (via `golangci-lint-action`, separate from the later `make linters` step), package tests, then `make integration` (docker-compose based, no external secrets needed — all backends are local emulators/containers). A step failing aborts the rest of the job, so a red run can be masking failures in later steps.

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
  `otelapi.Tracer("fleet-telemetry")` in `datastore/nats/nats.go`).
  Reuse that name for new instrumentation rather than introducing per-package scope names.
- Tracing and the global `TextMapPropagator` (W3C `traceparent`/`tracestate` +
  baggage) are both configured once in `telemetry/tracing.NewProvider` (gated by
  `Monitoring.OpenTelemetry.Tracing` in config), which runs before producers are constructed
  (`config/config_initializer.go`). Producers can therefore just call
  `otelapi.GetTextMapPropagator().Inject(ctx, carrier)` and get a real propagator when tracing is
  on, or a safe no-op when it's off — no need to thread config through each dispatcher.
- The NATS producer (`datastore/nats/nats.go`) creates a PRODUCER span per publish and injects
  trace context into NATS message headers via `nats.Msg.Header` (a `natsHeaderCarrier` adapts it
  to `propagation.TextMapCarrier`). Each publish is its own short root trace so consumers
  (api/cache/webhook) still join a real trace; there is deliberately no connection-level span for
  them to parent under (see below). The header/propagation behavior is covered both by
  pure-function unit tests (`datastore/nats/nats_test.go`) and by the embedded-server
  end-to-end harness (`datastore/nats/nats_e2e_test.go`).
- There is intentionally **no per-connection span** (`server/streaming/socket.go`
  `ProcessTelemetry`). An earlier `websocket_connection` span wrapped the whole session, but a
  vehicle can hold a websocket open for many hours (p99 ~8.76h): a single span per connection
  right-censored every "currently connected" trace query, silently truncated ~13.8% of sessions at
  the OTel default 128-event span limit, and lost the whole session on any restart. Chunking it
  (one short connect span + a rolling event-budgeted chunk span + a disconnect span) was designed
  and prototyped (see the long-spans-judge-k4 design) but ultimately rejected as not worth the
  machinery: rate limiting isn't enabled, and connection lifecycle / message-count debugging is
  already served by the `socket_disconnected` log (`close_reason`, `duration_sec`, `RecordsStats`)
  and the `num_connected_sockets` metric, while the per-publish producer spans above own the data
  path. Do not reintroduce a connection-lifetime span; if session-level tracing is ever needed,
  revisit the judge design rather than adding one span per socket.
- Log/trace correlation: `logger.Logger.WithContext(ctx)` (in `logger/logger.go`) returns a
  logger scoped to `ctx` — this makes the OTel log hook (`logger/otel_hook.go`) pass `ctx` to
  `otelLogger.Emit`, which is what lets the OTel SDK log bridge stamp `trace_id`/`span_id`
  natively, and also adds them as plain fields for non-OTel output. Use it wherever a log line is
  emitted inside an active span so it correlates to that span. Connection-lifecycle logs
  (`socket_connected` / `socket_disconnected`) are intentionally **not** span-correlated — there is
  no connection span to scope them to (see above); they carry `close_reason` / `RecordsStats` for
  debugging instead.
- `isExpectedDisconnect` in `server/streaming/socket.go` classifies known-benign
  connection-teardown errors (`websocket.ErrCloseSent`, `net.ErrClosed`, benign
  `websocket.CloseError` codes 1000/1001/1005/1006, the `crypto/tls` "failed to send closeNotify
  alert (but connection was closed anyway)" message, and raw TCP `ECONNRESET` on the read path —
  matched via `errors.Is(err, syscall.ECONNRESET)` rather than a string match, since a vehicle on a
  lossy cellular link can drop the connection with a bare RST and no websocket close frame at all).
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
- Removing the connection span also dissolved the read-error span-hygiene problem it created:
  every read error — expected teardown or not — used to call `span.RecordError`, producing ~31.8k
  `exception` events/day that were ~100% benign teardown noise while `StatusCode` stayed `Unset`.
  With no connection span, `ProcessTelemetry`'s read-error path just records the first teardown
  error via `recordCloseReason`, so it surfaces as `close_reason` on `socket_disconnected` instead
  of a span exception. `isExpectedDisconnect` still gates the log side (writer / `Close`), so keep
  extending that one allowlist as new benign teardown strings appear.
- Graceful drain on SIGTERM/SIGINT (`cmd/main.go`): `signal.NotifyContext` cancels a context that
  `startServer` selects on alongside the serve error. On signal, `gracefulShutdown` calls
  `server.Shutdown` (stops accepting; hijacked websockets are **not** tracked by net/http so this
  returns fast), then `registry.CloseAllSockets()` — which snapshots the sockets under the read lock
  and calls `sm.RequestClose()` on each outside the lock (closing the ws unblocks that socket's read
  loop into its normal deferred teardown, dispatching in-flight records and logging
  `socket_disconnected`). `waitForSocketsDrain` polls `NumConnectedSockets()` until 0 or
  `shutdownDrainTimeout` (25s). `RequestClose` records `errServerShutdown` (`"server_shutdown"`) as
  the first close reason so the drain shows up as `close_reason` on `socket_disconnected`. On signal,
  `startServer` also calls the `signal.NotifyContext` stop function so a second SIGTERM/SIGINT during
  the drain hard-exits instead of being swallowed. A signal-driven shutdown returns nil from
  `startServer` (main returns normally, deferred `shutdownFuncs`/`provider.Shutdown()` flush any
  batched publish spans); only genuine serve faults `panic` so airbrake's `NotifyOnPanic` still
  fires. Do not restore the old unconditional `panic(startServer(...))` — that skipped the drain and
  the deferred shutdown funcs on any real termination, dropping in-flight telemetry and buffered
  spans.
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

## NATS integration test harness (`datastore/nats/nats_e2e_test.go`)

- `datastore/nats` is the only dispatcher this fork actually runs in production, and until this
  harness was added it had zero end-to-end coverage (pure-function unit tests only, per the git
  history). The harness runs against a real, **in-process embedded NATS server**
  (`github.com/nats-io/nats-server/v2`, via its importable `.../v2/test` helper package,
  `test.RunServer`/`RunServerCallback`) rather than the docker-compose harness under
  `test/integration`. Chosen over docker-compose because: (1) the docker-compose stack's non-NATS
  services (kafka/kinesis/pubsub/mqtt/zookeeper) exist for dispatchers this fork doesn't run and
  are targeted for CI trimming, so adding a `nats` service there would grow the stack we're trying
  to shrink; (2) an embedded server runs in plain `make test`/`go test ./...` — no Docker
  dependency, no separate opt-in CI job, executes in ~2-4s. The tradeoff: this needs a real Go
  dependency (`nats-io/nats-server/v2`, test-only) rather than reusing the existing
  docker-compose/`test/integration` pattern; pinned to `v2.10.29`, the newest tag that still
  supports `go 1.24.0` (newer tags require `go >= 1.25`, which would force the module's `go`
  directive up — avoid bumping that opportunistically).
- Build `*telemetry.Record`s in tests the same way `datastore/mqtt/mqtt_test.go` already does:
  `messages.StreamMessage{...}.ToBytes()` → `telemetry.NewRecord(serializer, msgBytes, socketID,
  transmitDecodedRecords)`. This runs the real decode + `applyRecordTransforms` path (VIN stamping,
  VIN-mismatch warning, etc.) instead of hand-constructing a `Record` struct — don't bypass it.
  `BinarySerializer.Deserialize` skips the sender-ID/device-ID equality check whenever
  `DispatchRules` already has an entry for the message's `TxType`, so tests don't need to fuss over
  matching `SenderID` exactly as long as `dispatchRules[txType]` is populated.
- **Known issue surfaced while building this harness, not fixed here (out of scope — keep NATS
  test-harness PRs single-issue):** `datastore/nats/nats.go`'s `NatsConnect(...)` registers a
  `nats.ClosedHandler` that unconditionally `panic()`s whenever the underlying `*nats.Conn`
  transitions to the CLOSED state — including a clean, intentional `Producer.Close()` call, not
  just an unrecoverable error. Reproduced directly: calling `producer.Close()` (or the wrapped
  `nats.Conn.Close()` it delegates to) always panics, even with a nil `LastError()`.
  `cmd/main.go`'s `startServer` calls `producer.Close()` on every dispatcher during graceful
  server shutdown, so today that shutdown path panics instead of exiting cleanly — worth its own
  follow-up issue. Because of this, the test harness never calls `Producer.Close()` on a real
  `fleetnats.NewProducer`-constructed producer (only on bare `nats.Connect()`-created test
  subscriber connections, which don't carry this handler) — reaching for it in a new test will
  crash the whole `go test` binary for the package, not just fail an assertion.
- `hook.LastEntry()` (from `github.com/sirupsen/logrus/hooks/test`) is unreliable in these tests:
  the NATS client's own connection-state handlers (`nats_connected`, `nats_reconnected`,
  `nats_disconnected`, all registered in `nats.go`'s `NewProducer`) log asynchronously from a
  background goroutine and can race with — and land after — the synchronous log line a test is
  actually asserting on. Search `hook.AllEntries()` for the expected `Message` instead (see the
  `findLogEntry` helper in the test file) rather than asserting against whatever happens to be
  last.
- `go.opentelemetry.io/otel`'s global `TracerProvider` delegates to the first *real* (non-default)
  provider it's ever given exactly once per process (`internal/global/state.go`'s
  `delegateTraceOnce`, a `sync.Once`). `nats.go`'s package-level `tracer` var is obtained from that
  global at package-init time, so the first `otel.SetTracerProvider(realProvider)` call anywhere in
  a test binary permanently wires `tracer` to forward to it — a later `SetTracerProvider(noop)` to
  "restore" the previous value does not un-delegate already-vended `Tracer` handles. Consequence
  for tests: any spec asserting "no trace headers when tracing isn't configured" must run *before*
  any spec that ever configures a real `TracerProvider`, for the lifetime of the whole test binary,
  not just within its own `BeforeEach`/`AfterEach`. In this file that's handled by ordering
  (Ginkgo runs sibling leaf specs within the same container in declaration order by default) —
  don't add a new tracer-configuring spec earlier in `datastore/nats`'s test files without
  accounting for this.
