package pinpoint

import (
	"sync"
	"time"
)

const (
	urlStatusSuccess       = 1
	urlStatusFail          = 2
	urlStatBucketVersion   = 0
	urlStatBucketSize      = 8
	urlStatCollectInterval = 30 * time.Second
)

var (
	clock           tickClock
	urlSnapshot     *urlStatSnapshot
	urlSnapshotLock sync.Mutex
)

func initUrlStat() {
	clock = newTickClock(urlStatCollectInterval)
	urlSnapshot = newUrlStatSnapshot()
}

type urlStat struct {
	url     string
	status  int
	endTime time.Time
	elapsed int64
}

type urlStatSnapshot struct {
	urlMap      map[urlKey]*eachUrlStat
	maxCapacity int
	count       int
}

type urlKey struct {
	url  string
	tick time.Time
}

type eachUrlStat struct {
	url             string
	totalHistogram  *urlStatHistogram
	failedHistogram *urlStatHistogram
	tickTime        time.Time
}

type urlStatHistogram struct {
	total     int64
	max       int64
	histogram []int32
}

type tickClock struct {
	interval time.Duration
}

func newUrlStatSnapshot() *urlStatSnapshot {
	return &urlStatSnapshot{
		urlMap:      make(map[urlKey]*eachUrlStat, 0),
		maxCapacity: GetConfig().Int(CfgHttpUrlStatLimitSize),
	}
}

func (snapshot *urlStatSnapshot) add(us *urlStat) {
	key := urlKey{us.url, clock.tick(us.endTime)}

	e, ok := snapshot.urlMap[key]
	if !ok {
		if snapshot.count >= snapshot.maxCapacity {
			return
		}
		e = newEachUrlStat(us.url, key.tick)
		snapshot.urlMap[key] = e
		snapshot.count++
	}

	e.totalHistogram.add(us.elapsed)
	if urlStatStatus(us.status) == urlStatusFail {
		e.failedHistogram.add(us.elapsed)
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

func newEachUrlStat(url string, tick time.Time) *eachUrlStat {
	return &eachUrlStat{
		url:             url,
		totalHistogram:  newStatHistogram(),
		failedHistogram: newStatHistogram(),
		tickTime:        tick,
	}
}

func newStatHistogram() *urlStatHistogram {
	return &urlStatHistogram{
		histogram: make([]int32, urlStatBucketSize),
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

func newTickClock(interval time.Duration) tickClock {
	return tickClock{interval}
}

func (t tickClock) tick(tm time.Time) time.Time {
	return tm.Truncate(t.interval)
}

func urlStatStatus(status int) int {
	if status/100 < 4 {
		return urlStatusSuccess
	} else {
		return urlStatusFail
	}
}
