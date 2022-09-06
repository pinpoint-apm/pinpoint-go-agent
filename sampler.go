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

func newRateSampler(r int) *rateSampler {
	return &rateSampler{
		samplingRate: (uint64)(r),
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

func newPercentSampler(r float64) *percentSampler {
	return &percentSampler{
		samplingRate: (uint64)(r * 100),
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
	newSamplelimiter      *rate.Limiter
	continueSamplelimiter *rate.Limiter
}

func newThroughputLimitTraceSampler(base sampler, newTps int, continueTps int) *throughputLimitTraceSampler {
	return &throughputLimitTraceSampler{
		baseSampler:           base,
		newSamplelimiter:      rate.NewLimiter(per(newTps, time.Second), 1),
		continueSamplelimiter: rate.NewLimiter(per(continueTps, time.Second), 1),
	}
}

func per(throughput int, d time.Duration) rate.Limit {
	return rate.Every(d / time.Duration(throughput))
}

func (s *throughputLimitTraceSampler) isNewSampled() bool {
	sampled := s.baseSampler.isSampled()
	if sampled {
		sampled = s.newSamplelimiter.Allow()
		if sampled {
			incrSampleNew()
		} else {
			incrSkipNew()
		}
	} else {
		incrUnSampleNew()
	}

	return sampled
}

func (s *throughputLimitTraceSampler) isContinueSampled() bool {
	sampled := s.baseSampler.isSampled()
	if sampled {
		sampled = s.continueSamplelimiter.Allow()
		if sampled {
			incrSampleCont()
		} else {
			incrSkipCont()
		}
	} else {
		incrUnSampleCont()
	}

	return sampled
}
