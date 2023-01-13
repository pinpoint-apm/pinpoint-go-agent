package pinpoint

import (
	"testing"
)

func makeConfig(rate uint64) *Config {
	c := defaultConfig()
	c.samplingCounterRate = rate
	return c
}

func Test_rateSampler_isSampled(t *testing.T) {
	type fields struct {
		config  *Config
		counter uint64
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{makeConfig(1), 0}, true},
		{"2", fields{makeConfig(10), 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &rateSampler{
				config:  tt.fields.config,
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
		{"1", fields{newRateSampler(makeConfig(1))}, true},
		{"2", fields{newRateSampler(makeConfig(10))}, false},
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

func Test_throughputLimitTraceSampler_isNewSampled(t *testing.T) {
	type fields struct {
		sampler traceSampler
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{"1", fields{newThroughputLimitTraceSampler(newRateSampler(makeConfig(1)), 10, 10)}, true},
		{"2", fields{newThroughputLimitTraceSampler(newRateSampler(makeConfig(10)), 10, 10)}, false},
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
