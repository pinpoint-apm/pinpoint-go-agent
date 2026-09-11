package pinpoint

import (
	"container/list"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// metaCache replaces the four hashicorp/golang-lru metadata caches. That
// library wraps one process-global mutex around the whole cache, and the
// Peek/PeekOrAdd pattern used here never promoted entries, so eviction order
// degenerated to insertion order (FIFO): a hot SQL was evicted before a
// cold-but-recent one, re-issuing its id and re-sending its metadata to the
// collector. This cache shards the key space and restores real LRU ordering
// sync.Map keeps steady-state hits lock-free, while an aged entry only takes
// the shard lock when it needs to move to the front. Promoting on every hit
// per hot-set hit at 16 threads).
const metaCacheShardCount = 16 // power of two

var metaCacheSeed = maphash.MakeSeed()

type metaCacheEntry[K comparable, V any] struct {
	key     K
	value   V
	element *list.Element
	shard   *metaCacheShard
	// Shard opSeq at insert / last promotion. Reads are lock-free.
	lastPromoted atomic.Uint64
	// Shard insertSeq at insert / last promotion: a promotion is skipped while
	// it is still current, see metaCacheShardInternal.insertSeq.
	promotedInsertSeq atomic.Uint64
	insertedAt        time.Time // set only when the cache has a ttl
}

type metaCacheShardInternal struct {
	mu           sync.Mutex
	order        *list.List // front = most recently used
	cap          int
	ageThreshold uint64
	// opSeq counts inserts and promotions; entry age = opSeq - lastPromoted.
	// Both count because both move an entry back: an insert goes to the
	// front, and so does a promoted entry, pushing everything behind it one
	// position further from the front. Age therefore bounds an entry's
	// distance from the front, and an entry promoted at ageThreshold (cap/2)
	// is always caught in the front half. Counting inserts alone broke that
	// bound - hot entries were evicted from under a threshold that no longer
	// measured position - and the resend advantage over FIFO fell from
	// 11-20x to 3-7x on the churn workload in meta_cache_test.
	opSeq atomic.Uint64
	// insertSeq counts inserts alone. A hit whose entry has aged past the
	// threshold still skips the promotion while no insert has happened since
	// the entry was last promoted: only an insert can evict, so until one
	// arrives the order is not consulted, and promoting would only take the
	// lock. Without this, a working set larger than ageThreshold in a full
	// shard made every hit a promotion - each promotion aged the rest past
	// the threshold - and the "lock-free hit" path was never taken again.
	// The order is stale only over such an insert-free stretch; the first
	// insert is followed by one promotion per aged hot entry, and the bound
	// above holds again from there.
	insertSeq atomic.Uint64
	size      atomic.Int64
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
	m      sync.Map // K -> *metaCacheEntry[K, V]
	shards [metaCacheShardCount]metaCacheShard
	// ttl > 0 expires an entry that long after its insert: peek drops it and
	// reports a miss, so the next lookup re-registers the metadata. Only the
	// SQL UID cache sets one (SQL.CacheExpireHours): the collector's
	// SqlUidMetaData rows have a 180-day TTL, and a UID whose row lapsed
	// while the entry stayed cached showed an empty SQL in the web UI until
	// the process restarted. The id caches keep entries for the process
	ttl time.Duration
	now func() time.Time // time.Now, replaced by tests
}

// newMetaCache splits capacity evenly across the shards, so a hot shard
// for removing the shared lock line.
func newMetaCache[K comparable, V any](capacity int) *metaCache[K, V] {
	c := &metaCache[K, V]{now: time.Now}
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

// shard picks the lock that serializes inserts of key. maphash.Comparable
// dispatches to the runtime's own type hasher, the same one a map[K]V would
// use, which is AES-accelerated for strings; maphash.String is not, and
// measured 4-6x slower on the SQL-sized keys these caches hold: 590 ns vs
// 105 ns for 8 KB, 6.5 us vs 1.5 us for maxSqlSize (#189).
func (c *metaCache[K, V]) shard(key K) *metaCacheShard {
	return &c.shards[maphash.Comparable(metaCacheSeed, key)&(metaCacheShardCount-1)]
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
	if c.ttl > 0 && c.now().Sub(e.insertedAt) >= c.ttl {
		c.removeEntry(e)
		var zero V
		return zero, false
	}
	s := e.shard
	v := e.value
	if s.size.Load() < int64(s.cap) {
		return v, true
	}
	if !s.promotionDue(e.lastPromoted.Load(), e.promotedInsertSeq.Load()) {
		return v, true
	}

	s.mu.Lock()
	// Re-resolve and re-check: another goroutine may have promoted, evicted,
	// or removed the entry while this goroutine waited for the lock.
	if raw, ok := c.m.Load(key); ok && raw.(*metaCacheEntry[K, V]) == e {
		if s.size.Load() >= int64(s.cap) && s.promotionDue(e.lastPromoted.Load(), e.promotedInsertSeq.Load()) {
			s.order.MoveToFront(e.element)
			e.lastPromoted.Store(s.opSeq.Add(1))
			e.promotedInsertSeq.Store(s.insertSeq.Load())
		}
	}
	s.mu.Unlock()
	return v, true
}

// promotionDue reports whether a hit on an entry with the given marks should
// move it to the front: it has aged past the threshold, and at least one insert
// has happened since it was last promoted (see insertSeq). Lock-free; the
// caller re-checks under the shard lock before moving anything.
func (s *metaCacheShardInternal) promotionDue(lastPromoted, promotedInsertSeq uint64) bool {
	return s.opSeq.Load()-lastPromoted >= s.ageThreshold && s.insertSeq.Load() != promotedInsertSeq
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
	insertSeq := s.insertSeq.Add(1)
	e := &metaCacheEntry[K, V]{key: key, value: value, shard: s}
	if c.ttl > 0 {
		e.insertedAt = c.now()
	}
	e.lastPromoted.Store(opSeq)
	e.promotedInsertSeq.Store(insertSeq)
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
// this to retry metadata whose send failed. isExpected sees the cached value
// and rejects an entry that is not the one the caller means: the key may have
// been evicted and re-inserted since, and that entry's metadata was sent.
func (c *metaCache[K, V]) remove(key K, isExpected func(V) bool) {
	if raw, ok := c.m.Load(key); ok {
		if e := raw.(*metaCacheEntry[K, V]); isExpected(e.value) {
			c.removeEntry(e)
		}
	}
}

// removeEntry deletes exactly e, not whatever the key maps to by the time the
// shard lock is taken.
func (c *metaCache[K, V]) removeEntry(e *metaCacheEntry[K, V]) {
	s := e.shard
	s.mu.Lock()
	if raw, ok := c.m.Load(e.key); ok && raw.(*metaCacheEntry[K, V]) == e {
		c.m.Delete(e.key)
		s.order.Remove(e.element)
		s.size.Add(-1)
	}
	s.mu.Unlock()
}

// removeValue deletes the entry whose value isExpected accepts, for a caller
// that holds the value but not the key (see sqlMeta). A full scan, but only
// the metadata drop paths take it, and a cache holds at most its capacity.
func (c *metaCache[K, V]) removeValue(isExpected func(V) bool) {
	c.m.Range(func(_, raw any) bool {
		e := raw.(*metaCacheEntry[K, V])
		if isExpected(e.value) {
			c.removeEntry(e)
			return false
		}
		return true
	})
}
