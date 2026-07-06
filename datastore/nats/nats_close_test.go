package nats_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"

	fleetnats "github.com/teslamotors/fleet-telemetry/datastore/nats"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/server/airbrake"
)

// closeSubprocessEnvVar re-execs this same test binary to run only
// runCloseSubprocessBody, mirroring the standard os/exec TestHelperProcess
// pattern. This is necessary because nats.go's ClosedHandler runs
// asynchronously on the client's internal callback dispatcher goroutine (see
// nats.Conn.close's nc.ach.push in the vendored nats.go source), not on the
// goroutine that called Close(). A panic there is a panic in a goroutine this
// test can't recover from with a plain defer/recover - it crashes the whole
// `go test` process, taking the entire Ginkgo suite in this package down with
// it. Running the repro in a subprocess contains the crash (if the bug
// regresses) to that subprocess's own non-zero exit instead.
const closeSubprocessEnvVar = "FLEET_TELEMETRY_NATS_CLOSE_SUBPROCESS"

// TestProducerCloseDoesNotPanic is a regression test for a bug where
// datastore/nats's ClosedHandler unconditionally panicked whenever the
// underlying *nats.Conn transitioned to CLOSED - including a clean,
// intentional Producer.Close() call (see the NATS section of AGENTS.md).
func TestProducerCloseDoesNotPanic(t *testing.T) {
	if os.Getenv(closeSubprocessEnvVar) == "1" {
		runCloseSubprocessBody(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestProducerCloseDoesNotPanic$", "-test.v") //nolint:gosec
	cmd.Env = append(os.Environ(), closeSubprocessEnvVar+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(output), "panic:") {
		t.Fatalf("Producer.Close() panicked in a subprocess (err: %v):\n%s", err, output)
	}
}

func runCloseSubprocessBody(t *testing.T) {
	srv := natstest.RunServer(&natsserver.Options{Port: -1, Host: "127.0.0.1", NoLog: true, NoSigs: true})
	defer func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	}()

	logger, _ := logrus.NoOpLogger()
	collector := metrics.NewCollector(nil, logger)
	airbrakeHandler := airbrake.NewAirbrakeHandler(nil)

	producer, err := fleetnats.NewProducer(&fleetnats.Config{URL: srv.ClientURL(), Name: "close-repro"}, "telemetry", false, collector, airbrakeHandler, nil, nil, logger)
	if err != nil {
		t.Fatalf("failed to create producer: %v", err)
	}

	if err := producer.Close(); err != nil {
		t.Fatalf("Close returned an error: %v", err)
	}

	// The ClosedHandler callback runs asynchronously; give it time to fire
	// before the subprocess exits so an unfixed regression is observed here
	// rather than racing process teardown.
	time.Sleep(200 * time.Millisecond)
}
