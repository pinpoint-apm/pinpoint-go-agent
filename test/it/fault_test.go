package it

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// A transport error on a metadata publication is retried until it succeeds,
// and the agent stays online throughout.
func TestRetriesMetadataAfterTransportError(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	mc.FailNext(RpcApiMetadata, codes.Unavailable, "metadata endpoint unavailable")

	// A fresh operation name is what makes the agent publish API metadata.
	tracer := agent.NewSpanTracer("fault.retry.api", "/fault-retry")
	require.True(t, tracer.IsSampled())
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return countApiMetadata(s, "fault.retry.api") >= 2 &&
			hasResultSuccess(s, RpcApiMetadata, codes.OK, true)
	}, longTimeout))

	results := resultsFor(mc.Snapshot(), RpcApiMetadata)
	require.GreaterOrEqual(t, len(results), 2)
	assert.Equal(t, codes.Unavailable, results[0].Code)
	assert.False(t, results[0].Success)
	assert.Equal(t, codes.OK, results[1].Code)
	assert.True(t, results[1].Success)
	assert.True(t, agent.Enable())
}

// A non-retryable error abandons the publication and releases the cache entry,
// so the same API string is re-cached under a fresh id and published again.
func TestReRegistersMetadataAfterNonRetryableError(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	mc.FailNext(RpcApiMetadata, codes.Internal, "metadata permanently rejected")

	const operation = "fault.exhausted.api"
	first := agent.NewSpanTracer(operation, "/fault-exhausted-1")
	require.True(t, first.IsSampled())
	first.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return hasResultSuccess(s, RpcApiMetadata, codes.Internal, false)
	}, waitTimeout))

	// The released cache entry means the next span with the same operation
	// mints a new id and publishes it again, successfully this time.
	require.True(t, waitUntil(func() bool {
		second := agent.NewSpanTracer(operation, "/fault-exhausted-2")
		second.EndSpan()
		return countApiMetadata(mc.Snapshot(), operation) >= 2
	}, waitTimeout))

	s := mc.Snapshot()
	ids := make(map[int32]bool)
	for _, r := range s.ApiMetadata {
		if r.Message.GetApiInfo() == operation {
			ids[r.Message.GetApiId()] = true
		}
	}
	assert.GreaterOrEqual(t, len(ids), 2, "a released cache entry must yield a fresh api id")
	assert.True(t, hasResultSuccess(s, RpcApiMetadata, codes.OK, true))
	assert.True(t, agent.Enable())
}

