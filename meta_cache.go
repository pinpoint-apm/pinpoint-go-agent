package pinpoint

import (
	"container/list"
	"hash/maphash"
	"math"
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
// without a global lock: a sync.Map keeps steady-state hits lock-free, while an
// aged entry only takes the shard lock when it needs to move to the front.
// Promoting on every hit would take that lock on every hit instead.
//
// The capacity bounds the cache as a whole, not each shard: a shard holds as
// many entries as its keys need, and an insert past the capacity evicts the
// least recently used entry of whichever shard holds it (evictFor). Each
// shard bounded by its share instead evicted before the cache was full: keys
// do not hash evenly, so a working set at 90% of the capacity overfilled a few
// shards and missed a fifth of its lookups, each miss of the SQL id cache
// minting a new id.
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
	// used is the cache's clock at insert / last promotion, which orders the
	// entries of different shards. Read and written under the shard lock.
	used       uint64
	insertedAt time.Time // set only when the cache has a ttl
}

type metaCacheShardInternal struct {
	mu           sync.Mutex
	order        *list.List // front = most recently used
	ageThreshold uint64
	// opSeq counts inserts and promotions; entry age = opSeq - lastPromoted.
	// Both count because both move an entry back: an insert goes to the front,
	// and so does a promoted entry, pushing everything behind it one position
	// further from the front. Age therefore bounds an entry's distance from the
	// front, and an entry promoted at ageThreshold (cap/2) is always caught in
	// the front half.
	opSeq atomic.Uint64
	// insertSeq counts inserts alone. A hit whose entry has aged past the
	// threshold still skips the promotion while no insert has happened since the
	// entry was last promoted: only an insert can evict, so until one arrives
	// the order is not consulted and promoting would only take the lock. Without
	// it, a working set larger than ageThreshold in a full shard makes every hit
	// a promotion and the lock-free hit path is never taken. The order is stale
	// only over such an insert-free stretch.
	//
	// An insert into another shard can evict from this one, too, but keys hash
	// evenly enough that this shard sees inserts at its share of the rate: its
	// entries age and are promoted in step with the cache.
	insertSeq atomic.Uint64
	index     uint64 // in metaCache.shards and metaCache.tailUsed
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
	// shardCount is the number of shards in use: metaCacheShardCount, or the
	// capacity when that is smaller.
	shardCount uint64
	capacity   int64
	// ttl > 0 expires an entry that long after its insert: peek drops it and
	// reports a miss, so the next lookup re-registers the metadata. Only the SQL
	// UID cache sets one (SQL.CacheExpireHours), because the collector's
	// SqlUidMetaData rows expire too and a UID whose row lapsed while the entry
	// stayed cached shows an empty SQL in the web UI. The other caches keep
	// their entries for the life of the process.
	ttl time.Duration
	now func() time.Time // time.Now, replaced by tests
	// full is set once size reaches capacity, when an insert can evict and the
	// order starts to matter: until then a hit never promotes. It is a word of
	// its own, apart from size, because every hit reads it while size changes
	// with every insert and eviction.
	full atomic.Bool

	_ [cacheLinePadSize]byte
	// size counts the entries of every shard, and clock ticks once per insert
	// and per promotion to stamp used.
	size  atomic.Int64
	clock atomic.Uint64
	// tailUsed[i] is the used of shards[i]'s least recently used entry, the
	// back of its order, or math.MaxUint64 while the shard is empty.
	// evictOverflow reads them without a lock to find the cache's least
	// recently used entry. Kept together, beside the words every insert writes
	// anyway, so that the scan reads a line or two rather than each shard's.
	tailUsed [metaCacheShardCount]atomic.Uint64
}

// newMetaCache makes a cache of capacity entries, over as many shards as there
// are entries when that is fewer than metaCacheShardCount.
func newMetaCache[K comparable, V any](capacity int) *metaCache[K, V] {
	c := &metaCache[K, V]{now: time.Now}
	capacity = max(capacity, 1)
	c.capacity = int64(capacity)
	c.shardCount = uint64(min(capacity, metaCacheShardCount))
	// An entry is promoted once it has aged past half a shard's share of the
	// capacity, which keeps it in the front half of a shard holding its share.
	ageThreshold := uint64(max(capacity/int(c.shardCount)/2, 1))
	for i := range c.shards {
		s := &c.shards[i]
		s.order = list.New()
		s.ageThreshold = ageThreshold
		s.index = uint64(i)
		c.tailUsed[i].Store(math.MaxUint64)
	}
	return c
}

// shard picks the lock that serializes inserts of key. maphash.Comparable
// dispatches to the runtime's own type hasher, the same one a map[K]V would
// use, which hashes a string in one AES-accelerated call; maphash.String feeds
// that hasher one 128-byte block at a time and measured 4-6x slower on
// SQL-sized keys: 590 ns vs 105 ns for 8 KB, 6.5 us vs 1.5 us for maxSqlSize
// (#189).
func (c *metaCache[K, V]) shard(key K) *metaCacheShard {
	return &c.shards[maphash.Comparable(metaCacheSeed, key)%c.shardCount]
}

