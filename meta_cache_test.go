package pinpoint

import (
	"container/list"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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

	c.remove("a")
	_, ok = c.peek("a")
	assert.False(t, ok)
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
					c.remove(k)
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
