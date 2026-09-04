package it

import (
	"fmt"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// urlStatFlushInterval is the agent's hard-coded URL-statistics send interval.
// There is no configuration knob for it, so the URL-stat tests have to wait
// for a real tick.
const urlStatFlushInterval = 30 * time.Second

func TestStreamsAgentStatistics(t *testing.T) {
	// A one-second interval, matching production: getStats truncates the
	// measured interval to whole seconds, so a shorter tick reports 0.
	cfg := defaultAgentConfig()
	cfg.statCollectInterval = 1000
	mc, agent := startStack(t, cfg)

	active := agent.NewSpanTracer("active.request", "/active")
	require.True(t, active.IsSampled())

	// An in-flight sampled request must show up in the active-trace histogram.
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		for _, stat := range agentStats(s) {
			var total int32
			for _, c := range stat.GetActiveTrace().GetHistogram().GetActiveTraceCount() {
				total += c
			}
			if total > 0 {
				return true
			}
		}
		return false
	}, waitTimeout))
	active.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return sampledNewAgentStat(s) != nil
	}, waitTimeout))

	s := mc.Snapshot()
	require.NotEmpty(t, s.StatStreams)
	expectCommonMetadata(t, s.StatStreams[0], false)

	stat := sampledNewAgentStat(s)
	require.NotNil(t, stat)
	assert.Greater(t, stat.GetTimestamp(), int64(0))
	assert.Equal(t, int64(1000), stat.GetCollectInterval())
	require.NotNil(t, stat.GetResponseTime())
	require.NotNil(t, stat.GetTotalThread())
	assert.Greater(t, stat.GetTotalThread().GetTotalThreadCount(), int64(0))
	require.NotNil(t, stat.GetActiveTrace().GetHistogram())
	assert.Len(t, stat.GetActiveTrace().GetHistogram().GetActiveTraceCount(), 4)
	assert.Equal(t, int32(2), stat.GetActiveTrace().GetHistogram().GetHistogramSchemaType())
}

func TestReportsResponseTimeAndRuntimeStatistics(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return agentStatCount(s) >= 1 }, waitTimeout))
	baseline := agentStatCount(mc.Snapshot())

	// Span elapsed time is measured, not settable, so the slow request has to
	// actually take that long.
	for i, elapsed := range []time.Duration{50 * time.Millisecond, 150 * time.Millisecond, 450 * time.Millisecond} {
		tracer := agent.NewSpanTracer(fmt.Sprintf("stat.response.%d", i), fmt.Sprintf("/stat-response/%d", i))
		require.True(t, tracer.IsSampled())
		time.Sleep(elapsed)
		tracer.EndSpan()
	}

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return transactionTotalsAfter(s, baseline).sampledNew >= 3 &&
			maxResponseTimeAfter(s, baseline) >= 400
	}, waitTimeout))

	s := mc.Snapshot()
	totals := transactionTotalsAfter(s, baseline)
	assert.Equal(t, int64(3), totals.sampledNew)
	assert.Equal(t, int64(0), totals.unsampledNew)
	assert.Equal(t, int64(0), totals.skippedNew)

	var runtime *pb.PAgentStat
	for i, stat := range agentStats(s) {
		if i >= baseline && stat.GetResponseTime().GetMax() >= 400 {
			runtime = stat
			break
		}
	}
	require.NotNil(t, runtime)
	assert.Greater(t, runtime.GetResponseTime().GetAvg(), int64(0))
	assert.GreaterOrEqual(t, runtime.GetResponseTime().GetMax(), int64(400))
	require.NotNil(t, runtime.GetCpuLoad())
	assert.GreaterOrEqual(t, runtime.GetCpuLoad().GetJvmCpuLoad(), 0.0)
	assert.LessOrEqual(t, runtime.GetCpuLoad().GetJvmCpuLoad(), 1.0)
	assert.GreaterOrEqual(t, runtime.GetCpuLoad().GetSystemCpuLoad(), 0.0)
	assert.LessOrEqual(t, runtime.GetCpuLoad().GetSystemCpuLoad(), 1.0)
	require.NotNil(t, runtime.GetGc())
	assert.Greater(t, runtime.GetGc().GetJvmMemoryHeapUsed(), int64(0))
	assert.GreaterOrEqual(t, runtime.GetGc().GetJvmMemoryHeapMax(), int64(0))
	// gopsutil reports no file-descriptor count on some platforms (darwin),
	// so only the field's presence is asserted.
	assert.GreaterOrEqual(t, runtime.GetFileDescriptor().GetOpenFileDescriptorCount(), int64(0))
}

