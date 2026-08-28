package pinpoint

import (
	"container/list"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"unsafe"
)

// metaCache replaces the four hashicorp/golang-lru metadata caches. That
// library wraps one process-global mutex around the whole cache, and the
// Peek/PeekOrAdd pattern used here never promoted entries, so eviction order
// degenerated to insertion order (FIFO): a hot SQL was evicted before a
// cold-but-recent one, re-issuing its id and re-sending its metadata to the
// collector. This cache shards the key space and restores real LRU ordering
// with aged promotion, mirroring the C++ agent's ShardedLruCache (src/cache.h):
// sync.Map keeps steady-state hits lock-free, while an aged entry only takes
// the shard lock when it needs to move to the front. Promoting on every hit
// would serialize all hits behind the lock (C++ measured 75 ns vs 1,333 ns
// per hot-set hit at 16 threads).
const metaCacheShardCount = 16 // power of two; matches the C++ agent

var metaCacheSeed = maphash.MakeSeed()

func hashStringKey(s string) uint64 { return maphash.String(metaCacheSeed, s) }

func hashApiCacheKey(k apiCacheKey) uint64 {
	return maphash.String(metaCacheSeed, k.descriptor) ^ (uint64(k.apiType) * 0x9e3779b97f4a7c15)
}

type metaCacheEntry[K comparable, V any] struct {
	key     K
	value   V
	element *list.Element
	shard   *metaCacheShard
	// Shard opSeq at insert / last promotion. Reads are lock-free.
	lastPromoted atomic.Uint64
}

type metaCacheShardInternal struct {
	mu           sync.Mutex
	order        *list.List // front = most recently used
	cap          int
	ageThreshold uint64
	opSeq        atomic.Uint64 // counts inserts and promotions; entry age = opSeq - lastPromoted
	size         atomic.Int64
}

// metaCacheShard is padded to cacheLinePadSize for the same reason as
// activeSpanShard in stats.go: the mutex and atomics are the contended words,
// and unpadded shards would ping-pong a shared line between goroutines using
// *different* shards.
type metaCacheShard struct {
	metaCacheShardInternal
	_ [cacheLinePadSize - unsafe.Sizeof(metaCacheShardInternal{})%cacheLinePadSize]byte
}

type metaCache[K comparable, V any] struct {
	hash   func(K) uint64
	m      sync.Map // K -> *metaCacheEntry[K, V]
	shards [metaCacheShardCount]metaCacheShard
}

// newMetaCache splits capacity evenly across the shards, so a hot shard
// evicts within its own slice — the same trade-off the C++ agent accepts
// for removing the shared lock line.
func newMetaCache[K comparable, V any](capacity int, hash func(K) uint64) *metaCache[K, V] {
	c := &metaCache[K, V]{hash: hash}
	perShard := capacity / metaCacheShardCount
	if perShard < 1 {
		perShard = 1
	}
	for i := range c.shards {
		s := &c.shards[i]
		s.order = list.New()
		s.cap = perShard
		s.ageThreshold = uint64(perShard / 2)
		if s.ageThreshold < 1 {
			s.ageThreshold = 1
		}
	}
	return c
}

func (c *metaCache[K, V]) shard(key K) *metaCacheShard {
	return &c.shards[c.hash(key)&(metaCacheShardCount-1)]
}

// peek returns the cached value. A hit is normally lock-free; only when the
// shard is full and the entry has aged past ageThreshold does it take the
// shard lock to move the entry to the front (aged promotion).
func (c *metaCache[K, V]) peek(key K) (V, bool) {
	raw, ok := c.m.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	e := raw.(*metaCacheEntry[K, V])
	s := e.shard
	v := e.value
	if s.size.Load() < int64(s.cap) {
		return v, true
	}
	lastPromoted := e.lastPromoted.Load()
	opSeq := s.opSeq.Load()
	if opSeq-lastPromoted < s.ageThreshold {
		return v, true
	}

	s.mu.Lock()
	// Re-resolve and re-check: another goroutine may have promoted, evicted,
	// or removed the entry while this goroutine waited for the lock.
	if raw, ok := c.m.Load(key); ok && raw.(*metaCacheEntry[K, V]) == e {
		lastPromoted = e.lastPromoted.Load()
		opSeq = s.opSeq.Load()
		if s.size.Load() >= int64(s.cap) && opSeq-lastPromoted >= s.ageThreshold {
			s.order.MoveToFront(e.element)
			e.lastPromoted.Store(s.opSeq.Add(1))
		}
	}
	s.mu.Unlock()
	return v, true
}

// peekOrAdd inserts the value unless the key is already present, evicting the
// least recently used entry if the shard is over capacity. It returns the
// existing value and true when another goroutine won the insert race, so
// callers keep a single id per key (the loser's freshly generated id is
// discarded, same as with golang-lru's PeekOrAdd).
func (c *metaCache[K, V]) peekOrAdd(key K, value V) (V, bool) {
	s := c.shard(key)
	s.mu.Lock()
	if raw, ok := c.m.Load(key); ok {
		v := raw.(*metaCacheEntry[K, V]).value
		s.mu.Unlock()
		return v, true
	}
	opSeq := s.opSeq.Add(1)
	e := &metaCacheEntry[K, V]{key: key, value: value, shard: s}
	e.lastPromoted.Store(opSeq)
	e.element = s.order.PushFront(e)
	c.m.Store(key, e)
	if s.size.Add(1) > int64(s.cap) {
		victim := s.order.Back().Value.(*metaCacheEntry[K, V])
		c.m.Delete(victim.key)
		s.order.Remove(victim.element)
		s.size.Add(-1)
	}
	s.mu.Unlock()
	var zero V
	return zero, false
}

// remove deletes the entry so the key misses next time; sendMetaWorker uses
// this to retry metadata whose send failed.
func (c *metaCache[K, V]) remove(key K) {
	s := c.shard(key)
	s.mu.Lock()
	if raw, ok := c.m.Load(key); ok {
		e := raw.(*metaCacheEntry[K, V])
		c.m.Delete(key)
		s.order.Remove(e.element)
		s.size.Add(-1)
	}
	s.mu.Unlock()
}
