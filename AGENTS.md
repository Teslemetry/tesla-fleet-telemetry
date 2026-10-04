# AGENTS.md

This is **Teslemetry's fork** of `teslamotors/fleet-telemetry`. **NATS is the only dispatcher we run in production**; the others exist for upstream parity. If a change would conflict with a future `teslamotors/main` merge, say so in the PR description so a human can weigh it.

## Commands

Run after every change (mirrors the CI `build` job): `make format && make linters && make test`. `make format` must produce no diff. Other targets: `build`, `test-race`, `vet`, `integration` (docker-compose), `generate-protos`.

## Environment setup

- **Go 1.26.0 from a real tarball**, `bin/` first on `PATH`. `invalid go version` means old Go, not a broken repo. Don't rely on `GOTOOLCHAIN` auto-switch: a module-cache toolchain has no `covdata`, so `make test` fails with `go: no such tool "covdata"`.
  ```bash
  mkdir -p /tmp/goroot && curl -sL https://go.dev/dl/go1.26.0.linux-amd64.tar.gz | tar -C /tmp/goroot -xzf -
  export PATH=/tmp/goroot/go/bin:$PATH
  ```
- **golangci-lint `v2.12.2`** (CI pin), built with Go 1.26: `GOTOOLCHAIN=go1.26.0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`.
- **`make generate-protos`** needs `protoc` (v5.28.3-compatible) and `protoc-gen-go` **pinned to `v1.28.1`**. A newer `protoc-gen-go` rewrites every `*.pb.go`, and CI's `git diff --exit-code` gate fails.
- **macOS:** `brew install librdkafka pkg-config libsodium zmq`; on libcrypto errors add OpenSSL's pkgconfig dir to `PKG_CONFIG_PATH`.

## Rules

- Code comments explain the WHY concisely. History, incident and PR context go in the PR body, never in code.
- **Releases** go only through `release-binary.yml` (`workflow_dispatch` with `version` + exact `sha`, behind the `production` environment). There is deliberately **no `publish.yml`**: don't create one; any Docker publishing hangs off `release-binary.yml`. The binary cut and the host rollout stay separate steps.
- **Integration tests are opt-in** (`workflow_dispatch` or the `run-integration-tests` PR label). Keep `labeled` in the `pull_request` `types:` list.
- `cloud.google.com/go/pubsub` v1 is deprecated: suppress with `//nolint:staticcheck` on each import line. Never blanket-disable staticcheck.
- `test/integration/Dockerfile`'s Go version must track `go.mod` (official images set `GOTOOLCHAIN=local`).
- Keep `kinesis` pinned to `localstack/localstack:3.8`; newer tags need a paid auth token.
- Never copy `test/integration/config.json`'s `0.0.0.0` profiler/metrics hosts into production config; production binds `127.0.0.1`.

## Observability

- OTel scope name is always `"fleet-telemetry"`. No per-package scope names.
- Tracing and the global propagator are set once in `telemetry/tracing.NewProvider`. Dispatchers call `otelapi.GetTextMapPropagator().Inject(...)`; don't thread tracing config through them.
- **No per-connection span** in `server/streaming/socket.go`. Websockets live for hours, which right-censors trace queries and hits span event limits. Use the `socket_disconnected` log and `num_connected_sockets` metric.
- Inside an active span, log with `logger.Logger.WithContext(ctx)` so `trace_id`/`span_id` are stamped.
- **Label a Counter, never a Gauge**: the otel Gauge adapter's labels key iterates an unsorted map and splits series.
- Log stat fields as ints, not strings, so ClickHouse aggregates without casts.
- **`tesla.cost` is a cross-repo contract** with the api repo: name, unit `USD`, and exactly two attributes `teslemetry.cost.type` and `teslemetry.cost.id` (the VIN). Changing any of them breaks cross-service per-vehicle sums. Price is `signals / 150000` USD; connectivity records are unbilled.

## Connections and shutdown

- `isExpectedDisconnect` (`server/streaming/socket.go`): extend the allowlist for new benign teardown errors; never revert to blanket `ErrorLog`.
- Never restore an unconditional `panic(startServer(...))` in `cmd/main.go`: it skips the graceful drain and deferred flushes, dropping in-flight telemetry and spans.
- If VIN-mismatch warnings are extended to `V`/`alerts`/`errors`, reuse `record.logVinMismatch` and the per-connection cap.

## Data connectors (`connector/`)

- The nats adapter's wire contract (subject `vin_allowed`, `{"vin":...}` → `{"allowed":...}`, timeout) is pinned to an external responder; changing it is a breaking cross-service change.
- It **fails open** on any check failure: telemetry availability outranks enforcement.
- Keep `RetryOnFailedConnect` and unlimited reconnects: FT boots before the co-located nats-server.
- A connector that is configured but fails to construct must never be nil (nil admits everyone silently).
- The fork dropped upstream's `grpc`/`http`/`redis` adapters to keep grpc and redis out of the deps. Restore from upstream commit `1371902` only if needed.

## NATS tests (`datastore/nats/`)

- The embedded `nats-server/v2` is pinned to `v2.10.29`; bump it only in a dedicated change.
- Build `*telemetry.Record`s via `messages.StreamMessage{...}.ToBytes()` → `telemetry.NewRecord(...)`; never hand-construct one.
- `hook.LastEntry()` is unreliable (connection handlers log from background goroutines); use `findLogEntry` over `hook.AllEntries()`.
- OTel's global tracer delegation is one-shot per process, so "no trace headers when tracing is off" specs must be declared before any spec that sets a provider.
- If "buffers publishes across a brief server outage" flakes, suspect the resubscribe ordering, not the `Eventually` window.

## Maintaining this file

Keep only what an agent can't learn from the code: commands, unusual setup, rules that differ from normal practice, safety boundaries, and decisions with their reason. Prefer pruning to appending.
