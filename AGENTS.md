# AGENTS.md

Guidance for agents working in this repository.

## Project Overview

Tesla Fleet Telemetry is a Go server reference implementation for Tesla's telemetry protocol. Vehicles connect via WebSocket with TLS client certificates, send Flatbuffers-encoded telemetry, and the server dispatches data to configurable backends (Kafka, Kinesis, Google Pub/Sub, MQTT, NATS, ZMQ, or logger).

This is **Teslemetry's fork** of `teslamotors/fleet-telemetry`. The valuable knowledge here is fork-specific: how we cut releases, and where we diverge from upstream. **NATS is the only dispatcher we run in production** - the others exist for upstream parity. Flag changes that would conflict with a future `teslamotors/main` merge in the PR description so a human can weigh the tradeoff.

## Conventions

- Code comments explain the WHY in isolation, concisely: the constraint, trade-off, or invariant a reader needs. History narration and investigation/incident/PR-number context belong in the PR body, never in the code.

## Build, Test & Toolchain

Targets are in the `Makefile`; the ones you need: `build`, `test` (excludes `test/integration`), `test-race`, `format` (must produce no diff), `linters`, `vet`, `integration` (docker-compose, opt-in), `generate-protos`.

Run `make format && make linters && make test` after every change - this mirrors the `build` CI job.

Non-obvious setup:

- **Go toolchain.** `go.mod` requires `go 1.26.0`; pre-1.21 Go can't even parse that directive (no toolchain auto-switch), so `invalid go version` means install a toolchain, not a broken repo. Install it as a real tar.gz and put its `bin/` first on `PATH`:
  ```bash
  curl -sL https://go.dev/dl/go1.26.0.linux-amd64.tar.gz | tar -C /tmp/goroot -xzf -
  export PATH=/tmp/goroot/go/bin:$PATH
  ```
  Don't rely on `GOTOOLCHAIN` auto-switch for `make test`/`make linters`: no Go distribution ships a prebuilt `covdata`, and a toolchain resolved out of the read-only module cache can't build one on demand, so `make test`'s `go test -cover` fails with `go: no such tool "covdata"` on most packages.
- **golangci-lint** isn't preinstalled; CI pins `v2.12.2` (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`). Config is v2 format. Build it with `GOTOOLCHAIN=go1.26.0` - a lower-Go-version build refuses to load a `.golangci.yml` targeting `go 1.26.0`.
- **`make generate-protos`** needs `protoc` (v5.28.3-compatible; ruby/python output is protoc-builtin) and `protoc-gen-go` **pinned to `v1.28.1`**, which is deliberately older than `go.mod`'s protobuf runtime. A newer `protoc-gen-go` rewrites every `*.pb.go`'s internal representation (opaque-API struct tags, `unsafe` import), and CI's `make generate-protos && git diff --exit-code` gate only tolerates the diff your `.proto` edit actually produces.
- **macOS deps:** `brew install librdkafka pkg-config libsodium zmq`. On libcrypto errors, add your OpenSSL pkgconfig dir to `PKG_CONFIG_PATH`.

## Architecture

```
Vehicles (WebSocket/TLS) → server/streaming → telemetry/record → datastore/* dispatchers → Backends
```

`cmd/main.go` (entry point, TLS init, graceful drain), `config/`, `server/streaming/` (websocket server + per-vehicle `socket.go`), `telemetry/` (`Producer`, `Record`, serialization), `datastore/<name>/` (dispatchers), `connector/` (connection gating), `messages/` (Flatbuffers, identity), `protos/`, `metrics/` (Prometheus + StatsD).

**Record types** (routed to dispatchers via `records` in config.json): `V` (telemetry), `alerts`, `errors`, `connectivity` (connection state changes).

**Adding a dispatcher:** implement `telemetry.Producer`, add config handling in `config/config.go`, create `datastore/<name>/`, add integration tests.

**Testing framework:** Ginkgo v2 + Gomega.

**Configuration:** see `examples/server_config.json`. `transmit_decoded_records` picks JSON (`true`) vs protobuf (`false`) output; `namespace` prefixes topics/subjects; `reliable_ack_sources` names one dispatcher per record type for ack confirmation.

## Releases & CI

`.github/workflows/build.yml` `build` job (every push/PR): proto-gen check → format check → golangci-lint → `make linters` → `make test`. A failing step aborts the rest, so a red run can mask later failures.

