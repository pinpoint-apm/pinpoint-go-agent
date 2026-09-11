package pinpoint

import (
	"os"
	"runtime"
	"runtime/metrics"
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
const statShardCount = 16 // power of two

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

	// The previous sample's timestamp, which the next sample turns into a
	// collection interval. Only the agent's single stat worker touches it.
	// init primes it, so a constructed agentStats never carries the zero time
	// into a measurement.
	lastCollectTime time.Time

	// The batch under construction. Owned by the stat worker, and kept on the
	// agent rather than in collectAgentStatWorker's frame so that a supervisor
	// restart of the worker resumes the partial batch instead of discarding
	// the snapshots gathered before the panic. workerStarted tells a restart
	// from the first run (see collectAgentStatWorker).
	// batch is atomic only so tests can watch the cursor from another
	// goroutine; the worker is its sole writer.
	collected     []*inspectorStats
	batch         atomic.Int32
	workerStarted bool
	// collectFailures throttles the WARN for a collection that panicked.
	collectFailures logThrottle
	// Test seam: number of upcoming getStats calls that panic.
	failCollects atomic.Int32

	// memSamples is the runtime/metrics read that replaced runtime.ReadMemStats,
	// which stops the world on every call - a periodic latency blip in every
	// goroutine of the host, once per Stat.CollectInterval, for numbers that
	// runtime/metrics reads without a pause. The slice is built once and
	// reused; only the stat worker reads it. Order matches memSample*.
	memSamples []metrics.Sample
}

// Indexes into agentStats.memSamples.
const (
	memSampleHeapObjects  = iota // /memory/classes/heap/objects:bytes
	memSampleHeapUnused          // /memory/classes/heap/unused:bytes
	memSampleHeapReleased        // /memory/classes/heap/released:bytes
	memSampleHeapFree            // /memory/classes/heap/free:bytes
	memSampleHeapStacks          // /memory/classes/heap/stacks:bytes
	memSampleOsStacks            // /memory/classes/os-stacks:bytes
	memSampleGcCycles            // /gc/cycles/total:gc-cycles
	memSampleGcPauseCpu          // /cpu/classes/gc/pause:cpu-seconds
	memSampleCount
)

func newMemSamples() []metrics.Sample {
	names := [memSampleCount]string{
		memSampleHeapObjects:  "/memory/classes/heap/objects:bytes",
		memSampleHeapUnused:   "/memory/classes/heap/unused:bytes",
		memSampleHeapReleased: "/memory/classes/heap/released:bytes",
		memSampleHeapFree:     "/memory/classes/heap/free:bytes",
		memSampleHeapStacks:   "/memory/classes/heap/stacks:bytes",
		memSampleOsStacks:     "/memory/classes/os-stacks:bytes",
		memSampleGcCycles:     "/gc/cycles/total:gc-cycles",
		memSampleGcPauseCpu:   "/cpu/classes/gc/pause:cpu-seconds",
	}
	samples := make([]metrics.Sample, len(names))
	for i, name := range names {
		samples[i].Name = name
	}
	return samples
}

// memStats is the subset of runtime.MemStats the agent reports, read through
// runtime/metrics. Each field is the documented equivalent of the MemStats
// field it replaces (see the runtime/metrics package doc): HeapInuse is
// objects+unused, HeapSys adds released+free, StackInuse is heap/stacks and
// StackSys adds os-stacks. NumGC is /gc/cycles/total.
//
// PauseTotalNs has no exact counterpart: /gc/pauses:seconds is a histogram.
// /cpu/classes/gc/pause:cpu-seconds is the pause wall time multiplied by
// GOMAXPROCS at the time of each pause, so dividing by the current GOMAXPROCS
// recovers the wall time exactly while GOMAXPROCS is constant, which it is in
// practice; a change mid-run skews the pauses before it by the ratio.
type memStats struct {
	heapInuse, heapSys, stackInuse, stackSys uint64
	numGC                                    uint64
	pauseTotalNs                             uint64
}

func (stats *agentStats) readMemStats() memStats {
	s := stats.memSamples
	metrics.Read(s)
	u := func(i int) uint64 {
		if s[i].Value.Kind() == metrics.KindUint64 {
			return s[i].Value.Uint64()
		}
		return 0
	}
	var pauseNs uint64
	if s[memSampleGcPauseCpu].Value.Kind() == metrics.KindFloat64 {
		pauseNs = uint64(s[memSampleGcPauseCpu].Value.Float64() / float64(runtime.GOMAXPROCS(0)) * float64(time.Second))
	}
	heapInuse := u(memSampleHeapObjects) + u(memSampleHeapUnused)
	stackInuse := u(memSampleHeapStacks)
	return memStats{
		heapInuse:    heapInuse,
		heapSys:      heapInuse + u(memSampleHeapReleased) + u(memSampleHeapFree),
		stackInuse:   stackInuse,
		stackSys:     stackInuse + u(memSampleOsStacks),
		numGC:        u(memSampleGcCycles),
		pauseTotalNs: pauseNs,
	}
}

