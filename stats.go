package pinpoint

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/process"
)

type inspectorStats struct {
	sampleTime   time.Time
	interval     int64
	cpuProcLoad  float64
	cpuSysLoad   float64
	heapUsed     int64
	heapMax      int64
	nonHeapUsed  int64
	nonHeapMax   int64
	gcNum        int64
	gcTime       int64
	numOpenFD    int64
	numThreads   int64
	responseAvg  int64
	responseMax  int64
	sampleNew    int64
	sampleCont   int64
	unSampleNew  int64
	unSampleCont int64
	skipNew      int64
	skipCont     int64
	activeSpan   []int32
}

var (
	proc            *process.Process
	lastMemStat     runtime.MemStats
	lastCollectTime time.Time

	activeSpan = newActiveSpanRegistry()
)

// The per-request counters (response time acc/max/count plus the six sampler
// outcomes) are sharded by goroutine id. As process-global singles, every
// request's atomic RMW hit the same cache lines and the max update spun on a
// contended CAS; sharding puts each request's RMWs on cache lines other
// goroutines' requests rarely touch. Go offers no relaxed atomics, so the
// full-barrier cost of AddInt64 remains — only the cross-core traffic goes
// away (measured on an M1 Pro: 67→2.1 ns/op at -cpu=4, 158→1.0 at -cpu=16).
const statShardCount = 16 // power of two; matches the C++ agent's ResponseTimeShard count

// statShard is padded to 128 bytes so two shards never share a cache line
// regardless of the array's base alignment (Go has no alignas).
type statShard struct {
	accResponseTime int64
	maxResponseTime int64
	requestCount    int64
	sampleNew       int64
	unSampleNew     int64
	sampleCont      int64
	unSampleCont    int64
	skipNew         int64
	skipCont        int64
	_               [7]int64
}

var statShards [statShardCount]statShard

// statShardSelf returns the calling goroutine's shard. When the goid offset
// is unavailable (goIdOffset == 0) every goroutine shares shard 0: the
// goIdFromDump fallback parses a stack dump and is far too slow for this
// path, and a single shard is exactly the pre-sharding behavior.
func statShardSelf() *statShard {
	if goIdOffset == 0 {
		return &statShards[0]
	}
	return &statShards[uint64(goIdFromG())&(statShardCount-1)]
}

// activeSpanRegistry tracks the start time of in-flight spans keyed by span id.
// It replaces a sync.Map so that store/delete on the span hot path avoid boxing
// the int64 key and time.Time value into interface{} (the sync.Map did 3 heap
// allocations per sampled span). Sharding by span id keeps the per-span
// store/delete churn from serializing on a single lock.
const activeSpanShardCount = 32 // must be a power of two

type activeSpanRegistry struct {
	shards [activeSpanShardCount]activeSpanShard
}

// cacheLinePadSize is the stride the shards are spaced at. 128 and not 64:
// arm64 uses 128-byte lines (hw.cachelinesize is 128 on Apple silicon) and x86
// prefetches adjacent line pairs, and since Go aligns a heap struct to only 8
// bytes the extra slack also absorbs the shard array starting mid-line. Same
// constant sync.Pool pads poolLocal with, for the same reason.
const cacheLinePadSize = 128

type activeSpanShardInternal struct {
	mu sync.Mutex
	m  map[int64]time.Time
}

// activeSpanShard gives every shard its own cache line. The payload is 16 bytes
// (unsafe.Sizeof: an 8-byte sync.Mutex plus an 8-byte map header), so unpadded
// eight shards share one line and two goroutines locking *different* shards
// still ping-pong it -- worth -35% per op at -cpu=16, see
// BenchmarkActiveSpanRegistryParallel. Deriving the pad from unsafe.Sizeof
// rather than hardcoding 112 keeps it correct if a field is added.
type activeSpanShard struct {
	activeSpanShardInternal
	_ [cacheLinePadSize - unsafe.Sizeof(activeSpanShardInternal{})%cacheLinePadSize]byte
}

func newActiveSpanRegistry() *activeSpanRegistry {
	r := &activeSpanRegistry{}
	for i := range r.shards {
		r.shards[i].m = make(map[int64]time.Time)
	}
	return r
}

func (r *activeSpanRegistry) shard(spanId int64) *activeSpanShard {
	return &r.shards[uint64(spanId)&(activeSpanShardCount-1)]
}