// peek returns the cached value. A hit is normally lock-free; only when the
// cache is full and the entry has aged past ageThreshold does it take the
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
	if !c.full.Load() {
		return v, true
	}
	if !s.promotionDue(e.lastPromoted.Load(), e.promotedInsertSeq.Load()) {
		return v, true
	}

	s.mu.Lock()
	// Re-resolve and re-check: another goroutine may have promoted, evicted,
	// or removed the entry while this goroutine waited for the lock.
	if raw, ok := c.m.Load(key); ok && raw.(*metaCacheEntry[K, V]) == e {
		if c.full.Load() && s.promotionDue(e.lastPromoted.Load(), e.promotedInsertSeq.Load()) {
			wasBack := s.order.Back() == e.element
			s.order.MoveToFront(e.element)
			e.lastPromoted.Store(s.opSeq.Add(1))
			e.promotedInsertSeq.Store(s.insertSeq.Load())
			e.used = c.clock.Add(1)
			if wasBack {
				c.syncTail(s)
			}
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
// least recently used entry if the cache is over capacity. It returns the
// existing value and true when another goroutine won the insert race, so
// callers keep a single id per key (the loser's freshly generated id is
// discarded, same as with golang-lru's PeekOrAdd). An expired entry is a
// miss here as it is in peek: it is replaced by value, so the TTL holds for
// a caller that never peeked first.
func (c *metaCache[K, V]) peekOrAdd(key K, value V) (V, bool) {
	s := c.shard(key)
	s.mu.Lock()
	backChanged := false
	if raw, ok := c.m.Load(key); ok {
		e := raw.(*metaCacheEntry[K, V])
		if c.ttl == 0 || c.now().Sub(e.insertedAt) < c.ttl {
			v := e.value
			s.mu.Unlock()
			return v, true
		}
		backChanged = s.order.Back() == e.element
		c.m.Delete(e.key)
		s.order.Remove(e.element)
		c.size.Add(-1)
	}
	opSeq := s.opSeq.Add(1)
	insertSeq := s.insertSeq.Add(1)
	e := &metaCacheEntry[K, V]{key: key, value: value, shard: s, used: c.clock.Add(1)}
	if c.ttl > 0 {
		e.insertedAt = c.now()
	}
	e.lastPromoted.Store(opSeq)
	e.promotedInsertSeq.Store(insertSeq)
	e.element = s.order.PushFront(e)
	c.m.Store(key, e)
	// The front is not the back unless the shard was empty.
	if backChanged || s.order.Len() == 1 {
		c.syncTail(s)
	}
	size := c.size.Add(1)
	if size >= c.capacity && !c.full.Load() {
		c.full.Store(true)
	}
	if size > c.capacity && !c.evictFor(s) {
		s.mu.Unlock()
		c.evictOverflow()
	} else {
		s.mu.Unlock()
	}
	var zero V
	return zero, false
}

// evictFor evicts one entry for an insert into s past the capacity, with s.mu
// held: the cache's least recently used entry, the back of the shard whose back
// was used longest ago. That shard's lock is only tried, never waited for:
// concurrent inserts all pick the same shard, and waiting queued them on its
// lock, a quarter slower at 14 goroutines. When it is taken, or the oldest is s
// itself, s's own least recently used entry goes instead - never the entry
// just inserted, at its front. It reports false when there was none to evict
// in s.
func (c *metaCache[K, V]) evictFor(s *metaCacheShard) bool {
	if oldest := c.oldestShard(); oldest != nil && oldest != s && oldest.mu.TryLock() {
		evicted := c.evictBack(oldest)
		oldest.mu.Unlock()
		if evicted {
			return true
		}
	}
	return s.order.Len() > 1 && c.evictBack(s)
}

// evictOverflow evicts least recently used entries until the cache is back
// within its capacity, for an insert evictFor could not evict for. It runs
// with no shard lock held, so it can wait for any shard's. The size is checked
// again under that lock, as another insert may have evicted for this one; an
// insert evictFor is still evicting for can then leave the cache an entry
// short, never over.
func (c *metaCache[K, V]) evictOverflow() {
	for c.size.Load() > c.capacity {
		s := c.oldestShard()
		if s == nil {
			return
		}
		s.mu.Lock()
		if c.size.Load() > c.capacity {
			c.evictBack(s)
		}
		s.mu.Unlock()
	}
}

// evictBack evicts s's least recently used entry, the back of its order, and
// reports whether there was one. The caller holds s.mu.
func (c *metaCache[K, V]) evictBack(s *metaCacheShard) bool {
	back := s.order.Back()
	if back == nil {
		return false
	}
	c.m.Delete(back.Value.(*metaCacheEntry[K, V]).key)
	s.order.Remove(back)
	c.size.Add(-1)
	c.syncTail(s)
	return true
}

// oldestShard returns the shard whose least recently used entry is the
// cache's, or nil when every shard is empty. Lock-free: a shard it picks may
// have changed by the time the caller locks it.
func (c *metaCache[K, V]) oldestShard() *metaCacheShard {
	var oldest *metaCacheShard
	least := uint64(math.MaxUint64)
	for i := range c.shardCount {
		if used := c.tailUsed[i].Load(); used < least {
			oldest, least = &c.shards[i], used
		}
	}
	return oldest
}

// syncTail publishes the used of s's back in tailUsed. Callers hold s.mu and
// call it after every change to s.order that moves the back.
func (c *metaCache[K, V]) syncTail(s *metaCacheShard) {
	if back := s.order.Back(); back != nil {
		c.tailUsed[s.index].Store(back.Value.(*metaCacheEntry[K, V]).used)
	} else {
		c.tailUsed[s.index].Store(math.MaxUint64)
	}
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
		wasBack := s.order.Back() == e.element
		c.m.Delete(e.key)
		s.order.Remove(e.element)
		if wasBack {
			c.syncTail(s)
		}
		if c.size.Add(-1) < c.capacity && c.full.Load() {
			c.full.Store(false)
		}
	}
	s.mu.Unlock()
}
