package logrus

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Logger.WithContext", func() {
	It("carries the context through to log entries without a span", func() {
		logger, hook := NoOpLogger()

		ctx := context.Background()
		logger.WithContext(ctx).ActivityLog("no_span", nil)

		Expect(hook.Entries).To(HaveLen(1))
		Expect(hook.LastEntry().Context).To(Equal(ctx))
		Expect(hook.LastEntry().Data).ToNot(HaveKey("trace_id"))
	})

	It("stamps trace_id/span_id fields when ctx holds a valid span", func() {
		logger, hook := NoOpLogger()

		traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
		Expect(err).ToNot(HaveOccurred())
		spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
		Expect(err).ToNot(HaveOccurred())

		sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
		ctx := trace.ContextWithSpanContext(context.Background(), sc)

		logger.WithContext(ctx).ActivityLog("with_span", nil)

		Expect(hook.Entries).To(HaveLen(1))
		entry := hook.LastEntry()
		Expect(entry.Data["trace_id"]).To(Equal(traceID.String()))
		Expect(entry.Data["span_id"]).To(Equal(spanID.String()))
		Expect(entry.Context).To(Equal(ctx))
	})

	It("does not mutate the original logger", func() {
		logger, hook := NoOpLogger()

		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: [16]byte{1}, SpanID: [8]byte{1}, TraceFlags: trace.FlagsSampled,
		})
		ctx := trace.ContextWithSpanContext(context.Background(), sc)

		_ = logger.WithContext(ctx)
		logger.ActivityLog("unaffected", nil)

		Expect(hook.Entries).To(HaveLen(1))
		Expect(hook.LastEntry().Data).ToNot(HaveKey("trace_id"))
	})
})
