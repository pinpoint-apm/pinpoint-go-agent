package pinpoint

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

// readStatsCounters sums the shards without resetting them, for tests that
// assert on cumulative counts (drainStatsCounters is destructive).
func readStatsCounters() statsCounterSnapshot {
	var c statsCounterSnapshot
	for i := range statShards {
		s := &statShards[i]
		c.sampleNew += atomic.LoadInt64(&s.sampleNew)
		c.skipNew += atomic.LoadInt64(&s.skipNew)
		c.sampleCont += atomic.LoadInt64(&s.sampleCont)
		c.skipCont += atomic.LoadInt64(&s.skipCont)
	}
	return c
}

func Test_drainStatsCountersSwapsAndResets(t *testing.T) {
	resetResponseTime()

	collectResponseTime(100)
	collectResponseTime(200)
	incrSampleNew()
	incrUnSampleNew()
	incrSampleCont()
	incrUnSampleCont()
	incrSkipNew()
	incrSkipCont()

	counters := drainStatsCounters()

	assert.Equal(t, int64(300), counters.accResponseTime)
	assert.Equal(t, int64(200), counters.maxResponseTime)
	assert.Equal(t, int64(2), counters.requestCount)
	assert.Equal(t, int64(1), counters.sampleNew)
	assert.Equal(t, int64(1), counters.unSampleNew)
	assert.Equal(t, int64(1), counters.sampleCont)
	assert.Equal(t, int64(1), counters.unSampleCont)
	assert.Equal(t, int64(1), counters.skipNew)
	assert.Equal(t, int64(1), counters.skipCont)

	assert.Equal(t, statsCounterSnapshot{}, drainStatsCounters(), "second drain must return zeros")
}

// Every increment must be aggregated exactly once across all shards,
// regardless of which goroutine (and therefore which shard) recorded it.
func Test_drainStatsCountersAggregatesAllShards(t *testing.T) {
	resetResponseTime()

	const goroutines = 64
	const perG = 100

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				collectResponseTime(int64(g*perG + i + 1))
				incrSampleNew()
				incrUnSampleNew()
				incrSampleCont()
				incrUnSampleCont()
				incrSkipNew()
				incrSkipCont()
			}
		}(g)
	}
	wg.Wait()

	counters := drainStatsCounters()

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
	assert.Equal(t, statsCounterSnapshot{}, drainStatsCounters(), "second drain must return zeros")
}

// Without a goid offset the counters degrade to a single shard but must
// still aggregate correctly.
func Test_statShardSelfWithoutGoIdOffset(t *testing.T) {
	saved := goIdOffset
	goIdOffset = 0
	defer func() { goIdOffset = saved }()

	resetResponseTime()
	collectResponseTime(100)
	incrSkipNew()

	assert.Equal(t, &statShards[0], statShardSelf())

	counters := drainStatsCounters()
	assert.Equal(t, int64(100), counters.accResponseTime)
	assert.Equal(t, int64(100), counters.maxResponseTime)
	assert.Equal(t, int64(1), counters.requestCount)
	assert.Equal(t, int64(1), counters.skipNew)
}

func Test_collectResponseTimePreservesMax(t *testing.T) {
	resetResponseTime()

	collectResponseTime(300)
	collectResponseTime(100)
	collectResponseTime(200)

	counters := drainStatsCounters()

	assert.Equal(t, int64(600), counters.accResponseTime)
	assert.Equal(t, int64(3), counters.requestCount)
	assert.Equal(t, int64(300), counters.maxResponseTime)
	assert.Equal(t, int64(200), calcResponseAvg(counters.accResponseTime, counters.requestCount))
}

func Test_calcResponseAvgReturnsZeroWithoutRequests(t *testing.T) {
	assert.Equal(t, int64(0), calcResponseAvg(100, 0))
}
