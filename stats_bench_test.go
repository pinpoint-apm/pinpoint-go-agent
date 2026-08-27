package pinpoint

import (
	"testing"
)

// Benchmarks for the per-request stat counters (stats.go). The counters are
// process-global int64s, so every request's atomic RMW hits the same cache
// lines; run with -cpu=1,4,16 to expose the cross-core traffic.

// The per-request combo: response time (acc+count+max) plus one sampler
// outcome counter.
func BenchmarkStatsCounterUpdate(b *testing.B) {
	resetResponseTime()
	b.RunParallel(func(pb *testing.PB) {
		i := int64(0)
		for pb.Next() {
			collectResponseTime(i & 1023)
			incrSampleNew()
			i++
		}
	})
}

func BenchmarkCollectResponseTime(b *testing.B) {
	resetResponseTime()
	b.RunParallel(func(pb *testing.PB) {
		i := int64(0)
		for pb.Next() {
			collectResponseTime(i & 1023)
			i++
		}
	})
}