func TestAppliesCounterAndParentSamplingAndReportsDecisions(t *testing.T) {
	cfg := defaultAgentConfig()
	cfg.samplingCounterRate = 3
	mc, agent := startStack(t, cfg)

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return agentStatCount(s) >= 1 }, waitTimeout))
	baseline := agentStatCount(mc.Snapshot())

	// The counter sampler admits the first new trace and then one in every
	// CounterRate after it.
	expected := []bool{true, false, false, true, false, false}
	sampledTraceID := driveSamplingPattern(t, agent, "sampling.counter", "/sampling/counter/", expected, nil)
	require.NotEmpty(t, sampledTraceID)

	continued := agent.NewSpanTracerWithReader("sampling.continued", "/sampling/continued", mapCarrier{
		pinpoint.HeaderTraceId: sampledTraceID,
	})
	assert.True(t, continued.IsSampled())
	continued.EndSpan()

	unsampled := agent.NewSpanTracerWithReader("sampling.parent-denied", "/sampling/parent-denied", mapCarrier{
		pinpoint.HeaderSampled: "s0",
	})
	assert.False(t, unsampled.IsSampled())
	unsampled.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		totals := transactionTotalsAfter(s, baseline)
		return totals.sampledNew >= 2 && totals.unsampledNew >= 4 &&
			totals.sampledContinuation >= 1 && totals.unsampledCont >= 1 &&
			countSpansByRpc(s, "/sampling/continued") == 1
	}, waitTimeout))

	s := mc.Snapshot()
	totals := transactionTotalsAfter(s, baseline)
	assert.Equal(t, int64(2), totals.sampledNew)
	assert.Equal(t, int64(4), totals.unsampledNew)
	assert.Equal(t, int64(1), totals.sampledContinuation)
	assert.Equal(t, int64(1), totals.unsampledCont)
	assert.Equal(t, int64(0), totals.skippedNew)
	assert.Equal(t, int64(0), totals.skippedCont)
	expectSamplingPattern(t, s, "/sampling/counter/", expected)
	assert.Equal(t, 1, countSpansByRpc(s, "/sampling/continued"))
	assert.Equal(t, 0, countSpansByRpc(s, "/sampling/parent-denied"))
}

func TestAppliesPercentSamplingPattern(t *testing.T) {
	cfg := defaultAgentConfig()
	cfg.samplingType = "PERCENT"
	cfg.samplingPercentRate = 50
	mc, agent := startStack(t, cfg)

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return agentStatCount(s) >= 1 }, waitTimeout))
	baseline := agentStatCount(mc.Snapshot())

	// The percent sampler accumulates the rate (50% == 5000/10000) per request,
	// so admission alternates deterministically: skip, sample, skip, sample.
	expected := []bool{false, true, false, true}
	driveSamplingPattern(t, agent, "sampling.percent", "/sampling/percent/", expected, nil)

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		totals := transactionTotalsAfter(s, baseline)
		return totals.sampledNew >= 2 && totals.unsampledNew >= 2
	}, waitTimeout))

	s := mc.Snapshot()
	totals := transactionTotalsAfter(s, baseline)
	assert.Equal(t, int64(2), totals.sampledNew)
	assert.Equal(t, int64(2), totals.unsampledNew)
	assert.Equal(t, int64(0), totals.skippedNew)
	expectSamplingPattern(t, s, "/sampling/percent/", expected)
}

func TestSamplesOnlyContinuedTracesWhenCounterRateIsZero(t *testing.T) {
	// CounterRate 0 means "never sample a new trace"; continued traces bypass
	// the base sampler entirely, so they must still be recorded.
	cfg := defaultAgentConfig()
	cfg.samplingCounterRate = 0
	mc, agent := startStack(t, cfg)

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return agentStatCount(s) >= 1 }, waitTimeout))
	baseline := agentStatCount(mc.Snapshot())

	driveSamplingPattern(t, agent, "sampling.zero", "/sampling/zero/", []bool{false, false, false}, nil)

	continued := agent.NewSpanTracerWithReader("sampling.zero.continued", "/sampling/zero/continued", mapCarrier{
		pinpoint.HeaderTraceId: "java-agent-7^1700000000000^99",
	})
	assert.True(t, continued.IsSampled())
	continued.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		totals := transactionTotalsAfter(s, baseline)
		return totals.unsampledNew >= 3 && totals.sampledContinuation >= 1 &&
			countSpansByRpc(s, "/sampling/zero/continued") == 1
	}, waitTimeout))

	s := mc.Snapshot()
	totals := transactionTotalsAfter(s, baseline)
	assert.Equal(t, int64(0), totals.sampledNew)
	assert.Equal(t, int64(3), totals.unsampledNew)
	assert.Equal(t, int64(1), totals.sampledContinuation)
	wire := findSpanByRpc(s, "/sampling/zero/continued")
	require.NotNil(t, wire)
	assert.Equal(t, "java-agent-7", wire.GetTransactionId().GetAgentId())
}

