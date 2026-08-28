package pinpoint

import (
	"testing"
)

// Benchmarks for the per-request stat counters (stats.go). The counters are
// sharded by goroutine id within one agent's agentStats; run with -cpu=1,4,16
// to expose the cross-core traffic the sharding removes.

// The per-request combo: response time (acc+count+max) plus one sampler
// outcome counter.
func BenchmarkStatsCounterUpdate(b *testing.B) {
	stats := newAgentStats()
	b.RunParallel(func(pb *testing.PB) {
		i := int64(0)
		for pb.Next() {
			stats.collectResponseTime(i & 1023)
			stats.incrSampleNew()
			i++
		}
	})
}

func BenchmarkCollectResponseTime(b *testing.B) {
	stats := newAgentStats()
	b.RunParallel(func(pb *testing.PB) {
		i := int64(0)
		for pb.Next() {
			stats.collectResponseTime(i & 1023)
			i++
		}
	})
}
