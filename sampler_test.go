package pinpoint

import (
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
			assert.Equal(t, int64(1), sampleNew, "sampleNew")
			assert.Equal(t, int64(99), skipNew, "skipNew")

			time.Sleep(1 * time.Second)

			for i := 0; i < 100; i++ {
				s.isNewSampled()
			}
			assert.Equal(t, int64(1*2), sampleNew, "sampleNew")
			assert.Equal(t, int64(99*2), skipNew, "skipNew")
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
			assert.Equal(t, int64(1), sampleCont, "sampleCont")
			assert.Equal(t, int64(99), skipCont, "skipCont")

			time.Sleep(1 * time.Second)

			for i := 0; i < 100; i++ {
				s.isContinueSampled()
			}
			assert.Equal(t, int64(1*2), sampleCont, "sampleCont")
			assert.Equal(t, int64(99*2), skipCont, "skipCont")
		})
	}
}
