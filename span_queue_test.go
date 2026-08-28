package pinpoint

import (
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
