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

	activeSpan sync.Map
)

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
	activeSpan = sync.Map{}
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
		responseAvg:  calcResponseAvg(),
		responseMax:  maxResponseTime,
		sampleNew:    sampleNew,
		sampleCont:   sampleCont,
		unSampleNew:  unSampleNew,
		unSampleCont: unSampleCont,
		skipNew:      skipNew,
		skipCont:     skipCont,
		activeSpan:   activeSpanCount(now),
	}

	lastMemStat = memStat
	lastCollectTime = now
	resetResponseTime()

	return &stats
}

func calcResponseAvg() int64 {
	if requestCount > 0 {
		return accResponseTime / requestCount
	}

	return 0
}

func activeSpanCount(now time.Time) []int32 {
	count := []int32{0, 0, 0, 0}
	activeSpan.Range(func(k, v interface{}) bool {
		s := v.(time.Time)
		d := now.Sub(s).Seconds()

		if d < 1 {
			count[0]++
		} else if d < 3 {
			count[1]++
		} else if d < 5 {
			count[2]++
		} else {
			count[3]++
		}
		return true
	})

	return count
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

	if atomic.LoadInt64(&maxResponseTime) < resTime {
		atomic.StoreInt64(&maxResponseTime, resTime)
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
	activeSpan.Store(span.spanId, span.startTime)
	addRealTimeSampledActiveSpan(span)
}

func dropSampledActiveSpan(span *span) {
	activeSpan.Delete(span.spanId)
	dropRealTimeSampledActiveSpan(span)
}

func addUnSampledActiveSpan(span *noopSpan) {
	activeSpan.Store(span.spanId, span.startTime)
	addRealTimeUnSampledActiveSpan(span)
}

func dropUnSampledActiveSpan(span *noopSpan) {
	activeSpan.Delete(span.spanId)
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
