# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Tesla Fleet Telemetry is a Go server reference implementation for Tesla's telemetry protocol. Vehicles connect via WebSocket with TLS client certificates, send Flatbuffers-encoded telemetry, and the server dispatches data to configurable backends (Kafka, Kinesis, Google Pub/Sub, MQTT, NATS, ZMQ, or logger).

This is **Teslemetry's fork** of `teslamotors/fleet-telemetry`. The valuable knowledge here is fork-specific: how we cut releases, and where we diverge from upstream. NATS is the **only dispatcher we run in production** - the others exist for upstream parity. Changes that would conflict with a future `teslamotors/main` merge are worth flagging in the PR description so a human can weigh the tradeoff.

## Conventions

- Code comments explain the WHY in isolation, concisely: the constraint, trade-off, or invariant a reader needs. History narration and investigation/incident/PR-number context belong in the PR body, never in the code.

## Build, Test & Toolchain

```bash
make build            # binary -> $GOPATH/bin/fleet-telemetry
make test             # package tests (excludes test/integration; includes embedded-NATS e2e)
make test-race        # with race detector
make format           # must produce no diff
make linters          # golangci-lint
make vet
make integration      # docker-compose backends (opt-in, see CI Notes)
make generate-protos  # regenerate Go/Python/Ruby protobuf

go test ./telemetry -run TestName -v   # single test
go test -cover ./config                # coverage
```

Run `make format && make linters && make test` after every change - this mirrors the `build` CI job.

**Toolchain:** `go.mod` requires `go 1.26.0`. Older system Go (pre-1.21) can't parse the `go 1.26.0` directive at all - toolchain auto-switch didn't exist before 1.21. If `go build` fails with `invalid go version`, install a matching toolchain rather than assuming the repo is broken:
```bash
curl -sL https://go.dev/dl/go1.26.0.linux-amd64.tar.gz | tar -C /tmp/goroot -xzf -
export PATH=/tmp/goroot/go/bin:$PATH
```
`golangci-lint` isn't preinstalled; CI pins `v2.12.2` - `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`. Config is v2 format (`.golangci.yml` starts with `version: "2"`).

**macOS deps:** `brew install librdkafka pkg-config libsodium zmq`. On libcrypto errors, add your OpenSSL pkgconfig dir to `PKG_CONFIG_PATH`.

## Architecture

```
Vehicles (WebSocket/TLS) → server/streaming → telemetry/record → datastore/* dispatchers → Backends
```

