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
		// A negative rate disables sampling, and is warned about because an
		// explicit 0 is the deliberate switch. This runs whenever the sampler is
		// (re)built, so a reload with the same typo warns again.
		Log("config").Warnf("sampling counter rate %d is negative, no new transaction is sampled", rate)
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
	// Rate 1 (the default) samples every transaction, so the counter below would
	// only add a contended process-wide RMW per request for a known answer.
	if s.rate == 1 {
		return true
	}
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
		// A negative rate disables sampling, warned about like a negative
		// counter rate. An explicit 0 stays quiet.
		Log("config").Warnf("sampling percent rate %v is negative, no new transaction is sampled", percent)
		percent = 0
	} else if percent > 100 {
		// Clamped, but not silently: 100 is the documented maximum, so a rate
		// above it is a misread of the option - a per-mille value, or a 1/rate
		// counter.
		Log("config").Warnf("sampling percent rate %v is above the maximum 100, every new transaction is sampled", percent)
		percent = 100
	} else if percent > 0 && percent < 0.01 {
		// Truncated to a rate of 0 below, i.e. never sampled. Warned about
		// because it reads as a typo, where an explicit 0 is a deliberate off.
		Log("config").Warnf("sampling percent rate %v is below the minimum 0.01, no new transaction is sampled", percent)
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
	// A rate of 100% reaches the max, where the remainder below always falls in
	// the sampling window anyway.
	if s.rate >= samplingMaxPercentRate {
		return true
	}
	// The window is (0, rate], so the first request of the process lands on a
	// remainder of exactly rate and is sampled; a [0, rate) window would sample
	// the second one instead.
	samplingCount := atomic.AddUint64(&s.counter, s.rate)
	r := samplingCount % samplingMaxPercentRate
	return r > 0 && r <= s.rate
}

// traceSampler takes the agentStats to count into as an argument rather than
// holding one: the sampler is built by Config, which is created before the agent
// whose stats it counts into.
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

	if newTps > 0 {
		newLimiter = newTokenBucket(newTps)
	}
	if continueTps > 0 {
		contLimiter = newTokenBucket(continueTps)
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

// newTokenBucket builds the limiter behind every per-second throughput option.
//
//   - Steady-state capacity is one second of permits, so a burst of tps requests
//     after an idle second is sampled in full. A burst of 1 would spread the
//     same tps into one sample per 1/tps seconds and drop most of a bursty load.
//   - The bucket starts empty but for one permit, not full: a fresh limiter
//     admits its first caller and paces everyone after it at tps until idle
//     time has refilled the bucket. rate.NewLimiter starts full, so the tokens
//     above the first are drained here, which also starts the refill clock.
//
// A limiter is rebuilt on every sampling reload, so a reload starts from an
// empty bucket; the callers keep the previous limiter when the option did not
// change, so an unrelated reload does not restart the pacing.
//
// AllowN rather than SetTokensAt, which does not exist in the x/time version
// go.mod pins. On an unlimited rate (tps above one per nanosecond, see per)
// AllowN is a no-op, which is the intended result.
func newTokenBucket(tps int) *rate.Limiter {
	l := rate.NewLimiter(per(tps, time.Second), tps)
	l.AllowN(time.Now(), tps-1)
	return l
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
