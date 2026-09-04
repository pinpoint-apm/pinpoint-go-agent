package it

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendsAllMetadataAndCompleteSpanShapes(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	root := agent.NewSpanTracer("http.server", "/orders/42")
	require.True(t, root.IsSampled())
	rootTxID := root.TransactionId()
	rootSpanID := root.SpanId()

	root.Span().SetServiceType(pinpoint.ServiceTypeGoApp)
	root.Span().SetRemoteAddress("192.0.2.10")
	root.Span().SetEndPoint("orders.internal:8443")
	root.Span().SetAcceptorHost("api.example.test")
	root.Span().SetLogging(pinpoint.Logged)
	root.Span().SetError(errors.New("upstream unavailable"))
	root.AddMetric(pinpoint.MetricURLStat, &pinpoint.UrlStatEntry{
		Url: "/orders/{id}", Method: "GET", Status: 503,
	})

	// Every annotation shape the public API can record.
	ann := root.Span().Annotations()
	ann.AppendInt(9000, 7)
	ann.AppendLong(9001, 9000000000)
	ann.AppendString(9002, "root-value")
	ann.AppendStringString(9003, "left", "right")
	ann.AppendIntStringString(9004, 11, "one", "two")
	ann.AppendBytesStringString(9005, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, "sql", "args")
	ann.AppendLongIntIntByteByteString(9006, 123456, 1, 2, 3, 4, "network-detail")
	ann.AppendInt(pinpoint.AnnotationHttpStatusCode, 503)
	ann.AppendStringString(pinpoint.AnnotationHttpRequestHeader, "x-request-id", "request-123")

	logging := mapCarrier{}
	logging.Set(pinpoint.LogTransactionIdKey, rootTxID.String())
	logging.Set(pinpoint.LogSpanIdKey, strconv.FormatInt(rootSpanID, 10))
	assert.Equal(t, rootTxID.String(), logging[pinpoint.LogTransactionIdKey])

	outbound := root.NewSpanEvent("database.query")
	outbound.SpanEvent().SetServiceType(pinpoint.ServiceTypeMysqlExecuteQuery)
	outbound.SpanEvent().SetDestination("mysql-primary")
	outbound.SpanEvent().SetEndPoint("db.example.test:3306")
	outbound.SpanEvent().SetSQL("SELECT * FROM orders WHERE id = ?", "42")
	outbound.SpanEvent().Annotations().AppendStringString(
		pinpoint.AnnotationHttpRequestHeader, "x-client-request", "client-request-456")
	outbound.SpanEvent().SetError(errors.New("connection refused"), "DatabaseError")

	async := root.NewGoroutineTracer()
	require.True(t, async.IsSampled())
	async.SpanEvent().SetDestination("audit-queue")

	propagated := mapCarrier{}
	outbound.Inject(propagated)
	assert.Equal(t, rootTxID.String(), propagated[pinpoint.HeaderTraceId])
	assert.Equal(t, strconv.FormatInt(rootSpanID, 10), propagated[pinpoint.HeaderParentSpanId])
	assert.Equal(t, "mysql-primary", propagated[pinpoint.HeaderHost])
	require.True(t, propagated.has(pinpoint.HeaderSpanId))
	propagatedSpanID, err := strconv.ParseInt(propagated[pinpoint.HeaderSpanId], 10, 64)
	require.NoError(t, err)

	continued := agent.NewSpanTracerWithReader("continued.server", "/downstream", propagated)
	require.True(t, continued.IsSampled())
	assert.Equal(t, rootTxID.String(), continued.TransactionId().String())
	assert.Equal(t, propagatedSpanID, continued.SpanId())

	root.EndSpanEvent()
	async.EndSpan()
	continued.EndSpan()

	// Cross the event-chunk threshold so both PSpanChunk and the final PSpan
	// wire shapes are exercised in addition to the async chunk above.
	for i := 0; i < 3; i++ {
		event := root.NewSpanEvent(fmt.Sprintf("chunk.event.%d", i))
		event.SpanEvent().Annotations().AppendInt(int32(9100+i), int32(i))
		root.EndSpanEvent()
	}
	root.EndSpan()

	unsampled := agent.NewSpanTracerWithReader("not.sampled", "/unsampled", mapCarrier{
		pinpoint.HeaderSampled: "s0",
	})
	assert.False(t, unsampled.IsSampled())
	unsampled.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/orders/42") != nil &&
			findSpanByRpc(s, "/downstream") != nil &&
			// Span batches are sent concurrently, so the final PSpan can
			// overtake the chunks carrying its events.
			len(eventsForSpan(s, rootSpanID)) >= 5 &&
			len(asyncChunksFor(s, rootSpanID)) > 0 &&
			len(s.ApiMetadata) > 0 && len(s.StringMetadata) > 0 &&
			len(s.SqlUidMetadata) > 0 && len(s.ExceptionMetadata) > 0
	}, waitTimeout))

	s := mc.Snapshot()
	rootWire := findSpanByRpc(s, "/orders/42")
	require.NotNil(t, rootWire)
	assert.Equal(t, rootSpanID, rootWire.GetSpanId())
	assert.Equal(t, itAgentID, rootWire.GetTransactionId().GetAgentId())
	assert.Equal(t, "192.0.2.10", rootWire.GetAcceptEvent().GetRemoteAddr())
	assert.Equal(t, "orders.internal:8443", rootWire.GetAcceptEvent().GetEndPoint())
	assert.Equal(t, "api.example.test", rootWire.GetAcceptEvent().GetParentInfo().GetAcceptorHost())
	assert.Equal(t, int32(1), rootWire.GetErr())
	assert.Equal(t, int32(pinpoint.Logged), rootWire.GetLoggingTransactionInfo())
	require.NotNil(t, rootWire.GetExceptionInfo())
	assert.Equal(t, "upstream unavailable", rootWire.GetExceptionInfo().GetStringValue().GetValue())

	annotations := rootWire.GetAnnotation()
	require.NotNil(t, findAnnotation(annotations, 9000))
	assert.Equal(t, int32(7), findAnnotation(annotations, 9000).GetValue().GetIntValue())
	assert.Equal(t, int64(9000000000), findAnnotation(annotations, 9001).GetValue().GetLongValue())
	assert.Equal(t, "root-value", findAnnotation(annotations, 9002).GetValue().GetStringValue())
	assert.Equal(t, "right", findAnnotation(annotations, 9003).GetValue().GetStringStringValue().GetStringValue2().GetValue())
	assert.Equal(t, int32(11), findAnnotation(annotations, 9004).GetValue().GetIntStringStringValue().GetIntValue())
	assert.Len(t, findAnnotation(annotations, 9005).GetValue().GetBytesStringStringValue().GetBytesValue(), 16)
	assert.Equal(t, "network-detail",
		findAnnotation(annotations, 9006).GetValue().GetLongIntIntByteByteStringValue().GetStringValue().GetValue())
	assert.Equal(t, int32(503),
		findAnnotation(annotations, pinpoint.AnnotationHttpStatusCode).GetValue().GetIntValue())
	assert.Equal(t, "request-123",
		findAnnotation(annotations, pinpoint.AnnotationHttpRequestHeader).
			GetValue().GetStringStringValue().GetStringValue2().GetValue())
	// The operation name is published as API metadata and referenced by apiId.
	assert.Greater(t, rootWire.GetApiId(), int32(0))
	assert.True(t, hasApiMetadata(s, "http.server", apiTypeWebRequest))

	continuedWire := findSpanByRpc(s, "/downstream")
	require.NotNil(t, continuedWire)
	assert.Equal(t, rootWire.GetTransactionId().GetAgentId(), continuedWire.GetTransactionId().GetAgentId())
	assert.Equal(t, rootWire.GetTransactionId().GetAgentStartTime(), continuedWire.GetTransactionId().GetAgentStartTime())
	assert.Equal(t, rootWire.GetTransactionId().GetSequence(), continuedWire.GetTransactionId().GetSequence())
	assert.Equal(t, rootSpanID, continuedWire.GetParentSpanId())

	events := eventsForSpan(s, rootSpanID)
	dbEvent := findEventByServiceType(events, pinpoint.ServiceTypeMysqlExecuteQuery)
	require.NotNil(t, dbEvent)
	assert.NotNil(t, findAnnotation(dbEvent.GetAnnotation(), pinpoint.AnnotationSqlUid))
	assert.NotNil(t, findAnnotation(dbEvent.GetAnnotation(), pinpoint.AnnotationExceptionChainId))
	assert.NotNil(t, findAnnotation(dbEvent.GetAnnotation(), pinpoint.AnnotationHttpRequestHeader))
	assert.Equal(t, "mysql-primary", dbEvent.GetNextEvent().GetMessageEvent().GetDestinationId())

	require.NotEmpty(t, s.SqlUidMetadata)
	assert.Len(t, s.SqlUidMetadata[0].Message.GetSqlUid(), 16)

	require.NotEmpty(t, s.ExceptionMetadata)
	exception := findExceptionForSpan(s, rootSpanID)
	require.NotNil(t, exception)
	assert.Equal(t, "/orders/{id}", exception.GetUriTemplate())
	require.Len(t, exception.GetExceptions(), 1)
	assert.Equal(t, "connection refused", exception.GetExceptions()[0].GetExceptionMessage())
	assert.NotEmpty(t, exception.GetExceptions()[0].GetStackTraceElement())

	require.NotEmpty(t, s.SpanBatches)
	expectCommonMetadata(t, s.SpanBatches[0].Metadata, false)

	assert.Equal(t, 0, countSpansByRpc(s, "/unsampled"))
}

