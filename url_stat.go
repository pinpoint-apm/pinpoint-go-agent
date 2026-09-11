package pinpoint

import (
	"slices"
	"sync"
	"time"
)

const (
	urlStatBucketVersion   = 0
	urlStatBucketSize      = 8
	urlStatCollectInterval = 30 * time.Second
)

// urlStatUnknown is the stand-in URI recorded when a span collects URL stats
// agent's URL_STAT_UNKNOWN (src/url_stat.h) so a mixed deployment aggregates
// its "no URI recorded" traffic under one server-side key.
const urlStatUnknown = "/NULL"

type urlStat struct {
	entry     *UrlStatEntry
	endTime   time.Time
	elapsed   int64
	statusErr int
}

// maxCompletedUrlStatSnapshots caps the completed queue. Four ticks is two
// snapshotQueue capacity. Bounded because a stats stream that never recovers
// would otherwise grow the queue without limit; the oldest tick is the one
// worth losing first.
// maxCompletedUrlStatSnapshots is the number of closed ticks kept while the
// stat stream is not draining. Java's AsyncQueueingUriStatStorage
// .addCompletedData compares snapshotQueue.size() > SNAPSHOT_LIMIT (4) before
// offering, so it retains five; the C++ agent's kMaxCompletedSnapshots is the
// same five.
const maxCompletedUrlStatSnapshots = 5

// urlStatSnapshotDropLog reports completed ticks evicted at the queue cap. Its
// own throttle, not urlStatLimitLog's: sharing one would let whichever drop
// cause fires first silence the other for a whole window.
var urlStatSnapshotDropLog = logThrottle{src: "url stat"}

// urlStatNow is the clock urlStats reads to tell whether the tick in progress
// is past its window. A variable so tests can place the boundary where they
// need it instead of waiting on the wall clock.
var urlStatNow = time.Now

// urlStats owns one agent's URL statistics: the tick its collect worker is
// filling, plus the ticks already closed and waiting for a send. Keeping this
// state per agent prevents a restart from mixing workers or snapshots.
//
// A tick is sent only once it is over, which happens in one of two ways: the
// arrival of an entry belonging to a newer tick closes it, or - when traffic
// stops and no such entry ever arrives - its own window elapses on the clock.
// The send interval is not aligned with the tick interval, so a send that took
// a tick still inside its window would split that tick's counts across two
// consecutive messages - the collector stores each part under the same
// for the same reason and avoids it the same way, by polling a queue that only
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
	// Ticks handed to the sender (or evicted) cannot be reopened: a later
	// partial write would replace their existing collector-side counts.
	retiredThrough time.Time
	// completedWake is signalled (without blocking, capacity 1) each time a
	// tick lands on completed, so sendUrlStatWorker sends it at once instead
	// of on its next timer tick. A tick that is over has nothing left to
	// join it, so sending it now is not the split the arrival cut avoids.
	completedWake chan struct{}
}

func newUrlStats(config *Config) *urlStats {
	stats := &urlStats{config: config, completedWake: make(chan struct{}, 1)}
	stats.snapshot = stats.newSnapshot()
	return stats
}

// completedTick is readable once a tick has been closed onto the completed
// queue since the previous read. Several closes coalesce into one wakeup;
// takeSnapshot drains the whole queue anyway.
func (stats *urlStats) completedTick() <-chan struct{} {
	return stats.completedWake
}

func (stats *urlStats) newSnapshot() *urlStatSnapshot {
	return &urlStatSnapshot{
		urlMap: make(map[urlKey]*eachUrlStat),
		config: stats.config.load(),
	}
}

func (stats *urlStats) add(us *urlStat) {
	if us.endTime.IsZero() {
		return
	}
	tick := us.endTime.Truncate(urlStatCollectInterval)

	stats.mu.Lock()
	defer stats.mu.Unlock()
	if !stats.retiredThrough.IsZero() && !tick.After(stats.retiredThrough) {
		urlStatLateLog.warnf("dropping late url stat for retired tick %s", tick)
		return
	}
	for _, completed := range stats.completed {
		if tick.Equal(completed.tick) {
			completed.add(us)
			return
		}
	}
	if !stats.snapshot.isEmpty() && tick.Before(stats.snapshot.tick) {
		late := stats.newSnapshot()
		late.add(us)
		stats.completeLocked(late)
		return
	}

	// Tick boundary: the first entry of a newer tick closes the one in
	// progress. Entries carry an end time of about "now", so the cut lands on
	//
	// This is the cut of an agent under traffic, and it cannot be the only one:
	// the last tick of a burst has no newer entry coming to close it. Once its
	// window is over takeSnapshot closes it instead.
	//
	// Stragglers were routed to their own completed tick above, so every
	// snapshot contains exactly one tick, including during ordinary sends.
	if len(stats.snapshot.urlMap) > 0 && tick.After(stats.snapshot.tick) {
		stats.completeLocked(stats.snapshot)
		stats.snapshot = stats.newSnapshot()
	}

	stats.snapshot.add(us)
}

