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

// agentStats owns everything the agent stat collector reads: the per-request
// counters, the registry of in-flight spans, the process handle and the
// previous sample's baselines. One instance per agent, reached from the request
// path through span.agent, mirroring the C++ agent's AgentStats class. As
// package globals these had to be built once for the process lifetime and
// never rebuilt, because a restart would otherwise re-prime or swap them while
// a previous agent's abandoned stat worker and its still-in-flight spans were
// reading them; owning them per agent is what makes rebuilding them safe.
type agentStats struct {
	// proc is nil when the process handle cannot be opened; every reader
	// below tolerates that.
	proc *process.Process

	shards     [statShardCount]statShard
	activeSpan activeSpanRegistry

	// The previous sample's memory counters and timestamp, which the next
	// sample turns into GC deltas and a collection interval. Only the agent's
	// single stat worker touches them.
	lastMemStat     runtime.MemStats
	lastCollectTime time.Time
}

func newAgentStats() *agentStats {
	stats := &agentStats{proc: newProcHandle()}
	stats.activeSpan.init()
	stats.init()
	return stats
}

// init primes the CPU and memory baselines and clears the counters so the
// first collection interval measures a real period. Mirrors the C++ agent's
// AgentStats::initAgentStats: the stat worker calls it again when it starts,
// which can be seconds after the agent was created.
func (stats *agentStats) init() {
	// The system-wide CPU baseline lives in a gopsutil package global, so it
	// is the one piece of this state that cannot move onto the agent.
	cpu.Percent(0, false)
	if stats.proc != nil {
		stats.proc.Percent(0)
	}

	stats.reset()
	runtime.ReadMemStats(&stats.lastMemStat)
	stats.lastCollectTime = time.Now()
}

func newProcHandle() *process.Process {
	p, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return nil
	}
	p.Percent(0)
	return p
}

