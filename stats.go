package pinpoint

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

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

	accResponseTime int64
	maxResponseTime int64
	requestCount    int64

	sampleNew    int64
	unSampleNew  int64
	sampleCont   int64
	unSampleCont int64
	skipNew      int64
	skipCont     int64

	activeSpan = newActiveSpanRegistry()
)

// activeSpanRegistry tracks the start time of in-flight spans keyed by span id.
// It replaces a sync.Map so that store/delete on the span hot path avoid boxing
// the int64 key and time.Time value into interface{} (the sync.Map did 3 heap
// allocations per sampled span). Sharding by span id keeps the per-span
// store/delete churn from serializing on a single lock.
const activeSpanShardCount = 32 // must be a power of two

type activeSpanRegistry struct {
	shards [activeSpanShardCount]activeSpanShard
}

type activeSpanShard struct {
	mu sync.Mutex
	m  map[int64]time.Time
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
	activeSpan = newActiveSpanRegistry()
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

func drainStatsCounters() statsCounterSnapshot {
	return statsCounterSnapshot{
		accResponseTime: atomic.SwapInt64(&accResponseTime, 0),
		maxResponseTime: atomic.SwapInt64(&maxResponseTime, 0),
		requestCount:    atomic.SwapInt64(&requestCount, 0),
		sampleNew:       atomic.SwapInt64(&sampleNew, 0),
		unSampleNew:     atomic.SwapInt64(&unSampleNew, 0),
		sampleCont:      atomic.SwapInt64(&sampleCont, 0),
		unSampleCont:    atomic.SwapInt64(&unSampleCont, 0),
		skipNew:         atomic.SwapInt64(&skipNew, 0),
		skipCont:        atomic.SwapInt64(&skipCont, 0),
	}
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
	agent.statTicker = time.NewTicker(interval)
	agent.statDone = make(chan bool)

	cfgBatchCount := agent.config.Int(CfgStatBatchCount)
	collected := make([]*inspectorStats, cfgBatchCount)
	batch := 0

	for agent.enable {
		select {
		case <-agent.statDone:
			Log("stats").Infof("end collect agent stat goroutine")
			return
		case <-agent.statTicker.C:
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
	atomic.AddInt64(&accResponseTime, resTime)
	atomic.AddInt64(&requestCount, 1)

	for {
		max := atomic.LoadInt64(&maxResponseTime)
		if max >= resTime {
			return
		}
		if atomic.CompareAndSwapInt64(&maxResponseTime, max, resTime) {
			return
		}
	}
}

func resetResponseTime() {
	atomic.StoreInt64(&accResponseTime, 0)
	atomic.StoreInt64(&requestCount, 0)
	atomic.StoreInt64(&maxResponseTime, 0)
	atomic.StoreInt64(&sampleNew, 0)
	atomic.StoreInt64(&unSampleNew, 0)
	atomic.StoreInt64(&sampleCont, 0)
	atomic.StoreInt64(&unSampleCont, 0)
	atomic.StoreInt64(&skipNew, 0)
	atomic.StoreInt64(&skipCont, 0)
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
	atomic.AddInt64(&sampleNew, 1)
}
func incrUnSampleNew() {
	atomic.AddInt64(&unSampleNew, 1)
}
func incrSampleCont() {
	atomic.AddInt64(&sampleCont, 1)
}
func incrUnSampleCont() {
	atomic.AddInt64(&unSampleCont, 1)
}
func incrSkipNew() {
	atomic.AddInt64(&skipNew, 1)
}
func incrSkipCont() {
	atomic.AddInt64(&skipCont, 1)
}
