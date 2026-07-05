# build executable and run integration tests
FROM golang:1.24-bullseye

ENV CGO_ENABLED=0

WORKDIR /go/src/fleet-telemetry

COPY go.* ./
RUN go mod download

COPY . ./


RUN --mount=type=cache,target=/root/.cache/go-build \
make build

WORKDIR /go/src/fleet-telemetry/test/integration

ARG it_target
ENV IT_TARGET=${it_target}

# hadolint ignore=DL3025
CMD go test -v ${IT_TARGET} --ginkgo.poll-progress-after=10s