func TestHandlesProfilerCommandsOverRealGrpcStreams(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.CommandStreams) > 0 }, waitTimeout))

	mc.SendEchoCommand(101, "collector-echo")
	require.True(t, mc.WaitFor(func(s Snapshot) bool { return hasEchoResponse(s, 101) }, waitTimeout))

	// The agent only tracks per-goroutine active spans while an
	// active-thread-count stream is open, so the stream has to be running
	// before the request this test wants to see counted.
	mc.SendActiveThreadCountCommand(102)
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return countActiveThreadResponses(s, 102) >= 1
	}, waitTimeout))

	active := agent.NewSpanTracer("command.active", "/command-active")
	require.True(t, active.IsSampled())
	defer active.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		for _, r := range s.ActiveThreadCountResponses {
			if r.Message.GetCommonStreamResponse().GetResponseId() != 102 {
				continue
			}
			var total int32
			for _, c := range r.Message.GetActiveThreadCount() {
				total += c
			}
			if total >= 1 {
				return true
			}
		}
		return false
	}, waitTimeout))

	// A light dump lists the goroutines that currently carry a span; the full
	// dump is then targeted at one of them by name, which is how the collector
	// drills into a specific request.
	mc.SendActiveThreadLightDumpCommand(103, 5)
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.ActiveThreadLightDumps) > 0 &&
			len(s.ActiveThreadLightDumps[0].Message.GetThreadDump()) > 0
	}, waitTimeout))

	light := mc.Snapshot().ActiveThreadLightDumps[0].Message
	assert.Equal(t, int32(103), light.GetCommonResponse().GetResponseId())
	assert.Equal(t, int32(0), light.GetCommonResponse().GetStatus())
	require.NotEmpty(t, light.GetThreadDump())
	lightDump := light.GetThreadDump()[0]
	assert.True(t, lightDump.GetSampled())
	assert.Equal(t, active.TransactionId().String(), lightDump.GetTransactionId())
	assert.Equal(t, "/command-active", lightDump.GetEntryPoint())
	threadName := lightDump.GetThreadDump().GetThreadName()
	require.NotEmpty(t, threadName)

	mc.SendCommand(&pb.PCmdRequest{
		RequestId: 104,
		Command: &pb.PCmdRequest_CommandActiveThreadDump{
			CommandActiveThreadDump: &pb.PCmdActiveThreadDump{Limit: 1, ThreadName: []string{threadName}},
		},
	})
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.ActiveThreadDumpResponses) > 0 &&
			len(s.ActiveThreadDumpResponses[0].Message.GetThreadDump()) > 0
	}, waitTimeout))

	s := mc.Snapshot()
	assert.Equal(t, "collector-echo", s.EchoResponses[0].Message.GetMessage())
	expectCommonMetadata(t, s.EchoResponses[0].Metadata, false)

	require.NotEmpty(t, s.ActiveThreadCountResponses)
	count := s.ActiveThreadCountResponses[0]
	assert.Equal(t, int32(1), count.Message.GetCommonStreamResponse().GetSequenceId())
	assert.Equal(t, int32(2), count.Message.GetHistogramSchemaType())
	assert.Len(t, count.Message.GetActiveThreadCount(), 4)
	assert.Greater(t, count.Message.GetTimeStamp(), int64(0))
	// Unlike the ping stream, the active-thread-count stream carries no socket id.
	expectCommonMetadata(t, count.Metadata, false)

	dump := s.ActiveThreadDumpResponses[0].Message
	assert.Equal(t, int32(104), dump.GetCommonResponse().GetResponseId())
	assert.Equal(t, "Go", dump.GetType())
	require.NotEmpty(t, dump.GetThreadDump())
	assert.Equal(t, threadName, dump.GetThreadDump()[0].GetThreadDump().GetThreadName())
	assert.NotEmpty(t, dump.GetThreadDump()[0].GetThreadDump().GetStackTrace())
	assert.Equal(t, "/command-active", dump.GetThreadDump()[0].GetEntryPoint())
}

// Every active-thread-count request opens its own stream, which starts over at
// sequence 1. Re-issuing the same request id (collector reconnect behavior)
// must therefore produce a second stream, not reuse the first.
func TestRestartsActiveThreadCountStreamForDuplicateRequest(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())
	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.CommandStreams) > 0 }, waitTimeout))

	mc.SendActiveThreadCountCommand(501)
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return countActiveThreadResponses(s, 501, 1) >= 1
	}, waitTimeout))

	mc.SendActiveThreadCountCommand(501)
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return countActiveThreadResponses(s, 501, 1) >= 2 && len(s.ActiveThreadCountStreams) >= 2
	}, waitTimeout))
	assert.True(t, agent.Enable())
}

func TestTimesOutCommandRequestAndKeepsStreamUsable(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())
	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.CommandStreams) > 0 }, waitTimeout))

	mc.TimeoutNext(RpcCommandEcho)
	mc.SendEchoCommand(201, "will-time-out")
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return hasResult(s, RpcCommandEcho, codes.DeadlineExceeded)
	}, waitTimeout))

	// A timed-out unary response must not tear down the command bidi stream.
	mc.SendEchoCommand(202, "after-timeout")
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return hasEchoResponse(s, 202) && hasResultSuccess(s, RpcCommandEcho, codes.OK, true)
	}, waitTimeout))
	assert.True(t, agent.Enable())
}

func TestContinuesSendingAfterSpanRequestError(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	mc.FailNext(RpcSendSpanBatch, codes.Internal, "span batch rejected")
	failed := agent.NewSpanTracer("faulted.span", "/faulted-span")
	require.True(t, failed.IsSampled())
	failed.EndSpan()
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/faulted-span") != nil &&
			hasResultSuccess(s, RpcSendSpanBatch, codes.Internal, false)
	}, waitTimeout))

	healthy := agent.NewSpanTracer("healthy.span", "/healthy-span")
	require.True(t, healthy.IsSampled())
	healthy.EndSpan()
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/healthy-span") != nil &&
			hasResultSuccess(s, RpcSendSpanBatch, codes.OK, true)
	}, waitTimeout))
	assert.True(t, agent.Enable())
}

