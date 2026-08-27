package pinpoint

import (
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
		{"2", fields{10, 0}, false},
		{"3", fields{10, 9}, true},
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
		{"2", fields{50, 0}, false},
		{"3", fields{50, 5000}, true},
		{"4", fields{1, 0}, false},
		{"5", fields{1, 9900}, true},
		{"6", fields{1, 10000}, false},
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
		{"2", fields{newRateSampler(10)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &basicTraceSampler{
				baseSampler: tt.fields.baseSampler,
			}
			if got := s.isNewSampled(); got != tt.want {
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
			if got := s.isContinueSampled(); got != tt.want {
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
		{"2", fields{newThroughputLimitTraceSampler(newRateSampler(10), 10, 10)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.fields.sampler
			if got := s.isNewSampled(); got != tt.want {
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
			resetResponseTime()

			for i := 0; i < 100; i++ {
				s.isNewSampled()
			}
			assert.Equal(t, int64(1), readStatsCounters().sampleNew, "sampleNew")
			assert.Equal(t, int64(99), readStatsCounters().skipNew, "skipNew")

			time.Sleep(1 * time.Second)

			for i := 0; i < 100; i++ {
				s.isNewSampled()
			}
			assert.Equal(t, int64(1*2), readStatsCounters().sampleNew, "sampleNew")
			assert.Equal(t, int64(99*2), readStatsCounters().skipNew, "skipNew")
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
			if got := s.isContinueSampled(); got != tt.want {
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
			resetResponseTime()

			for i := 0; i < 100; i++ {
				s.isContinueSampled()
			}
			assert.Equal(t, int64(1), readStatsCounters().sampleCont, "sampleCont")
			assert.Equal(t, int64(99), readStatsCounters().skipCont, "skipCont")

			time.Sleep(1 * time.Second)

			for i := 0; i < 100; i++ {
				s.isContinueSampled()
			}
			assert.Equal(t, int64(1*2), readStatsCounters().sampleCont, "sampleCont")
			assert.Equal(t, int64(99*2), readStatsCounters().skipCont, "skipCont")
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

	// a burst of tps requests arriving at once is sampled in full: the limiter
	// starts with tps tokens, like the fixed window of the Java and C++ agents.
	assert.Equal(t, tps, countConcurrent(tps, s.isNewSampled), "new burst")
	assert.Equal(t, tps, countConcurrent(tps, s.isContinueSampled), "continue burst")

	// the burst does not raise the average: the tokens are spent now, so a
	// second of sustained load past the empty bucket yields about tps samples.
	sampled := 0
	for deadline := time.Now().Add(1 * time.Second); time.Now().Before(deadline); {
		if s.isNewSampled() {
			sampled++
		}
	}
	assert.InDelta(t, tps, sampled, tps/10, "new average")
}

func Test_throughputLimitTraceSampler_hugeThroughput(t *testing.T) {
	// a tps beyond one event per nanosecond makes per() an infinite rate: the
	// burst of tps must neither overflow the limiter nor throttle anything.
	s := newThroughputLimitTraceSampler(newRateSampler(1), math.MaxInt32, math.MaxInt32)

	assert.Equal(t, 1000, countConcurrent(1000, s.isNewSampled), "new")
	assert.Equal(t, 1000, countConcurrent(1000, s.isContinueSampled), "continue")
}
