package pinpoint

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

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

	assert.Equal(t, int64(0), atomic.LoadInt64(&accResponseTime))
	assert.Equal(t, int64(0), atomic.LoadInt64(&maxResponseTime))
	assert.Equal(t, int64(0), atomic.LoadInt64(&requestCount))
	assert.Equal(t, int64(0), atomic.LoadInt64(&sampleNew))
	assert.Equal(t, int64(0), atomic.LoadInt64(&unSampleNew))
	assert.Equal(t, int64(0), atomic.LoadInt64(&sampleCont))
	assert.Equal(t, int64(0), atomic.LoadInt64(&unSampleCont))
	assert.Equal(t, int64(0), atomic.LoadInt64(&skipNew))
	assert.Equal(t, int64(0), atomic.LoadInt64(&skipCont))
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