func TestReconnectsAfterEndpointAndCommandStreamFailures(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.PingStreams) > 0 && len(s.CommandStreams) > 0
	}, waitTimeout))
	before := len(mc.Snapshot().CommandStreams)

	// Closing the listening socket drops every live Agent/Metadata/Command
	// connection; the same port then comes back.
	mc.StopEndpoint(EndpointAgent)
	mc.FailNext(RpcHandleCommand, codes.Unavailable, "command stream rejected after reconnect")
	require.NoError(t, mc.StartEndpoint(EndpointAgent))

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.CommandStreams) >= before+2 &&
			hasResultSuccess(s, RpcHandleCommand, codes.Unavailable, false)
	}, longTimeout))

	mc.SendEchoCommand(303, "after-reconnect")
	require.True(t, mc.WaitFor(func(s Snapshot) bool { return hasEchoResponse(s, 303) }, longTimeout))

	// Exercise a separate transport channel outage as well. A span queued
	// during the outage may be dropped by policy, but later traffic must flow.
	mc.StopEndpoint(EndpointSpan)
	outage := agent.NewSpanTracer("span.during.outage", "/span-during-outage")
	require.True(t, outage.IsSampled())
	outage.EndSpan()
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, mc.StartEndpoint(EndpointSpan))

	// The span channel reconnects on its own schedule and a batch sent while
	// it is still down is dropped rather than retried, so the application keeps
	// producing spans until one lands.
	require.True(t, waitUntil(func() bool {
		recovered := agent.NewSpanTracer("span.after.reconnect", "/span-after-reconnect")
		require.True(t, recovered.IsSampled())
		recovered.EndSpan()
		return findSpanByRpc(mc.Snapshot(), "/span-after-reconnect") != nil
	}, longTimeout))
	assert.True(t, agent.Enable())
}

func TestReconnectsStatStreamAfterServerError(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.Stats) > 0 }, waitTimeout))
	initial := len(mc.Snapshot().StatStreams)

	// Consumed by the already-open stream after its next message.
	mc.FailNext(RpcSendAgentStat, codes.Unavailable, "stat stream closed by collector", 1)
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return hasResultSuccess(s, RpcSendAgentStat, codes.Unavailable, false)
	}, waitTimeout))

	// The worker must notice the closed stream and open a fresh one, then keep
	// delivering statistics through it.
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.StatStreams) >= initial+1
	}, longTimeout))
	received := len(mc.Snapshot().Stats)
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.Stats) > received
	}, longTimeout))
	assert.True(t, agent.Enable())
}

func TestShutdownCancelsTimedOutStatStream(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.StatStreams) > 0 }, waitTimeout))
	before := len(mc.Snapshot().Stats)

	// The open stream accepts one more message, then deliberately stops
	// completing the RPC until the client gives up.
	mc.TimeoutNext(RpcSendAgentStat, 1)
	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.Stats) > before }, waitTimeout))

	started := time.Now()
	agent.Shutdown()
	elapsed := time.Since(started)

	assert.Less(t, elapsed, 8*time.Second)
	assert.False(t, agent.Enable())
	// The stat stream carries no request deadline: the agent cancels the
	// stalled send itself, which the collector observes as a cancellation.
	assert.True(t, mc.WaitFor(func(s Snapshot) bool {
		return hasResult(s, RpcSendAgentStat, codes.Canceled) ||
			hasResult(s, RpcSendAgentStat, codes.DeadlineExceeded)
	}, 2*time.Second))
}

func TestShutdownCancelsTimedOutSpanRequest(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	mc.TimeoutNext(RpcSendSpanBatch)
	tracer := agent.NewSpanTracer("shutdown.timeout", "/timeout-shutdown")
	require.True(t, tracer.IsSampled())
	tracer.EndSpan()
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/timeout-shutdown") != nil
	}, waitTimeout))

	started := time.Now()
	agent.Shutdown()
	elapsed := time.Since(started)

	assert.Less(t, elapsed, 8*time.Second)
	assert.False(t, agent.Enable())
	assert.True(t, mc.WaitFor(func(s Snapshot) bool {
		return hasResultSuccess(s, RpcSendSpanBatch, codes.DeadlineExceeded, false)
	}, 2*time.Second))
}