// Go's EndSpan finalizes a span whose events were left open by the
// application: the unclosed events are ended and discarded, and only the
// properly closed events reach the collector. The span itself is still
// delivered exactly once for the closed events.
func TestFinalizesSpanWithUnclosedEvents(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	tracer := agent.NewSpanTracer("span.lifecycle", "/span-lifecycle")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()

	closed := tracer.NewSpanEvent("closed.event")
	closed.SpanEvent().SetServiceType(pinpoint.ServiceTypeGoFunction)
	closed.SpanEvent().SetDestination("closed-worker")
	tracer.EndSpanEvent()

	open := tracer.NewSpanEvent("implicitly.finished.event")
	open.SpanEvent().SetServiceType(pinpoint.ServiceTypeRedis)
	open.SpanEvent().SetDestination("redis-cache")

	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return countSpansByRpc(s, "/span-lifecycle") == 1 && len(eventsForSpan(s, spanID)) >= 1
	}, waitTimeout))

	s := mc.Snapshot()
	assert.Equal(t, 1, countSpansByRpc(s, "/span-lifecycle"))
	events := eventsForSpan(s, spanID)
	require.Len(t, events, 1)
	assert.Equal(t, int32(pinpoint.ServiceTypeGoFunction), events[0].GetServiceType())
}

