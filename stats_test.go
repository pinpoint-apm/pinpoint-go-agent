package pinpoint

import (
	"bytes"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
)

// readCounters sums the shards without resetting them, for tests that assert
// on cumulative counts (drainCounters is destructive).
func (stats *agentStats) readCounters() statsCounterSnapshot {
	var c statsCounterSnapshot
	for i := range stats.shards {
		s := &stats.shards[i]
		c.sampleNew += atomic.LoadInt64(&s.sampleNew)
		c.skipNew += atomic.LoadInt64(&s.skipNew)
		c.unSampleNew += atomic.LoadInt64(&s.unSampleNew)
		c.sampleCont += atomic.LoadInt64(&s.sampleCont)
		c.unSampleCont += atomic.LoadInt64(&s.unSampleCont)
		c.skipCont += atomic.LoadInt64(&s.skipCont)
	}
	return c
}

func Test_drainStatsCountersSwapsAndResets(t *testing.T) {
	stats := newAgentStats()

	stats.collectResponseTime(100)
	stats.collectResponseTime(200)
	stats.incrSampleNew()
	stats.incrUnSampleNew()
	stats.incrSampleCont()
	stats.incrUnSampleCont()
	stats.incrSkipNew()
	stats.incrSkipCont()

	counters := stats.drainCounters()

	assert.Equal(t, int64(300), counters.accResponseTime)
	assert.Equal(t, int64(200), counters.maxResponseTime)
	assert.Equal(t, int64(2), counters.requestCount)
	assert.Equal(t, int64(1), counters.sampleNew)
	assert.Equal(t, int64(1), counters.unSampleNew)
	assert.Equal(t, int64(1), counters.sampleCont)
	assert.Equal(t, int64(1), counters.unSampleCont)
	assert.Equal(t, int64(1), counters.skipNew)
	assert.Equal(t, int64(1), counters.skipCont)

	assert.Equal(t, statsCounterSnapshot{}, stats.drainCounters(), "second drain must return zeros")
}

// Every increment must be aggregated exactly once across all shards,
// regardless of which goroutine (and therefore which shard) recorded it.
func Test_drainStatsCountersAggregatesAllShards(t *testing.T) {
	stats := newAgentStats()

	const goroutines = 64
	const perG = 100

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				stats.collectResponseTime(int64(g*perG + i + 1))
				stats.incrSampleNew()
				stats.incrUnSampleNew()
				stats.incrSampleCont()
				stats.incrUnSampleCont()
				stats.incrSkipNew()
				stats.incrSkipCont()
			}
		}(g)
	}
	wg.Wait()

	counters := stats.drainCounters()

	const n = int64(goroutines * perG)
	assert.Equal(t, n*(n+1)/2, counters.accResponseTime)
	assert.Equal(t, n, counters.maxResponseTime)
	assert.Equal(t, n, counters.requestCount)
	assert.Equal(t, n, counters.sampleNew)
	assert.Equal(t, n, counters.unSampleNew)
	assert.Equal(t, n, counters.sampleCont)
	assert.Equal(t, n, counters.unSampleCont)
	assert.Equal(t, n, counters.skipNew)
	assert.Equal(t, n, counters.skipCont)
	assert.Equal(t, statsCounterSnapshot{}, stats.drainCounters(), "second drain must return zeros")
}

// Without a goid offset the counters degrade to a single shard but must
// still aggregate correctly.
func Test_statShardWithoutGoIdOffset(t *testing.T) {
	saved := goIdOffset
	goIdOffset = 0
	defer func() { goIdOffset = saved }()

	stats := newAgentStats()
	stats.collectResponseTime(100)
	stats.incrSkipNew()

	assert.Equal(t, &stats.shards[0], stats.shard())

	counters := stats.drainCounters()
	assert.Equal(t, int64(100), counters.accResponseTime)
	assert.Equal(t, int64(100), counters.maxResponseTime)
	assert.Equal(t, int64(1), counters.requestCount)
	assert.Equal(t, int64(1), counters.skipNew)
}

func Test_collectResponseTimePreservesMax(t *testing.T) {
	stats := newAgentStats()

	stats.collectResponseTime(300)
	stats.collectResponseTime(100)
	stats.collectResponseTime(200)

	counters := stats.drainCounters()

	assert.Equal(t, int64(600), counters.accResponseTime)
	assert.Equal(t, int64(3), counters.requestCount)
	assert.Equal(t, int64(300), counters.maxResponseTime)
	assert.Equal(t, int64(200), calcResponseAvg(counters.accResponseTime, counters.requestCount))
}

func Test_calcResponseAvgReturnsZeroWithoutRequests(t *testing.T) {
	assert.Equal(t, int64(0), calcResponseAvg(100, 0))
}

// Test_activeSpanShardIsCacheLinePadded guards the false-sharing fix: the shards
// must stay a whole cache line apart, not packed several to a line.
func Test_getStatsReportsCumulativeGcCounters(t *testing.T) {
	stats := newAgentStats()

	first := stats.getStats()
	runtime.GC()
	second := stats.getStats()

	assert.Greater(t, second.gcNum, first.gcNum, "gcNum is cumulative, so a GC between samples must raise it")
	assert.GreaterOrEqual(t, second.gcTime, first.gcTime, "gcTime is cumulative and never decreases")
}

