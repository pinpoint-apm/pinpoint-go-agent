package pinpoint

import (
	"container/list"
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
	assert.Equal(t, int64(1), c.shard("a").size.Load())
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
	assert.Equal(t, int64(0), c.shard("a").size.Load(), "the expired entry is removed, not left to age out")

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

func TestMetaCacheEvictsLeastRecentlyUsed(t *testing.T) {
	// capacity 16 over 16 shards = 1 entry per shard: two keys in the same
	// shard evict each other. Find such a pair (the hash seed is random per
	// process), then check the older key is the one that goes.
	c := newMetaCache[string, int32](metaCacheShardCount)
	first := "key-0"
	second := ""
	for i := 1; i < 1000; i++ {
		k := fmt.Sprintf("key-%d", i)
		if c.shard(k) == c.shard(first) {
			second = k
			break
		}
	}
	assert.NotEmpty(t, second)

	c.peekOrAdd(first, 1)
	c.peekOrAdd(second, 2)
	_, ok := c.peek(first)
	assert.False(t, ok)
	v, ok := c.peek(second)
	assert.True(t, ok)
	assert.Equal(t, int32(2), v)
}

// fifoCache replicates how the four caches behaved on hashicorp/golang-lru:
// Peek never promotes, so eviction order is insertion order.
type fifoCache struct {
	m     map[string]*list.Element
	order *list.List
	cap   int
}

func newFifoCache(capacity int) *fifoCache {
	return &fifoCache{m: make(map[string]*list.Element), order: list.New(), cap: capacity}
}

func (c *fifoCache) peek(k string) bool {
	_, ok := c.m[k]
	return ok
}

func (c *fifoCache) add(k string) {
	if _, ok := c.m[k]; ok {
		return
	}
	c.m[k] = c.order.PushFront(k)
	if len(c.m) > c.cap {
		victim := c.order.Back()
		delete(c.m, victim.Value.(string))
		c.order.Remove(victim)
	}
}

// runMetaWorkload interleaves a fixed hot set with a stream of one-shot churn
// keys, the access pattern where FIFO eviction hurts: churn pushes hot keys
// out even though they are hit every round. It returns how many times a hot
// key had to be re-inserted after the warmup round — in the agent each such
// re-insert is a new id plus a metadata resend to the collector.
func runMetaWorkload(peek func(string) bool, add func(string)) int {
	const hotN, rounds, churnPerRound = 256, 32, 256
	hot := make([]string, hotN)
	for i := range hot {
		hot[i] = fmt.Sprintf("select * from hot_table_%03d where id = ?", i)
	}
	hotResends := 0
	churn := 0
	for r := 0; r < rounds; r++ {
		for i := 0; i < hotN; i++ {
			if !peek(hot[i]) {
				add(hot[i])
				if r > 0 {
					hotResends++
				}
			}
			ck := fmt.Sprintf("select * from churn_table_%06d", churn)
			churn++
			if !peek(ck) {
				add(ck)
			}
		}
	}
	return hotResends
}

func TestMetaCacheLruBeatsFifoOnResends(t *testing.T) {
	fifo := newFifoCache(cacheSize)
	fifoResends := runMetaWorkload(fifo.peek, fifo.add)

	c := newMetaCache[string, int32](cacheSize)
	lruResends := runMetaWorkload(
		func(k string) bool { _, ok := c.peek(k); return ok },
		func(k string) { c.peekOrAdd(k, 0) },
	)

	t.Logf("hot-key metadata resends: fifo(old)=%d lru(new)=%d", fifoResends, lruResends)
	// observed ~11-20x fewer resends; ×4 leaves headroom for hash-seed variance
	assert.Greater(t, fifoResends, lruResends*4)
}

func TestMetaCacheConcurrent(t *testing.T) {
	// Small capacity keeps every shard full, so promotion, eviction, and
	// removal all race against lock-free peeks under -race.
	c := newMetaCache[string, int32](64)
	keys := make([]string, 512)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%03d", i)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20000; i++ {
				k := keys[(i*7+g*13)&511]
				if _, ok := c.peek(k); !ok {
					c.peekOrAdd(k, int32(i))
				}
				if i&255 == 0 {
					c.remove(k, func(int32) bool { return true })
				}
			}
		}(g)
	}
	wg.Wait()
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
// dispatches to is AES-accelerated, which is what keeps a maxSqlSize key off
// the microsecond scale on the insert path (#189).
func BenchmarkMetaCacheShard(b *testing.B) {
	c := newMetaCache[string, int32](cacheSize)
	for _, size := range []int{190, 1024, 8 * 1024, maxSqlSize} {
		key := "select * from t where x in (" + strings.Repeat("9", size) + ")"
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = c.shard(key)
			}
		})
	}
}