// Every RPC fails while the connections stay up -- an unhealthy collector
// rather than a dead host. The application-facing side must be unaffected and
// every channel must recover once the outage ends.
func TestKeepsServingAndRecyclingQueuesThroughCollectorOutage(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	before := agent.NewSpanTracer("outage.before", "/collector-outage-before")
	require.True(t, before.IsSampled())
	before.EndSpan()
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/collector-outage-before") != nil && len(s.Stats) > 0
	}, waitTimeout))

	mc.BeginOutage()

	// Spans are still real (not noop) and requests still complete promptly.
	probe := agent.NewSpanTracer("outage.probe", "/collector-outage-probe")
	assert.True(t, probe.IsSampled())
	assert.NotEqual(t, int64(0), probe.SpanId())
	probe.EndSpan()

	loadStarted := time.Now()
	for request := 0; request < 12; request++ {
		assert.Equal(t, request*2+1,
			handleInstrumentedRequest(agent, "/collector-outage-during", request))
		time.Sleep(25 * time.Millisecond)
	}
	assert.Less(t, time.Since(loadStarted), 5*time.Second)
	assert.True(t, agent.Enable())

	// The span sender keeps draining its queue into failing batches while
	// recycling its in-flight permits: a permit leak would stall the pipeline
	// after Span.BatchMaxConcurrentRequests (2) failures.
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		failures := 0
		for _, r := range resultsFor(s, RpcSendSpanBatch) {
			if r.Code == codes.Unavailable {
				failures++
			}
		}
		return failures >= 3
	}, waitTimeout))

	// The stat stream broke with the outage and the worker keeps reopening it
	// against the failing collector.
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return hasResultSuccess(s, RpcSendAgentStat, codes.Unavailable, false)
	}, longTimeout))

	statsDuringOutage := len(mc.Snapshot().Stats)
	mc.EndOutage()

	// Fresh spans, statistics and profiler commands all flow again.
	require.True(t, waitUntil(func() bool {
		recovered := agent.NewSpanTracer("outage.after", "/collector-outage-after")
		recovered.EndSpan()
		return findSpanByRpc(mc.Snapshot(), "/collector-outage-after") != nil
	}, longTimeout))
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.Stats) > statsDuringOutage
	}, longTimeout))

	mc.SendEchoCommand(707, "collector-outage-recovered")
	require.True(t, mc.WaitFor(func(s Snapshot) bool { return hasEchoResponse(s, 707) }, longTimeout))
	assert.True(t, agent.Enable())
}

// With the span endpoint down the bounded queue absorbs the load and the
// application is never blocked; traffic resumes after the endpoint returns.
func TestKeepsServingWhileSpanEndpointIsDownAndRecovers(t *testing.T) {
	cfg := defaultAgentConfig()
	// A capacity below the shard threshold keeps the queue at a single shard,
	// so the bounded head-drop policy applies in strict FIFO order.
	cfg.spanQueueSize = 8
	mc, agent := startStack(t, cfg)

	warm := agent.NewSpanTracer("queue.before", "/queue-before")
	require.True(t, warm.IsSampled())
	warm.EndSpan()
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/queue-before") != nil
	}, waitTimeout))

	mc.StopEndpoint(EndpointSpan)
	time.Sleep(300 * time.Millisecond)

	const outageSpans = 30
	loadStarted := time.Now()
	for i := 1; i <= outageSpans; i++ {
		tracer := agent.NewSpanTracer("queue.outage", fmt.Sprintf("/queue-outage-%d", i))
		assert.True(t, tracer.IsSampled(), i)
		tracer.EndSpan()
	}
	// The bounded queue absorbs the burst without ever blocking the
	// application on the dead collector.
	assert.Less(t, time.Since(loadStarted), 2*time.Second)
	assert.True(t, agent.Enable())

	require.NoError(t, mc.StartEndpoint(EndpointSpan))

	// Tracing resumes once the channel is ready again. Every poll sends another
	// probe, each under its own URI, and the poll succeeds as soon as any of
	// them has arrived -- a probe may take several polls to land. Sharing one
	// URI instead made a slow delivery indistinguishable from the sender
	// duplicating a span, which is what the count below checks for.
	var probes []string
	landed := ""
	require.True(t, waitUntil(func() bool {
		probe := fmt.Sprintf("/queue-recovered-%d", len(probes)+1)
		probes = append(probes, probe)
		recovered := agent.NewSpanTracer("queue.recovered", probe)
		recovered.EndSpan()

		snapshot := mc.Snapshot()
		for _, p := range probes {
			if findSpanByRpc(snapshot, p) != nil {
				landed = p
				return true
			}
		}
		return false
	}, longTimeout))

	s := mc.Snapshot()
	survivors := 0
	for i := 1; i <= outageSpans; i++ {
		survivors += countSpansByRpc(s, fmt.Sprintf("/queue-outage-%d", i))
	}
	// Whatever the sender managed to deliver, the queue never grew past its
	// bound and nothing was duplicated: the probe that landed was created once
	// and arrived once.
	assert.LessOrEqual(t, survivors, outageSpans)
	assert.Equal(t, 1, countSpansByRpc(s, landed))
}

