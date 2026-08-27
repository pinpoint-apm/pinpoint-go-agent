package pinpoint

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	spanQueueMaxShards   = 32
	spanQueueMinShardCap = 32
)

// spanQueue is a bounded multi-producer queue with producer contention sharded
// across independently locked ring buffers, replacing the single channel whose
// internal mutex every producer met on. Capacity stays the configured global
// bound; rings are preallocated so the enqueue path never allocates.
//
// Shards are picked per enqueue with math/rand/v2 (per-thread state, no shared
// cache line) rather than a sticky per-goroutine home shard: Go has no stable
// cheap goroutine identity, and non-sticky placement also lets a single busy
// producer spread over every shard, so the whole capacity is used without the
// C++ agent's quota-borrowing machinery. Head-drop on a full shard happens in
// the same critical section as the enqueue, so a drop is always paired with
// exactly one successful enqueue.
//
// Concurrency contract: multi-producer, single consumer. cursor is a plain
// field, so two concurrent dequeuers are a data race, not merely an
// unspecified order. Cross-shard dequeue order is unspecified.
type spanQueue struct {
	shards []spanQueueShard

	// wake carries at most one token; enqueue tops it up and the consumer
	// sweeps every shard per token, so coalesced wake-ups lose nothing.
	wake   chan struct{}
	done   chan struct{}
	closed atomic.Bool

	// cursor rotates the consumer's sweep start so a persistently hot shard
	// cannot starve the others. Only the single consumer touches it; the pad
	// keeps its writes off the line producers read closed from.
	_      [cacheLinePadSize]byte
	cursor int
}

type spanQueueShardInternal struct {
	mu    sync.Mutex
	cells []*spanChunk
	head  int
	size  int
	drops int64
}

// spanQueueShard gives every shard its own cache line, same reasoning and
// stride as activeSpanShard.
type spanQueueShard struct {
	spanQueueShardInternal
	_ [cacheLinePadSize - unsafe.Sizeof(spanQueueShardInternal{})%cacheLinePadSize]byte
}

func newSpanQueue(capacity int) *spanQueue {
	if capacity < 1 {
		capacity = 1
	}
	// Tiny queues gain no useful contention reduction from near-empty shards
	// and would unnecessarily relax their FIFO behavior.
	shardCount := capacity / spanQueueMinShardCap
	if shardCount > spanQueueMaxShards {
		shardCount = spanQueueMaxShards
	}
	if shardCount < 1 {
		shardCount = 1
	}

	q := &spanQueue{
		shards: make([]spanQueueShard, shardCount),
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	base := capacity / shardCount
	extra := capacity % shardCount
	for i := range q.shards {
		shardCap := base
		if i < extra {
			shardCap++
		}
		q.shards[i].cells = make([]*spanChunk, shardCap)
	}
	return q
}

// enqueue never blocks and, until close, never rejects: a full shard head-drops
// its oldest chunk in the same critical section, so recent traces are favored
// under backpressure. Trying a second shard first keeps a momentarily full
// shard from dropping while the queue as a whole still has room.
func (q *spanQueue) enqueue(chunk *spanChunk) bool {
	if q.closed.Load() {
		return false
	}

	first := rand.IntN(len(q.shards))
	if q.shards[first].tryEnqueue(chunk) {
		q.notify()
		return true
	}
	second := rand.IntN(len(q.shards))
	if second != first && q.shards[second].tryEnqueue(chunk) {
		q.notify()
		return true
	}
	q.shards[second].enqueueOrOverwrite(chunk)
	q.notify()
	return true
}

// dequeue blocks until a chunk is available, and reports false once the queue
// is closed and fully drained.
func (q *spanQueue) dequeue() (*spanChunk, bool) {
	for {
		if chunk, ok := q.tryDequeue(); ok {
			return chunk, true
		}
		select {
		case <-q.wake:
		case <-q.done:
			// A producer may have enqueued between the failed sweep and the
			// close; drain rather than trusting that sweep.
			return q.tryDequeue()
		}
	}
}

// tryDequeue sweeps the shards round-robin from the cursor and never blocks.
func (q *spanQueue) tryDequeue() (*spanChunk, bool) {
	for i := 0; i < len(q.shards); i++ {
		shard := (q.cursor + i) % len(q.shards)
		if chunk, ok := q.shards[shard].tryDequeue(); ok {
			q.cursor = (shard + 1) % len(q.shards)
			return chunk, true
		}
	}
	return nil, false
}

// close stops the queue: subsequent enqueues are rejected and the consumer is
// woken to drain what remains. Safe to call once (guarded by the caller, like
// the channel close it replaces).
func (q *spanQueue) close() {
	q.closed.Store(true)
	close(q.done)
}

func (q *spanQueue) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *spanQueue) dropCount() int64 {
	var total int64
	for i := range q.shards {
		s := &q.shards[i]
		s.mu.Lock()
		total += s.drops
		s.mu.Unlock()
	}
	return total
}

func (q *spanQueue) length() int {
	var total int
	for i := range q.shards {
		s := &q.shards[i]
		s.mu.Lock()
		total += s.size
		s.mu.Unlock()
	}
	return total
}

func (s *spanQueueShard) tryEnqueue(chunk *spanChunk) bool {
	s.mu.Lock()
	if s.size == len(s.cells) {
		s.mu.Unlock()
		return false
	}
	s.push(chunk)
	s.mu.Unlock()
	return true
}

func (s *spanQueueShard) enqueueOrOverwrite(chunk *spanChunk) {
	s.mu.Lock()
	if s.size == len(s.cells) {
		s.cells[s.head] = nil
		s.head = s.next(s.head)
		s.size--
		s.drops++
		if IsDebugLogLevelEnabled() {
			Log("agent").Debugf("discard oldest span, shard size:%d", s.size)
		}
	}
	s.push(chunk)
	s.mu.Unlock()
}

func (s *spanQueueShard) tryDequeue() (*spanChunk, bool) {
	s.mu.Lock()
	if s.size == 0 {
		s.mu.Unlock()
		return nil, false
	}
	chunk := s.cells[s.head]
	s.cells[s.head] = nil
	s.head = s.next(s.head)
	s.size--
	s.mu.Unlock()
	return chunk, true
}

func (s *spanQueueShard) push(chunk *spanChunk) {
	tail := s.head + s.size
	if tail >= len(s.cells) {
		tail -= len(s.cells)
	}
	s.cells[tail] = chunk
	s.size++
}

func (s *spanQueueShard) next(position int) int {
	position++
	if position == len(s.cells) {
		return 0
	}
	return position
}