func TestPreservesNestedEventSequenceAndDepth(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	tracer := agent.NewSpanTracer("event.lifecycle", "/event-lifecycle")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()

	outer := tracer.NewSpanEvent("event.outer")
	outer.SpanEvent().SetServiceType(pinpoint.ServiceTypeRedis)
	outer.SpanEvent().SetDestination("outer-destination")

	inner := tracer.NewSpanEvent("event.inner")
	inner.SpanEvent().SetServiceType(pinpoint.ServiceTypeMemcached)
	inner.SpanEvent().SetDestination("inner-destination")
	inner.SpanEvent().SetEndPoint("inner.example.test:11211")
	inner.SpanEvent().SetError(errors.New("inner-error-message"), "InnerError")
	inner.SpanEvent().Annotations().AppendString(9200, "inner-annotation")
	tracer.EndSpanEvent() // inner
	tracer.EndSpanEvent() // outer

	// The two completed events cross Span.EventChunkSize=2 and are handed to
	// the gRPC worker; a third event opens a new chunk.
	later := tracer.NewSpanEvent("event.later")
	later.SpanEvent().SetServiceType(pinpoint.ServiceTypeKafkaClient)
	tracer.EndSpanEvent()
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return countSpansByRpc(s, "/event-lifecycle") == 1 && len(eventsForSpan(s, spanID)) == 3
	}, waitTimeout))

	events := eventsForSpan(mc.Snapshot(), spanID)
	require.Len(t, events, 3)
	sort.Slice(events, func(i, j int) bool { return events[i].GetSequence() < events[j].GetSequence() })

	assert.Equal(t, int32(0), events[0].GetSequence())
	assert.Equal(t, int32(1), events[0].GetDepth())
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), events[0].GetServiceType())
	assert.Equal(t, int32(1), events[1].GetSequence())
	assert.Equal(t, int32(2), events[1].GetDepth())
	assert.Equal(t, int32(pinpoint.ServiceTypeMemcached), events[1].GetServiceType())
	assert.Equal(t, int32(2), events[2].GetSequence())
	assert.Equal(t, int32(pinpoint.ServiceTypeKafkaClient), events[2].GetServiceType())

	innerWire := events[1]
	assert.Equal(t, "inner-destination", innerWire.GetNextEvent().GetMessageEvent().GetDestinationId())
	assert.Equal(t, "inner.example.test:11211", innerWire.GetNextEvent().GetMessageEvent().GetEndPoint())
	require.NotNil(t, innerWire.GetExceptionInfo())
	assert.Equal(t, "inner-error-message", innerWire.GetExceptionInfo().GetStringValue().GetValue())
	require.NotNil(t, findAnnotation(innerWire.GetAnnotation(), 9200))
	assert.Equal(t, "inner-annotation", findAnnotation(innerWire.GetAnnotation(), 9200).GetValue().GetStringValue())
}

