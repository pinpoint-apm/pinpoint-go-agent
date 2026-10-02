package pinpoint

import (
	"bytes"
	"fmt"
	"math"
	"strings"
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
			assert.Equal(t, tt.want, s.isSampled())
		})
	}
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
			assert.Equal(t, tt.want, s.isSampled())
		})
	}
}

func Test_traceSampler_unlimited_isNewSampled(t *testing.T) {
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
			s := &traceSampler{
				baseSampler: tt.fields.baseSampler,
			}
			assert.Equal(t, tt.want, s.isNewSampled(newAgentStats()))
		})
	}
}

func Test_traceSampler_unlimited_isContinueSampled(t *testing.T) {
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
			s := &traceSampler{
				baseSampler: tt.fields.baseSampler,
			}
			assert.Equal(t, tt.want, s.isContinueSampled(newAgentStats()))
		})
	}
}

func Test_traceSampler_isNewSampled(t *testing.T) {
	type fields struct {
		sampler *traceSampler
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{buildTraceSampler(newRateSampler(1), 10, 10)}, true},
		{"2", fields{buildTraceSampler(&rateSampler{rate: 10, counter: 1}, 10, 10)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.fields.sampler
			assert.Equal(t, tt.want, s.isNewSampled(newAgentStats()))
		})
	}
}

// The throughput limit samples one request per second per permit and skips
// the rest, for both the new and the continue sampler.
func Test_traceSampler_skip(t *testing.T) {
	for _, tt := range []struct {
		name      string
		sampler   *traceSampler
		isSampled func(*traceSampler, *agentStats) bool
		perSecond statsCounterSnapshot
	}{
		{"new", buildTraceSampler(newRateSampler(1), 1, 10), (*traceSampler).isNewSampled, statsCounterSnapshot{sampleNew: 1, skipNew: 99}},
		{"continue", buildTraceSampler(newRateSampler(100), 10, 1), (*traceSampler).isContinueSampled, statsCounterSnapshot{sampleCont: 1, skipCont: 99}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stats := newAgentStats()
			for second := 0; second < 2; second++ {
				if second > 0 {
					time.Sleep(time.Second)
				}
				for i := 0; i < 100; i++ {
					tt.isSampled(tt.sampler, stats)
				}
				assert.Equal(t, tt.perSecond, stats.drainCounters(), "second %d", second)
			}
		})
	}
}

func Test_traceSampler_isContinueSampled(t *testing.T) {
	type fields struct {
		sampler *traceSampler
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{buildTraceSampler(newRateSampler(1), 10, 10)}, true},
		{"2", fields{buildTraceSampler(newRateSampler(100), 10, 10)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.fields.sampler
			assert.Equal(t, tt.want, s.isContinueSampled(newAgentStats()))
		})
	}
}

// countConcurrent fires n concurrent calls and returns how many were sampled.
func countConcurrent(n int, isSampled func() bool) int {
	var wg sync.WaitGroup
	var count int64

	for i := 0; i < n; i++ {
		wg.Go(func() {
			if isSampled() {
				atomic.AddInt64(&count, 1)
			}
		})
	}
	wg.Wait()
	return int(count)
}