func (r *activeSpanRegistry) store(spanId int64, startTime time.Time) {
	s := r.shard(spanId)
	s.mu.Lock()
	s.m[spanId] = startTime
	s.mu.Unlock()
}

func (r *activeSpanRegistry) remove(spanId int64) {
	s := r.shard(spanId)
	s.mu.Lock()
	delete(s.m, spanId)
	s.mu.Unlock()
}

// count buckets active spans by elapsed time: [<1s, <3s, <5s, >=5s].
func (r *activeSpanRegistry) count(now time.Time) []int32 {
	count := []int32{0, 0, 0, 0}
	for i := range r.shards {
		s := &r.shards[i]
		s.mu.Lock()
		for _, startTime := range s.m {
			d := now.Sub(startTime).Seconds()
			if d < 1 {
				count[0]++
			} else if d < 3 {
				count[1]++
			} else if d < 5 {
				count[2]++
			} else {
				count[3]++
			}
		}
		s.mu.Unlock()
	}
	return count
}

type statsCounterSnapshot struct {
	accResponseTime int64
	maxResponseTime int64
	requestCount    int64
	sampleNew       int64
	unSampleNew     int64
	sampleCont      int64
	unSampleCont    int64
	skipNew         int64
	skipCont        int64
}

func initStats() {
	var err error
	proc, err = process.NewProcess(int32(os.Getpid()))
	if err != nil {
		proc = nil
	} else {
		proc.Percent(0)
	}

	cpu.Percent(0, false)
	runtime.ReadMemStats(&lastMemStat)
	lastCollectTime = time.Now()
	// activeSpan is created once at package init and is read by the request
	// path; re-creating it here raced with in-flight spans and dropped the
	// entries of spans that started before a restart and ended after it.
}

func getNumFD() int32 {
	if proc != nil {
		n, _ := proc.NumFDs()
		return n
	}
	return 0
}

func getNumThreads() int32 {
	if proc != nil {
		n, _ := proc.NumThreads()
		return n
	}
	return 0
}

func getCpuLoad() (float64, float64) {
	var procCpu float64
	if proc != nil {
		procCpu, _ = proc.Percent(0)
	} else {
		procCpu = 0
	}
	sysCpu, _ := cpu.Percent(0, false)

	return procCpu / 100, sysCpu[0] / 100
}

func getStats() *inspectorStats {
	now := time.Now()
	procCpu, sysCpu := getCpuLoad()
	counters := drainStatsCounters()

	var memStat runtime.MemStats
	runtime.ReadMemStats(&memStat)
	elapsed := now.Sub(lastCollectTime).Seconds()

	stats := inspectorStats{
		sampleTime:   now,
		interval:     int64(elapsed) * 1000,
		cpuProcLoad:  procCpu,
		cpuSysLoad:   sysCpu,
		heapUsed:     int64(memStat.HeapInuse),
		heapMax:      int64(memStat.HeapSys),
		nonHeapUsed:  int64(memStat.StackInuse),
		nonHeapMax:   int64(memStat.StackSys),
		gcNum:        int64(memStat.NumGC - lastMemStat.NumGC),
		gcTime:       int64(memStat.PauseTotalNs-lastMemStat.PauseTotalNs) / int64(time.Millisecond),
		numOpenFD:    int64(getNumFD()),
		numThreads:   int64(getNumThreads()),
		responseAvg:  calcResponseAvg(counters.accResponseTime, counters.requestCount),
		responseMax:  counters.maxResponseTime,
		sampleNew:    counters.sampleNew,
		sampleCont:   counters.sampleCont,
		unSampleNew:  counters.unSampleNew,
		unSampleCont: counters.unSampleCont,
		skipNew:      counters.skipNew,
		skipCont:     counters.skipCont,
		activeSpan:   activeSpanCount(now),
	}

	lastMemStat = memStat
	lastCollectTime = now

	return &stats
}