func TestKeepsTraceContextWhenEventLimitsOverflow(t *testing.T) {
	// The smallest limits the config accepts, so the overflow paths are
	// reachable with a handful of events.
	cfg := defaultAgentConfig()
	cfg.maxCallStackDepth = 2
	cfg.maxCallStackSequence = 4
	mc, agent := startStack(t, cfg)
	require.Equal(t, 2, agent.Config().Int("Span.MaxCallStackDepth"))
	require.Equal(t, 4, agent.Config().Int("Span.MaxCallStackSequence"))

	tracer := agent.NewSpanTracer("overflow.depth", "/overflow-depth")
	require.True(t, tracer.IsSampled())
	traceID := tracer.TransactionId().String()
	spanID := tracer.SpanId()

	// MaxCallStackDepth is 2, so both of these levels are recorded: the limit is
	// the deepest level kept, not the first one dropped.
	tracer.NewSpanEvent("depth.level1").SpanEvent().SetDestination("depth-destination")
	tracer.NewSpanEvent("depth.level2").SpanEvent().SetDestination("depth-destination2")

	// The third level overflows and records nothing. An async span cannot be
	// forked from an overflowed event either.
	overflowed := tracer.NewSpanEvent("depth.level3.discarded")
	overflowed.SpanEvent().SetDestination("discarded-destination")
	assert.False(t, tracer.NewGoroutineTracer().IsSampled())

	// A depth overflow is a profiling limit, not a sampling decision: the
	// discarded event still propagates the complete trace context so the
	// distributed trace is not cut here.
	outbound := mapCarrier{}
	overflowed.Inject(outbound)
	assert.Equal(t, traceID, outbound[pinpoint.HeaderTraceId])
	assert.Equal(t, strconv.FormatInt(spanID, 10), outbound[pinpoint.HeaderParentSpanId])
	require.True(t, outbound.has(pinpoint.HeaderSpanId))

	continued := agent.NewSpanTracerWithReader("overflow.continued", "/overflow-continued", outbound)
	require.True(t, continued.IsSampled())
	assert.Equal(t, traceID, continued.TransactionId().String())
	continued.EndSpan()

	// Ending the overflowed placeholder must not desync the event stack.
	tracer.EndSpanEvent()
	tracer.EndSpanEvent()
	tracer.EndSpanEvent()
	tracer.EndSpan()

	// MaxCallStackSequence is 4: a fifth event on one span is discarded even
	// when the depth stays flat.
	seqTracer := agent.NewSpanTracer("overflow.sequence", "/overflow-sequence")
	require.True(t, seqTracer.IsSampled())
	seqSpanID := seqTracer.SpanId()
	for i := 0; i < 5; i++ {
		seqTracer.NewSpanEvent(fmt.Sprintf("seq.event.%d", i))
		seqTracer.EndSpanEvent()
	}
	seqTracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/overflow-depth") != nil &&
			findSpanByRpc(s, "/overflow-continued") != nil &&
			findSpanByRpc(s, "/overflow-sequence") != nil &&
			len(eventsForSpan(s, spanID)) >= 2 &&
			len(eventsForSpan(s, seqSpanID)) >= 4
	}, waitTimeout))

	s := mc.Snapshot()
	depthEvents := eventsForSpan(s, spanID)
	require.Len(t, depthEvents, 2)
	sort.Slice(depthEvents, func(i, j int) bool { return depthEvents[i].GetSequence() < depthEvents[j].GetSequence() })
	assert.Equal(t, "depth-destination", depthEvents[0].GetNextEvent().GetMessageEvent().GetDestinationId())
	assert.Equal(t, "depth-destination2", depthEvents[1].GetNextEvent().GetMessageEvent().GetDestinationId())
	assert.Len(t, eventsForSpan(s, seqSpanID), 4)
}

