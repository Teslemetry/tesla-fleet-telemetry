# Data Connectors

Data connectors are pluggable sources of supplemental data that enhance server
functionality. With none configured, every capability check passes through
(e.g. `vin_allowed` admits every VIN) - the feature ships default off.

Currently available capabilities:
- `vin_allowed`: check whether a VIN is allowed to connect to the server. Checked once
  per incoming websocket connection, before it's accepted.

## Available Connectors

### File

Reads an allowlist from a JSON file. Watches the file for changes at runtime.

- `capabilities`: `[]string` capabilities to use the data connector for.
- `path`: `string` path of the file to watch.

**Example File**:
```json
{
    "allowed_vins": ["VIN1"]
}
```

**Example Config**:
```json
{
    "data_connectors": {
        "file": {
            "capabilities": ["vin_allowed"],
            "path": "path/to/file"
        }
    }
}
```

### NATS

Checks capabilities over a NATS request-reply round trip against an externally-deployed
responder, using its own dedicated NATS connection (independent of whether NATS is also
configured as a record dispatcher under the top-level `nats` config block).

For `vin_allowed`, the connector publishes a request on subject `vin_allowed` with body
`{"vin":"<vin>"}` and expects a reply `{"allowed":true|false}` within 1 second. The
request carries a fabricated W3C `traceparent` header (this connector runs with no OTel
tracer of its own) so an OTel-instrumented responder joins the check into one trace
instead of starting a disconnected root; the fail-open log includes the same trace id
for pivoting from an FT log line to that trace. Any failure to get a well-formed reply
in time - no responder, a timeout, or a malformed body - **fails open**: the vehicle is
admitted, a `nats_connector_vin_allowed_fail_open` error is logged, and the
`data_connector_nats_fail_open_count` metric is incremented. Customer telemetry
availability outranks enforcement latency here, and cleaning up an already-admitted,
disallowed vehicle is best-effort.

- `capabilities`: `[]string` capabilities to use the data connector for.
- `url`: `string` NATS server URL.
- `name`: `string` connection name reported to the NATS server (optional).

**Example Config**:
```json
{
    "data_connectors": {
        "nats": {
            "capabilities": ["vin_allowed"],
            "url": "nats://localhost:4222",
            "name": "fleet-telemetry-vin-allowed"
        }
    }
}
```

**Enable prerequisite**: don't turn this on until a `vin_allowed` responder is deployed
and subscribed on the NATS bus - the fail-open behavior means enabling it without a
responder is a no-op (every vehicle is admitted, one fail-open metric increment per
connection), but the responder should exist first so enforcement actually takes effect.
