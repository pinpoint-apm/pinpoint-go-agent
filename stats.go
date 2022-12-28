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

	clock = newTickClock(30 * time.Second)
	urlSnapshot = newUrlStatSnapshot()
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
		sampleNew:    sampleNew,
		sampleCont:   sampleCont,
		unSampleNew:  unSampleNew,
		unSampleCont: unSampleCont,
		skipNew:      skipNew,
		skipCont:     skipCont,
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

func (agent *agent) collectAgentStatWorker(config *Config) {
	Log("stats").Infof("start collect agent stat goroutine")
	defer agent.wg.Done()

	initStats()
	resetResponseTime()

	interval := time.Duration(config.Int(CfgStatCollectInterval)) * time.Millisecond
	time.Sleep(interval)
	cfgBatchCount := config.Int(CfgStatBatchCount)
	collected := make([]*inspectorStats, cfgBatchCount)
	batch := 0

	for agent.enable {
		collected[batch] = getStats()
		batch++

		if batch == cfgBatchCount {
			agent.enqueueStat(makePAgentStatBatch(collected))
			batch = 0
		}
		time.Sleep(interval)
	}
	Log("stats").Infof("end collect agent stat goroutine")
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

const (
	urlStatusSuccess     = 1
	urlStatusFail        = 2
	urlStatBucketVersion = 0
	urlStatBucketSize    = 8
)

type urlStat struct {
	url     string
	status  int
	endTime time.Time
	elapsed int64
}

type eachUrlStat struct {
	url             string
	totalHistogram  urlStatHistogram
	failedHistogram urlStatHistogram
	tickTime        time.Time
}

type urlStatHistogram struct {
	total     int64
	max       int64
	histogram []int32
}

type urlKey struct {
	url  string
	tick time.Time
}

type urlStatSnapshot struct {
	urlMap map[urlKey]*eachUrlStat
}

func urlStatStatus(status int) int {
	if status/100 < 4 {
		return urlStatusSuccess
	} else {
		return urlStatusFail
	}
}

func (hg *urlStatHistogram) add(elapsed int64) {
	hg.total += elapsed
	if hg.max < elapsed {
		hg.max = elapsed
	}
	hg.histogram[getBucket(elapsed)]++
}

func getBucket(elapsed int64) int {
	if elapsed < 100 {
		return 0
	} else if elapsed < 300 {
		return 1
	} else if elapsed < 500 {
		return 2
	} else if elapsed < 1000 {
		return 3
	} else if elapsed < 3000 {
		return 4
	} else if elapsed < 5000 {
		return 5
	} else if elapsed < 8000 {
		return 6
	} else {
		return 7
	}
}

var (
	clock           tickClock
	urlSnapshot     *urlStatSnapshot
	urlSnapshotLock sync.Mutex
)

func newUrlStatSnapshot() *urlStatSnapshot {
	return &urlStatSnapshot{
		urlMap: make(map[urlKey]*eachUrlStat, 0),
	}
}

func currentUrlStatSnapshot() *urlStatSnapshot {
	urlSnapshotLock.Lock()
	defer urlSnapshotLock.Unlock()
	return urlSnapshot
}

func takeUrlStatSnapshot() *urlStatSnapshot {
	urlSnapshotLock.Lock()
	defer urlSnapshotLock.Unlock()

	oldSnapshot := urlSnapshot
	urlSnapshot = newUrlStatSnapshot()
	return oldSnapshot
}

func (snapshot *urlStatSnapshot) add(us *urlStat) {
	key := urlKey{us.url, clock.tick(us.endTime)}

	e, ok := snapshot.urlMap[key]
	if !ok {
		e = newEachUrlStat(us.url, key.tick)
		snapshot.urlMap[key] = e
	}

	e.totalHistogram.add(us.elapsed)
	if urlStatStatus(us.status) == urlStatusFail {
		e.failedHistogram.add(us.elapsed)
	}
}

func newEachUrlStat(url string, tick time.Time) *eachUrlStat {
	return &eachUrlStat{
		url: url,
		totalHistogram: urlStatHistogram{
			histogram: make([]int32, urlStatBucketSize),
		},
		failedHistogram: urlStatHistogram{
			histogram: make([]int32, urlStatBucketSize),
		},
		tickTime: tick,
	}
}

type tickClock struct {
	interval time.Duration
}

func newTickClock(interval time.Duration) tickClock {
	return tickClock{interval}
}

func (t tickClock) tick(tm time.Time) time.Time {
	return tm.Truncate(t.interval)
}