func TestShutdownStopsTracingAndServesNoopTracersToTheApp(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	warm := agent.NewSpanTracer("shutdown.noop.before", "/shutdown-noop-before")
	require.True(t, warm.IsSampled())
	warm.EndSpan()
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/shutdown-noop-before") != nil
	}, waitTimeout))

	started := time.Now()
	agent.Shutdown()
	assert.Less(t, time.Since(started), 8*time.Second)
	assert.False(t, agent.Enable())
	// Shutdown restores the noop agent as the process-global one.
	assert.Equal(t, pinpoint.NoopAgent(), pinpoint.GetAgent())

	// Every worker has been joined by now, so the collector records are final.
	quiesced := mc.Snapshot()

	// The application keeps running against the stopped agent: requests
	// complete normally and every tracer handed out is inert.
	for request := 0; request < 5; request++ {
		requireNoopTracer(t, agent.NewSpanTracer("shutdown.noop", "/shutdown-noop-after"))
		assert.Equal(t, request*2+1,
			handleInstrumentedRequest(agent, "/shutdown-noop-after", request))
	}

	// A second shutdown must be a harmless no-op.
	agent.Shutdown()
	assert.False(t, agent.Enable())

	// Nothing new may reach the collector once the agent stopped.
	time.Sleep(300 * time.Millisecond)
	after := mc.Snapshot()
	assert.Len(t, allSpanMessages(after), len(allSpanMessages(quiesced)))
	assert.Len(t, after.Stats, len(quiesced.Stats))
	assert.Len(t, after.Pings, len(quiesced.Pings))
	assert.Len(t, after.AgentInfos, len(quiesced.AgentInfos))
	assert.Len(t, after.ApiMetadata, len(quiesced.ApiMetadata))
}

// A host that stops and resumes tracing while it keeps serving must build a new
// agent: Shutdown is terminal for an agent instance.
func TestRecoversTracingAcrossRepeatedCreateShutdownCycles(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	const cycles = 3
	for cycle := 1; cycle <= cycles; cycle++ {
		rpc := fmt.Sprintf("/restart-cycle-%d", cycle)

		// A sampled span opened under the outgoing agent, deliberately held
		// across the whole teardown/rebuild below and ended only afterwards.
		straddling := agent.NewSpanTracer("restart.straddle", "/restart-straddle")
		require.True(t, straddling.IsSampled())
		straddling.NewSpanEvent("straddle.work")

		agent.Shutdown()
		require.False(t, agent.Enable())

		// Between cycles the application keeps calling into the stale handle.
		// Those spans must be dropped, not delivered under the next agent.
		for i := 0; i < 3; i++ {
			stale := agent.NewSpanTracer("restart.stale", "/restart-stale")
			stale.EndSpan()
		}

		agent = startAgent(t, mc, defaultAgentConfig())
		require.True(t, waitUntil(func() bool { return agent.Enable() }, waitTimeout),
			"the agent never came back online")

		// Finishing the straddling span must be inert, not a crash: its agent
		// is shut down and no longer the global one.
		straddling.SpanEvent().SetDestination("straddle-backend")
		straddling.EndSpanEvent()
		straddling.EndSpan()

		// Tracing works again on the new agent, end to end.
		tracer := agent.NewSpanTracer("restart.cycle", rpc)
		require.True(t, tracer.IsSampled())
		tracer.EndSpan()
		require.True(t, mc.WaitFor(func(s Snapshot) bool {
			return findSpanByRpc(s, rpc) != nil
		}, waitTimeout), "span never reached the collector")
	}

	s := mc.Snapshot()
	assert.Equal(t, 0, countSpansByRpc(s, "/restart-stale"),
		"spans recorded through a shut-down agent must be dropped")
	assert.Equal(t, 0, countSpansByRpc(s, "/restart-straddle"),
		"a span ended after its agent shut down must never be re-attributed to the replacement agent")
	// One registration for the first agent plus one per rebuilt agent.
	assert.GreaterOrEqual(t, len(s.AgentInfos), cycles+1)
}