var urlStatLateLog = logThrottle{src: "url stat"}

// Caller holds mu. Sort the small queue because a previously unseen older
// tick may arrive after the current tick. Eviction still drops the oldest.
func (stats *urlStats) completeLocked(snapshot *urlStatSnapshot) {
	stats.completed = append(stats.completed, snapshot)
	slices.SortFunc(stats.completed, func(a, b *urlStatSnapshot) int { return a.tick.Compare(b.tick) })
	if len(stats.completed) > maxCompletedUrlStatSnapshots {
		stats.retiredThrough = stats.completed[0].tick
		stats.completed[0] = nil
		stats.completed = stats.completed[1:]
		urlStatSnapshotDropLog.warnf(
			"url stat snapshot queue overflow: dropping the oldest completed tick (max %d completed ticks); the stats stream is not draining",
			maxCompletedUrlStatSnapshots)
	}
	select {
	case stats.completedWake <- struct{}{}:
	default: // a wakeup is already pending
	}
}

// takeSnapshot collects the ticks that are over into one snapshot to send: the
// ones urlStats.add has already closed, plus the tick in progress once its own
// window has elapsed. This also closes the final tick when traffic stops.
// Entries arriving after the handover are dropped by add: sending them later
// under the same tick would replace the counts already handed to the sender.
//
// includeInProgress takes the tick in progress whatever its window, and is set
// only on the shutdown path (agent.shutdownAgent): a tick cut short by the stop
// would otherwise be stranded here, losing up to a full tick interval of
// traffic on every clean stop.
func (stats *urlStats) takeSnapshot(includeInProgress bool) *urlStatSnapshot {
	// The replacement is built before the lock is taken: allocating the map
	// and loading the config snapshot has nothing to do with the handover, and
	// doing it under the lock would stall the request-path adds for it.
	fresh := stats.newSnapshot()
	now := urlStatNow()

	stats.mu.Lock()
	defer stats.mu.Unlock()

	taken := fresh
	if includeInProgress || stats.tickIsOverLocked(now) {
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
	if !taken.isEmpty() && taken.tick.After(stats.retiredThrough) {
		stats.retiredThrough = taken.tick
	}
	return taken
}

// tickIsOverLocked reports whether the tick in progress holds anything and the
// window it belongs to has already passed. Callers hold stats.mu.
func (stats *urlStats) tickIsOverLocked(now time.Time) bool {
	return !stats.snapshot.isEmpty() && now.After(stats.snapshot.tick.Add(urlStatCollectInterval))
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

// urlKey identifies a snapshot entry. method is kept apart from url rather
// than joined into it: the key is built for every request the collect worker
// receives, and "METHOD url" was a string allocation per request under
// Http.UrlStat.WithMethod even for a hit on an existing entry. The joined
// display text is built once, in newEachUrlStat, for a new key only.
type urlKey struct {
	method string
	url    string
	tick   time.Time
}

// displayUrl is the pattern as it is reported: "METHOD url" when method is
// set, url alone otherwise.
func (k urlKey) displayUrl() string {
	if k.method == "" {
		return k.url
	}
	return k.method + " " + k.url
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
// the same way. It repeats rather than latching after one line (the way the
// span event overflow does): the limit being reached is a standing condition
// an operator has to size the limit for, not a one-off event.
var urlStatLimitLog = logThrottle{src: "url stat"}

func (snapshot *urlStatSnapshot) add(us *urlStat) {
	if us.endTime.IsZero() {
		return
	}

	key := urlKey{url: us.entry.Url, tick: us.endTime.Truncate(urlStatCollectInterval)}
	if snapshot.config.urlStatWithMethod {
		key.method = us.entry.Method
	}

	e, ok := snapshot.urlMap[key]
	if !ok {
		if snapshot.count >= snapshot.config.urlStatLimitSize {
			urlStatLimitLog.warnf(
				"url stat limit reached: dropping %q and every other new url pattern (max %d distinct urls per snapshot)",
				key.displayUrl(), snapshot.config.urlStatLimitSize)
			return
		}
		e = newEachUrlStat(key.displayUrl(), key.tick)
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
	// Wall-clock elapsed can go negative across an NTP step; unclamped it
	// would decrement total and skew the average (the C++ UrlStatHistogram
	// ::add clamps the same way, at the sink, whatever the producer did).
	elapsed = max(elapsed, 0)
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