- **cmd/main.go**: entry point - loads config, inits TLS, starts server, handles graceful drain.
- **config/**: central config for all dispatchers and server settings.
- **server/streaming/**: WebSocket server and per-vehicle connections (`socket.go`).
- **telemetry/**: core types - `Producer` interface, `Record`, serialization.
- **datastore/**: dispatcher implementations (kafka, kinesis, googlepubsub, mqtt, nats, zmq, simple).
- **connector/**: pluggable data connectors that gate a vehicle's connection (e.g. `vin_allowed`) - see "Data connectors" below.
- **messages/**: Flatbuffers schemas, identity handling.
- **protos/**: Protocol Buffer definitions for vehicle data types.
- **metrics/**: Prometheus and StatsD adapters.

**Record types** (routed to dispatchers via `records` in config.json): `V` (telemetry), `alerts`, `errors`, `connectivity` (connection state changes).

**Adding a dispatcher:** implement `telemetry.Producer` (Close, Produce, ProcessReliableAck, ReportError), add config handling in `config/config.go`, create `datastore/<name>/`, add integration tests.

**Testing framework:** Ginkgo v2 + Gomega (`Describe/Context/It`). `datastore/nats/nats_e2e_test.go` runs an embedded in-process NATS server, so it's part of plain `make test`.

## Configuration

Example: `examples/server_config.json`. Key fields:
- `records`: record type → dispatcher array.
- `reliable_ack_sources`: record type → single dispatcher for ack confirmation.
- `namespace`: topic/subject prefix.
- `transmit_decoded_records`: `true` for JSON output, `false` for protobuf.

## Releases & CI

`.github/workflows/build.yml` `build` job (every push/PR): proto-gen check → format check → `golangci-lint-action` → `make linters` → `make test`. A failing step aborts the rest, so a red run can mask later failures.

**Release path:** `release-binary.yml` runs only on `workflow_dispatch` with a `version` and an exact `sha` input - there is no `release: created` trigger. It runs behind the `production` GitHub environment (required reviewer: repo admin) and pauses for approval before anything executes. Once approved it runs the same proto-gen/format/lint/test gate as `build.yml`'s `build` job against that exact commit, builds the `linux-amd64` binary, computes its sha256, publishes the GitHub Release (tag targets the given `sha`), and opens a PR in `Teslemetry/servers` bumping `fleet_telemetry_version`/`fleet_telemetry_sha256` in `roles/fleet_telemetry/defaults/main.yaml` - that PR goes through its own review and deploy window, so the binary cut and the host rollout stay distinct steps. The servers PR step needs a `SERVERS_REPO_TOKEN` secret (cross-repo PAT/App token, contents+PRs write on `servers`); without it the workflow logs the pin-bump values to the run summary instead of silently skipping. There is deliberately **no `publish.yml`**: don't recreate one; if Docker publishing is ever wanted, it must trigger `release-binary.yml`'s asset-producing path, not a parallel release.

**Integration tests are opt-in.** The `integration` job runs only on `workflow_dispatch` or when a PR carries the `run-integration-tests` label (which needs `labeled` in the `pull_request` `types:` list to retrigger on an already-open PR). It's gated off the default path because 5 of its 9 containers (zookeeper, kafka, mqtt, pubsub, kinesis) only exercise dispatchers we don't run - NATS is covered by `make test`'s embedded-server specs instead and isn't in `test/integration` at all.

Sharp edges in the integration/backend setup:
- `cloud.google.com/go/pubsub` (v1) is deprecated for `.../pubsub/v2`; suppressed with `//nolint:staticcheck` scoped to each import line until someone does the v2 migration. Don't blanket-disable staticcheck.
- `test/integration/Dockerfile`'s base image Go version must track `go.mod`'s `go` directive - official `golang` images ship `GOTOOLCHAIN=local`, so a mismatch fails `go mod download` outright.
- `docker-compose.yml`'s `kinesis` is pinned to `localstack/localstack:3.8`; newer tags refuse to start without a paid `LOCALSTACK_AUTH_TOKEN`. Don't float back to `:latest`.
- `datastore/googlepubsub`'s `Produce` publishes **every** record type to one topic named after `namespace` (not `namespace_<recordtype>` like kafka/mqtt/zmq/kinesis); `test/integration` subscribes once and filters by the `txtype` message attribute.
- `test/integration/config.json` binds `profiler_host`/`prometheus_metrics_host` to `0.0.0.0` because its HTTP checks run from a separate container. Production defaults these to `127.0.0.1` (`server/monitoring/metrics_server.go`) for security - don't copy the `0.0.0.0` into production config.

## OpenTelemetry conventions

- **Scope name is always `"fleet-telemetry"`** (`otelapi.Tracer("fleet-telemetry")`). Reuse it; don't introduce per-package scope names.
- Tracing and the global `TextMapPropagator` (W3C `traceparent`/`tracestate` + baggage) are configured once in `telemetry/tracing.NewProvider` (gated by `Monitoring.OpenTelemetry.Tracing`), which runs before producers are built. Producers just call `otelapi.GetTextMapPropagator().Inject(ctx, carrier)` - a real propagator when tracing is on, a no-op when off. No need to thread config through each dispatcher.
- The NATS producer creates a **PRODUCER span per publish** and injects trace context into `nats.Msg.Header` (via `natsHeaderCarrier`). Each publish is its own short root trace so consumers (api/cache/webhook) still join a real trace.
- **No per-connection span** (`server/streaming/socket.go` `ProcessTelemetry`). A vehicle can hold a websocket open for hours, which right-censors "currently connected" trace queries and can silently truncate long sessions at OTel's default span event limit. Connection-lifecycle and message-count debugging is served instead by the `socket_disconnected` log (`close_reason`, `duration_sec`, `RecordsStats`) and the `num_connected_sockets` metric. Do not reintroduce a connection-lifetime span.
- **Log/trace correlation:** use `logger.Logger.WithContext(ctx)` wherever a log line is emitted inside an active span - it lets the OTel log hook stamp `trace_id`/`span_id` natively and as plain fields. Connection-lifecycle logs are intentionally not span-correlated (there's no connection span); they carry `close_reason`/`RecordsStats` instead.

## Connection teardown & shutdown

- **`isExpectedDisconnect`** (`server/streaming/socket.go`) classifies benign teardown errors: `websocket.ErrCloseSent`, `net.ErrClosed`, `websocket.CloseError` codes 1000/1001/1005/1006, the `crypto/tls` "failed to send closeNotify alert" message, and raw TCP `ECONNRESET` (matched via `errors.Is(err, syscall.ECONNRESET)`, since a lossy cellular link can drop with a bare RST and no close frame). **Extend this allowlist** rather than reverting to blanket `ErrorLog` when new benign teardown strings appear.
- Teardown logging is **deduplicated onto the single `socket_disconnected` line**. `sm.recordCloseReason(err)` (mutex-guarded, first-error-wins, since the read loop, writer goroutine, and `Close()` can each observe a teardown error) records the string, and `Close()` attaches it as `close_reason`. Genuinely unexpected errors still get their own `ErrorLog` (`socket_err`/`websocket_close_err`) on top of feeding `close_reason`. `RecordsStatsToLogInfo` emits int values (not strings) so ClickHouse can aggregate without casts.
- **Graceful drain on SIGTERM/SIGINT** (`cmd/main.go`): `signal.NotifyContext` cancels a context `startServer` selects on. `gracefulShutdown` calls `server.Shutdown` (hijacked websockets aren't tracked by net/http, so this returns fast), then `registry.CloseAllSockets()` → `sm.RequestClose()` on each socket (closing the ws unblocks its read loop into normal teardown, dispatching in-flight records). `waitForSocketsDrain` polls `NumConnectedSockets()` until 0 or `shutdownDrainTimeout` (25s). `RequestClose` records `errServerShutdown` (`"server_shutdown"`) as the close reason. A second signal during drain hard-exits (`startServer` calls the `NotifyContext` stop fn). A signal-driven shutdown returns nil so deferred `shutdownFuncs`/`provider.Shutdown()` flush batched spans; only genuine serve faults `panic` so airbrake's `NotifyOnPanic` fires. **Do not restore an unconditional `panic(startServer(...))`** - that skips the drain and deferred flushes, dropping in-flight telemetry and buffered spans.

## VIN-spoof observability

`telemetry.Record.applyProtoRecordTransforms` always overwrites a payload's claimed `Vin` with the connection-authenticated `record.Vin` (all four record arms do `message.Vin = record.Vin`) - a silent correction, not a drop. The `connectivity` arm additionally calls `record.logVinMismatch(...)` to emit a `WARN "unexpected_vin"` (fields: `socket_id`, `txid`, `record_type`, `claimed_vin`, `connection_vin`) when a non-empty claimed VIN differs from the authenticated one. Rate-capped to once per connection via `BinarySerializer.ShouldLogVinMismatch()` (an `atomic.Bool` on the per-connection serializer). If extended to the `V`/`alerts`/`errors` arms, reuse the same helper and per-connection cap.

## Data connectors (`connector/`)

Pluggable checks gate a vehicle's websocket accept in `server/streaming/server.go`'s `isConnectionAllowed` - currently just the `vin_allowed` capability. Default off: with no `data_connectors` config block (or a hand-built `config.Config` with `DataConnector` left nil, as in most `server/streaming` tests), every VIN is admitted. Config surface and behavior are documented in `connector/README.md`.

Adapters: `file` (watches a JSON allowlist) and `nats` (request-reply over its own dedicated NATS connection - not `datastore/nats`'s publish-producer connection, since that one only exists when NATS is configured as a record dispatcher, and its `natsConn` field is unexported anyway). The NATS adapter's wire contract - subject `vin_allowed`, JSON request `{"vin":...}`, JSON reply `{"allowed":...}`, 1s timeout (`connectornats.VinAllowedTimeout`, a var so tests can shrink it) - is pinned to match an externally-deployed responder; changing it is a breaking cross-service change. It fails OPEN (admits the vehicle) on any check failure - no responder, timeout, or malformed reply - logging `nats_connector_vin_allowed_fail_open` and incrementing `data_connector_nats_fail_open_count`, since customer telemetry availability outranks enforcement latency and denying is best-effort anyway.

This fork drops upstream's `grpc`/`http`/`redis` connector adapters to avoid promoting `google.golang.org/grpc` to a direct dependency and pulling in an unused `redis` client; pull them back from upstream commit `1371902` if a future capability genuinely needs one.

## NATS test harness (`datastore/nats/`)

`datastore/nats` is our only production dispatcher, covered end-to-end by an **in-process embedded NATS server** (`nats-io/nats-server/v2`, test-only) rather than docker-compose - so it runs in plain `make test` with no Docker, in ~2-4s. Pinned to `v2.10.29`; don't bump it opportunistically outside a dedicated change.

- **Build `*telemetry.Record`s the real way:** `messages.StreamMessage{...}.ToBytes()` → `telemetry.NewRecord(...)`, as `datastore/mqtt/mqtt_test.go` does. This exercises the real decode + `applyRecordTransforms` path (VIN stamping/warning); don't hand-construct a `Record`. `BinarySerializer.Deserialize` skips the sender-ID check when `DispatchRules[txType]` is populated, so tests only need that entry, not an exact `SenderID`.
- **Clean `Producer.Close()` must not panic.** `NatsConnect`'s `ClosedHandler` panics on an unexpected CLOSED transition, but `Close()` sets an `*atomic.Bool` `closing` field *before* tearing down `natsConn`, so the handler only panics on a genuinely unexpected close. The flag is written synchronously before the underlying `nats.Conn.Close()`, so it's correct regardless of callback races. Regression covered by `nats_close_test.go` in a **subprocess** (a panic in nats.go's async callback goroutine can't be `recover()`-ed and would crash the whole test binary).
- **`hook.LastEntry()` is unreliable here:** NATS connection-state handlers (`nats_connected`/`reconnected`/`disconnected`) log from a background goroutine and can land after the line under test. Search `hook.AllEntries()` for the expected `Message` (see `findLogEntry`).
- **Tracer delegation is process-global and one-shot:** OTel's global `TracerProvider` delegates to the first real provider exactly once (`delegateTraceOnce`), and nats.go's package-level `tracer` is vended at package init. So any spec asserting "no trace headers when tracing is unconfigured" must run *before* any spec that ever sets a real provider - handled by declaration order. Don't add a tracer-configuring spec earlier in this package's files without accounting for it.
- **The "buffers publishes across a brief server outage" spec races core NATS's lack of durability, not reconnect speed.** A restarted core-NATS server (no JetStream) only routes to subscribers already registered when the publish is processed; if the producer reconnects and flushes before the subscriber resubscribes, the message is silently dropped (normal at-most-once behavior). The fix removes the race, not the timeout: park the producer connection in `RECONNECTING` with an effectively-infinite `ReconnectWait` (via the `NatsConnect` seam), confirm the subscriber is *actually* resubscribed with a round-trip probe, then call `producerConn.ForceReconnect()` - `doReconnect` runs `resendSubscriptions()` before flushing pending items. If this flakes again, suspect this ordering race before enlarging any `Eventually` window.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