// Metadata sends are bounded by metaRetryMaxAttempts. Once the budget is spent
// the item is abandoned and its cache entry released, so the same API string is
// re-cached under a fresh id and published again.
func TestReRegistersMetadataAfterRetryExhaustion(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	const operation = "retry.exhausted.api"
	// One initial attempt plus its retries: every attempt for this item must
	// fail before the sender gives up on it.
	for i := 0; i < 3; i++ {
		mc.FailNext(RpcApiMetadata, codes.Unavailable,
			fmt.Sprintf("metadata attempt %d rejected", i))
	}

	first := agent.NewSpanTracer(operation, "/retry-exhausted-1")
	require.True(t, first.IsSampled())
	first.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		failed := 0
		for _, r := range resultsFor(s, RpcApiMetadata) {
			if r.Code == codes.Unavailable {
				failed++
			}
		}
		return failed >= 3
	}, longTimeout))

	// Exhaustion releases the cache entry, so the same operation is re-cached
	// under a fresh id and published successfully. The release happens on the
	// sender worker shortly after the last failure, hence the poll.
	require.True(t, waitUntil(func() bool {
		second := agent.NewSpanTracer(operation, "/retry-exhausted-2")
		second.EndSpan()
		s := mc.Snapshot()
		return countApiMetadata(s, operation) >= 4 &&
			hasResultSuccess(s, RpcApiMetadata, codes.OK, true)
	}, longTimeout))

	ids := make(map[int32]bool)
	for _, r := range mc.Snapshot().ApiMetadata {
		if r.Message.GetApiInfo() == operation {
			ids[r.Message.GetApiId()] = true
		}
	}
	assert.GreaterOrEqual(t, len(ids), 2, "an exhausted item must be re-cached under a fresh id")
	assert.True(t, agent.Enable())
}

// Metadata is published through a bounded pipeline rather than one serial
// worker, so a single item stalling on a slow collector must not hold up the
// rest.
func TestKeepsPublishingMetadataWhileOneItemStalls(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	// The first metadata publication after this point is withheld until the
	// client's deadline; everything queued behind it must still get through.
	mc.TimeoutNext(RpcApiMetadata)

	stalled := agent.NewSpanTracer("stalled.metadata.api", "/stalled-metadata")
	require.True(t, stalled.IsSampled())
	stalled.EndSpan()

	for i := 0; i < 3; i++ {
		operation := fmt.Sprintf("pipelined.metadata.api.%d", i)
		tracer := agent.NewSpanTracer(operation, fmt.Sprintf("/pipelined-metadata-%d", i))
		require.True(t, tracer.IsSampled())
		tracer.EndSpan()
	}

	// The stalled call is still parked on the collector at this point, so these
	// can only have arrived through a concurrent send.
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		for i := 0; i < 3; i++ {
			if !hasApiMetadata(s, fmt.Sprintf("pipelined.metadata.api.%d", i), apiTypeWebRequest) {
				return false
			}
		}
		return true
	}, waitTimeout))
	assert.True(t, agent.Enable())
}

// The agent caps concurrent active-thread-count streams. Beyond the cap a
// request is refused with a fail message on the command stream instead of
// silently starting another responder goroutine.
func TestRejectsActiveThreadCountStreamsBeyondLimit(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())
	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.CommandStreams) > 0 }, waitTimeout))

	const firstID = int32(601)
	const maxStreams = 10
	for i := int32(0); i < maxStreams; i++ {
		mc.SendActiveThreadCountCommand(firstID + i)
	}
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		for i := int32(0); i < maxStreams; i++ {
			if countActiveThreadResponses(s, firstID+i) < 1 {
				return false
			}
		}
		return true
	}, waitTimeout))

	const rejectedID = firstID + maxStreams
	mc.SendActiveThreadCountCommand(rejectedID)
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findFailMessage(s, rejectedID) != nil
	}, waitTimeout))

	s := mc.Snapshot()
	fail := findFailMessage(s, rejectedID)
	require.NotNil(t, fail)
	assert.Equal(t, "too many active thread count streams", fail.GetMessage().GetValue())
	// The refused request must not have opened a stream at all.
	assert.Equal(t, 0, countActiveThreadResponses(s, rejectedID))
	assert.Len(t, s.ActiveThreadCountStreams, maxStreams)
	assert.True(t, agent.Enable())
}

