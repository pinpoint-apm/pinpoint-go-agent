package pinpoint

import (
	"runtime"
	"sync"
	"syscall"
	"time"
)

type inspectorStats struct {
	sampleTime   time.Time
	cpuUserTime  float64
	cpuSysTime   float64
	goroutineNum int
	heapAlloc    int64
	heapMax      int64
	nonHeapAlloc int64
	nonHeapMax   int64
	gcNum        int64
	gcTime       int64
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

var lastRusage syscall.Rusage
var lastMemStats runtime.MemStats
var lastCollectTime time.Time
var statsMux sync.Mutex

var accResponseTime int64
var maxResponseTime int64
var requestCount int64

var sampleNew int64
var unsampleNew int64
var sampleCont int64
var unsampleCont int64
var skipNew int64
var skipCont int64

var activeSpan sync.Map

type activeSpanInfo struct {
	startTime time.Time
	txId      TransactionId
}

func initStats() {
	err := syscall.Getrusage(syscall.RUSAGE_SELF, &lastRusage)
	if err != nil {
		log("stats").Error(err)
	}

	runtime.ReadMemStats(&lastMemStats)
	lastCollectTime = time.Now()

	activeSpan = sync.Map{}
}

func getStats() *inspectorStats {
	statsMux.Lock()
	defer statsMux.Unlock()

	now := time.Now()

	var rsg syscall.Rusage
	err := syscall.Getrusage(syscall.RUSAGE_SELF, &rsg)
	if err != nil {
		log("stats").Error(err)
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	dur := now.Sub(lastCollectTime)

	activeSpanCount := []int32{0, 0, 0, 0}
	activeSpan.Range(func(k, v interface{}) bool {
		s := v.(activeSpanInfo)
		d := now.Sub(s.startTime).Seconds()
		log("stats").Debug("getStats: ", now, s.startTime, d)

		if d < 1 {
			activeSpanCount[0]++
		} else if d < 3 {
			activeSpanCount[1]++
		} else if d < 5 {
			activeSpanCount[2]++
		} else {
			activeSpanCount[3]++
		}
		return true
	})

	stats := inspectorStats{
		sampleTime:   now,
		cpuUserTime:  cpuUtilization(rsg.Utime, lastRusage.Utime, dur),
		cpuSysTime:   cpuUtilization(rsg.Stime, lastRusage.Stime, dur),
		goroutineNum: runtime.NumGoroutine(),
		heapAlloc:    int64(mem.HeapAlloc),
		heapMax:      int64(mem.Sys),
		nonHeapAlloc: int64(mem.StackInuse),
		nonHeapMax:   int64(mem.StackSys),
		gcNum:        int64(mem.NumGC - lastMemStats.NumGC),
		gcTime:       int64(mem.PauseTotalNs-lastMemStats.PauseTotalNs) / int64(time.Millisecond),
		responseAvg:  calcResponseAvg(),
		responseMax:  maxResponseTime,
		sampleNew:    sampleNew / int64(dur.Seconds()),
		sampleCont:   sampleCont / int64(dur.Seconds()),
		unSampleNew:  unsampleNew / int64(dur.Seconds()),
		unSampleCont: unsampleCont / int64(dur.Seconds()),
		skipNew:      skipNew / int64(dur.Seconds()),
		skipCont:     skipCont / int64(dur.Seconds()),
		activeSpan:   activeSpanCount,
	}

	lastRusage = rsg
	lastMemStats = mem
	lastCollectTime = now
	resetResponseTime()

	return &stats
}

func cpuTime(timeval syscall.Timeval) time.Time {
	return time.Unix(timeval.Sec, int64(timeval.Usec)*1000)
}

func cpuUtilization(cur syscall.Timeval, prev syscall.Timeval, dur time.Duration) float64 {
	return float64(toMicroseconds(cpuTime(cur).Sub(cpuTime(prev)))) / float64(toMicroseconds(dur)) * 100 / float64(runtime.NumCPU())
}

func calcResponseAvg() int64 {
	if requestCount > 0 {
		return accResponseTime / requestCount
	}

	return 0
}

func (agent *agent) sendStatsWorker() {
	log("stats").Info("stat goroutine start")
	defer agent.wg.Done()

	initStats()
	resetResponseTime()

	sleepTime := time.Duration(agent.config.Stat.CollectInterval) * time.Millisecond
	time.Sleep(sleepTime)

	statStream := agent.statGrpc.newStatStreamWithRetry()
	collected := make([]*inspectorStats, agent.config.Stat.BatchCount)
	batch := 0

	for true {
		if !agent.enable {
			break
		}

		collected[batch] = getStats()
		batch++

		if batch == agent.config.Stat.BatchCount {
			err := statStream.sendStats(collected)
			if err != nil {
				log("stats").Errorf("fail to sendStats(): %v", err)
				statStream.close()
				statStream = agent.statGrpc.newStatStreamWithRetry()
			}
			batch = 0
		}

		time.Sleep(sleepTime)
	}

	statStream.close()
	log("stats").Info("stat goroutine finish")
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
	unsampleNew = 0
	sampleCont = 0
	unsampleCont = 0
	skipNew = 0
	skipCont = 0
}

func addActiveSpan(span *span) {
	span.goroutineId = curGoroutineID()
	s := activeSpanInfo{span.startTime, span.txId}
	activeSpan.Store(span.goroutineId, s)
	log("stats").Debug("addActiveSpan: ", span.goroutineId, s)
}

func dropActiveSpan(span *span) {
	activeSpan.Delete(span.goroutineId)
	log("stats").Debug("dropActiveSpan: ", span.goroutineId)
}

func getActiveSpanCount(now time.Time) []int32 {
	activeSpanCount := []int32{0, 0, 0, 0}
	activeSpan.Range(func(k, v interface{}) bool {
		s := v.(activeSpanInfo)
		d := now.Sub(s.startTime).Seconds()

		if d < 1 {
			activeSpanCount[0]++
		} else if d < 3 {
			activeSpanCount[1]++
		} else if d < 5 {
			activeSpanCount[2]++
		} else {
			activeSpanCount[3]++
		}
		return true
	})

	return activeSpanCount
}

func incrSampleNew() {
	sampleNew++
}
func incrUnsampleNew() {
	unsampleNew++
}
func incrSampleCont() {
	sampleCont++
}
func incrUnsampleCont() {
	unsampleCont++
}
func incrSkipNew() {
	skipNew++
}
func incrSkipCont() {
	skipCont++
}