// drainStatsCounters sweeps every shard, swapping each counter to zero and
// summing (max-combining maxResponseTime). Accuracy: each increment lands in
// exactly one collection interval — no loss, no double count — but the
// interval boundary is fuzzy by the duration of the sweep, and one request's
// (accResponseTime, requestCount) pair can split across two intervals if the
// sweep interleaves between the two adds. Both were already true of the nine
// sequential global swaps this replaces.
func drainStatsCounters() statsCounterSnapshot {
	var c statsCounterSnapshot
	for i := range statShards {
		s := &statShards[i]
		c.accResponseTime += atomic.SwapInt64(&s.accResponseTime, 0)
		if max := atomic.SwapInt64(&s.maxResponseTime, 0); max > c.maxResponseTime {
			c.maxResponseTime = max
		}
		c.requestCount += atomic.SwapInt64(&s.requestCount, 0)
		c.sampleNew += atomic.SwapInt64(&s.sampleNew, 0)
		c.unSampleNew += atomic.SwapInt64(&s.unSampleNew, 0)
		c.sampleCont += atomic.SwapInt64(&s.sampleCont, 0)
		c.unSampleCont += atomic.SwapInt64(&s.unSampleCont, 0)
		c.skipNew += atomic.SwapInt64(&s.skipNew, 0)
		c.skipCont += atomic.SwapInt64(&s.skipCont, 0)
	}
	return c
}

func calcResponseAvg(accResponseTime int64, requestCount int64) int64 {
	if requestCount > 0 {
		return accResponseTime / requestCount
	}

	return 0
}

func activeSpanCount(now time.Time) []int32 {
	return activeSpan.count(now)
}

func (agent *agent) collectAgentStatWorker() {
	Log("stats").Infof("start collect agent stat goroutine")
	defer agent.workerWg.Done()

	initStats()
	resetResponseTime()

	interval := time.Duration(agent.config.Int(CfgStatCollectInterval)) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	stop := agent.stopSignal().Done()

	cfgBatchCount := agent.config.Int(CfgStatBatchCount)
	collected := make([]*inspectorStats, cfgBatchCount)
	batch := 0

	for agent.enable.Load() {
		select {
		case <-stop:
			Log("stats").Infof("end collect agent stat goroutine")
			return
		case <-ticker.C:
			collected[batch] = getStats()
			batch++

			if batch == cfgBatchCount {
				agent.enqueueStat(makePAgentStatBatch(collected))
				batch = 0
			}
		}
	}
}

func collectResponseTime(resTime int64) {
	s := statShardSelf()
	atomic.AddInt64(&s.accResponseTime, resTime)
	atomic.AddInt64(&s.requestCount, 1)

	for {
		max := atomic.LoadInt64(&s.maxResponseTime)
		if max >= resTime {
			return
		}
		if atomic.CompareAndSwapInt64(&s.maxResponseTime, max, resTime) {
			return
		}
	}
}

func resetResponseTime() {
	for i := range statShards {
		s := &statShards[i]
		atomic.StoreInt64(&s.accResponseTime, 0)
		atomic.StoreInt64(&s.requestCount, 0)
		atomic.StoreInt64(&s.maxResponseTime, 0)
		atomic.StoreInt64(&s.sampleNew, 0)
		atomic.StoreInt64(&s.unSampleNew, 0)
		atomic.StoreInt64(&s.sampleCont, 0)
		atomic.StoreInt64(&s.unSampleCont, 0)
		atomic.StoreInt64(&s.skipNew, 0)
		atomic.StoreInt64(&s.skipCont, 0)
	}
}

func addSampledActiveSpan(span *span) {
	activeSpan.store(span.spanId, span.startTime)
	addRealTimeSampledActiveSpan(span)
}

func dropSampledActiveSpan(span *span) {
	activeSpan.remove(span.spanId)
	dropRealTimeSampledActiveSpan(span)
}

func addUnSampledActiveSpan(span *noopSpan) {
	activeSpan.store(span.spanId, span.startTime)
	addRealTimeUnSampledActiveSpan(span)
}

func dropUnSampledActiveSpan(span *noopSpan) {
	activeSpan.remove(span.spanId)
	dropRealTimeUnSampledActiveSpan(span)
}

func incrSampleNew() {
	atomic.AddInt64(&statShardSelf().sampleNew, 1)
}
func incrUnSampleNew() {
	atomic.AddInt64(&statShardSelf().unSampleNew, 1)
}
func incrSampleCont() {
	atomic.AddInt64(&statShardSelf().sampleCont, 1)
}
func incrUnSampleCont() {
	atomic.AddInt64(&statShardSelf().unSampleCont, 1)
}
func incrSkipNew() {
	atomic.AddInt64(&statShardSelf().skipNew, 1)
}
func incrSkipCont() {
	atomic.AddInt64(&statShardSelf().skipCont, 1)
}