// Go treats a malformed inbound trace id as "no context": it warns and starts a
// fresh transaction rather than dropping the request. A well-formed context
// from a foreign agent is adopted verbatim.
func TestStartsNewTransactionOnMalformedContextAndAdoptsForeignContext(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	malformed := []string{
		"missing-separators",
		"agent-only^123",
	}
	for i, tid := range malformed {
		rpc := fmt.Sprintf("/malformed/%d", i)
		tracer := agent.NewSpanTracerWithReader("malformed.context", rpc, mapCarrier{
			pinpoint.HeaderTraceId: tid,
		})
		require.True(t, tracer.IsSampled(), tid)
		assert.Equal(t, itAgentID, tracer.TransactionId().AgentId, tid)
		assert.NotEqual(t, tid, tracer.TransactionId().String(), tid)
		tracer.EndSpan()
	}

	carrier := mapCarrier{
		pinpoint.HeaderTraceId:               "java-agent-7^1700000000000^42",
		pinpoint.HeaderSpanId:                "77777",
		pinpoint.HeaderParentSpanId:          "88888",
		pinpoint.HeaderParentApplicationName: "upstream-app",
		pinpoint.HeaderParentApplicationType: "1010",
		pinpoint.HeaderParentServiceName:     "upstream-svc",
		pinpoint.HeaderHost:                  "gateway.example.test",
		pinpoint.HeaderFlags:                 "1",
	}
	continued := agent.NewSpanTracerWithReader("foreign.continued", "/foreign-continued", carrier)
	require.True(t, continued.IsSampled())
	assert.Equal(t, "java-agent-7^1700000000000^42", continued.TransactionId().String())
	assert.Equal(t, int64(77777), continued.SpanId())
	continued.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/foreign-continued") != nil &&
			findSpanByRpc(s, "/malformed/0") != nil
	}, waitTimeout))

	s := mc.Snapshot()
	for i := range malformed {
		assert.Equal(t, 1, countSpansByRpc(s, fmt.Sprintf("/malformed/%d", i)))
	}
	wire := findSpanByRpc(s, "/foreign-continued")
	require.NotNil(t, wire)
	assert.Equal(t, "java-agent-7", wire.GetTransactionId().GetAgentId())
	assert.Equal(t, int64(1700000000000), wire.GetTransactionId().GetAgentStartTime())
	assert.Equal(t, int64(42), wire.GetTransactionId().GetSequence())
	assert.Equal(t, int64(77777), wire.GetSpanId())
	assert.Equal(t, int64(88888), wire.GetParentSpanId())
	assert.Equal(t, int32(1), wire.GetFlag())
	assert.Equal(t, "gateway.example.test", wire.GetAcceptEvent().GetEndPoint())
	assert.Equal(t, "gateway.example.test", wire.GetAcceptEvent().GetRemoteAddr())
	parent := wire.GetAcceptEvent().GetParentInfo()
	require.NotNil(t, parent)
	assert.Equal(t, "upstream-app", parent.GetParentApplicationName())
	assert.Equal(t, int32(1010), parent.GetParentApplicationType())
	assert.Equal(t, "gateway.example.test", parent.GetAcceptorHost())
	assert.Equal(t, "upstream-svc", parent.GetParentServiceName())
}