func newAgentStats() *agentStats {
	stats := &agentStats{proc: newProcHandle(), memSamples: newMemSamples()}
	stats.collectFailures.src = "stats"
	stats.activeSpan.init()
	stats.init()
	return stats
}

// init primes the CPU and memory baselines and clears the counters so the
// AgentStats::initAgentStats: the stat worker calls it on its first run,
// which can be seconds after the agent was created.
func (stats *agentStats) init() {
	stats.resetBaseline()
	stats.reset()
	stats.batch.Store(0)
}

// resetBaseline re-takes only the CPU and collect-time baseline, leaving the
// request counters and the partial batch untouched. The stat worker calls it
// when the supervisor restarts it: the first sample after a restart must not
// report the restart gap as load or interval, yet the snapshots gathered
// AgentStats::resetCollectionBaseline.
func (stats *agentStats) resetBaseline() {
	// The system-wide CPU baseline lives in a gopsutil package global, so it
	// is the one piece of this state that cannot move onto the agent.
	cpu.Percent(0, false)
	if stats.proc != nil {
		stats.proc.Percent(0)
	}

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

// activeSpanMaxSize bounds the active-span registry. Without it a span that is
// never ended - an instrumentation bug
// in the application, or a plugin's missing EndSpan on an error path - leaves
// its entry behind forever, and since the entries are real map values the
// registry grows without bound. The bound is applied per shard
// (activeSpanMaxSize / activeSpanShardCount): span ids are random, so the
// shards fill evenly without a registry-wide lock or counter on the store path.
const activeSpanMaxSize = 10240

const activeSpanShardMaxSize = activeSpanMaxSize / activeSpanShardCount

type activeSpanRegistry struct {
	shards [activeSpanShardCount]activeSpanShard
	// evicted counts entries evicted over the registry's lifetime; the
	// throttled warning reports it so an operator can tell a one-off burst
	// from a steady leak.
	evicted atomic.Int64
}

// activeSpanEvictLog throttles the eviction warning: a leaking application
// evicts once per new span, and one line per request is a log flood.
var activeSpanEvictLog = logThrottle{src: "stats"}

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
	evicted := false
	if _, present := s.m[spanId]; !present && len(s.m) >= activeSpanShardMaxSize {
		// Full: make room by dropping one existing entry, as Caffeine evicts on
		// insert. Which one is up to Go's randomized map iteration - the same
		// of one-shot keys - and the victim's later remove is a harmless
		// delete of a missing key. Refusing the new span instead would freeze
		// the histogram on the leaked entries and hide every live request.
		for victim := range s.m {
			delete(s.m, victim)
			break
		}
		evicted = true
	}
	s.m[spanId] = startTime
	s.mu.Unlock()
	if evicted {
		total := r.evicted.Add(1)
		activeSpanEvictLog.warnf("active span registry full (%d spans, max %d): evicted an entry, "+
			"%d evicted in total; spans may not be ended (missing EndSpan on an error path)",
			r.size(), activeSpanMaxSize, total)
	}
}

// size is the number of registered spans, summed over the shards one lock at a
// time; concurrent store/remove may already have moved it. Off the hot path:
// the eviction report and tests.
func (r *activeSpanRegistry) size() int {
	n := 0
	for i := range r.shards {
		s := &r.shards[i]
		s.mu.Lock()
		n += len(s.m)
		s.mu.Unlock()
	}
	return n
}

func (r *activeSpanRegistry) remove(spanId int64) {
	s := r.shard(spanId)
	s.mu.Lock()
	delete(s.m, spanId)
	s.mu.Unlock()
}

// bucketActiveSpan increments the [<=1s, <=3s, <=5s, >5s] bucket in counts for
// a span started at startTime.
//
// NORMAL schema (BaseHistogramSchema: slots 1000/3000/5000ms, compared with
// elapsedTime <= slotTime). Comparing float seconds put a span at exactly
// 1000ms in the second bucket, and left the boundary at the mercy of float
// rounding.
func bucketActiveSpan(counts []int32, now time.Time, startTime time.Time) {
	switch d := now.Sub(startTime).Milliseconds(); {
	case d <= 1000:
		counts[0]++
	case d <= 3000:
		counts[1]++
	case d <= 5000:
		counts[2]++
	default:
		counts[3]++
	}
}

// count buckets active spans by elapsed time: [<=1s, <=3s, <=5s, >5s].
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

// uncollectedUsage is what numFD and numThreads report when the reading is
// unavailable - no process handle, or a failed read. Zero is a plausible
// measurement the inspector charts as fact, so it cannot mean "unknown"; -1 is
const uncollectedUsage = -1

func (stats *agentStats) numFD() int32 {
	if stats.proc != nil {
		if n, err := stats.proc.NumFDs(); err == nil {
			return n
		}
	}
	return uncollectedUsage
}

