package pinpoint

import (
	"bytes"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_rateSampler_isSampled(t *testing.T) {
	type fields struct {
		rate    uint64
		counter uint64
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{1, 0}, true},
		{"2", fields{10, 0}, true},
		{"3", fields{10, 9}, false},
		{"4", fields{10, 10}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &rateSampler{
				rate:    tt.fields.rate,
				counter: tt.fields.counter,
			}
			if got := s.isSampled(); got != tt.want {
				t.Errorf("rateSampler.isSampled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The first request of a fresh sampler is sampled, like Java's CountingSampler,
// and the rate-th one after it - not the rate-th request.
func Test_rateSampler_samplesFirstRequest(t *testing.T) {
	s := newRateSampler(10)

	assert.True(t, s.isSampled(), "request 1")
	for i := 2; i <= 10; i++ {
		assert.False(t, s.isSampled(), "request %d", i)
	}
	assert.True(t, s.isSampled(), "request 11")
}

func Test_percentSampler_isSampled(t *testing.T) {
	type fields struct {
		percent float64
		counter uint64
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{100, 0}, true},
		{"2", fields{50, 0}, true},
		{"3", fields{50, 5000}, false},
		{"4", fields{1, 0}, true},
		{"5", fields{1, 9900}, false},
		{"6", fields{1, 10000}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &percentSampler{
				rate:    uint64(tt.fields.percent * 100),
				counter: tt.fields.counter,
			}
			if got := s.isSampled(); got != tt.want {
				t.Errorf("rateSampler.isSampled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The admission window is (0, rate] like Java's PercentRateSampler, so at 50%
// the odd requests are sampled - starting with the first - not the even ones.
// A rate of 100% is clamped to the max and always samples, the case Java gives
// to TrueSampler.
func Test_percentSampler_samplesFirstRequest(t *testing.T) {
	half := newPercentSampler(50)
	for i := 1; i <= 4; i++ {
		assert.Equal(t, i%2 == 1, half.isSampled(), "50%% request %d", i)
	}

	full := newPercentSampler(100)
	for i := 1; i <= 3; i++ {
		assert.True(t, full.isSampled(), "100%% request %d", i)
	}
}

func Test_basicTraceSampler_isNewSampled(t *testing.T) {
	type fields struct {
		baseSampler sampler
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{newRateSampler(1)}, true},
		{"2", fields{&rateSampler{rate: 10, counter: 1}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &basicTraceSampler{
				baseSampler: tt.fields.baseSampler,
			}
			if got := s.isNewSampled(newAgentStats()); got != tt.want {
				t.Errorf("basicTraceSampler.isNewSampled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_basicTraceSampler_isContinueSampled(t *testing.T) {
	type fields struct {
		baseSampler sampler
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{newRateSampler(1)}, true},
		{"2", fields{newPercentSampler(10)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &basicTraceSampler{
				baseSampler: tt.fields.baseSampler,
			}
			if got := s.isContinueSampled(newAgentStats()); got != tt.want {
				t.Errorf("basicTraceSampler.isNewSampled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_throughputLimitTraceSampler_isNewSampled(t *testing.T) {
	type fields struct {
		sampler traceSampler
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{newThroughputLimitTraceSampler(newRateSampler(1), 10, 10)}, true},
		{"2", fields{newThroughputLimitTraceSampler(&rateSampler{rate: 10, counter: 1}, 10, 10)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.fields.sampler
			if got := s.isNewSampled(newAgentStats()); got != tt.want {
				t.Errorf("throughputLimitTraceSampler.isNewSampled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_throughputLimitTraceSampler_skipNew(t *testing.T) {
	type fields struct {
		sampler traceSampler
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{newThroughputLimitTraceSampler(newRateSampler(1), 1, 10)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.fields.sampler
			stats := newAgentStats()

			for i := 0; i < 100; i++ {
				s.isNewSampled(stats)
			}
			assert.Equal(t, int64(1), stats.readCounters().sampleNew, "sampleNew")
			assert.Equal(t, int64(99), stats.readCounters().skipNew, "skipNew")

			time.Sleep(1 * time.Second)

			for i := 0; i < 100; i++ {
				s.isNewSampled(stats)
			}
			assert.Equal(t, int64(1*2), stats.readCounters().sampleNew, "sampleNew")
			assert.Equal(t, int64(99*2), stats.readCounters().skipNew, "skipNew")
		})
	}
}

func Test_throughputLimitTraceSampler_isContinueSampled(t *testing.T) {
	type fields struct {
		sampler traceSampler
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{newThroughputLimitTraceSampler(newRateSampler(1), 10, 10)}, true},
		{"2", fields{newThroughputLimitTraceSampler(newRateSampler(100), 10, 10)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.fields.sampler
			if got := s.isContinueSampled(newAgentStats()); got != tt.want {
				t.Errorf("throughputLimitTraceSampler.isNewSampled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_throughputLimitTraceSampler_skipContinue(t *testing.T) {
	type fields struct {
		sampler traceSampler
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{newThroughputLimitTraceSampler(newRateSampler(100), 10, 1)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.fields.sampler
			stats := newAgentStats()

			for i := 0; i < 100; i++ {
				s.isContinueSampled(stats)
			}
			assert.Equal(t, int64(1), stats.readCounters().sampleCont, "sampleCont")
			assert.Equal(t, int64(99), stats.readCounters().skipCont, "skipCont")

			time.Sleep(1 * time.Second)

			for i := 0; i < 100; i++ {
				s.isContinueSampled(stats)
			}
			assert.Equal(t, int64(1*2), stats.readCounters().sampleCont, "sampleCont")
			assert.Equal(t, int64(99*2), stats.readCounters().skipCont, "skipCont")
		})
	}
}

// countConcurrent fires n concurrent calls and returns how many were sampled.
func countConcurrent(n int, isSampled func() bool) int {
	var wg sync.WaitGroup
	var count int64

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if isSampled() {
				atomic.AddInt64(&count, 1)
			}
		}()
	}
	wg.Wait()
	return int(count)
}

func Test_throughputLimitTraceSampler_burst(t *testing.T) {
	const tps = 100
	s := newThroughputLimitTraceSampler(newRateSampler(1), tps, tps)
	stats := newAgentStats()

	// A fresh limiter starts empty, so a burst of tps requests arriving at once
	// yields exactly one sample. This used to assert tps: the bucket started
	// full, on the belief that the Guava RateLimiter of the Java agent does.
	// It does not - SmoothBursty.doSetRate sets storedPermits to 0 in its
	// initial state, and the C++ agent's test_limiter.cpp locks the same
	// first-call-then-pace behaviour. See newTokenBucket.
	assert.Equal(t, 1, countConcurrent(tps, func() bool { return s.isNewSampled(stats) }), "new burst")
	assert.Equal(t, 1, countConcurrent(tps, func() bool { return s.isContinueSampled(stats) }), "continue burst")

	// a second of sustained load against the empty bucket yields about tps
	// samples: the pacing, not the initial state, sets the average.
	sampled := 0
	for deadline := time.Now().Add(1 * time.Second); time.Now().Before(deadline); {
		if s.isNewSampled(stats) {
			sampled++
		}
	}
	assert.InDelta(t, tps, sampled, tps/10, "new average")

	// the steady-state capacity is still one second of permits: after an idle
	// second a burst of 2*tps is sampled up to tps (plus what trickles in
	// while the burst runs).
	time.Sleep(1100 * time.Millisecond)
	burst := countConcurrent(2*tps, func() bool { return s.isNewSampled(stats) })
	assert.GreaterOrEqual(t, burst, tps, "idle burst")
	assert.Less(t, burst, tps+tps/10, "idle burst")
}

func Test_throughputLimitTraceSampler_hugeThroughput(t *testing.T) {
	// a tps beyond one event per nanosecond makes per() an infinite rate: the
	// burst of tps must neither overflow the limiter nor throttle anything,
	// and the drain that empties a fresh bucket must not apply either.
	s := newThroughputLimitTraceSampler(newRateSampler(1), math.MaxInt32, math.MaxInt32)
	stats := newAgentStats()

	assert.Equal(t, 1000, countConcurrent(1000, func() bool { return s.isNewSampled(stats) }), "new")
	assert.Equal(t, 1000, countConcurrent(1000, func() bool { return s.isContinueSampled(stats) }), "continue")
}

// The Java mapping table, ported input for input. Java truncates the
// configured rate to hundredths of a percent and then picks one of three
// samplers (PercentSamplerFactory.java:40-48,56-58): <= 0 never samples
// (FalseSampler), >= 10000 always samples (TrueSampler), anything between
// runs PercentRateSampler. Here the same three cases fall out of the
// truncation in newPercentSampler plus the two guards in isSampled.
func Test_percentSampler_javaMapping(t *testing.T) {
	tests := []struct {
		percent float64
		rate    uint64 // truncated internal rate
		sampled int    // out of 1000 calls
	}{
		{-1, 0, 0},      // Java: (long)(-1*100) = -100 -> FalseSampler
		{0, 0, 0},       // Java: 0 -> FalseSampler
		{0.005, 0, 0},   // Java: (long)0.5 = 0 -> FalseSampler
		{0.01, 1, 1},    // Java: 1 -> PercentRateSampler, 0.01%
		{50, 5000, 500}, // Java: 5000 -> PercentRateSampler, 50%
		{100, 10000, 1000},
		// Java truncates 150 to 15000 and hands it to TrueSampler; the clamp
		// to 100 lands on 10000, which isSampled treats as always-sample too.
		{150, 10000, 1000},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%g", tt.percent), func(t *testing.T) {
			s := newPercentSampler(tt.percent)
			assert.Equal(t, tt.rate, s.rate, "truncated rate")

			sampled := 0
			for i := 0; i < 1000; i++ {
				if s.isSampled() {
					sampled++
				}
			}
			assert.Equal(t, tt.sampled, sampled, "sampled out of 1000")
		})
	}
}

// The clamp to 100 is warned about, the same silence the sampling type and the
// out-of-range queue sizes were taken out of: a rate above the documented
// maximum is a misread of the option, not a request to sample everything. A
// rate inside the range stays quiet.
func Test_percentSampler_aboveMaximumWarns(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	s := newPercentSampler(1000)
	assert.Equal(t, uint64(10000), s.rate, "the clamp to 100 stopped working")
	assert.Contains(t, buf.String(), "sampling percent rate 1000 is above the maximum 100")

	buf.Reset()
	newPercentSampler(100)
	assert.Empty(t, buf.String(), "a rate at the maximum must not warn")
}

// The minimum rate still works after dropping the clamp that used to raise
// every sub-0.01 value to it: exactly one of 10,000 requests.
func Test_percentSampler_minimumRate(t *testing.T) {
	s := newPercentSampler(0.01)

	sampled := 0
	for i := 0; i < 10000; i++ {
		if s.isSampled() {
			sampled++
		}
	}
	assert.Equal(t, 1, sampled)
}

// Rate 1 samples everything without touching the shared counter: the answer is
// known in advance, and the RMW was one contended cache line per request.
func TestRateSamplerRateOneSkipsCounter(t *testing.T) {
	s := newRateSampler(1)
	for i := 0; i < 100; i++ {
		assert.True(t, s.isSampled(), "request %d", i)
	}
	assert.Equal(t, uint64(0), atomic.LoadUint64(&s.counter), "rate 1 must not spend the counter")
}
