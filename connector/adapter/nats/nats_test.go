package nats_test

import (
	"regexp"
	"time"

	natsclient "github.com/nats-io/nats.go"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"

	rawlogrus "github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	connectornats "github.com/teslamotors/fleet-telemetry/connector/adapter/nats"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/metrics/adapter/noop"
)

// This exercises the NATS connector against a real, in-process NATS server
// (github.com/nats-io/nats-server/v2, embedded - no Docker/network dependency),
// the same harness datastore/nats/nats_e2e_test.go uses, so it runs in plain
// `make test`.

// startNatsServer starts an in-process NATS server and waits until it is
// ready for client connections.
func startNatsServer() *natsserver.Server {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1}
	opts.NoLog = true
	opts.NoSigs = true
	return natstest.RunServer(opts)
}

// respondOnce subscribes to vin_allowed and replies with body to the first
// request received.
func respondOnce(nc *natsclient.Conn, body []byte) {
	sub, err := nc.Subscribe("vin_allowed", func(msg *natsclient.Msg) {
		Expect(msg.Respond(body)).To(Succeed())
	})
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = sub.Unsubscribe() })
}

// respondOnceCapturing behaves like respondOnce but also hands the received
// request back over the returned channel, so a test can inspect its headers.
func respondOnceCapturing(nc *natsclient.Conn, body []byte) <-chan *natsclient.Msg {
	received := make(chan *natsclient.Msg, 1)
	sub, err := nc.Subscribe("vin_allowed", func(msg *natsclient.Msg) {
		received <- msg
		Expect(msg.Respond(body)).To(Succeed())
	})
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = sub.Unsubscribe() })
	return received
}

// traceparentPattern matches a well-formed W3C traceparent: version "00", a
// 32-hex-digit trace id, a 16-hex-digit span id, and trailing flags.
var traceparentPattern = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

var _ = Describe("Connector", func() {
	var (
		server        *natsserver.Server
		responderConn *natsclient.Conn
		logger        *logrus.Logger
		hook          *logrustest.Hook
	)

	BeforeEach(func() {
		server = startNatsServer()
		DeferCleanup(server.Shutdown)

		var err error
		responderConn, err = natsclient.Connect(server.ClientURL())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(responderConn.Close)

		logger, hook = logrus.NoOpLogger()
		connectornats.VinAllowedTimeout = 200 * time.Millisecond
	})

	newConnector := func() *connectornats.Connector {
		c, err := connectornats.NewConnector(connectornats.Config{URL: server.ClientURL()}, noop.NewCollector(), logger)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(c.Close)
		return c
	}

	Context("with a responder that allows the vin", func() {
		It("returns allowed=true", func() {
			respondOnce(responderConn, []byte(`{"allowed":true}`))

			allowed, err := newConnector().VinAllowed("VIN1")
			Expect(err).NotTo(HaveOccurred())
			Expect(allowed).To(BeTrue())
		})

		It("carries a well-formed W3C traceparent header on the request", func() {
			received := respondOnceCapturing(responderConn, []byte(`{"allowed":true}`))

			_, err := newConnector().VinAllowed("VIN1")
			Expect(err).NotTo(HaveOccurred())

			var req *natsclient.Msg
			Eventually(received).Should(Receive(&req))
			traceparent := req.Header.Get("traceparent")
			Expect(traceparentPattern.MatchString(traceparent)).To(BeTrue(), "got %q", traceparent)
		})
	})

	Context("with a responder that denies the vin", func() {
		It("returns allowed=false", func() {
			respondOnce(responderConn, []byte(`{"allowed":false}`))

			allowed, err := newConnector().VinAllowed("VIN1")
			Expect(err).NotTo(HaveOccurred())
			Expect(allowed).To(BeFalse())
		})
	})

	Context("with no responder subscribed", func() {
		It("fails open: admits the vin, surfaces an error, and logs loudly", func() {
			allowed, err := newConnector().VinAllowed("VIN1")
			Expect(err).To(HaveOccurred())
			Expect(allowed).To(BeTrue())

			entry := findLogEntry(hook, "nats_connector_vin_allowed_fail_open")
			Expect(entry).NotTo(BeNil())
			Expect(entry.Data["vin"]).To(Equal("VIN1"))

			traceID, ok := entry.Data["trace_id"].(string)
			Expect(ok).To(BeTrue())
			Expect(traceID).To(MatchRegexp(`^[0-9a-f]{32}$`))
		})
	})

	Context("with a responder slower than the timeout", func() {
		It("fails open once the timeout elapses, without blocking past it", func() {
			sub, err := responderConn.Subscribe("vin_allowed", func(msg *natsclient.Msg) {
				time.Sleep(2 * connectornats.VinAllowedTimeout)
				_ = msg.Respond([]byte(`{"allowed":false}`))
			})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = sub.Unsubscribe() })

			start := time.Now()
			allowed, err := newConnector().VinAllowed("VIN1")
			Expect(err).To(HaveOccurred())
			Expect(allowed).To(BeTrue())
			Expect(time.Since(start)).To(BeNumerically("<", time.Second))
		})
	})

	Context("with a malformed reply", func() {
		It("fails open: admits the vin and surfaces an error", func() {
			respondOnce(responderConn, []byte(`not json`))

			allowed, err := newConnector().VinAllowed("VIN1")
			Expect(err).To(HaveOccurred())
			Expect(allowed).To(BeTrue())
		})
	})

	Context("connecting to an unreachable server", func() {
		It("returns an error", func() {
			_, err := connectornats.NewConnector(connectornats.Config{URL: "nats://127.0.0.1:1"}, noop.NewCollector(), logger)
			Expect(err).To(HaveOccurred())
		})
	})
})

// findLogEntry searches (rather than takes the last of) the hook's captured
// entries for one matching message, in case other background NATS handlers
// have also logged during the spec.
func findLogEntry(hook *logrustest.Hook, message string) *rawlogrus.Entry {
	for _, entry := range hook.AllEntries() {
		if entry.Message == message {
			return entry
		}
	}
	return nil
}