func TestEnforcesNewAndContinuationThroughputLimits(t *testing.T) {
	cfg := defaultAgentConfig()
	cfg.newThroughput = 2
	cfg.continueThroughput = 1
	mc, agent := startStack(t, cfg)

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return agentStatCount(s) >= 1 }, waitTimeout))
	baseline := agentStatCount(mc.Snapshot())

	// The limiter refills continuously, so the whole burst must be issued
	// without pauses for the skipped decisions to be deterministic.
	expectedNew := []bool{true, true, false, false}
	parentTraceID := driveSamplingPattern(t, agent, "sampling.throughput.new", "/sampling/throughput/new/", expectedNew, nil)
	require.NotEmpty(t, parentTraceID)

	expectedCont := []bool{true, false, false}
	driveSamplingPattern(t, agent, "sampling.throughput.continued", "/sampling/throughput/continued/",
		expectedCont, mapCarrier{pinpoint.HeaderTraceId: parentTraceID})

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		totals := transactionTotalsAfter(s, baseline)
		return totals.sampledNew >= 2 && totals.skippedNew >= 2 &&
			totals.sampledContinuation >= 1 && totals.skippedCont >= 2 &&
			countSpansByRpc(s, "/sampling/throughput/continued/0") == 1
	}, waitTimeout))

	s := mc.Snapshot()
	totals := transactionTotalsAfter(s, baseline)
	assert.Equal(t, int64(2), totals.sampledNew)
	assert.Equal(t, int64(2), totals.skippedNew)
	assert.Equal(t, int64(0), totals.unsampledNew)
	assert.Equal(t, int64(1), totals.sampledContinuation)
	assert.Equal(t, int64(2), totals.skippedCont)
	expectSamplingPattern(t, s, "/sampling/throughput/new/", expectedNew)
	expectSamplingPattern(t, s, "/sampling/throughput/continued/", expectedCont)
}

// URL statistics flush on the agent's fixed 30s tick, so this test waits for a
// real one and covers every URL-stat assertion in a single pass. Skipped in
// -short mode.
func TestAggregatesUrlStatisticsIncludingFailuresAndUnsampledSpans(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the agent's 30s URL-statistics tick")
	}
	mc, agent := startStack(t, defaultAgentConfig())

	// Two requests on the same URL template aggregate into one entry, and the
	// failed one is counted in both histograms.
	success := agent.NewSpanTracer("url.stat.success", "/url-stat/success")
	require.True(t, success.IsSampled())
	pphttp.CollectUrlStat(success, "/api/orders/{id}/items", "GET", 200)
	time.Sleep(120 * time.Millisecond)
	success.EndSpan()

	failure := agent.NewSpanTracer("url.stat.failure", "/url-stat/failure")
	require.True(t, failure.IsSampled())
	pphttp.CollectUrlStat(failure, "/api/orders/{id}/items", "GET", 503)
	failure.Span().SetFailure()
	time.Sleep(350 * time.Millisecond)
	failure.EndSpan()

	// An unsampled span still feeds URL statistics.
	unsampled := agent.NewSpanTracerWithReader("url.stat.unsampled", "/url-stat/unsampled", mapCarrier{
		pinpoint.HeaderSampled: "s0",
	})
	require.False(t, unsampled.IsSampled())
	pphttp.CollectUrlStat(unsampled, "/unsampled/{id}", "GET", 200)
	unsampled.EndSpan()

	const aggregated = "GET /api/orders/{id}/items"
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return uriStatTotalsFor(s, aggregated).totalCount >= 2 &&
			hasUriStat(s, "GET /unsampled/{id}")
	}, urlStatFlushInterval+waitTimeout))

	s := mc.Snapshot()
	totals := uriStatTotalsFor(s, aggregated)
	assert.Equal(t, int64(2), totals.totalCount)
	assert.Equal(t, int64(1), totals.failedCount)
	assert.GreaterOrEqual(t, totals.totalElapsed, int64(400))
	assert.GreaterOrEqual(t, totals.maxElapsed, int64(350))
	assert.GreaterOrEqual(t, totals.failedElapsed, int64(350))
	assert.GreaterOrEqual(t, totals.failedMax, int64(350))

	// The method prefix is configured on, so the bare path must not appear.
	assert.False(t, hasUriStat(s, "/api/orders/{id}/items"))

	unsampledTotals := uriStatTotalsFor(s, "GET /unsampled/{id}")
	assert.Equal(t, int64(1), unsampledTotals.totalCount)
	assert.Equal(t, int64(0), unsampledTotals.failedCount)

	for _, r := range s.Stats {
		if uriStat := r.Message.GetAgentUriStat(); uriStat != nil {
			assert.Equal(t, int32(0), uriStat.GetBucketVersion())
		}
	}
}
