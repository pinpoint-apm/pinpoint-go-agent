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

func (agent *agent) initUrlStat() {
	clock = newTickClock(urlStatCollectInterval)
	urlSnapshot = agent.newUrlStatSnapshot()
}

type urlStat struct {
	entry   *UrlStatEntry
	endTime time.Time
	elapsed int64
}

type urlStatSnapshot struct {
	urlMap map[urlKey]*eachUrlStat
	config *Config
	count  int
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

func (agent *agent) newUrlStatSnapshot() *urlStatSnapshot {
	return &urlStatSnapshot{
		urlMap: make(map[urlKey]*eachUrlStat, 0),
		config: agent.config,
	}
}

func (agent *agent) addUrlStatSnapshot(us *urlStat) {
	urlSnapshotLock.Lock()
	defer urlSnapshotLock.Unlock()

	urlSnapshot.add(us)
}

func (agent *agent) takeUrlStatSnapshot() *urlStatSnapshot {
	urlSnapshotLock.Lock()
	defer urlSnapshotLock.Unlock()

	oldSnapshot := urlSnapshot
	urlSnapshot = agent.newUrlStatSnapshot()
	return oldSnapshot
}

func (snapshot *urlStatSnapshot) add(us *urlStat) {
	var url string
	if snapshot.config.urlStatWithMethod && us.entry.Method != "" {
		url = us.entry.Method + " " + us.entry.Url
	} else {
		url = us.entry.Url
	}

	key := urlKey{url, clock.tick(us.endTime)}

	e, ok := snapshot.urlMap[key]
	if !ok {
		if snapshot.count >= snapshot.config.urlStatLimitSize {
			return
		}
		e = newEachUrlStat(url, key.tick)
		snapshot.urlMap[key] = e
		snapshot.count++
	}

	e.totalHistogram.add(us.elapsed)
	if urlStatStatus(us.entry.Status) == urlStatusFail {
		e.failedHistogram.add(us.elapsed)
	}
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