func (stats *agentStats) numThreads() int32 {
	if stats.proc != nil {
		if n, err := stats.proc.NumThreads(); err == nil {
			return n
		}
	}
	return uncollectedUsage
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

	return normalizeCpuLoad(procCpu, sysCpu, runtime.NumCPU())
}

// normalizeCpuLoad turns gopsutil percentages into the 0..1 loads the
// process.Percent is not divided by the core count, so a process saturating
// four cores reads 400; cpu.Percent(0, false) is already the whole-machine
// average. Both are clamped so a negative or NaN reading never leaves range.
//
// numCPU is runtime.NumCPU(), which under a cgroup CPU quota can exceed the
// cores the process may actually use; honoring the quota is out of scope.
func normalizeCpuLoad(procPercent, sysPercent float64, numCPU int) (float64, float64) {
	if numCPU < 1 {
		numCPU = 1
	}
	return clampUnit(procPercent / 100 / float64(numCPU)), clampUnit(sysPercent / 100)
}

func clampUnit(v float64) float64 {
	if v != v || v < 0 { // NaN or negative
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// getStats samples the agent and reports the measured collection interval in
// milliseconds, which the collector divides into the counts to derive TPS.
// Truncating to whole seconds first (4.99s -> 4000ms) inflated TPS by up to
// 25%. The first sample measures from init, which the stat worker calls just
// before its ticker starts, so it needs no configured value to stand in.
func (stats *agentStats) getStats() *inspectorStats {
	if stats.failCollects.Load() > 0 {
		stats.failCollects.Add(-1)
		panic("injected getStats failure")
	}

	now := time.Now()
	procCpu, sysCpu := stats.cpuLoad()
	counters := stats.drainCounters()

	memStat := stats.readMemStats()
	// At least 1ms: this is wall time, and an NTP step between two
	// collections makes the gap 0 or negative, which goes on the wire as the
	// window the counters cover and the collector divides by it (the C++
	// GrpcStats::collect clamps the same way).
	interval := max(now.Sub(stats.lastCollectTime).Milliseconds(), 1)

	inspector := inspectorStats{
		sampleTime:  now,
		interval:    interval,
		cpuProcLoad: procCpu,
		cpuSysLoad:  sysCpu,
		heapUsed:    int64(memStat.heapInuse),
		heapMax:     int64(memStat.heapSys),
		nonHeapUsed: int64(memStat.stackInuse),
		nonHeapMax:  int64(memStat.stackSys),
		// GarbageCollectorMXBean counts: the web's inspector-definition-for-agent.yml
		// runs gcOldCount/gcOldTime through its "delta" post-processor, so
		// sending per-interval deltas would be differentiated twice.
		gcNum:        int64(memStat.numGC),
		gcTime:       int64(memStat.pauseTotalNs / uint64(time.Millisecond)),
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

	stats := agent.stats
	cfgBatchCount := agent.config.Int(CfgStatBatchCount)

	// First run: cold-initialize everything. A later run is a supervisor
	// restart after a panic escaped the loop below: only the CPU/time
	// baseline is re-taken, so the first sample after the restart does not
	// report the restart gap as load, while the snapshots already in the
	// partial batch are kept rather than thrown away with the run.
	if stats.workerStarted {
		stats.resetBaseline()
	} else {
		stats.workerStarted = true
		stats.init()
	}
	if len(stats.collected) != cfgBatchCount {
		stats.collected = make([]*inspectorStats, cfgBatchCount)
		stats.batch.Store(0)
	}

	cfgInterval := int64(agent.config.Int(CfgStatCollectInterval))
	ticker := time.NewTicker(time.Duration(cfgInterval) * time.Millisecond)
	defer ticker.Stop()
	stop := agent.stopSignal().Done()

	for agent.workerContinues() {
		select {
		case <-stop:
			Log("stats").Infof("end collect agent stat goroutine")
			return
		case <-ticker.C:
			// collection costs this one snapshot, not the partial batch.
			// The batch cursor is left alone, so the next tick fills the
			// same slot. superviseWorker remains the backstop for anything
			// that panics outside this call.
			if snapshot := stats.collect(); snapshot != nil {
				stats.collected[stats.batch.Load()] = snapshot
				stats.batch.Add(1)
			}

			if int(stats.batch.Load()) == cfgBatchCount {
				// Reset before the send: the batch is complete, so a panic
				// in the enqueue (and the restart it causes) must not
				// leave the cursor at cfgBatchCount.
				batch := stats.collected
				stats.batch.Store(0)
				agent.enqueueStat(makePAgentStatBatch(batch))
			}
		}
	}
}

// collect is getStats with a per-collection recover: a panic while sampling
// (a transient /proc read failure, a gopsutil error path) is logged through a
// throttled WARN and reported as a nil snapshot, so the caller skips the
// sample and keeps collecting.
func (stats *agentStats) collect() (snapshot *inspectorStats) {
	defer func() {
		if e := recover(); e != nil {
			snapshot = nil
			stats.collectFailures.warnf("agent stat collection failed, snapshot skipped: %v", e)
		}
	}()
	return stats.getStats()
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
