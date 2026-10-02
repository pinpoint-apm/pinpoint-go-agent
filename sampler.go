package pinpoint

import (
	"math"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
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
		// A rate of 0, i.e. never sampled. Warned about because it reads as a
		// typo, where an explicit 0 is a deliberate off.
		Log("config").Warnf("sampling percent rate %v is below the minimum 0.01, no new transaction is sampled", percent)
		percent = 0
	}

	// Rounded, not truncated: 0.29 * 100 is 28.999999999999996 in float64, so
	// a truncated rate sampled 0.28%.
	return &percentSampler{
		rate:    uint64(math.Round(percent * 100)),
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

// traceSampler decides whether a transaction is traced: the base sampler for a
// new one, and for a new and a continued one the throughput limiters, nil when
// the throughput is unlimited. It takes the agentStats to count into as an
// argument rather than holding one: the sampler is built by Config, which is
// created before the agent whose stats it counts into.
type traceSampler struct {
	baseSampler           sampler
	newSampleLimiter      *rate.Limiter
	continueSampleLimiter *rate.Limiter
}

func buildTraceSampler(base sampler, newTps int, continueTps int) *traceSampler {
	return &traceSampler{
		baseSampler:           base,
		newSampleLimiter:      newTokenBucket(newTps),
		continueSampleLimiter: newTokenBucket(continueTps),
	}
}

// newTokenBucket builds the limiter behind every per-second throughput option,
// or nil for tps 0 or less, which means unlimited.
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
// go.mod pins. On an unlimited rate (tps above one per nanosecond) AllowN is a
// no-op, which is the intended result.
func newTokenBucket(tps int) *rate.Limiter {
	if tps <= 0 {
		return nil
	}
	l := rate.NewLimiter(rate.Every(time.Second/time.Duration(tps)), tps)
	l.AllowN(time.Now(), tps-1)
	return l
}

func (s *traceSampler) isNewSampled(stats *agentStats) bool {
	if !s.baseSampler.isSampled() {
		stats.incrUnSampleNew()
		return false
	}
	if s.newSampleLimiter != nil && !s.newSampleLimiter.Allow() {
		stats.incrSkipNew()
		return false
	}
	stats.incrSampleNew()
	return true
}

func (s *traceSampler) isContinueSampled(stats *agentStats) bool {
	if s.continueSampleLimiter != nil && !s.continueSampleLimiter.Allow() {
		stats.incrSkipCont()
		return false
	}
	stats.incrSampleCont()
	return true
}
