# Start by building the application.
FROM golang:1.23-bullseye AS build

WORKDIR /go/src/fleet-telemetry

COPY . .
ENV CGO_ENABLED=0

RUN make

# hadolint ignore=DL3006
FROM gcr.io/distroless/static-debian11:nonroot
WORKDIR /
COPY --from=build /go/bin/fleet-telemetry /

CMD ["/fleet-telemetry", "-config", "/etc/fleet-telemetry/config.json"]