func TestSharesAsyncIdAcrossAsyncSpansFromOneEvent(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	tracer := agent.NewSpanTracer("async.parent", "/async-parent")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()
	traceID := tracer.TransactionId().String()

	tracer.NewSpanEvent("async.spawner")

	// Every async span forked from the same event shares its async id and takes
	// the next sequence number; all of them stay on the parent trace.
	first := tracer.NewGoroutineTracer()
	second := tracer.NewGoroutineTracer()
	require.True(t, first.IsSampled())
	require.True(t, second.IsSampled())
	assert.Equal(t, traceID, first.TransactionId().String())
	assert.Equal(t, traceID, second.TransactionId().String())
	assert.Equal(t, spanID, first.SpanId())
	assert.Equal(t, spanID, second.SpanId())

	first.EndSpan()
	second.EndSpan()
	tracer.EndSpanEvent()
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/async-parent") != nil &&
			len(asyncChunksFor(s, spanID)) >= 2 &&
			len(eventsForSpan(s, spanID)) >= 3 &&
			hasApiMetadata(s, "Goroutine Invocation", apiTypeInvocation)
	}, waitTimeout))

	s := mc.Snapshot()
	chunks := asyncChunksFor(s, spanID)
	require.Len(t, chunks, 2)
	assert.Equal(t, chunks[0].GetLocalAsyncId().GetAsyncId(), chunks[1].GetLocalAsyncId().GetAsyncId())
	sequences := []int32{chunks[0].GetLocalAsyncId().GetSequence(), chunks[1].GetLocalAsyncId().GetSequence()}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	assert.Equal(t, []int32{1, 2}, sequences)
	asyncID := chunks[0].GetLocalAsyncId().GetAsyncId()

	// Each async chunk starts with the async root event, whose apiId this agent
	// registered as "Goroutine Invocation" - the id is per-agent, so it must be
	// published by the agent that uses it.
	assert.True(t, hasApiMetadata(s, "Goroutine Invocation", apiTypeInvocation))
	for _, chunk := range chunks {
		require.GreaterOrEqual(t, len(chunk.GetSpanEvent()), 1)
		assert.Equal(t, int32(pinpoint.ServiceTypeAsync), chunk.GetSpanEvent()[0].GetServiceType())
		assert.Greater(t, chunk.GetSpanEvent()[0].GetApiId(), int32(0))
	}

	// The spawning event carries the async id so the collector can stitch the
	// async chunks under it.
	found := false
	for _, event := range eventsForSpan(s, spanID) {
		if event.GetServiceType() != pinpoint.ServiceTypeAsync && event.GetAsyncEvent() == asyncID {
			found = true
		}
	}
	assert.True(t, found, "the spawning event must carry the async id")
}

func TestFlushesExceptionMetadataForAsyncSpans(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	tracer := agent.NewSpanTracer("async.exception.parent", "/async-exception")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()

	// A goroutine tracer attaches the async id to the currently open event.
	tracer.NewSpanEvent("async.exception.spawner")
	async := tracer.NewGoroutineTracer()
	require.True(t, async.IsSampled())

	// The error is captured on the async span itself, whose EndSpan runs the
	// async branch: it must still flush exception metadata even though the
	// non-async statistics path is skipped there.
	async.SpanEvent().SetError(errors.New("async job failed"), "AsyncJobError")
	async.EndSpan()
	tracer.EndSpanEvent()
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(asyncChunksFor(s, spanID)) > 0 && findExceptionForSpan(s, spanID) != nil
	}, waitTimeout))

	s := mc.Snapshot()
	chunks := asyncChunksFor(s, spanID)
	require.Len(t, chunks, 1)
	require.GreaterOrEqual(t, len(chunks[0].GetSpanEvent()), 1)
	asyncRoot := chunks[0].GetSpanEvent()[0]
	require.NotNil(t, asyncRoot.GetExceptionInfo())
	assert.Equal(t, "async job failed", asyncRoot.GetExceptionInfo().GetStringValue().GetValue())
	assert.NotNil(t, findAnnotation(asyncRoot.GetAnnotation(), pinpoint.AnnotationExceptionChainId))

	exception := findExceptionForSpan(s, spanID)
	require.NotNil(t, exception)
	// Async spans never carry a URL stat, so the template is the literal
	// fallback value.
	assert.Equal(t, "NULL", exception.GetUriTemplate())
	require.Len(t, exception.GetExceptions(), 1)
	assert.Equal(t, "async job failed", exception.GetExceptions()[0].GetExceptionMessage())
	assert.NotEmpty(t, exception.GetExceptions()[0].GetStackTraceElement())
}

