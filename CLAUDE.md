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

## Configuration

Server config example: `examples/server_config.json`

Key configuration fields:
- `records`: Maps record types to dispatcher arrays
- `reliable_ack_sources`: Maps record types to single dispatcher for ack confirmation
- `namespace`: Topic prefix for message routing
- `transmit_decoded_records`: true for JSON output, false for protobuf
