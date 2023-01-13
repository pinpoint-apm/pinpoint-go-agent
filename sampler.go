package pinpoint

import (
	"golang.org/x/time/rate"
	"sync/atomic"
	"time"
)

const samplingMaxPercentRate = 100 * 100

type sampler interface {
	isSampled() bool
}

type rateSampler struct {
	config  *Config
	counter uint64
}

func newRateSampler(config *Config) *rateSampler {
	if config.samplingCounterRate < 0 {
		config.samplingCounterRate = 0
	}
	return &rateSampler{
		config:  config,
		counter: 0,
	}
}

func (s *rateSampler) isSampled() bool {
	rate := s.config.samplingCounterRate
	if rate == 0 {
		return false
	}
	samplingCount := atomic.AddUint64(&s.counter, 1)
	isSampled := samplingCount % rate
	return isSampled == 0
}

type percentSampler struct {
	config  *Config
	counter uint64
}

func newPercentSampler(config *Config) *percentSampler {
	return &percentSampler{
		config:  config,
		counter: 0,
	}
}

func adjustPercentRate(rate float64) uint64 {
	if rate < 0 {
		rate = 0
	} else if rate < 0.01 {
		rate = 0.01
	} else if rate > 100 {
		rate = 100
	}

	return (uint64)(rate * 100)
}

func (s *percentSampler) isSampled() bool {
	rate := s.config.samplingPercentRate
	if rate == 0 {
		return false
	}
	samplingCount := atomic.AddUint64(&s.counter, rate)
	r := samplingCount % samplingMaxPercentRate
	return r < rate
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