func TestWrapGoroutinePropagatesTraceIntoGoroutine(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	tracer := agent.NewSpanTracer("goroutine.parent", "/goroutine-parent")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()

	tracer.NewSpanEvent("goroutine.spawner")
	done := make(chan struct{})
	go tracer.WrapGoroutine("goroutine.worker", func(ctx context.Context) {
		defer close(done)
		worker := pinpoint.FromContext(ctx)
		assert.True(t, worker.IsSampled())
		assert.Equal(t, spanID, worker.SpanId())
		worker.NewSpanEvent("goroutine.work")
		worker.EndSpanEvent()
		worker.EndSpan()
	}, nil)()
	<-done
	tracer.EndSpanEvent()
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/goroutine-parent") != nil &&
			len(asyncChunksFor(s, spanID)) >= 1 &&
			// the async root event plus the goroutine's own event
			len(eventsForSpan(s, spanID)) >= 3
	}, waitTimeout))

	s := mc.Snapshot()
	chunks := asyncChunksFor(s, spanID)
	require.NotEmpty(t, chunks)
	asyncID := chunks[0].GetLocalAsyncId().GetAsyncId()
	for _, chunk := range chunks {
		assert.Equal(t, asyncID, chunk.GetLocalAsyncId().GetAsyncId())
	}
}

func TestPropagatesUnsampledDecisionDownstream(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return agentStatCount(s) >= 1 }, waitTimeout))
	baseline := agentStatCount(mc.Snapshot())

	tracer := agent.NewSpanTracerWithReader("unsampled.origin", "/unsampled-origin", mapCarrier{
		pinpoint.HeaderSampled: "s0",
	})
	assert.False(t, tracer.IsSampled())
	// Unlike a plain noop tracer, an unsampled span keeps a real span id so it
	// still feeds active-request and response-time statistics.
	assert.NotEqual(t, int64(0), tracer.SpanId())

	// The outbound carrier must tell downstream services to skip sampling, and
	// must not leak any trace identifiers for the untraced request.
	event := tracer.NewSpanEvent("unsampled.client")
	outbound := mapCarrier{}
	event.Inject(outbound)
	assert.Equal(t, "s0", outbound[pinpoint.HeaderSampled])
	assert.False(t, outbound.has(pinpoint.HeaderTraceId))
	assert.False(t, outbound.has(pinpoint.HeaderSpanId))
	tracer.EndSpanEvent()
	tracer.EndSpan()

	downstream := agent.NewSpanTracerWithReader("unsampled.downstream", "/unsampled-downstream", outbound)
	assert.False(t, downstream.IsSampled())
	downstream.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return transactionTotalsAfter(s, baseline).unsampledCont >= 2
	}, waitTimeout))

	s := mc.Snapshot()
	assert.Equal(t, 0, countSpansByRpc(s, "/unsampled-origin"))
	assert.Equal(t, 0, countSpansByRpc(s, "/unsampled-downstream"))
	totals := transactionTotalsAfter(s, baseline)
	assert.Equal(t, int64(2), totals.unsampledCont)
	assert.Equal(t, int64(0), totals.sampledNew)
}

func findEventByServiceType(events []*pb.PSpanEvent, serviceType int32) *pb.PSpanEvent {
	for _, e := range events {
		if e.GetServiceType() == serviceType {
			return e
		}
	}
	return nil
}