// shard returns the calling goroutine's counter shard. When the goid offset is
// unavailable (goIdOffset == 0) every goroutine shares shard 0: the
// goIdFromDump fallback parses a stack dump and is far too slow for this path,
// and a single shard is exactly the pre-sharding behavior.
func (stats *agentStats) shard() *statShard {
	if goIdOffset == 0 {
		return &stats.shards[0]
	}
	return &stats.shards[uint64(goIdFromG())&(statShardCount-1)]
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

// init allocates the shard maps. The registry is a value field of agentStats,
// so it is initialized in place rather than returned by a constructor.
func (r *activeSpanRegistry) init() {
	for i := range r.shards {
		r.shards[i].m = make(map[int64]time.Time)
	}
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

// bucketActiveSpan increments the [<1s, <3s, <5s, >=5s] bucket in counts for
// a span started at startTime.
func bucketActiveSpan(counts []int32, now time.Time, startTime time.Time) {
	switch d := now.Sub(startTime).Seconds(); {
	case d < 1:
		counts[0]++
	case d < 3:
		counts[1]++
	case d < 5:
		counts[2]++
	default:
		counts[3]++
	}
}

// count buckets active spans by elapsed time: [<1s, <3s, <5s, >=5s].
func (r *activeSpanRegistry) count(now time.Time) []int32 {
	count := []int32{0, 0, 0, 0}
	for i := range r.shards {
		s := &r.shards[i]
		s.mu.Lock()
		for _, startTime := range s.m {
			bucketActiveSpan(count, now, startTime)
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

func (stats *agentStats) numFD() int32 {
	if stats.proc != nil {
		n, _ := stats.proc.NumFDs()
		return n
	}
	return 0
}

func (stats *agentStats) numThreads() int32 {
	if stats.proc != nil {
		n, _ := stats.proc.NumThreads()
		return n
	}
	return 0
}

func (stats *agentStats) cpuLoad() (float64, float64) {
	var procCpu float64
	if stats.proc != nil {
		procCpu, _ = stats.proc.Percent(0)
	}

	// A failed reading returns no per-cpu entries; reporting 0 keeps a
	// transient error from panicking the stat worker (and with it the process).
	var sysCpu float64
	if percent, err := cpu.Percent(0, false); err == nil && len(percent) > 0 {
		sysCpu = percent[0]
	}

	return procCpu / 100, sysCpu / 100
}

func (stats *agentStats) getStats() *inspectorStats {
	now := time.Now()
	procCpu, sysCpu := stats.cpuLoad()
	counters := stats.drainCounters()

	var memStat runtime.MemStats
	runtime.ReadMemStats(&memStat)
	elapsed := now.Sub(stats.lastCollectTime).Seconds()

	inspector := inspectorStats{
		sampleTime:   now,
		interval:     int64(elapsed) * 1000,
		cpuProcLoad:  procCpu,
		cpuSysLoad:   sysCpu,
		heapUsed:     int64(memStat.HeapInuse),
		heapMax:      int64(memStat.HeapSys),
		nonHeapUsed:  int64(memStat.StackInuse),
		nonHeapMax:   int64(memStat.StackSys),
		gcNum:        int64(memStat.NumGC - stats.lastMemStat.NumGC),
		gcTime:       int64(memStat.PauseTotalNs-stats.lastMemStat.PauseTotalNs) / int64(time.Millisecond),
		numOpenFD:    int64(stats.numFD()),
		numThreads:   int64(stats.numThreads()),
		responseAvg:  calcResponseAvg(counters.accResponseTime, counters.requestCount),
		responseMax:  counters.maxResponseTime,
		sampleNew:    counters.sampleNew,
		sampleCont:   counters.sampleCont,
		unSampleNew:  counters.unSampleNew,
		unSampleCont: counters.unSampleCont,
		skipNew:      counters.skipNew,
		skipCont:     counters.skipCont,
		activeSpan:   stats.activeSpan.count(now),
	}

	stats.lastMemStat = memStat
	stats.lastCollectTime = now

	return &inspector
}

// drainCounters sweeps every shard, swapping each counter to zero and summing
// (max-combining maxResponseTime). Accuracy: each increment lands in exactly
// one collection interval — no loss, no double count — but the interval
// boundary is fuzzy by the duration of the sweep, and one request's
// (accResponseTime, requestCount) pair can split across two intervals if the
// sweep interleaves between the two adds. Both were already true of the nine
// sequential global swaps this replaces.
func (stats *agentStats) drainCounters() statsCounterSnapshot {
	var c statsCounterSnapshot
	for i := range stats.shards {
		s := &stats.shards[i]
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

func (agent *agent) collectAgentStatWorker() {
	Log("stats").Infof("start collect agent stat goroutine")

	agent.stats.init()

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
			collected[batch] = agent.stats.getStats()
			batch++

			if batch == cfgBatchCount {
				agent.enqueueStat(makePAgentStatBatch(collected))
				batch = 0
			}
		}
	}
}

func (stats *agentStats) collectResponseTime(resTime int64) {
	s := stats.shard()
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

func (stats *agentStats) reset() {
	for i := range stats.shards {
		s := &stats.shards[i]
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
	span.agent.stats.activeSpan.store(span.spanId, span.startTime)
	addRealTimeSampledActiveSpan(span)
}

func dropSampledActiveSpan(span *span) {
	span.agent.stats.activeSpan.remove(span.spanId)
	dropRealTimeSampledActiveSpan(span)
}

func addUnSampledActiveSpan(span *noopSpan) {
	span.agent.stats.activeSpan.store(span.spanId, span.startTime)
	addRealTimeUnSampledActiveSpan(span)
}

func dropUnSampledActiveSpan(span *noopSpan) {
	span.agent.stats.activeSpan.remove(span.spanId)
	dropRealTimeUnSampledActiveSpan(span)
}

func (stats *agentStats) incrSampleNew() {
	atomic.AddInt64(&stats.shard().sampleNew, 1)
}
func (stats *agentStats) incrUnSampleNew() {
	atomic.AddInt64(&stats.shard().unSampleNew, 1)
}
func (stats *agentStats) incrSampleCont() {
	atomic.AddInt64(&stats.shard().sampleCont, 1)
}
func (stats *agentStats) incrUnSampleCont() {
	atomic.AddInt64(&stats.shard().unSampleCont, 1)
}
func (stats *agentStats) incrSkipNew() {
	atomic.AddInt64(&stats.shard().skipNew, 1)
}
func (stats *agentStats) incrSkipCont() {
	atomic.AddInt64(&stats.shard().skipCont, 1)
}