func Test_traceSampler_burst(t *testing.T) {
	const tps = 100
	s := buildTraceSampler(newRateSampler(1), tps, tps)
	stats := newAgentStats()

	// A fresh limiter starts empty, so a burst of tps requests arriving at once
	// yields exactly one sample. See newTokenBucket.
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

func Test_traceSampler_hugeThroughput(t *testing.T) {
	// a tps beyond one event per nanosecond makes per() an infinite rate: the
	// burst of tps must neither overflow the limiter nor throttle anything,
	// and the drain that empties a fresh bucket must not apply either.
	s := buildTraceSampler(newRateSampler(1), math.MaxInt32, math.MaxInt32)
	stats := newAgentStats()

	assert.Equal(t, 1000, countConcurrent(1000, func() bool { return s.isNewSampled(stats) }), "new")
	assert.Equal(t, 1000, countConcurrent(1000, func() bool { return s.isContinueSampled(stats) }), "continue")
}

// configured rate to hundredths of a percent and then picks one of three
// (FalseSampler), >= 10000 always samples (TrueSampler), anything between
// runs PercentRateSampler. Here the same three cases fall out of the
// truncation in newPercentSampler plus the two guards in isSampled.
func Test_percentSampler_javaMapping(t *testing.T) {
	tests := []struct {
		percent float64
		rate    uint64 // truncated internal rate
		sampled int    // out of 1000 calls
	}{
		{-1, 0, 0},
		{0, 0, 0},
		{0.005, 0, 0},
		{0.01, 1, 1},
		{0.5, 50, 5},
		{50, 5000, 500},
		{100, 10000, 1000},
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

// A negative rate turns sampling off like 0 does, but 0 is a deliberate switch
// and a negative value a typo: the coercion is logged once.
func Test_rateSampler_negativeWarns(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	s := newRateSampler(-1)
	assert.Equal(t, uint64(0), s.rate)
	assert.False(t, s.isSampled())
	assert.Equal(t, 1, strings.Count(buf.String(), "sampling counter rate -1 is negative, no new transaction is sampled"))

	buf.Reset()
	newRateSampler(0)
	assert.Empty(t, buf.String(), "an explicit 0 must not warn")
}

func Test_percentSampler_negativeWarns(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	s := newPercentSampler(-1)
	assert.Equal(t, uint64(0), s.rate)
	assert.False(t, s.isSampled())
	assert.Equal(t, 1, strings.Count(buf.String(), "sampling percent rate -1 is negative, no new transaction is sampled"))

	buf.Reset()
	newPercentSampler(0)
	assert.Empty(t, buf.String(), "an explicit 0 must not warn")
}

// The minimum rate samples exactly one of 10,000 requests.
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

// ===========================================================================
// Locked invariants - behaviour pinned against the Java and C++ agents. The
// cross-agent rationale and references live in doc/development.md.
// ===========================================================================

// Test_CountingSamplerPhase locks the counting sampler's phase.
// the process is sampled and every rate-th one after it - not the rate-th
// request.
func Test_CountingSamplerPhase(t *testing.T) {
	s := newRateSampler(3)

	var sampled []int
	for i := 1; i <= 10; i++ {
		if s.isSampled() {
			sampled = append(sampled, i)
		}
	}
	assert.Equal(t, []int{1, 4, 7, 10}, sampled, "the first call and every 3rd after it")
}

// TrueSampler and FalseSampler instead of CountingSampler.
func Test_CountingSamplerEdgeRates(t *testing.T) {
	always := newRateSampler(1)
	for i := 0; i < 5; i++ {
		assert.True(t, always.isSampled(), "rate 1 samples everything")
	}

	never := newRateSampler(0)
	for i := 0; i < 5; i++ {
		assert.False(t, never.isSampled(), "rate 0 samples nothing")
	}

	clamped := newRateSampler(-7)
	for i := 0; i < 5; i++ {
		assert.False(t, clamped.isSampled(), "a negative rate is clamped to 0, not treated as unsigned")
	}
}

// PercentRateSampler adds the rate to a counter and samples on a remainder in
// (0, rate] - the first request lands on exactly rate and is sampled, where a
// [0, rate) window would sample the second one instead.
func Test_PercentSamplerWindow(t *testing.T) {
	s := newPercentSampler(1) // rate 100 of 10000

	var sampled []int
	for i := 1; i <= 200; i++ {
		if s.isSampled() {
			sampled = append(sampled, i)
		}
	}
	assert.Equal(t, []int{1, 101}, sampled, "one per hundred, starting at the first call")
}

// Test_ThroughputLimiterInitialState locks the shape of the
// bucket behind every per-second throughput option (Sampling.NewThroughput,
// builds a Guava SmoothBursty whose initial storedPermits is 0: a fresh limiter
// injected through AllowN so the test is exact and sleep-free.
func Test_ThroughputLimiterInitialState(t *testing.T) {
	const tps = 10 // one token per 100ms
	l := newTokenBucket(tps)
	now := time.Now()

	assert.True(t, l.AllowN(now, 1), "the first call passes")
	assert.False(t, l.AllowN(now, 1), "no token is due yet")
	assert.False(t, l.AllowN(now.Add(50*time.Millisecond), 1), "half an interval is not a token")
	assert.True(t, l.AllowN(now.Add(100*time.Millisecond), 1), "one interval elapsed, one token due")
	assert.False(t, l.AllowN(now.Add(100*time.Millisecond), 1))
}

// Test_ThroughputLimiterCapacity locks the steady-state
// capacity at one second of permits, the maxBurstSeconds of RateLimiter.create:
// an idle bucket refills to exactly tps and no further, however long the idle.
func Test_ThroughputLimiterCapacity(t *testing.T) {
	const tps = 10
	l := newTokenBucket(tps)
	idle := time.Now().Add(10 * time.Second)

	admitted := 0
	for i := 0; i < 2*tps; i++ {
		if l.AllowN(idle, 1) {
			admitted++
		}
	}
	assert.Equal(t, tps, admitted, "an idle bucket holds exactly one second of permits")
}
