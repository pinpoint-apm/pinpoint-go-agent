package pinpoint

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
)

func TestMetaCacheShardPadding(t *testing.T) {
	assert.Equal(t, uintptr(cacheLinePadSize), unsafe.Sizeof(metaCacheShard{}))
}

func TestMetaCacheBasics(t *testing.T) {
	c := newMetaCache[string, int32](cacheSize)

	_, ok := c.peek("a")
	assert.False(t, ok)

	prev, ok := c.peekOrAdd("a", 1)
	assert.False(t, ok)
	assert.Equal(t, int32(0), prev)

	v, ok := c.peek("a")
	assert.True(t, ok)
	assert.Equal(t, int32(1), v)

	// losing the insert race returns the existing value
	prev, ok = c.peekOrAdd("a", 2)
	assert.True(t, ok)
	assert.Equal(t, int32(1), prev)

	c.remove("a", func(v int32) bool { return v == 1 })
	_, ok = c.peek("a")
	assert.False(t, ok)
}

// A failed send removes the entry that published its id, not whatever the key
// maps to by then: an entry re-inserted under a new id had its metadata sent.
func TestMetaCacheRemoveKeepsAnUnexpectedValue(t *testing.T) {
	c := newMetaCache[string, int32](cacheSize)
	c.peekOrAdd("a", 2)

	c.remove("a", func(v int32) bool { return v == 1 })
	v, ok := c.peek("a")
	assert.True(t, ok, "an entry with a different value is not the one that failed")
	assert.Equal(t, int32(2), v)
	assert.Equal(t, int64(1), c.size.Load())
}

// An entry older than the ttl reads as a miss and is dropped, so the next
// peekOrAdd inserts afresh and the caller re-registers the metadata.
func TestMetaCacheExpiresAfterTtl(t *testing.T) {
	c := newMetaCache[string, int32](cacheSize)
	c.ttl = 168 * time.Hour
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }

	c.peekOrAdd("a", 1)
	now = now.Add(c.ttl - time.Nanosecond)
	v, ok := c.peek("a")
	assert.True(t, ok, "still fresh just before the ttl")
	assert.Equal(t, int32(1), v)

	now = now.Add(time.Nanosecond)
	_, ok = c.peek("a")
	assert.False(t, ok, "expired at the ttl")
	assert.Equal(t, int64(0), c.size.Load(), "the expired entry is removed, not left to age out")

	_, ok = c.peekOrAdd("a", 2)
	assert.False(t, ok, "the key is free for re-registration")
	v, _ = c.peek("a")
	assert.Equal(t, int32(2), v)

	// No ttl: the same age is not an expiry.
	c.ttl = 0
	now = now.Add(1000 * time.Hour)
	_, ok = c.peek("a")
	assert.True(t, ok)
}

// An insert past the capacity evicts the cache's least recently used entry,
// whatever shard it is in and whatever shard the new key lands on.
func TestMetaCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newMetaCache[string, int32](metaCacheShardCount)
	for i := 0; i < metaCacheShardCount; i++ {
		c.peekOrAdd(fmt.Sprintf("key-%d", i), int32(i))
	}
	// Two keys in one shard, as the hash seed (random per process) puts them,
	// no longer evict each other while the cache has room.
	_, ok := c.m.Load("key-0")
	assert.True(t, ok, "within the capacity nothing is evicted")

	c.peekOrAdd("key-new", 99)
	_, ok = c.m.Load("key-0")
	assert.False(t, ok, "the least recently used entry goes")
	for i := 1; i < metaCacheShardCount; i++ {
		_, ok := c.m.Load(fmt.Sprintf("key-%d", i))
		assert.True(t, ok, "key-%d stays", i)
	}
	v, ok := c.peek("key-new")
	assert.True(t, ok)
	assert.Equal(t, int32(99), v)
	assert.EqualValues(t, metaCacheShardCount, c.size.Load())
}

func TestMetaCacheConcurrent(t *testing.T) {
	// Small capacity keeps the cache full, so promotion, eviction across
	// shards, and removal all race against lock-free peeks under -race.
	c := newMetaCache[string, int32](64)
	keys := make([]string, 512)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%03d", i)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Go(func() {
			for i := 0; i < 20000; i++ {
				k := keys[(i*7+g*13)&511]
				if _, ok := c.peek(k); !ok {
					c.peekOrAdd(k, int32(i))
				}
				if i&255 == 0 {
					c.remove(k, func(int32) bool { return true })
				}
			}
		})
	}
	wg.Wait()
	n := int64(0)
	c.m.Range(func(any, any) bool { n++; return true })
	assert.Equal(t, n, c.size.Load(), "size counts the entries")
	assert.LessOrEqual(t, n, int64(64))
}

