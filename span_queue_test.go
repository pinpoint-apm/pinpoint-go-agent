package pinpoint

import (
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
)

// Test_spanQueueShardIsCacheLinePadded guards the false-sharing fix: the shards
// must stay a whole cache line apart, not packed several to a line.
func Test_spanQueueShardIsCacheLinePadded(t *testing.T) {
	if got := unsafe.Sizeof(spanQueueShard{}); got%cacheLinePadSize != 0 {
		t.Errorf("spanQueueShard is %d bytes, not a multiple of the %d-byte shard stride: shards share a cache line", got, cacheLinePadSize)
	}
}

func Test_spanQueue_shardCapacitySumsToCapacity(t *testing.T) {
	for _, capacity := range []int{1, 2, 31, 32, 33, 256, 1000, 1024, 4096} {
		q := newSpanQueue(capacity)
		total := 0
		for i := range q.shards {
			total += len(q.shards[i].cells)
		}
		assert.Equal(t, capacity, total, "capacity %d", capacity)
		assert.LessOrEqual(t, len(q.shards), spanQueueMaxShards, "capacity %d", capacity)
	}
}

// Test_spanQueue_singleProducerUsesFullCapacity is the scenario that motivated
// the C++ agent's quota borrowing: consumer stalled, one producer. Non-sticky
// shard placement must retain the full configured capacity, not one shard's
// slice of it.
func Test_spanQueue_singleProducerUsesFullCapacity(t *testing.T) {
	const capacity = 1024
	q := newSpanQueue(capacity)
	agent := newTestAgent(defaultConfig())
	chunk := newTestSpanChunk(agent)

	for i := 0; i < capacity; i++ {
		assert.True(t, q.enqueue(chunk))
	}

	assert.Equal(t, capacity, q.length(), "retained spans must fill the whole buffer")
	assert.Zero(t, q.dropCount(), "filling the configured capacity must not drop spans")

	assert.True(t, q.enqueue(chunk))
	assert.Equal(t, capacity, q.length(), "the queue must remain bounded after saturation")
	assert.Equal(t, int64(1), q.dropCount(), "an enqueue beyond capacity must count one drop")
}

func Test_spanQueue_closeRejectsEnqueueAndReportsDone(t *testing.T) {
	q := newSpanQueue(32)
	agent := newTestAgent(defaultConfig())
	chunk := newTestSpanChunk(agent)

	assert.True(t, q.enqueue(chunk))
	q.close()
	assert.False(t, q.enqueue(chunk), "enqueue after close is rejected")

	got, ok := q.dequeue()
	assert.True(t, ok, "closed queue still drains what it holds")
	assert.Equal(t, chunk, got)
	_, ok = q.dequeue()
	assert.False(t, ok, "drained closed queue reports done")
}

// This is the stale-check interleaving split into deterministic steps: a
// producer observes open, close completes, then the producer reaches its shard.
// The shard-level recheck must reject the late write.
func Test_spanQueue_staleOpenCheckCannotEnqueueAfterClose(t *testing.T) {
	q := newSpanQueue(1)
	chunk := new(spanChunk)

	assert.False(t, q.closed.Load(), "producer observes the queue open")
	q.close()

	assert.False(t, q.shards[0].tryEnqueue(chunk, &q.closed), "stale open check must not authorize a write")
	assert.Zero(t, q.length())
}

func Test_spanQueue_closeConcurrentProducersDrainsAccepted(t *testing.T) {
	q := newSpanQueue(256)
	chunk := new(spanChunk)

	var accepted atomic.Int64
	var consumed atomic.Int64
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			if _, ok := q.dequeue(); !ok {
				return
			}
			consumed.Add(1)
		}
	}()

	const producers = 16
	start := make(chan struct{})
	ready := make(chan struct{}, producers)
	var producerWg sync.WaitGroup
	producerWg.Add(producers)
	for i := 0; i < producers; i++ {
		go func() {
			defer producerWg.Done()
			<-start
			if q.enqueue(chunk) {
				accepted.Add(1)
			}
			ready <- struct{}{}
			for q.enqueue(chunk) {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	for i := 0; i < producers; i++ {
		<-ready
	}

	q.close()
	producerWg.Wait()
	<-consumerDone

	assert.Zero(t, q.length(), "consumer must not exit ahead of an accepted enqueue")
	assert.Equal(t, accepted.Load(), consumed.Load()+q.dropCount(), "accepted == consumed + head-dropped")
}

// Test_spanQueue_saturationHintBoundsTheScan pins the hint's life cycle: a
// scan that finds every shard full sets it, the next enqueue overwrites without
// re-scanning, and the first enqueue that finds room clears it. Capacity and
// drop accounting are unchanged throughout.
func Test_spanQueue_saturationHintBoundsTheScan(t *testing.T) {
	const capacity = 1024
	q := newSpanQueue(capacity)
	agent := newTestAgent(defaultConfig())
	chunk := newTestSpanChunk(agent)

	for i := 0; i < capacity; i++ {
		assert.True(t, q.enqueue(chunk))
	}
	assert.False(t, q.saturated.Load(), "filling to capacity finds room every time")

	assert.True(t, q.enqueue(chunk))
	assert.True(t, q.saturated.Load(), "a scan that finds no room sets the hint")
	assert.Equal(t, capacity, q.length())
	assert.Equal(t, int64(1), q.dropCount())

	// Saturated: every enqueue still lands and still costs exactly one drop.
	for i := 0; i < capacity; i++ {
		assert.True(t, q.enqueue(chunk))
	}
	assert.True(t, q.saturated.Load())
	assert.Equal(t, capacity, q.length(), "the queue stays bounded while saturated")
	assert.Equal(t, int64(1+capacity), q.dropCount(), "one drop per saturated enqueue")

	// The consumer drains everything; the hint is stale until a producer
	// finds room, which the very next enqueue does.
	for {
		if _, ok := q.tryDequeue(); !ok {
			break
		}
	}
	assert.True(t, q.saturated.Load(), "dequeue does not touch the hint")
	assert.True(t, q.enqueue(chunk))
	assert.False(t, q.saturated.Load(), "an enqueue that finds room clears the hint")
	assert.Equal(t, 1, q.length())
	assert.Equal(t, int64(1+capacity), q.dropCount(), "no drop once there is room")
}

// Benchmark_spanQueue_enqueueSaturated is the outage path: every shard full,
// every enqueue a head-drop. Compare against Benchmark_spanQueue_enqueueDequeue
// for the cost the saturation hint keeps the request path from paying.
func Benchmark_spanQueue_enqueueSaturated(b *testing.B) {
	q := newSpanQueue(1024)
	chunk := &spanChunk{}
	for i := 0; i < 1024; i++ {
		q.enqueue(chunk)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			q.enqueue(chunk)
		}
	})
}

func Benchmark_spanQueue_enqueueDequeue(b *testing.B) {
	q := newSpanQueue(1024)
	chunk := &spanChunk{}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			q.enqueue(chunk)
			q.tryDequeue()
		}
	})
}