func Test_activeSpanShardIsCacheLinePadded(t *testing.T) {
	if got := unsafe.Sizeof(activeSpanShard{}); got%cacheLinePadSize != 0 {
		t.Errorf("activeSpanShard is %d bytes, not a multiple of the %d-byte shard stride: shards share a cache line", got, cacheLinePadSize)
	}
}

func Test_getStatsIntervalIsMeasuredMilliseconds(t *testing.T) {
	stats := newAgentStats()
	stats.lastCollectTime = time.Now().Add(-4990 * time.Millisecond)

	interval := stats.getStats().interval
	assert.GreaterOrEqual(t, interval, int64(4990))
	assert.Less(t, interval, int64(5000), "must not truncate to whole seconds")
}

func Test_normalizeCpuLoad(t *testing.T) {
	nan := math.NaN()
	tests := []struct {
		name         string
		proc, sys    float64
		numCPU       int
		wantP, wantS float64
	}{
		{"four cores saturated", 400, 100, 4, 1.0, 1.0},
		{"half a core of four", 50, 50, 4, 0.125, 0.5},
		{"over range clamps", 900, 150, 4, 1.0, 1.0},
		{"negative clamps", -10, -1, 4, 0, 0},
		{"nan clamps", nan, nan, 4, 0, 0},
		{"zero cpus treated as one", 50, 50, 0, 0.5, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, s := normalizeCpuLoad(tt.proc, tt.sys, tt.numCPU)
			if p != tt.wantP || s != tt.wantS {
				t.Errorf("got (%v, %v), want (%v, %v)", p, s, tt.wantP, tt.wantS)
			}
		})
	}
}

// The bucket boundaries are inclusive on the upper side and compared in whole
// milliseconds, matching the Java agent's NORMAL schema (slots 1000/3000/5000,
// elapsedTime <= slotTime). An exact boundary is the case float seconds got
// wrong.
func Test_bucketActiveSpanBoundariesAreInclusiveMilliseconds(t *testing.T) {
	now := time.Now()

	for _, tc := range []struct {
		elapsed time.Duration
		bucket  int
	}{
		{0, 0},
		{999 * time.Millisecond, 0},
		{1000 * time.Millisecond, 0}, // exactly 1s belongs to the first slot
		{1001 * time.Millisecond, 1},
		{3000 * time.Millisecond, 1},
		{3001 * time.Millisecond, 2},
		{5000 * time.Millisecond, 2},
		{5001 * time.Millisecond, 3},
		{time.Hour, 3},
	} {
		counts := []int32{0, 0, 0, 0}
		bucketActiveSpan(counts, now, now.Add(-tc.elapsed))

		want := []int32{0, 0, 0, 0}
		want[tc.bucket] = 1
		assert.Equal(t, want, counts, "%v elapsed belongs in bucket %d", tc.elapsed, tc.bucket)
	}
}

// A reading that could not be taken must not look like a measured zero, which
// the inspector charts as fact.
func Test_numFDAndNumThreadsReportUncollectedAsMinusOne(t *testing.T) {
	stats := newAgentStats()
	stats.proc = nil

	assert.EqualValues(t, uncollectedUsage, stats.numFD())
	assert.EqualValues(t, uncollectedUsage, stats.numThreads())

	snapshot := stats.getStats()
	assert.EqualValues(t, uncollectedUsage, snapshot.numOpenFD)
	assert.EqualValues(t, uncollectedUsage, snapshot.numThreads)
}

// readMemStats replaced runtime.ReadMemStats (a stop-the-world per sample) with
// runtime/metrics. The fields it derives must stay the documented equivalents
// of the MemStats fields they replaced, within the drift of two separate reads.
func Test_readMemStatsMatchesRuntimeMemStats(t *testing.T) {
	stats := newAgentStats()
	// Settle allocation so the two reads see the same heap.
	runtime.GC()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	got := stats.readMemStats()

	within := func(name string, got, want uint64) {
		t.Helper()
		// Background allocation between the reads moves the numbers a little.
		tolerance := max(want/10, 1<<20)
		assert.InDelta(t, float64(want), float64(got), float64(tolerance), name)
	}
	within("heapInuse", got.heapInuse, ms.HeapInuse)
	within("heapSys", got.heapSys, ms.HeapSys)
	within("stackInuse", got.stackInuse, ms.StackInuse)
	within("stackSys", got.stackSys, ms.StackSys)
	assert.Equal(t, uint64(ms.NumGC), got.numGC, "numGC")
	assert.Greater(t, got.numGC, uint64(0))
}

// fastStatConfig builds a config with a sub-second collect interval, which
// publish rejects (Stat.CollectInterval is range-checked to >= 1 s), so worker
// tests can tick in milliseconds. The published snapshot is patched after the
// range check; nothing reloads it during these tests.
func fastStatConfig(collectIntervalMs, batchCount int) *Config {
	c := defaultConfig()
	c.Set(CfgStatBatchCount, batchCount)
	c.load().values[CfgStatCollectInterval] = collectIntervalMs
	return c
}

