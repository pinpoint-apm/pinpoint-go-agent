package pinpoint

import (
	"sync"
	"time"
)

const (
	urlStatBucketVersion   = 0
	urlStatBucketSize      = 8
	urlStatCollectInterval = 30 * time.Second
)

type urlStat struct {
	entry     *UrlStatEntry
	endTime   time.Time
	elapsed   int64
	statusErr int
}

// maxCompletedUrlStatSnapshots caps the completed queue. Four ticks is two
// minutes at the default 30s interval, matching the C++ agent's
// kMaxCompletedSnapshots (src/url_stat.h) and Java's AsyncQueueingUriStatStorage
// snapshotQueue capacity. Bounded because a stats stream that never recovers
// would otherwise grow the queue without limit; the oldest tick is the one
// worth losing first.
const maxCompletedUrlStatSnapshots = 4

// urlStatSnapshotDropLog reports completed ticks evicted at the queue cap. Its
// own throttle, not urlStatLimitLog's: sharing one would let whichever drop
// cause fires first silence the other for a whole window.
var urlStatSnapshotDropLog = logThrottle{src: "url stat"}

// urlStats owns this agent's url statistics: the tick its collect worker is
// filling, plus the ticks already closed and waiting for a send. One instance
// per agent, mirroring the C++ agent's UrlStats class - the snapshot used to be
// a package global reassigned per agent start, so a restart could swap it out
// from under the previous agent's worker and mix the two agents' stats.
//
// Only closed ticks are sent. The send interval is not aligned with the tick
// interval, so a send that took the tick in progress would split one tick's
// counts across two consecutive messages - the collector stores each part
// under the same (uri, tick) key, and the second write is not a merge. Java
// has the same split for the same reason and avoids it the same way, by
// polling a queue that only completed data enters
// (AsyncQueueingUriStatStorage.java:188-189).
type urlStats struct {
	config *Config
	mu     sync.Mutex
	// snapshot collects the tick in progress; completed holds the closed
	// ticks, oldest first, until a send drains them. Keeping each tick in its
	// own snapshot is also what makes Http.UrlStat.LimitSize a per-tick
	// capacity rather than a cap shared by however many ticks a stalled
	// stream has piled up.
	snapshot  *urlStatSnapshot
	completed []*urlStatSnapshot
}

func newUrlStats(config *Config) *urlStats {
	stats := &urlStats{config: config}
	stats.snapshot = stats.newSnapshot()
	return stats
}

func (stats *urlStats) newSnapshot() *urlStatSnapshot {
	return &urlStatSnapshot{
		urlMap: make(map[urlKey]*eachUrlStat),
		config: stats.config.load(),
	}
}

func (stats *urlStats) add(us *urlStat) {
	tick := us.endTime.Truncate(urlStatCollectInterval)

	stats.mu.Lock()
	defer stats.mu.Unlock()

	// Tick boundary: the first entry of a newer tick closes the one in
	// progress. Entry arrival drives this rather than a timer - entries carry
	// an end time of about "now", so the cut lands on the boundary anyway, and
	// a tick with no traffic has nothing to cut. Same structure as the C++
	// agent's UrlStats::addLocked (src/url_stat.cpp:100-121).
	//
	// Strictly newer only: a straggler for an already-closed tick must not cut
	// again. It lands in the current snapshot under its own tick key, which is
	// what the server aggregates by, and merge folds it back together on send.
	if len(stats.snapshot.urlMap) > 0 && tick.After(stats.snapshot.tick) {
		if len(stats.completed) >= maxCompletedUrlStatSnapshots {
			stats.completed = stats.completed[1:]
			urlStatSnapshotDropLog.warnf(
				"url stat snapshot queue overflow: dropping the oldest completed tick (max %d completed ticks); the stats stream is not draining",
				maxCompletedUrlStatSnapshots)
		}
		stats.completed = append(stats.completed, stats.snapshot)
		stats.snapshot = stats.newSnapshot()
	}

	stats.snapshot.add(us)
}