func TestMetaCachePeekNoAlloc(t *testing.T) {
	c := newMetaCache[string, int32](cacheSize)
	keys := make([]string, 256)
	for i := range keys {
		keys[i] = fmt.Sprintf("select * from hot_table_%03d where id = ?", i)
		c.peekOrAdd(keys[i], int32(i))
	}
	i := 0
	allocs := testing.AllocsPerRun(1000, func() {
		c.peek(keys[i&255])
		i++
	})
	assert.Equal(t, 0.0, allocs)
}

// BenchmarkMetaCacheHit measures the contended pure-hit path (cache not
// full, so no promotion): run with -cpu=1,4,16 to see the sharding effect.
func BenchmarkMetaCacheHit(b *testing.B) {
	c := newMetaCache[string, int32](cacheSize)
	keys := make([]string, 512)
	for i := range keys {
		keys[i] = fmt.Sprintf("select * from table_%04d where id = ? and name = ?", i)
		c.peekOrAdd(keys[i], int32(i))
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.peek(keys[i&511])
			i++
		}
	})
}

// BenchmarkMetaCacheMixedSaturated measures the saturated steady state: hot
// hits with a trickle of new keys (1 in 64), so eviction and aged promotion
// stay active throughout.
func BenchmarkMetaCacheMixedSaturated(b *testing.B) {
	c := newMetaCache[string, int32](cacheSize)
	hot := make([]string, 256)
	for i := range hot {
		hot[i] = fmt.Sprintf("select * from hot_table_%03d where id = ?", i)
	}
	churn := make([]string, 1<<16)
	for i := range churn {
		churn[i] = fmt.Sprintf("select * from churn_table_%06d", i)
	}
	// warmup to steady state: saturate the shards while keeping the hot set live
	ci := 0
	for i := 0; i < 1<<16; i++ {
		if i&63 == 63 {
			k := churn[ci&(1<<16-1)]
			ci++
			if _, ok := c.peek(k); !ok {
				c.peekOrAdd(k, 0)
			}
		} else if _, ok := c.peek(hot[i&255]); !ok {
			c.peekOrAdd(hot[i&255], 0)
		}
	}
	var churnIdx int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i&63 == 63 {
				k := churn[int(atomic.AddInt64(&churnIdx, 1))&(1<<16-1)]
				if _, ok := c.peek(k); !ok {
					c.peekOrAdd(k, 0)
				}
			} else if _, ok := c.peek(hot[i&255]); !ok {
				c.peekOrAdd(hot[i&255], 0)
			}
			i++
		}
	})
}

// BenchmarkMetaCacheShard guards the reason shard hashes with
// maphash.Comparable rather than maphash.String: the runtime hasher it
// dispatches to hashes the key in one call, where maphash.String chains one
// call per 128 bytes (#189).
func BenchmarkMetaCacheShard(b *testing.B) {
	c := newMetaCache[string, int32](cacheSize)
	for _, size := range []int{190, 1024, 8 * 1024, maxSqlSize} {
		key := "select * from t where x in (" + strings.Repeat("9", size) + ")"
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			for b.Loop() {
				c.shard(key)
			}
		})
	}
}

// A hit promotes only after an insert since its previous promotion; otherwise
// it avoids the shard lock and leaves the order untouched.
func TestMetaCacheHitsWithoutInsertsDoNotPromote(t *testing.T) {
	c := newMetaCache[string, int32](cacheSize)
	// Fill the cache, so it is full and every entry is a promotion candidate
	// once aged.
	keys := make([]string, cacheSize)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
		c.peekOrAdd(keys[i], int32(i))
	}
	assert.True(t, c.full.Load())
	entry := func(k string) *metaCacheEntry[string, int32] {
		raw, _ := c.m.Load(k)
		return raw.(*metaCacheEntry[string, int32])
	}
	// The fill itself was a run of inserts, so the first rotation may promote
	// each aged entry once; that settles the order for the insert-free stretch.
	for _, k := range keys {
		c.peek(k)
	}
	before := c.clock.Load()

	// Rotate over the whole cache several times over: the entries age past
	// the threshold again and again, yet nothing was inserted.
	for round := 0; round < 4; round++ {
		for _, k := range keys {
			_, ok := c.peek(k)
			assert.True(t, ok)
		}
	}
	assert.Equal(t, before, c.clock.Load(), "hits without an insert must not promote")

	// One insert re-arms promotion in its shard: the next hit on an aged entry
	// there promotes it exactly once, and the hits after that are lock-free
	// again.
	s := c.shard(keys[0])
	extra := ""
	for i := len(keys); ; i++ {
		if k := fmt.Sprintf("k%d", i); c.shard(k) == s {
			extra = k
			break
		}
	}
	c.peekOrAdd(extra, 0)
	assert.EqualValues(t, cacheSize, c.size.Load(), "the insert evicts exactly one entry")
	var survivor *metaCacheEntry[string, int32]
	for _, k := range keys {
		if _, ok := c.m.Load(k); ok && c.shard(k) == s {
			if e := entry(k); s.promotionDue(e.lastPromoted.Load(), e.promotedInsertSeq.Load()) {
				survivor = e
				break
			}
		}
	}
	if !assert.NotNil(t, survivor, "an aged entry in the shard the insert went to") {
		return
	}
	mark := survivor.lastPromoted.Load()
	c.peek(survivor.key)
	promoted := survivor.lastPromoted.Load()
	assert.Greater(t, promoted, mark, "an aged entry is promoted once an insert happened")
	for i := 0; i < 8; i++ {
		c.peek(survivor.key)
	}
	assert.Equal(t, promoted, survivor.lastPromoted.Load(), "promoted once per insert, not per hit")
}