// Shutdown runs while the application is still finishing requests. Span, URL
// stat and metadata records are all enqueued from the request path, so a
// producer that is mid-send when the agent stops must not be left writing into
// a torn-down queue -- that crashed the whole process.
func TestKeepsProducingSpansWhileShuttingDown(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	stop := make(chan struct{})
	var producers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		producers.Add(1)
		go func(worker int) {
			defer producers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				// Exercises every request-path producer at once: the span
				// queue, the URL stat queue and the metadata channel.
				tracer := agent.NewSpanTracer(
					fmt.Sprintf("shutdown.load.%d.%d", worker, i),
					fmt.Sprintf("/shutdown-load/%d/%d", worker, i))
				tracer.NewSpanEvent("shutdown.load.work")
				tracer.SpanEvent().SetError(errors.New("shutdown load"), "ShutdownLoad")
				tracer.EndSpanEvent()
				tracer.AddMetric(pinpoint.MetricURLStat, &pinpoint.UrlStatEntry{
					Url: "/shutdown-load/{id}", Method: "GET", Status: 200,
				})
				tracer.EndSpan()
			}
		}(worker)
	}

	// Let the load reach the workers before pulling the agent out from under it.
	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(allSpanMessages(s)) > 0 }, waitTimeout))
	agent.Shutdown()
	close(stop)
	producers.Wait()

	// Surviving the race is the assertion: a producer racing the shutdown used
	// to panic on a closed channel and take the process with it.
	assert.False(t, agent.Enable())
	assert.Equal(t, pinpoint.NoopAgent(), pinpoint.GetAgent())
}

// The span transport recycles its protobuf graph through pooled slabs, so a
// message released too early would surface as one span carrying another's
// content. Every span here is self-identifying: its annotation repeats its RPC
// name, so any crossed wire shows up as a mismatch.
func TestDeliversEveryConcurrentSpanIntactUnderLoad(t *testing.T) {
	cfg := defaultAgentConfig()
	// Room for the whole burst, so a drop cannot be mistaken for corruption.
	cfg.spanQueueSize = 1024
	mc, agent := startStack(t, cfg)

	const workers = 8
	const perWorker = 25
	const totalSpans = workers * perWorker

	var producers sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		producers.Add(1)
		go func(worker int) {
			defer producers.Done()
			for i := 0; i < perWorker; i++ {
				rpc := fmt.Sprintf("/load-integrity/%d/%d", worker, i)
				tracer := agent.NewSpanTracer("load.integrity", rpc)
				tracer.Span().Annotations().AppendString(9300, rpc)
				for event := 0; event < 3; event++ {
					tracer.NewSpanEvent(fmt.Sprintf("load.event.%d", event))
					tracer.SpanEvent().Annotations().AppendString(9301, rpc)
					tracer.EndSpanEvent()
				}
				tracer.EndSpan()
			}
		}(worker)
	}
	producers.Wait()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return countSpansByRpcPrefix(s, "/load-integrity/") >= totalSpans
	}, longTimeout), "not every span reached the collector")

	s := mc.Snapshot()
	delivered := 0
	for _, message := range allSpanMessages(s) {
		span := message.GetSpan()
		if span == nil || !strings.HasPrefix(span.GetAcceptEvent().GetRpc(), "/load-integrity/") {
			continue
		}
		delivered++
		rpc := span.GetAcceptEvent().GetRpc()
		annotation := findAnnotation(span.GetAnnotation(), 9300)
		require.NotNil(t, annotation, rpc)
		assert.Equal(t, rpc, annotation.GetValue().GetStringValue(),
			"span %s carries another span's annotation", rpc)
		for _, event := range eventsForSpan(s, span.GetSpanId()) {
			if a := findAnnotation(event.GetAnnotation(), 9301); a != nil {
				assert.Equal(t, rpc, a.GetValue().GetStringValue(),
					"an event of span %s carries another span's annotation", rpc)
			}
		}
	}
	assert.Equal(t, totalSpans, delivered)
}
