package nats

import (
	"context"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("subject and span naming", func() {
	It("maps the V topic to data", func() {
		Expect(normalizeTopic("V")).To(Equal("data"))
	})

	It("leaves other topics untouched", func() {
		Expect(normalizeTopic("alerts")).To(Equal("alerts"))
		Expect(normalizeTopic("errors")).To(Equal("errors"))
		Expect(normalizeTopic("connectivity")).To(Equal("connectivity"))
	})

	It("builds a per-vehicle subject", func() {
		Expect(buildSubject("telemetry", "5YJ3E1EA3RF872290", "V")).To(Equal("telemetry.5YJ3E1EA3RF872290.data"))
	})

	It("builds a low cardinality span name with the VIN wildcarded", func() {
		Expect(publishSpanName("telemetry", "V")).To(Equal("publish telemetry.*.data"))
		Expect(publishSpanName("telemetry", "alerts")).To(Equal("publish telemetry.*.alerts"))
	})
})

var _ = Describe("natsHeaderCarrier", func() {
	It("gets and sets header values", func() {
		header := make(nats.Header)
		carrier := natsHeaderCarrier{header: header}

		carrier.Set("traceparent", "00-abc-def-01")

		Expect(carrier.Get("traceparent")).To(Equal("00-abc-def-01"))
		Expect(carrier.Keys()).To(ContainElement("traceparent"))
	})

	It("round-trips a W3C trace context through NATS headers", func() {
		traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
		Expect(err).ToNot(HaveOccurred())
		spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
		Expect(err).ToNot(HaveOccurred())

		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     spanID,
			TraceFlags: trace.FlagsSampled,
		})
		ctx := trace.ContextWithSpanContext(context.Background(), sc)

		header := make(nats.Header)
		propagation.TraceContext{}.Inject(ctx, natsHeaderCarrier{header: header})

		Expect(header.Get("traceparent")).To(Equal("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"))

		extractedCtx := propagation.TraceContext{}.Extract(context.Background(), natsHeaderCarrier{header: header})
		extractedSC := trace.SpanContextFromContext(extractedCtx)

		Expect(extractedSC.TraceID()).To(Equal(traceID))
		Expect(extractedSC.SpanID()).To(Equal(spanID))
		Expect(extractedSC.IsSampled()).To(BeTrue())
	})
})