// The capacity bounds the cache as a whole: it holds that many entries, the
// most recently used, however their keys hash. The shard count is clamped to
// the capacity, and every key lands on a shard in use.
func TestMetaCacheHoldsExactlyItsCapacity(t *testing.T) {
	for _, tc := range []struct{ capacity, shards int }{{1024, 16}, {1000, 16}, {10, 10}, {1, 1}, {17, 16}} {
		c := newMetaCache[string, int32](tc.capacity)
		assert.EqualValues(t, tc.shards, c.shardCount, "capacity %d: shards in use", tc.capacity)
		n := 3 * tc.capacity
		for i := 0; i < n; i++ {
			key := fmt.Sprintf("key-%d", i)
			c.peekOrAdd(key, int32(i))
			v, ok := c.peek(key)
			assert.True(t, ok, "%q must be cached", key)
			assert.Equal(t, int32(i), v)
		}
		assert.EqualValues(t, tc.capacity, c.size.Load(), "capacity %d: size", tc.capacity)
		for i := 0; i < n; i++ {
			_, ok := c.m.Load(fmt.Sprintf("key-%d", i))
			assert.Equal(t, i >= n-tc.capacity, ok, "capacity %d: key-%d", tc.capacity, i)
		}
	}
}

// peekOrAdd honours the TTL as peek does: an expired entry is a miss that the
// new value replaces. A caller that only peekOrAdds, or a TTL that lapses
// between a peek and a peekOrAdd, would otherwise get the stale UID back and
// suppress the re-publication the TTL exists for.
func TestMetaCachePeekOrAddExpiresLikePeek(t *testing.T) {
	c := newMetaCache[string, int32](cacheSize)
	c.ttl = time.Hour
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }

	c.peekOrAdd("a", 1)
	now = now.Add(c.ttl - time.Nanosecond)
	v, ok := c.peekOrAdd("a", 2)
	assert.True(t, ok, "fresh: the existing value wins")
	assert.Equal(t, int32(1), v)

	now = now.Add(time.Nanosecond)
	_, ok = c.peekOrAdd("a", 3)
	assert.False(t, ok, "expired: a miss, replaced by the new value")
	v, ok = c.peek("a")
	assert.True(t, ok)
	assert.Equal(t, int32(3), v)
	assert.Equal(t, int64(1), c.size.Load(), "replaced in place, not double counted")
}

// A working set below the capacity stays cached whatever shards its keys hash
// to. Bounded per shard, a cyclic working set at 90% of the capacity missed a
// fifth of its lookups: a shard its keys overfilled evicted the key due next,
// and for the SQL id cache each miss minted a new id.
func TestMetaCacheWorkingSetBelowCapacityStaysCached(t *testing.T) {
	for _, capacity := range []int{16, 100, cacheSize} {
		c := newMetaCache[string, int32](capacity)
		keys := make([]string, capacity*9/10)
		for i := range keys {
			keys[i] = fmt.Sprintf("select * from t%d where id = ?", i)
		}
		misses := 0
		for round := 0; round < 5; round++ {
			for i, k := range keys {
				if _, ok := c.peek(k); !ok {
					if round > 0 {
						misses++
					}
					c.peekOrAdd(k, int32(i))
				}
			}
		}
		assert.Zero(t, misses, "capacity %d: a working set of %d", capacity, len(keys))
	}
}

// Past the capacity the cache evicts the least recently used entry across
// shards: a hot set read between inserts of one-off keys stays cached, and the
// cache holds no more than its capacity.
func TestMetaCacheKeepsAHotSetUnderChurn(t *testing.T) {
	c := newMetaCache[string, int32](cacheSize)
	hot := make([]string, cacheSize/2)
	for i := range hot {
		hot[i] = fmt.Sprintf("select * from hot%d where id = ?", i)
	}
	misses := 0
	for i := 0; i < 50*cacheSize; i++ {
		k := hot[i%len(hot)]
		if i%4 == 3 {
			k = fmt.Sprintf("select * from churn%d", i)
		}
		if _, ok := c.peek(k); !ok {
			if i >= 10*cacheSize && strings.Contains(k, "hot") {
				misses++
			}
			c.peekOrAdd(k, 0)
		}
	}
	assert.Zero(t, misses, "hot keys evicted by one-off keys")
	n := 0
	c.m.Range(func(any, any) bool { n++; return true })
	assert.LessOrEqual(t, n, cacheSize)
}