// A hit promotes only after an insert since its previous promotion; otherwise
// it avoids the shard lock and leaves the order untouched.
func TestMetaCacheHitsWithoutInsertsDoNotPromote(t *testing.T) {
	c := newMetaCache[string, int32](cacheSize)
	s := c.shard("k0")

	// Fill s to capacity through keys that hash to it, so it is full and
	// every entry is a promotion candidate once aged.
	keys := make([]string, 0, s.cap)
	next := 0 // first key number the fill did not try
	for ; len(keys) < s.cap; next++ {
		k := fmt.Sprintf("k%d", next)
		if c.shard(k) == s {
			c.peekOrAdd(k, int32(next))
			keys = append(keys, k)
		}
	}
	assert.Equal(t, int64(s.cap), s.size.Load())
	entry := func(k string) *metaCacheEntry[string, int32] {
		raw, _ := c.m.Load(k)
		return raw.(*metaCacheEntry[string, int32])
	}
	// The fill itself was a run of inserts, so the first rotation may promote
	// each aged entry once; that settles the order for the insert-free stretch.
	for _, k := range keys {
		c.peek(k)
	}
	oldest := entry(keys[0])
	before, oldestMark := s.opSeq.Load(), oldest.lastPromoted.Load()

	// Rotate over more keys than ageThreshold, several times over: the entries
	// age past the threshold again and again, yet nothing was inserted.
	for round := 0; round < 4; round++ {
		for _, k := range keys {
			_, ok := c.peek(k)
			assert.True(t, ok)
		}
	}
	assert.Equal(t, before, s.opSeq.Load(), "hits without an insert must not promote")
	assert.Equal(t, oldestMark, oldest.lastPromoted.Load())

	// One insert re-arms promotion: the next hit on an aged entry promotes it
	// exactly once, and the hits after that are lock-free again.
	extra := ""
	for i := next; ; i++ { // past every key the fill tried, so this is a real insert
		if k := fmt.Sprintf("k%d", i); c.shard(k) == s {
			extra = k
			break
		}
	}
	c.peekOrAdd(extra, 0)
	// The settling rotation promoted the entries in key order, so keys[1] sits
	// near the back, well past the threshold, and survives the one eviction.
	assert.Equal(t, int64(s.cap), s.size.Load(), "the insert evicts exactly one entry")
	_, ok := c.m.Load(keys[1])
	assert.True(t, ok)
	survivor := entry(keys[1])
	mark := survivor.lastPromoted.Load()
	c.peek(keys[1])
	promoted := survivor.lastPromoted.Load()
	assert.Greater(t, promoted, mark, "an aged entry is promoted once an insert happened")
	for i := 0; i < 8; i++ {
		c.peek(keys[1])
	}
	assert.Equal(t, promoted, survivor.lastPromoted.Load(), "promoted once per insert, not per hit")
}

// removeValue finds an entry by its value for a caller that no longer holds
// the key (sqlMeta), and, like remove, leaves an entry whose value is not the
// one the caller means.
func TestMetaCacheRemoveValue(t *testing.T) {
	c := newMetaCache[string, int32](64)
	c.peekOrAdd("a", 1)
	c.peekOrAdd("b", 2)

	c.removeValue(func(v int32) bool { return v == 1 })
	_, ok := c.peek("a")
	assert.False(t, ok, "the entry holding the value is removed")
	v, ok := c.peek("b")
	assert.True(t, ok, "other entries stay")
	assert.Equal(t, int32(2), v)

	c.removeValue(func(v int32) bool { return v == 99 })
	_, ok = c.peek("b")
	assert.True(t, ok, "no match, nothing removed")
}

// The shard split keeps the configured total: the remainder goes to the first
// shards and the shard count is clamped to the capacity.
func TestMetaCacheCapacityIsSplitExactly(t *testing.T) {
	for _, tc := range []struct{ capacity, shards int }{{1024, 16}, {1000, 16}, {10, 10}, {1, 1}, {17, 16}} {
		c := newMetaCache[string, int32](tc.capacity)
		total := 0
		for i := range c.shards {
			total += c.shards[i].cap
		}
		assert.Equal(t, tc.capacity, total, "capacity %d: total", tc.capacity)
		assert.EqualValues(t, tc.shards, c.shardCount, "capacity %d: shards in use", tc.capacity)
		for i := range c.shards {
			if i < tc.shards {
				assert.GreaterOrEqual(t, c.shards[i].cap, 1, "capacity %d: shard %d in use", tc.capacity, i)
			} else {
				assert.Zero(t, c.shards[i].cap, "capacity %d: shard %d unused", tc.capacity, i)
			}
		}
	}

	// Every key lands on a shard in use.
	c := newMetaCache[string, int32](10)
	for i := 0; i < 200; i++ {
		key := string(rune('a'+i%26)) + string(rune(i))
		c.peekOrAdd(key, int32(i))
		v, ok := c.peek(key)
		assert.True(t, ok, "%q must be cached", key)
		assert.Equal(t, int32(i), v)
	}
}

// peekOrAdd honours the TTL as peek does: an expired entry is a miss that the
// new value replaces. The invariant used to hold only because cacheSqlUid
// peeks first; a caller that only peekOrAdds, or a TTL that lapses between the
// two calls, got the stale UID back and the re-publication the TTL exists for
// was suppressed.
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
	assert.Equal(t, int64(1), c.shard("a").size.Load(), "replaced in place, not double counted")
}
