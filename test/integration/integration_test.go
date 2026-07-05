package integration_test

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/teslamotors/fleet-telemetry/messages"
)

const (
	deviceID    = "device-1"
	vehicleName = "My Test Vehicle"
	location    = "(37.412374 S, 122.145867 E)"
)

var _ = Describe("Test messages", Ordered, func() {
	var (
		payload    []byte
		connection *websocket.Conn
		tlsConfig  *tls.Config
		timestamp  *timestamppb.Timestamp
	)

	BeforeAll(func() {
		var err error
		tlsConfig, err = GetTLSConfig()
		Expect(err).NotTo(HaveOccurred())
		timestamp = timestamppb.Now()

		payload = GenerateVehicleMessage(deviceID, vehicleName, location, timestamp)
	})

	BeforeEach(func() {
		connection = CreateWebSocket(tlsConfig)
	})

	AfterEach(func() {
		Expect(connection.Close()).NotTo(HaveOccurred())
	})

	// NOTE: with no non-NATS dispatcher left in this fork, there is no consumer here to read
	// back dispatched messages (NATS itself has no integration harness either - see
	// datastore/nats in CLAUDE.md). These checks are limited to the connect/decode/ack path;
	// they no longer verify what actually reaches a backend (record body, headers, or the
	// VIN-spoofing correction).
	Describe("v records", Ordered, func() {

		It("accepts a vehicle message and acks it", func() {
			defer GinkgoRecover()
			err := connection.WriteMessage(websocket.BinaryMessage, payload)
			Expect(err).NotTo(HaveOccurred())
			verifyAckMessage(connection, "V")
		})
	})

	Describe("health checks", Ordered, func() {

		It("returns 200 for mtls status", func() {
			body, err := VerifyHTTPSRequest(serviceURL, "status", tlsConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(Equal("mtls ok"))
		})

		It("returns 200 for status", func() {
			body, err := VerifyHTTPRequest(statusURL, "status")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(Equal("ok"))
		})

		It("returns 200 for prom metrics", func() {
			_, err := VerifyHTTPRequest(prometheusURL, "metrics")
			Expect(err).NotTo(HaveOccurred())
		})
	})

})

func verifyAckMessage(connection *websocket.Conn, expectedTxType string) {
	mType, msg, err := connection.ReadMessage()
	Expect(err).NotTo(HaveOccurred())
	Expect(mType).To(Equal(websocket.BinaryMessage))
	streamMessage, err := messages.StreamAckMessageFromBytes(msg)
	Expect(err).NotTo(HaveOccurred())
	Expect(streamMessage.MessageTopic).To(Equal([]byte(expectedTxType)))
	Expect(streamMessage.TXID).To(Equal([]byte("integration-test-txid")))
}

// VerifyHTTPSRequest validates API returns 200 status code
func VerifyHTTPSRequest(url string, path string, tlsConfig *tls.Config) ([]byte, error) {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	res, err := client.Get(fmt.Sprintf("https://%s/%s", url, path))
	if err != nil {
		return nil, err
	}
	Expect(res.StatusCode).To(Equal(200))
	return io.ReadAll(res.Body)
}

// VerifyHTTPRequest validates API returns 200 status code
func VerifyHTTPRequest(url string, path string) ([]byte, error) {
	res, err := http.Get(fmt.Sprintf("http://%s/%s", url, path))
	if err != nil {
		return nil, err
	}

	Expect(res.StatusCode).To(Equal(200))

	// nolint:errcheck
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}
