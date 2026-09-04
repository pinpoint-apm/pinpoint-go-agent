package pinpoint

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

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
		c.sampleCont += atomic.LoadInt64(&s.sampleCont)
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
