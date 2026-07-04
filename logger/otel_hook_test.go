package logrus

import (
	"context"

	githubLogrus "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/trace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// capturingLogger records the context and record passed to the last Emit call
type capturingLogger struct {
	noop.Logger
	lastCtx    context.Context
	lastRecord log.Record
}

func (c *capturingLogger) Emit(ctx context.Context, record log.Record) {
	c.lastCtx = ctx
	c.lastRecord = record
}

var _ = Describe("OTelHook.Fire", func() {
	It("emits with a background context when the entry has none", func() {
		captured := &capturingLogger{}
		hook := &OTelHook{otelLogger: captured}

		entry := githubLogrus.NewEntry(githubLogrus.New()).WithField("hello", "world")
		entry.Message = "test_message"
		Expect(hook.Fire(entry)).To(Succeed())

		Expect(captured.lastCtx).To(Equal(context.Background()))
		Expect(captured.lastRecord.Body()).To(Equal(log.StringValue("test_message")))
	})

	It("propagates the entry's context so trace_id/span_id can be stamped by the SDK", func() {
		captured := &capturingLogger{}
		hook := &OTelHook{otelLogger: captured}

		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: [16]byte{1}, SpanID: [8]byte{1}, TraceFlags: trace.FlagsSampled,
		})
		ctx := trace.ContextWithSpanContext(context.Background(), sc)

		entry := githubLogrus.NewEntry(githubLogrus.New()).WithContext(ctx)
		entry.Message = "with_span"
		Expect(hook.Fire(entry)).To(Succeed())

		Expect(captured.lastCtx).To(Equal(ctx))
	})
})