// A getStats panic used to escape the collect loop and restart the worker,
// which rebuilt collected/batch and threw away the partial batch (up to
// batch_count-1 snapshots). It now costs exactly that tick's snapshot, as in
// Java's CollectJob.run(): the cursor stays put, the next tick fills the same
// slot, and the failure is reported through a throttled WARN.
func Test_collectAgentStatWorker_failedCollectionSkipsOnlyThatSnapshot(t *testing.T) {
	config := fastStatConfig(10, 100) // never completes within the test
	agent := newTestAgent(config)
	agent.statChan = make(chan *pb.PStatMessage, 1)

	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	agent.workerWg.Add(1)
	go agent.superviseWorker("collect agent stat", agent.collectAgentStatWorker)

	// Two good snapshots first, so a wrongly reset cursor is distinguishable
	// from one that was simply not advanced.
	assert.Eventually(t, func() bool { return agent.stats.batch.Load() >= 2 },
		5*time.Second, time.Millisecond, "the worker never collected")
	agent.stats.failCollects.Store(1)
	assert.Eventually(t, func() bool { return agent.stats.failCollects.Load() == 0 },
		5*time.Second, time.Millisecond, "the injected failure was never hit")
	// Read well inside the 10 ms until the next tick: a reset would show 0.
	assert.GreaterOrEqual(t, agent.stats.batch.Load(), int32(2), "a collection failure must not reset the partial batch")
	assert.Eventually(t, func() bool { return agent.stats.batch.Load() >= 3 },
		5*time.Second, time.Millisecond, "the loop must continue collecting after a failed tick")

	agent.Shutdown()

	logged := buf.String()
	assert.Contains(t, logged, "agent stat collection failed", "a skipped snapshot must be reported")
	assert.NotContains(t, logged, "goroutine panic", "the collect loop must not have been restarted")
	assert.NotContains(t, logged, "restart collect agent stat", "the collect loop must not have been restarted")
}

// Two failures inside one report interval yield one WARN line that counts
// what it suppressed, like the other throttled warning sites.
func Test_collectAgentStatWorker_collectionFailureWarningIsThrottled(t *testing.T) {
	config := fastStatConfig(5, 100)
	agent := newTestAgent(config)
	agent.statChan = make(chan *pb.PStatMessage, 1)
	agent.stats.failCollects.Store(3)

	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	agent.workerWg.Add(1)
	go agent.superviseWorker("collect agent stat", agent.collectAgentStatWorker)
	assert.Eventually(t, func() bool { return agent.stats.failCollects.Load() == 0 && agent.stats.batch.Load() >= 1 },
		5*time.Second, time.Millisecond, "collection must resume after the failures")
	agent.Shutdown()

	assert.Equal(t, 1, strings.Count(buf.String(), "agent stat collection failed"),
		"the warning must be throttled to one line per interval; got: %s", buf.String())
	assert.EqualValues(t, 2, agent.stats.collectFailures.suppressed.Load(),
		"the suppressed failures must be counted for the next report")
}

// A supervisor restart (a panic outside the collect call) keeps the partial
// batch: the worker re-takes only the CPU/time baseline. The restart is
// driven by a second worker run on the same stats, which is what
// superviseWorker does after a panic.
func Test_collectAgentStatWorker_restartKeepsPartialBatchAndRetakesBaseline(t *testing.T) {
	config := fastStatConfig(10, 100)
	agent := newTestAgent(config)
	agent.statChan = make(chan *pb.PStatMessage, 1)

	agent.workerWg.Add(1)
	go agent.superviseWorker("collect agent stat", agent.collectAgentStatWorker)
	assert.Eventually(t, func() bool { return agent.stats.batch.Load() >= 2 },
		5*time.Second, time.Millisecond, "the worker never collected")
	agent.Shutdown()
	kept := agent.stats.batch.Load()
	assert.GreaterOrEqual(t, kept, int32(2))
	before := agent.stats.lastCollectTime

	// Second run on the same stats, as superviseWorker's restart does.
	time.Sleep(30 * time.Millisecond)
	agent.enable.Store(true)
	agent.shutdown.Store(false)
	agent.stopOnce = sync.Once{}
	agent.stopCtx, agent.stopCancel = nil, nil
	agent.workerWg.Add(1)
	go agent.superviseWorker("collect agent stat", agent.collectAgentStatWorker)
	assert.Eventually(t, func() bool { return agent.stats.batch.Load() >= kept+1 },
		5*time.Second, time.Millisecond, "the restarted worker must continue the batch")
	agent.Shutdown()

	assert.True(t, agent.stats.lastCollectTime.After(before), "the time baseline must be re-taken on restart")
	assert.GreaterOrEqual(t, agent.stats.batch.Load(), kept+1, "the partial batch must survive the restart")
	// The first sample after the restart measures from the restart, not
	// across the gap since the last sample of the previous run.
	first := agent.stats.collected[kept]
	assert.Less(t, first.interval, int64(30), "the restart gap must not be reported as the interval")
}