**Release path:** `release-binary.yml` runs only on `workflow_dispatch` with `version` + an exact `sha` - there is no `release: created` trigger. It sits behind the `production` environment (admin approval) and pauses before anything executes; then it re-runs the full `build` gate against that commit, builds `linux-amd64`, publishes the GitHub Release, and opens a PR in `Teslemetry/servers` bumping `fleet_telemetry_version`/`fleet_telemetry_sha256`. The binary cut and the host rollout stay distinct steps. The servers PR needs the `SERVERS_REPO_TOKEN` secret (cross-repo token, contents+PRs write); without it the workflow logs the pin values to the run summary rather than skipping silently. There is deliberately **no `publish.yml`** - don't create one; any Docker publishing must hang off `release-binary.yml`, not a parallel release.

**Integration tests are opt-in:** the `integration` job runs only on `workflow_dispatch` or a PR carrying the `run-integration-tests` label (`labeled` must stay in the `pull_request` `types:` list for the label to retrigger an open PR). It's off the default path because most of its containers exercise dispatchers we don't run; NATS isn't in `test/integration` at all.

Sharp edges in the integration/backend setup:
- `cloud.google.com/go/pubsub` (v1) is deprecated; suppressed with `//nolint:staticcheck` **scoped to each import line** until someone does the v2 migration. Don't blanket-disable staticcheck.
- `test/integration/Dockerfile`'s base image Go version must track `go.mod`'s `go` directive - official `golang` images ship `GOTOOLCHAIN=local`, so a mismatch fails `go mod download` outright.
- `docker-compose.yml`'s `kinesis` is pinned to `localstack/localstack:3.8`; newer tags refuse to start without a paid `LOCALSTACK_AUTH_TOKEN`. Don't float back to `:latest`.
- `datastore/googlepubsub` publishes **every** record type to one topic named after `namespace` (not `namespace_<recordtype>` like kafka/mqtt/zmq/kinesis); `test/integration` subscribes once and filters on the `txtype` attribute.
- `test/integration/config.json` binds `profiler_host`/`prometheus_metrics_host` to `0.0.0.0` because its checks run from another container. Production defaults these to `127.0.0.1` (`server/monitoring/metrics_server.go`) - don't copy `0.0.0.0` into production config.

## OpenTelemetry conventions

- **Scope name is always `"fleet-telemetry"`** (`otelapi.Tracer("fleet-telemetry")`). Don't introduce per-package scope names.
- Tracing and the global `TextMapPropagator` (W3C `traceparent`/`tracestate` + baggage) are configured once in `telemetry/tracing.NewProvider`, before producers are built. Producers just call `otelapi.GetTextMapPropagator().Inject(ctx, carrier)` - real when tracing is on, no-op when off; don't thread config through each dispatcher.
- The NATS producer creates a **PRODUCER span per publish** and injects trace context into `nats.Msg.Header`. Each publish is its own short root trace so consumers (api/cache/webhook) still join a real trace.
- **No per-connection span** in `server/streaming/socket.go`. A vehicle can hold a websocket open for hours, which right-censors "currently connected" trace queries and can silently truncate long sessions at OTel's default span event limit. Connection-lifecycle debugging is served by the `socket_disconnected` log and the `num_connected_sockets` metric instead. Do not reintroduce a connection-lifetime span.
- **Log/trace correlation:** use `logger.Logger.WithContext(ctx)` for any log line emitted inside an active span, so the OTel log hook can stamp `trace_id`/`span_id`. Connection-lifecycle logs are intentionally not span-correlated.
- **Label a Counter, never a Gauge.** The otel Gauge adapter's `labelsKey` iterates an unsorted map (`metrics/adapter/otel/gauge.go`), so one logical labelled series can silently split. `adapter.FloatCounter` (fork divergence from upstream) carries values below the integer `Counter`'s resolution; its prometheus implementation maps dotted OTel names onto Prometheus' character set, since `MustRegister` rejects them.
- **`api.client.cost` is a cross-repo contract.** Emitted beside `signal_count` at `trackSignalUsage`: each accepted `V`/`alerts`/`errors` record adds `signals / 150000` US dollars (Tesla sells 150,000 streaming signals per US$1) against the connection-authenticated VIN. Its name, unit `USD` and attribute names (including `teslemetry.cost.currency=USD`) are shared with the api repo's `src/lib/tesla-endpoint-cost.ts` so both services' entries sum into one per-vehicle figure - changing any of them is a breaking cross-service change. Connectivity records are server-generated and unbilled.

## Connection teardown & shutdown

