package pinpoint

import (
	"golang.org/x/time/rate"
	"sync/atomic"
	"time"
)

type sampler interface {
	isSampled() bool
}

type rateSampler struct {
	samplingRate uint64
	counter      uint64
}

func newRateSampler(rate int) *rateSampler {
	if rate < 0 {
		rate = 0
	}
	return &rateSampler{
		samplingRate: (uint64)(rate),
		counter:      0,
	}
}

func (s *rateSampler) isSampled() bool {
	if s.samplingRate == 0 {
		return false
	}

	samplingCount := atomic.AddUint64(&s.counter, 1)
	isSampled := samplingCount % s.samplingRate
	return isSampled == 0
}

type percentSampler struct {
	samplingRate uint64
	counter      uint64
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
		samplingRate: (uint64)(percent * 100),
		counter:      0,
	}
}

func (s *percentSampler) isSampled() bool {
	if s.samplingRate == 0 {
		return false
	}

	samplingCount := atomic.AddUint64(&s.counter, s.samplingRate)
	r := samplingCount % SmaplingMaxPercentRate
	return r < s.samplingRate
}

type traceSampler interface {
	isNewSampled() bool
	isContinueSampled() bool
}

type basicTraceSampler struct {
	baseSampler sampler
}

func newBasicTraceSampler(base sampler) *basicTraceSampler {
	return &basicTraceSampler{
		baseSampler: base,
	}
}

func (s *basicTraceSampler) isNewSampled() bool {
	sampled := s.baseSampler.isSampled()
	if sampled {
		incrSampleNew()
	} else {
		incrUnSampleNew()
	}
	return sampled
}

func (s *basicTraceSampler) isContinueSampled() bool {
	sampled := s.baseSampler.isSampled()
	if sampled {
		incrSampleCont()
	} else {
		incrUnSampleCont()
	}
	return sampled
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
		newLimiter = rate.NewLimiter(per(newTps, time.Second), 1)
	}
	if continueTps > 0 {
		contLimiter = rate.NewLimiter(per(continueTps, time.Second), 1)
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

func (s *throughputLimitTraceSampler) isNewSampled() bool {
	sampled := s.baseSampler.isSampled()
	if sampled {
		if s.newSampleLimiter != nil {
			sampled = s.newSampleLimiter.Allow()
			if sampled {
				incrSampleNew()
			} else {
				incrSkipNew()
			}
		} else {
			incrSampleNew()
		}
	} else {
		incrUnSampleNew()
	}

	return sampled
}

func (s *throughputLimitTraceSampler) isContinueSampled() bool {
	sampled := s.baseSampler.isSampled()
	if sampled {
		if s.continueSampleLimiter != nil {
			sampled = s.continueSampleLimiter.Allow()
			if sampled {
				incrSampleCont()
			} else {
				incrSkipCont()
			}
		} else {
			incrSampleCont()
		}
	} else {
		incrUnSampleCont()
	}

	return sampled
}
