package pinpoint

import (
	"container/list"
	"hash/maphash"
	"sync"
)

// metaCache replaces the four hashicorp/golang-lru metadata caches. That
// library wraps one process-global mutex around the whole cache, and the
// Peek/PeekOrAdd pattern used here never promoted entries, so eviction order
// degenerated to insertion order (FIFO): a hot SQL was evicted before a
// cold-but-recent one, re-issuing its id and re-sending its metadata to the
// collector. This cache shards the key space (per-shard RWMutex) and restores
// real LRU ordering with aged promotion, mirroring the C++ agent's
// ShardedLruCache (src/cache.h): a hit only takes the write lock to splice
// when the shard is full AND the entry has aged past half the shard capacity.
// Promoting on every hit would serialize all hits behind the exclusive lock
// (C++ measured 75 ns vs 1,333 ns per hot-set hit at 16 threads). Typed keys
// also drop the interface{} boxing golang-lru forced on every lookup.
const metaCacheShardCount = 16 // power of two; matches the C++ agent

var metaCacheSeed = maphash.MakeSeed()

func hashStringKey(s string) uint64 { return maphash.String(metaCacheSeed, s) }

func hashApiCacheKey(k apiCacheKey) uint64 {
	return maphash.String(metaCacheSeed, k.descriptor) ^ (uint64(k.apiType) * 0x9e3779b97f4a7c15)
}

type metaCacheEntry[K comparable, V any] struct {
	key   K
	value V
	// Shard opSeq at insert / last promotion. Written under the shard's
	// write lock, read under its read lock.
	lastPromoted uint64
}

// metaCacheShard is padded to cacheLinePadSize for the same reason as
// activeSpanShard in stats.go: the mutex and opSeq are the contended words,
// and unpadded shards would ping-pong a shared line between goroutines
// holding *different* shard locks. The payload is 64 bytes; the size
// assertion in meta_cache_test.go fails if a field changes that.
type metaCacheShard[K comparable, V any] struct {
	mu           sync.RWMutex
	m            map[K]*list.Element
	order        *list.List // front = most recently used
	cap          int
	ageThreshold uint64
	opSeq        uint64 // counts inserts and promotions; entry age = opSeq - lastPromoted
	_            [cacheLinePadSize - 64]byte
}

type metaCache[K comparable, V any] struct {
	hash   func(K) uint64
	shards [metaCacheShardCount]metaCacheShard[K, V]
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
		s.m = make(map[K]*list.Element, perShard+1)
		s.order = list.New()
		s.cap = perShard
		s.ageThreshold = uint64(perShard / 2)
		if s.ageThreshold < 1 {
			s.ageThreshold = 1
		}
	}
	return c
}

func (c *metaCache[K, V]) shard(key K) *metaCacheShard[K, V] {
	return &c.shards[c.hash(key)&(metaCacheShardCount-1)]
}

// peek returns the cached value. A hit is normally a pure read-lock lookup;
// only when the shard is full and the entry has aged past ageThreshold does
// it take the write lock to move the entry to the front (aged promotion).
func (c *metaCache[K, V]) peek(key K) (V, bool) {
	s := c.shard(key)
	s.mu.RLock()
	el, ok := s.m[key]
	if !ok {
		s.mu.RUnlock()
		var zero V
		return zero, false
	}
	e := el.Value.(*metaCacheEntry[K, V])
	v := e.value
	promote := len(s.m) >= s.cap && s.opSeq-e.lastPromoted >= s.ageThreshold
	s.mu.RUnlock()

	if promote {
		s.mu.Lock()
		// Re-resolve: the entry may have been evicted or removed between
		// the two locks. The value read above is still a valid answer.
		if el, ok := s.m[key]; ok {
			s.order.MoveToFront(el)
			s.opSeq++
			el.Value.(*metaCacheEntry[K, V]).lastPromoted = s.opSeq
		}
		s.mu.Unlock()
	}
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
	if el, ok := s.m[key]; ok {
		v := el.Value.(*metaCacheEntry[K, V]).value
		s.mu.Unlock()
		return v, true
	}
	s.opSeq++
	e := &metaCacheEntry[K, V]{key: key, value: value, lastPromoted: s.opSeq}
	s.m[key] = s.order.PushFront(e)
	if len(s.m) > s.cap {
		victim := s.order.Back()
		delete(s.m, victim.Value.(*metaCacheEntry[K, V]).key)
		s.order.Remove(victim)
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
	if el, ok := s.m[key]; ok {
		delete(s.m, key)
		s.order.Remove(el)
	}
	s.mu.Unlock()
}
