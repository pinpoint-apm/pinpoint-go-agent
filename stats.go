package pinpoint

import (
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/process"
)

type inspectorStats struct {
	sampleTime   time.Time
	interval     int64
	cpuUserTime  float64
	cpuSysTime   float64
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
	lastCpuStat     *cpu.TimesStat
	lastMemStat     runtime.MemStats
	lastCollectTime time.Time
	statsMux        sync.Mutex

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
	}

	lastCpuStat = getCpuUsage()
	runtime.ReadMemStats(&lastMemStat)
	lastCollectTime = time.Now()

	activeSpan = sync.Map{}
}

func getCpuUsage() *cpu.TimesStat {
	if proc != nil {
		cpuStat, _ := proc.Times()
		return cpuStat
	}
	return nil
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

func getStats() *inspectorStats {
	statsMux.Lock()
	defer statsMux.Unlock()

	now := time.Now()
	cpuStat := getCpuUsage()
	numFd := getNumFD()

	var memStat runtime.MemStats
	runtime.ReadMemStats(&memStat)
	elapsed := now.Sub(lastCollectTime).Seconds()

	stats := inspectorStats{
		sampleTime:   now,
		interval:     int64(elapsed) * 1000,
		cpuUserTime:  cpuUserPercent(cpuStat, lastCpuStat, elapsed),
		cpuSysTime:   cpuSystemPercent(cpuStat, lastCpuStat, elapsed),
		heapUsed:     int64(memStat.HeapInuse),
		heapMax:      int64(memStat.HeapSys),
		nonHeapUsed:  int64(memStat.StackInuse),
		nonHeapMax:   int64(memStat.StackSys),
		gcNum:        int64(memStat.NumGC - lastMemStat.NumGC),
		gcTime:       int64(memStat.PauseTotalNs-lastMemStat.PauseTotalNs) / int64(time.Millisecond),
		numOpenFD:    int64(numFd),
		numThreads:   int64(getNumThreads()),
		responseAvg:  calcResponseAvg(),
		responseMax:  maxResponseTime,
		sampleNew:    sampleNew / int64(elapsed),
		sampleCont:   sampleCont / int64(elapsed),
		unSampleNew:  unSampleNew / int64(elapsed),
		unSampleCont: unSampleCont / int64(elapsed),
		skipNew:      skipNew / int64(elapsed),
		skipCont:     skipCont / int64(elapsed),
		activeSpan:   activeSpanCount(now),
	}

	lastCpuStat = cpuStat
	lastMemStat = memStat
	lastCollectTime = now
	resetResponseTime()

	return &stats
}

func cpuUserPercent(cur *cpu.TimesStat, prev *cpu.TimesStat, elapsed float64) float64 {
	if cur == nil || prev == nil {
		return 0
	}
	return cpuUtilization(cur.User, prev.User, elapsed)
}

func cpuSystemPercent(cur *cpu.TimesStat, prev *cpu.TimesStat, elapsed float64) float64 {
	if cur == nil || prev == nil {
		return 0
	}
	return cpuUtilization(cur.System, prev.System, elapsed)
}

func cpuUtilization(cur float64, prev float64, elapsed float64) float64 {
	return (cur - prev) / (elapsed * float64(runtime.NumCPU())) * 100
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

func (agent *agent) sendStatsWorker(config *Config) {
	Log("stats").Info("stat goroutine start")
	defer agent.wg.Done()

	initStats()
	resetResponseTime()

	statStream := agent.statGrpc.newStatStreamWithRetry()
	interval := time.Duration(config.Int(cfgStatCollectInterval)) * time.Millisecond
	time.Sleep(interval)
	cfgBatchCount := config.Int(cfgStatBatchCount)
	collected := make([]*inspectorStats, cfgBatchCount)
	batch := 0

	for agent.enable {
		collected[batch] = getStats()
		batch++

		if batch == cfgBatchCount {
			err := statStream.sendStats(collected)
			if err != nil {
				if err != io.EOF {
					Log("stats").Errorf("fail to send stats - %v", err)
				}

				statStream.close()
				statStream = agent.statGrpc.newStatStreamWithRetry()
			}
			batch = 0
		}

		time.Sleep(interval)
	}

	statStream.close()
	Log("stats").Info("stat goroutine finish")
}

func collectResponseTime(resTime int64) {
	statsMux.Lock()
	defer statsMux.Unlock()

	accResponseTime += resTime
	requestCount++

	if maxResponseTime < resTime {
		maxResponseTime = resTime
	}
}

func resetResponseTime() {
	accResponseTime = 0
	requestCount = 0
	maxResponseTime = 0
	sampleNew = 0
	unSampleNew = 0
	sampleCont = 0
	unSampleCont = 0
	skipNew = 0
	skipCont = 0
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
	sampleNew++
}
func incrUnSampleNew() {
	unSampleNew++
}
func incrSampleCont() {
	sampleCont++
}
func incrUnSampleCont() {
	unSampleCont++
}
func incrSkipNew() {
	skipNew++
}
func incrSkipCont() {
	skipCont++
}
