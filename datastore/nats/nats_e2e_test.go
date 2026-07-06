package nats_test

import (
	"fmt"
	"net"
	"strings"
	"time"

	natsclient "github.com/nats-io/nats.go"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"

	rawlogrus "github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	fleetnats "github.com/teslamotors/fleet-telemetry/datastore/nats"
	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/messages"
	"github.com/teslamotors/fleet-telemetry/metrics"
	"github.com/teslamotors/fleet-telemetry/protos"
	"github.com/teslamotors/fleet-telemetry/server/airbrake"
	"github.com/teslamotors/fleet-telemetry/telemetry"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This file exercises the NATS producer end-to-end against a real, in-process
// NATS server (github.com/nats-io/nats-server/v2, embedded - no Docker/network
// dependency) rather than mocks. See AGENTS.md for why this shape was chosen
// over the docker-compose harness used by test/integration.

const traceparentRegexp = `^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`

// startNatsServer starts an in-process NATS server on the given options and
// waits until it is ready for client connections.
func startNatsServer(opts *natsserver.Options) *natsserver.Server {
	if opts == nil {
		opts = &natsserver.Options{}
	}
	if opts.Host == "" {
		opts.Host = "127.0.0.1"
	}
	opts.NoLog = true
	opts.NoSigs = true
	return natstest.RunServer(opts)
}

// newTestProducer wires up a nats.Producer against the given server URL using
// the same construction path config.go uses in production.
func newTestProducer(url, namespace string, logger *logrus.Logger, ackChan chan (*telemetry.Record), reliableAckTxTypes map[string]interface{}) (telemetry.Producer, error) {
	cfg := &fleetnats.Config{URL: url, Name: "fleet-telemetry-test"}
	collector := metrics.NewCollector(nil, logger)
	airbrakeHandler := airbrake.NewAirbrakeHandler(nil)
	return fleetnats.NewProducer(cfg, namespace, false, collector, airbrakeHandler, ackChan, reliableAckTxTypes, logger)
}

// buildRecord constructs a *telemetry.Record the same way the real ingest path
// does: a wire-format StreamMessage decoded through the real serializer, so
// VIN stamping and other record transforms run exactly as they do in
// production. dispatchRules routes txType to the producer(s) under test.
func buildRecord(deviceID, txType, txid string, payload []byte, dispatchRules map[string][]telemetry.Producer, logger *logrus.Logger) (*telemetry.Record, error) {
	serializer := telemetry.NewBinarySerializer(
		&telemetry.RequestIdentity{DeviceID: deviceID, SenderID: fmt.Sprintf("vehicle_device.%s", deviceID)},
		dispatchRules,
		logger,
	)

	msg := messages.StreamMessage{
		TXID:         []byte(txid),
		SenderID:     []byte(fmt.Sprintf("vehicle_device.%s", deviceID)),
		MessageTopic: []byte(txType),
		Payload:      payload,
	}
	msgBytes, err := msg.ToBytes()
	if err != nil {
		return nil, err
	}

	return telemetry.NewRecord(serializer, msgBytes, "socket-1", false)
}

func marshalVehiclePayload(vin, vehicleName string) []byte {
	payload := &protos.Payload{
		Vin: vin,
		Data: []*protos.Datum{{
			Key:   protos.Field_VehicleName,
			Value: &protos.Value{Value: &protos.Value_StringValue{StringValue: vehicleName}},
		}},
		CreatedAt: timestamppb.Now(),
	}
	b, err := proto.Marshal(payload)
	Expect(err).NotTo(HaveOccurred())
	return b
}

// findLogEntry searches (rather than takes the last of) the hook's captured
// entries for one matching message. The NATS client's connection handlers
// (nats_connected, nats_reconnected, ...) log asynchronously from a background
// goroutine, so they can race with - and land after - a synchronous log we're
// asserting on; hook.LastEntry() alone is not reliable here.
func findLogEntry(hook *logrustest.Hook, message string) *rawlogrus.Entry {
	for _, entry := range hook.AllEntries() {
		if entry.Message == message {
			return entry
		}
	}
	return nil
}

func marshalConnectivityPayload(claimedVin, connectionID string) []byte {
	payload := &protos.VehicleConnectivity{
		Vin:              claimedVin,
		ConnectionId:     connectionID,
		Status:           protos.ConnectivityEvent_CONNECTED,
		NetworkInterface: "wifi",
		CreatedAt:        timestamppb.Now(),
	}
	b, err := proto.Marshal(payload)
	Expect(err).NotTo(HaveOccurred())
	return b
}

var _ = Describe("NATS producer against a real embedded server", func() {
	var (
		srv    *natsserver.Server
		logger *logrus.Logger
		hook   *logrustest.Hook
	)

	BeforeEach(func() {
		srv = startNatsServer(&natsserver.Options{Port: -1})
		logger, hook = logrus.NoOpLogger()
	})

	AfterEach(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})

	Describe("dispatch path", func() {
		It("publishes to the correct per-vehicle subject with an intact payload", func() {
			const namespace, vin, vehicleName = "telemetry", "5YJ3E1EA3RF872290", "My Test Vehicle"

			sub, err := natsclient.Connect(srv.ClientURL())
			Expect(err).NotTo(HaveOccurred())
			defer sub.Close()
			expectedSubject := fmt.Sprintf("%s.%s.data", namespace, vin)
			subscription, err := sub.SubscribeSync(expectedSubject)
			Expect(err).NotTo(HaveOccurred())
			Expect(sub.Flush()).NotTo(HaveOccurred())

			producer, err := newTestProducer(srv.ClientURL(), namespace, logger, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			record, err := buildRecord(vin, "V", "txid-1", marshalVehiclePayload(vin, vehicleName), map[string][]telemetry.Producer{"V": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())

			record.Dispatch()

			msg, err := subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(msg.Subject).To(Equal(expectedSubject))

			var received protos.Payload
			Expect(proto.Unmarshal(msg.Data, &received)).To(Succeed())
			Expect(received.GetVin()).To(Equal(vin))
			Expect(received.GetData()).To(HaveLen(1))
			Expect(received.GetData()[0].GetValue().GetStringValue()).To(Equal(vehicleName))
		})

		It("maps the connectivity topic onto its own subject segment", func() {
			const namespace, vin = "telemetry", "5YJ3E1EA3RF872291"

			sub, err := natsclient.Connect(srv.ClientURL())
			Expect(err).NotTo(HaveOccurred())
			defer sub.Close()
			expectedSubject := fmt.Sprintf("%s.%s.connectivity", namespace, vin)
			subscription, err := sub.SubscribeSync(expectedSubject)
			Expect(err).NotTo(HaveOccurred())
			Expect(sub.Flush()).NotTo(HaveOccurred())

			producer, err := newTestProducer(srv.ClientURL(), namespace, logger, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			record, err := buildRecord(vin, "connectivity", "txid-2", marshalConnectivityPayload(vin, "conn-1"), map[string][]telemetry.Producer{"connectivity": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())

			record.Dispatch()

			msg, err := subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(msg.Subject).To(Equal(expectedSubject))
		})

		// NB: order matters here. go.opentelemetry.io/otel's global TracerProvider
		// delegates to the first real (non-default) provider it's ever given,
		// exactly once per process (see internal/global/state.go's
		// delegateTraceOnce); nats.go's package-level `tracer` var is obtained
		// from that global at package-init time, so once anything in this test
		// binary calls otel.SetTracerProvider with a real provider, `tracer`
		// forwards to it forever - a later SetTracerProvider(noop) does not
		// un-delegate already-vended Tracer handles. So the "tracing not
		// configured" case must run before the "tracing configured" Context
		// below, not after; both are siblings of the same "dispatch path"
		// container, which Ginkgo runs in declaration order by default.
		It("does not inject trace headers when tracing is not configured", func() {
			const namespace, vin = "telemetry", "5YJ3E1EA3RF872293"

			sub, err := natsclient.Connect(srv.ClientURL())
			Expect(err).NotTo(HaveOccurred())
			defer sub.Close()
			subject := fmt.Sprintf("%s.%s.data", namespace, vin)
			subscription, err := sub.SubscribeSync(subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(sub.Flush()).NotTo(HaveOccurred())

			producer, err := newTestProducer(srv.ClientURL(), namespace, logger, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			record, err := buildRecord(vin, "V", "txid-4", marshalVehiclePayload(vin, "Untraced Vehicle"), map[string][]telemetry.Producer{"V": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())

			record.Dispatch()

			msg, err := subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(msg.Header.Get("traceparent")).To(BeEmpty())
		})

		Context("when OpenTelemetry tracing is configured", func() {
			var (
				tp           *sdktrace.TracerProvider
				originalTP   oteltrace.TracerProvider
				originalProp propagation.TextMapPropagator
			)

			BeforeEach(func() {
				originalTP = otel.GetTracerProvider()
				originalProp = otel.GetTextMapPropagator()
				tp = sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
				otel.SetTracerProvider(tp)
				otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
			})

			AfterEach(func() {
				otel.SetTracerProvider(originalTP)
				otel.SetTextMapPropagator(originalProp)
			})

			It("injects a W3C traceparent header onto the published message", func() {
				const namespace, vin = "telemetry", "5YJ3E1EA3RF872292"

				sub, err := natsclient.Connect(srv.ClientURL())
				Expect(err).NotTo(HaveOccurred())
				defer sub.Close()
				subject := fmt.Sprintf("%s.%s.data", namespace, vin)
				subscription, err := sub.SubscribeSync(subject)
				Expect(err).NotTo(HaveOccurred())
				Expect(sub.Flush()).NotTo(HaveOccurred())

				producer, err := newTestProducer(srv.ClientURL(), namespace, logger, nil, nil)
				Expect(err).NotTo(HaveOccurred())

				record, err := buildRecord(vin, "V", "txid-3", marshalVehiclePayload(vin, "Traced Vehicle"), map[string][]telemetry.Producer{"V": {producer}}, logger)
				Expect(err).NotTo(HaveOccurred())

				record.Dispatch()

				msg, err := subscription.NextMsg(5 * time.Second)
				Expect(err).NotTo(HaveOccurred())
				Expect(msg.Header.Get("traceparent")).To(MatchRegexp(traceparentRegexp))
			})
		})
	})

	Describe("VIN spoofing protection", func() {
		It("overwrites a mismatched claimed VIN with the connection-authenticated VIN before publishing", func() {
			const namespace, authenticatedVin, spoofedVin = "telemetry", "5YJREALVIN000001", "5YJSPOOFED000002"

			sub, err := natsclient.Connect(srv.ClientURL())
			Expect(err).NotTo(HaveOccurred())
			defer sub.Close()
			subject := fmt.Sprintf("%s.%s.connectivity", namespace, authenticatedVin)
			subscription, err := sub.SubscribeSync(subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(sub.Flush()).NotTo(HaveOccurred())

			producer, err := newTestProducer(srv.ClientURL(), namespace, logger, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			record, err := buildRecord(authenticatedVin, "connectivity", "txid-5", marshalConnectivityPayload(spoofedVin, "conn-2"), map[string][]telemetry.Producer{"connectivity": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())
			Expect(record.Vin).To(Equal(authenticatedVin))

			record.Dispatch()

			msg, err := subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(msg.Subject).To(Equal(subject))

			var received protos.VehicleConnectivity
			Expect(proto.Unmarshal(msg.Data, &received)).To(Succeed())
			Expect(received.GetVin()).To(Equal(authenticatedVin))
			Expect(received.GetVin()).NotTo(Equal(spoofedVin))

			warnEntry := findLogEntry(hook, "unexpected_vin")
			Expect(warnEntry).NotTo(BeNil())
			Expect(warnEntry.Message).To(Equal("unexpected_vin"))
			Expect(warnEntry.Data["claimed_vin"]).To(Equal(spoofedVin))
			Expect(warnEntry.Data["connection_vin"]).To(Equal(authenticatedVin))
		})

		It("does not warn when the claimed VIN already matches the authenticated VIN", func() {
			const namespace, vin = "telemetry", "5YJMATCHINGVIN0003"

			sub, err := natsclient.Connect(srv.ClientURL())
			Expect(err).NotTo(HaveOccurred())
			defer sub.Close()
			subject := fmt.Sprintf("%s.%s.connectivity", namespace, vin)
			subscription, err := sub.SubscribeSync(subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(sub.Flush()).NotTo(HaveOccurred())

			producer, err := newTestProducer(srv.ClientURL(), namespace, logger, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			record, err := buildRecord(vin, "connectivity", "txid-6", marshalConnectivityPayload(vin, "conn-3"), map[string][]telemetry.Producer{"connectivity": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())

			record.Dispatch()

			_, err = subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())

			Expect(findLogEntry(hook, "unexpected_vin")).To(BeNil())
		})
	})

	Describe("reconnect and publish-error behavior", func() {
		It("buffers publishes across a brief server outage and delivers them once the server returns", func() {
			const namespace, vin = "telemetry", "5YJRECONNECT00004"
			port := srv.Addr().(*net.TCPAddr).Port
			url := srv.ClientURL()

			subConn, err := natsclient.Connect(url, natsclient.MaxReconnects(-1), natsclient.ReconnectWait(50*time.Millisecond))
			Expect(err).NotTo(HaveOccurred())
			defer subConn.Close()
			subject := fmt.Sprintf("%s.%s.data", namespace, vin)
			subscription, err := subConn.SubscribeSync(subject)
			Expect(err).NotTo(HaveOccurred())
			probeSubject := fmt.Sprintf("%s.%s.probe", namespace, vin)
			probeSubscription, err := subConn.SubscribeSync(probeSubject)
			Expect(err).NotTo(HaveOccurred())
			Expect(subConn.Flush()).NotTo(HaveOccurred())

			// The restarted server below is a brand-new *server.Server with no
			// memory of who was subscribed - core NATS (no JetStream, no
			// durability) only routes a publish to subscribers already
			// registered on the broker at the moment it's processed. So once
			// both subConn and the producer race to reconnect independently,
			// *whichever wins* decides the outcome: if the producer's buffered
			// publish reaches the new server before subConn has resubscribed,
			// it is delivered to no one and silently dropped - not a client bug,
			// just at-most-once pub/sub semantics. That race, not raw reconnect
			// latency, is what made this spec CI-timing-sensitive: it usually
			// resolved in the subscriber's favor locally, but had no ordering
			// guarantee, so a slower or differently-scheduled CI runner could
			// flip it. Confirmed by instrumenting both connections' reconnect
			// order in a standalone repro: delivery failed in every run where
			// the producer's ReconnectHandler fired before the subscriber's.
			//
			// Fix: take the ordering out of the scheduler's hands entirely.
			// Give the producer's connection an effectively-infinite
			// ReconnectWait (so it parks in RECONNECTING and never attempts a
			// reconnect on its own timeline) and capture the underlying
			// *nats.Conn via the fleetnats.NatsConnect seam (same injection
			// pattern the "wrong credentials" spec below already uses). Later,
			// once a probe message has round-tripped through subConn on the
			// restarted server - proving its subscription is actually
			// registered there, not just that its TCP handshake completed -
			// explicitly call producerConn.ForceReconnect() to let it reconnect
			// and flush the buffered publish. This guarantees the subscriber is
			// ready before the message can possibly be sent, rather than hoping
			// a generous timeout keeps happening to work out.
			var producerConn *natsclient.Conn
			originalNatsConnect := fleetnats.NatsConnect
			fleetnats.NatsConnect = func(url string, opts ...natsclient.Option) (*natsclient.Conn, error) {
				conn, err := originalNatsConnect(url, append(opts, natsclient.ReconnectWait(time.Hour))...)
				producerConn = conn
				return conn, err
			}
			DeferCleanup(func() {
				fleetnats.NatsConnect = originalNatsConnect
			})

			producer, err := newTestProducer(url, namespace, logger, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			// Sanity: publish works while the server is up.
			record1, err := buildRecord(vin, "V", "txid-7", marshalVehiclePayload(vin, "Before Outage"), map[string][]telemetry.Producer{"V": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())
			record1.Dispatch()
			_, err = subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())

			// Take the server down; the producer's underlying nats.Conn is
			// configured (nats.RetryOnFailedConnect + MaxReconnects(-1), see
			// datastore/nats/nats.go) to buffer rather than error during a
			// transient outage.
			srv.Shutdown()
			srv.WaitForShutdown()

			// srv.WaitForShutdown only confirms the SERVER side has torn down;
			// it says nothing about whether the producer's client has itself
			// noticed yet. Publishing before the client's read loop has detected
			// the broken socket and flipped to RECONNECTING is a race: a write
			// issued while nats.go still believes it's CONNECTED can be handed
			// straight to the (already-dead) OS socket - which the kernel may
			// still accept into its send buffer without an error - rather than
			// into nats.go's in-memory reconnect buffer, so it never actually
			// gets (re)transmitted once the server returns. Waiting for the
			// producer's own DisconnectErrHandler (nats_disconnected, see
			// datastore/nats/nats.go) closes that window deterministically.
			Eventually(func() *rawlogrus.Entry {
				return findLogEntry(hook, "nats_disconnected")
			}, 5*time.Second, 20*time.Millisecond).ShouldNot(BeNil(), "producer should notice the outage before we publish into it")

			record2, err := buildRecord(vin, "V", "txid-8", marshalVehiclePayload(vin, "During Outage"), map[string][]telemetry.Producer{"V": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())
			record2.Dispatch()
			Expect(findLogEntry(hook, "nats_err")).To(BeNil(), "a transient outage should not surface a publish error")

			// Bring the server back up on the same address. The producer's
			// connection is parked (ReconnectWait(time.Hour)) and will not
			// attempt to reconnect until we force it below, so only subConn
			// races to reconnect here.
			srv = startNatsServer(&natsserver.Options{Port: port})

			// Confirm subConn's subscription is actually registered on the new
			// server - not merely that its TCP/protocol handshake finished -
			// via a real round trip on a separate probe subject: publish a
			// probe on a throwaway connection and wait for subConn to receive it.
			probeConn, err := natsclient.Connect(url)
			Expect(err).NotTo(HaveOccurred())
			defer probeConn.Close()
			Eventually(func() error {
				if err := probeConn.Publish(probeSubject, []byte("__probe__")); err != nil {
					return err
				}
				probeMsg, err := probeSubscription.NextMsg(200 * time.Millisecond)
				if err != nil {
					return err
				}
				if string(probeMsg.Data) != "__probe__" {
					return fmt.Errorf("unexpected message on probe subject: %q", probeMsg.Data)
				}
				return nil
			}, 15*time.Second, 20*time.Millisecond).Should(Succeed(), "subConn should be resubscribed on the restarted server")

			// Only now let the producer reconnect: its subscriber-side
			// counterpart is provably ready, so the flushed publish below
			// cannot race a not-yet-registered subscription.
			Expect(producerConn.ForceReconnect()).To(Succeed())

			Eventually(func() *rawlogrus.Entry {
				return findLogEntry(hook, "nats_reconnected")
			}, 5*time.Second, 20*time.Millisecond).ShouldNot(BeNil(), "producer should reconnect once forced")

			// The buffered publish is flushed synchronously as part of the
			// reconnect handshake (resendSubscriptions then
			// flushReconnectPendingItems in nats.go's doReconnect), so delivery
			// follows within milliseconds of the reconnect above; this bound is
			// a safety margin; it is not load-bearing for correctness.
			msg, err := subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())

			var received protos.Payload
			Expect(proto.Unmarshal(msg.Data, &received)).To(Succeed())
			Expect(received.GetData()[0].GetValue().GetStringValue()).To(Equal("During Outage"))
		})

		It("logs and reports a publish error rather than swallowing it", func() {
			// A payload over the server's advertised max_payload is rejected
			// synchronously by the client (see nats.go's publish()), giving a
			// deterministic way to exercise Produce's error path without ever
			// closing the connection.
			limitedSrv := startNatsServer(&natsserver.Options{Port: -1, MaxPayload: 64})
			defer func() {
				limitedSrv.Shutdown()
				limitedSrv.WaitForShutdown()
			}()

			const namespace, vin = "telemetry", "5YJOVERSIZEDPAYLOAD07"

			producer, err := newTestProducer(limitedSrv.ClientURL(), namespace, logger, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			oversizedName := strings.Repeat("x", 512)
			record, err := buildRecord(vin, "V", "txid-9", marshalVehiclePayload(vin, oversizedName), map[string][]telemetry.Producer{"V": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())

			Expect(func() { record.Dispatch() }).NotTo(Panic())

			Expect(findLogEntry(hook, "nats_err")).NotTo(BeNil())
		})
	})

	Describe("authenticated connections", func() {
		It("connects and publishes using credentials embedded in the config URL", func() {
			authSrv := startNatsServer(&natsserver.Options{Port: -1, Username: "fleet-telemetry", Password: "s3cret"})
			defer func() {
				authSrv.Shutdown()
				authSrv.WaitForShutdown()
			}()

			const namespace, vin = "telemetry", "5YJAUTHENTICATED006"
			authedURL := fmt.Sprintf("nats://fleet-telemetry:s3cret@%s", authSrv.Addr().String())

			sub, err := natsclient.Connect(authedURL)
			Expect(err).NotTo(HaveOccurred())
			defer sub.Close()
			subject := fmt.Sprintf("%s.%s.data", namespace, vin)
			subscription, err := sub.SubscribeSync(subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(sub.Flush()).NotTo(HaveOccurred())

			producer, err := newTestProducer(authedURL, namespace, logger, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			record, err := buildRecord(vin, "V", "txid-10", marshalVehiclePayload(vin, "Authenticated Vehicle"), map[string][]telemetry.Producer{"V": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())
			record.Dispatch()

			msg, err := subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(msg.Subject).To(Equal(subject))
		})

		It("does not publish with the wrong credentials embedded in the config URL", func() {
			authSrv := startNatsServer(&natsserver.Options{Port: -1, Username: "fleet-telemetry", Password: "s3cret"})
			defer func() {
				authSrv.Shutdown()
				authSrv.WaitForShutdown()
			}()

			const namespace, vin = "telemetry", "5YJAUTHFAIL000007"
			authedURL := fmt.Sprintf("nats://fleet-telemetry:s3cret@%s", authSrv.Addr().String())
			badURL := fmt.Sprintf("nats://fleet-telemetry:wrong@%s", authSrv.Addr().String())

			sub, err := natsclient.Connect(authedURL)
			Expect(err).NotTo(HaveOccurred())
			defer sub.Close()
			subject := fmt.Sprintf("%s.%s.data", namespace, vin)
			subscription, err := sub.SubscribeSync(subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(sub.Flush()).NotTo(HaveOccurred())

			originalNatsConnect := fleetnats.NatsConnect
			fleetnats.NatsConnect = func(url string, opts ...natsclient.Option) (*natsclient.Conn, error) {
				return originalNatsConnect(url, append(opts, natsclient.Timeout(2*time.Second))...)
			}
			DeferCleanup(func() {
				fleetnats.NatsConnect = originalNatsConnect
			})

			producer, err := newTestProducer(badURL, namespace, logger, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			record, err := buildRecord(vin, "V", "txid-11", marshalVehiclePayload(vin, "Unauthorized Vehicle"), map[string][]telemetry.Producer{"V": {producer}}, logger)
			Expect(err).NotTo(HaveOccurred())
			record.Dispatch()

			_, err = subscription.NextMsg(500 * time.Millisecond)
			Expect(err).To(MatchError(natsclient.ErrTimeout))
		})
	})
})
