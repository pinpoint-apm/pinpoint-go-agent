package pinpoint

import (
	"golang.org/x/time/rate"
	"sync/atomic"
	"time"
)

const (
	samplingMaxPercentRate = 100 * 100
)

type sampler interface {
	isSampled() bool
}

type rateSampler struct {
	rate    uint64
	counter uint64
}

func newRateSampler(rate int) *rateSampler {
	if rate < 0 {
		rate = 0
	}
	return &rateSampler{
		rate:    uint64(rate),
		counter: 0,
	}
}

func (s *rateSampler) isSampled() bool {
	if s.rate == 0 {
		return false
	}
	// The pre-increment value decides, like Java's CountingSampler doing a
	// getAndIncrement: the first request of the process is sampled and the
	// rate-th one after it, not the rate-th request.
	samplingCount := atomic.AddUint64(&s.counter, 1) - 1
	isSampled := samplingCount % s.rate
	return isSampled == 0
}

type percentSampler struct {
	rate    uint64
	counter uint64
}

func newPercentSampler(percent float64) *percentSampler {
	if percent < 0 {
		percent = 0
	} else if percent < 0.01 {
		percent = 0.01
	} else if percent > 100 {
		percent = 100
	}

	return &percentSampler{
		rate:    uint64(percent * 100),
		counter: 0,
	}
}

func (s *percentSampler) isSampled() bool {
	if s.rate == 0 {
		return false
	}
	// A rate of 100% is clamped to the max, where the remainder below is always
	// 0; Java hands that case to TrueSampler instead of PercentRateSampler.
	if s.rate >= samplingMaxPercentRate {
		return true
	}
	// The admission window is (0, rate], like Java's PercentRateSampler: the
	// first request of the process lands on a remainder of exactly rate and is
	// sampled, where a [0, rate) window samples the second one instead.
	samplingCount := atomic.AddUint64(&s.counter, s.rate)
	r := samplingCount % samplingMaxPercentRate
	return r > 0 && r <= s.rate
}

// traceSampler takes the agentStats to count into as an argument rather than
// holding one: the sampler is built by Config, which outlives - and is created
// before - any agent. The C++ agent's TraceSampler holds an AgentService and
// asks it for getAgentStats() per decision, which is the same thing.
type traceSampler interface {
	isNewSampled(stats *agentStats) bool
	isContinueSampled(stats *agentStats) bool
}

type basicTraceSampler struct {
	baseSampler sampler
}

func newBasicTraceSampler(base sampler) *basicTraceSampler {
	return &basicTraceSampler{
		baseSampler: base,
	}
}

func (s *basicTraceSampler) isNewSampled(stats *agentStats) bool {
	sampled := s.baseSampler.isSampled()
	if sampled {
		stats.incrSampleNew()
	} else {
		stats.incrUnSampleNew()
	}
	return sampled
}

func (s *basicTraceSampler) isContinueSampled(stats *agentStats) bool {
	stats.incrSampleCont()
	return true
}

type throughputLimitTraceSampler struct {
	baseSampler           sampler
	newSampleLimiter      *rate.Limiter
	continueSampleLimiter *rate.Limiter
}

func newThroughputLimitTraceSampler(base sampler, newTps int, continueTps int) *throughputLimitTraceSampler {
	var (
		newLimiter  *rate.Limiter
		contLimiter *rate.Limiter
	)

	// The burst is the tps itself, not 1: Java's RateLimitTraceSampler uses a
	// Guava RateLimiter, a token bucket equivalent to x/time/rate that holds up
	// to one second of permits, so a burst of tps requests arriving at once is
	// sampled in full. A burst of 1 would spread the same tps into one sample
	// per 1/tps seconds and drop most of a bursty load.
	if newTps > 0 {
		newLimiter = rate.NewLimiter(per(newTps, time.Second), newTps)
	}
	if continueTps > 0 {
		contLimiter = rate.NewLimiter(per(continueTps, time.Second), continueTps)
	}
	return &throughputLimitTraceSampler{
		baseSampler:           base,
		newSampleLimiter:      newLimiter,
		continueSampleLimiter: contLimiter,
	}
}

func per(throughput int, d time.Duration) rate.Limit {
	return rate.Every(d / time.Duration(throughput))
}

func (s *throughputLimitTraceSampler) isNewSampled(stats *agentStats) bool {
	sampled := s.baseSampler.isSampled()
	if sampled {
		if s.newSampleLimiter != nil {
			sampled = s.newSampleLimiter.Allow()
			if sampled {
				stats.incrSampleNew()
			} else {
				stats.incrSkipNew()
			}
		} else {
			stats.incrSampleNew()
		}
	} else {
		stats.incrUnSampleNew()
	}

	return sampled
}

func (s *throughputLimitTraceSampler) isContinueSampled(stats *agentStats) bool {
	sampled := true
	if s.continueSampleLimiter != nil {
		sampled = s.continueSampleLimiter.Allow()
		if sampled {
			stats.incrSampleCont()
		} else {
			stats.incrSkipCont()
		}
	} else {
		stats.incrSampleCont()
	}

	return sampled
}