- **`isExpectedDisconnect`** (`server/streaming/socket.go`) allowlists benign teardown errors (close codes 1000/1001/1005/1006, `net.ErrClosed`, the TLS closeNotify message, raw `ECONNRESET` from lossy cellular links, …). **Extend the allowlist** when new benign teardown strings appear; never revert to blanket `ErrorLog`.
- Teardown logging is **deduplicated onto the single `socket_disconnected` line**: `sm.recordCloseReason(err)` is mutex-guarded and first-error-wins, because the read loop, writer goroutine, and `Close()` can each observe a teardown error. Genuinely unexpected errors still get their own `ErrorLog` on top. `RecordsStatsToLogInfo` emits ints (not strings) so ClickHouse can aggregate without casts.
- **Graceful drain on SIGTERM/SIGINT** (`cmd/main.go`): `signal.NotifyContext` → `gracefulShutdown` → `server.Shutdown` (returns fast; hijacked websockets aren't tracked by net/http) → `registry.CloseAllSockets()` so each read loop unblocks into normal teardown and dispatches in-flight records → `waitForSocketsDrain` (bounded by `shutdownDrainTimeout`). A second signal hard-exits. A signal-driven shutdown returns nil so deferred `shutdownFuncs`/`provider.Shutdown()` flush batched spans; only genuine serve faults `panic`, so airbrake's `NotifyOnPanic` fires. **Do not restore an unconditional `panic(startServer(...))`** - that skips the drain and deferred flushes, dropping in-flight telemetry and buffered spans.

## VIN-spoof observability

`telemetry.Record.applyProtoRecordTransforms` always overwrites a payload's claimed `Vin` with the connection-authenticated one - a silent correction, not a drop. The `connectivity` arm additionally calls `record.logVinMismatch(...)` to emit a `WARN "unexpected_vin"`, rate-capped to once per connection via `BinarySerializer.ShouldLogVinMismatch()`. If this is extended to the `V`/`alerts`/`errors` arms, reuse the same helper and per-connection cap.

## Data connectors (`connector/`)

Pluggable checks gate a vehicle's websocket accept in `server/streaming/server.go`'s `isConnectionAllowed` - currently just `vin_allowed`. **Default off:** with no `data_connectors` config block (or `DataConnector` left nil, as in most `server/streaming` tests) every VIN is admitted. Config surface and behavior: `connector/README.md`.

Adapters are `file` (watches a JSON allowlist) and `nats`, which uses its **own dedicated NATS connection** - not `datastore/nats`'s producer connection, which only exists when NATS is configured as a record dispatcher and is unexported anyway. The nats adapter's wire contract (subject `vin_allowed`, JSON `{"vin":...}` → `{"allowed":...}`, `VinAllowedTimeout`) is pinned to an externally-deployed responder; changing it is a breaking cross-service change. It **fails OPEN** on any check failure - no responder, timeout, malformed reply - logging `nats_connector_vin_allowed_fail_open` and incrementing `data_connector_nats_fail_open_count`, since customer telemetry availability outranks enforcement latency and denying is best-effort anyway.

This fork drops upstream's `grpc`/`http`/`redis` connector adapters to keep `google.golang.org/grpc` out of the direct dependencies and avoid an unused redis client; pull them back from upstream commit `1371902` if a future capability genuinely needs one.

## NATS test harness (`datastore/nats/`)

Covered end-to-end by an **in-process embedded NATS server** (`nats-io/nats-server/v2`, test-only) rather than docker-compose, so it runs in plain `make test` with no Docker. Pinned to `v2.10.29`; don't bump it opportunistically outside a dedicated change.

- **Build `*telemetry.Record`s the real way:** `messages.StreamMessage{...}.ToBytes()` → `telemetry.NewRecord(...)` (as `datastore/mqtt/mqtt_test.go` does), which exercises the real decode + transform path. Don't hand-construct a `Record`. `Deserialize` skips the sender-ID check when `DispatchRules[txType]` is populated, so tests need only that entry.
- **A clean `Producer.Close()` must not panic.** `NatsConnect`'s `ClosedHandler` panics on an unexpected CLOSED transition; `Close()` sets the `closing` flag first so it only fires on a genuinely unexpected close. Guarded by `nats_close_test.go`, which runs in a **subprocess** because a panic in a nats.go async callback goroutine can't be recovered and would kill the test binary.
- **`hook.LastEntry()` is unreliable here:** NATS connection-state handlers log from a background goroutine and can land after the line under test. Use `findLogEntry` over `hook.AllEntries()`.
- **Tracer delegation is process-global and one-shot:** OTel's global `TracerProvider` delegates to the first real provider exactly once, and nats.go's package-level `tracer` is vended at package init. Any spec asserting "no trace headers when tracing is unconfigured" must therefore run *before* any spec that sets a real provider - currently handled by declaration order. Account for this before adding a tracer-configuring spec earlier in the package.
- **The "buffers publishes across a brief server outage" spec races core NATS's lack of durability, not reconnect speed.** A restarted core-NATS server only routes to subscribers already registered when the publish is processed, so an early reconnect+flush silently drops the message (normal at-most-once behavior). The spec removes the race by parking the producer in `RECONNECTING`, probing that the subscriber has actually resubscribed, then calling `ForceReconnect()`. If it flakes, suspect this ordering rather than enlarging any `Eventually` window.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