// takeSnapshot collects the closed ticks into one snapshot to send, leaving the
// tick in progress to keep filling. includeInProgress takes that one too and is
// set only on the shutdown path (agent.shutdownAgent): a tick nothing will ever
// close again would otherwise be stranded here, losing up to a full tick
// interval of traffic on every clean stop.
func (stats *urlStats) takeSnapshot(includeInProgress bool) *urlStatSnapshot {
	// The replacement is built before the lock is taken: allocating the map
	// and loading the config snapshot has nothing to do with the handover, and
	// doing it under the lock would stall the request-path adds for it.
	fresh := stats.newSnapshot()

	stats.mu.Lock()
	defer stats.mu.Unlock()

	taken := fresh
	if includeInProgress {
		taken, stats.snapshot = stats.snapshot, fresh
	}
	// Every retained tick goes out in one message: PAgentUriStat carries a
	// repeated eachUriStat and each entry stamps its own tick, so draining one
	// tick per send would take four send intervals to clear a backlog the
	// stream is finally able to accept.
	for _, completed := range stats.completed {
		taken.merge(completed)
	}
	stats.completed = nil
	return taken
}

type urlStatSnapshot struct {
	urlMap map[urlKey]*eachUrlStat
	config *configSnapshot
	count  int
	// tick is the newest tick accepted so far, and is the boundary
	// urlStats.add cuts on. Advanced by max rather than by last, so an
	// out-of-order straggler cannot move it back and cut the same tick twice.
	tick time.Time
}

func (snapshot *urlStatSnapshot) isEmpty() bool {
	return len(snapshot.urlMap) == 0
}

// merge moves other's entries into this snapshot, folding the histograms of any
// key present in both. Deliberately does not re-check Http.UrlStat.LimitSize:
// the limit caps each tick as it is collected, and merging is what assembles
// those ticks for one send.
func (snapshot *urlStatSnapshot) merge(other *urlStatSnapshot) {
	for key, stat := range other.urlMap {
		if e, ok := snapshot.urlMap[key]; ok {
			// The same url in the same tick reached both snapshots - a
			// straggler that arrived after its tick was closed. Fold, do not
			// drop.
			e.totalHistogram.merge(stat.totalHistogram)
			e.failedHistogram.merge(stat.failedHistogram)
			continue
		}
		snapshot.urlMap[key] = stat
		snapshot.count++
	}
	if snapshot.tick.Before(other.tick) {
		snapshot.tick = other.tick
	}
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

// urlStatLimitLog reports the dropped url patterns. At the limit every request
// carrying a new pattern reaches this site, so the warning is rate-limited and
// carries the count it held back - the C++ agent's QueueDropReporter reports
// the same way. It repeats rather than latching after one line (the way the
// span event overflow does): the limit being reached is a standing condition
// an operator has to size the limit for, not a one-off event.
var urlStatLimitLog = logThrottle{src: "url stat"}

func (snapshot *urlStatSnapshot) add(us *urlStat) {
	if us.endTime.IsZero() {
		return
	}

	var url string
	if snapshot.config.urlStatWithMethod && us.entry.Method != "" {
		url = us.entry.Method + " " + us.entry.Url
	} else {
		url = us.entry.Url
	}

	key := urlKey{url, us.endTime.Truncate(urlStatCollectInterval)}

	e, ok := snapshot.urlMap[key]
	if !ok {
		if snapshot.count >= snapshot.config.urlStatLimitSize {
			urlStatLimitLog.warnf(
				"url stat limit reached: dropping %q and every other new url pattern (max %d distinct urls per snapshot)",
				url, snapshot.config.urlStatLimitSize)
			return
		}
		e = newEachUrlStat(url, key.tick)
		snapshot.urlMap[key] = e
		snapshot.count++
	}

	// Only accepted entries advance the cut boundary: an entry the limit
	// dropped contributed nothing to this snapshot, so closing the tick on it
	// would leave its counts nowhere.
	if snapshot.tick.Before(key.tick) {
		snapshot.tick = key.tick
	}

	e.totalHistogram.add(us.elapsed)
	if us.statusErr != 0 {
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

func (hg *urlStatHistogram) merge(other *urlStatHistogram) {
	hg.total += other.total
	if hg.max < other.max {
		hg.max = other.max
	}
	for i, count := range other.histogram {
		hg.histogram[i] += count
	}
}

func (hg *urlStatHistogram) isEmpty() bool {
	for _, count := range hg.histogram {
		if count != 0 {
			return false
		}
	}
	return true
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
