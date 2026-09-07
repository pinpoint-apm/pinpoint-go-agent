package pinpoint

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_urlStatBucketLayoutFromJavaAgent(t *testing.T) {
	assert.Equal(t, 0, urlStatBucketVersion)
	assert.Equal(t, 8, urlStatBucketSize)
	assert.Equal(t, 0, getBucket(1))

	tests := []struct {
		elapsed int64
		bucket  int
	}{
		{elapsed: 0, bucket: 0},
		{elapsed: 99, bucket: 0},
		{elapsed: 100, bucket: 1},
		{elapsed: 299, bucket: 1},
		{elapsed: 300, bucket: 2},
		{elapsed: 499, bucket: 2},
		{elapsed: 500, bucket: 3},
		{elapsed: 999, bucket: 3},
		{elapsed: 1000, bucket: 4},
		{elapsed: 2999, bucket: 4},
		{elapsed: 3000, bucket: 5},
		{elapsed: 4999, bucket: 5},
		{elapsed: 5000, bucket: 6},
		{elapsed: 7999, bucket: 6},
		{elapsed: 8000, bucket: 7},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.bucket, getBucket(tt.elapsed), "elapsed=%d", tt.elapsed)
	}
}

func Test_urlStatSnapshotKeepsAggregatingExistingPatternAtCapacity(t *testing.T) {
	snapshot, endTime := newUrlStatTestSnapshot(1, false)

	addTestUrlStat(snapshot, "/test", "", 0, 10, endTime)
	addTestUrlStat(snapshot, "/test", "", 0, 20, endTime)
	addTestUrlStat(snapshot, "/other", "", 0, 30, endTime)

	stat := findEachUrlStat(t, snapshot, "/test", endTime)
	assert.Len(t, snapshot.urlMap, 1)
	assert.Equal(t, int32(2), histogramCount(stat.totalHistogram))
	assert.Equal(t, int64(30), stat.totalHistogram.total)
	assert.Equal(t, int64(20), stat.totalHistogram.max)
}

func Test_urlStatSnapshotAddPatternFromJavaAgent(t *testing.T) {
	snapshot, endTime := newUrlStatTestSnapshot(10, false)

	addTestUrlStat(snapshot, "pattern1", "", 0, 10, endTime)
	addTestUrlStat(snapshot, "pattern1", "", 0, 20, endTime)
	addTestUrlStat(snapshot, "pattern2", "", 0, 30, endTime)

	assert.Len(t, snapshot.urlMap, 2)

	pattern1 := findEachUrlStat(t, snapshot, "pattern1", endTime)
	assert.Equal(t, int32(2), histogramCount(pattern1.totalHistogram))
	assert.Equal(t, int64(30), pattern1.totalHistogram.total)
	assert.Equal(t, int64(20), pattern1.totalHistogram.max)
}

func Test_urlStatSnapshotTransformsMethodLikeJavaAgent(t *testing.T) {
	snapshot, endTime := newUrlStatTestSnapshot(10, true)

	addTestUrlStat(snapshot, "/orders/{id}", "GET", 0, 90, endTime)
	addTestUrlStat(snapshot, "/orders/{id}", "GET", 1, 110, endTime)
	addTestUrlStat(snapshot, "/orders/{id}", "", 1, 300, endTime)

	assert.Len(t, snapshot.urlMap, 2)

	withMethod := findEachUrlStat(t, snapshot, "GET /orders/{id}", endTime)
	assert.Equal(t, int32(2), histogramCount(withMethod.totalHistogram))
	assert.Equal(t, int64(200), withMethod.totalHistogram.total)
	assert.Equal(t, int64(110), withMethod.totalHistogram.max)
	assert.Equal(t, int32(1), withMethod.totalHistogram.histogram[0])
	assert.Equal(t, int32(1), withMethod.totalHistogram.histogram[1])
	assert.Equal(t, int32(1), histogramCount(withMethod.failedHistogram))
	assert.Equal(t, int64(110), withMethod.failedHistogram.total)

	withoutMethod := findEachUrlStat(t, snapshot, "/orders/{id}", endTime)
	assert.Equal(t, int32(1), histogramCount(withoutMethod.totalHistogram))
	assert.Equal(t, int32(1), histogramCount(withoutMethod.failedHistogram))
	assert.Equal(t, int64(300), withoutMethod.failedHistogram.total)
}

func Test_makePAgentUriStatConvertsLikeJavaAgentMapper(t *testing.T) {
	snapshot, endTime := newUrlStatTestSnapshot(10, false)

	samples := []urlStatSample{
		{url: "/index.html", statusErr: 0, elapsed: 50},
		{url: "/index.html", statusErr: 1, elapsed: 150},
		{url: "/main", statusErr: 0, elapsed: 350},
		{url: "/main", statusErr: 0, elapsed: 900},
		{url: "/error", statusErr: 1, elapsed: 1200},
		{url: "/error", statusErr: 1, elapsed: 6000},
	}
	expected := makeExpectedUrlStats(samples, endTime)
	for _, sample := range samples {
		addTestUrlStat(snapshot, sample.url, "", sample.statusErr, sample.elapsed, endTime)
	}

	agentUriStat := makePAgentUriStat(snapshot).GetAgentUriStat()

	assert.Equal(t, int32(urlStatBucketVersion), agentUriStat.GetBucketVersion())
	assert.Len(t, agentUriStat.GetEachUriStat(), len(expected))
	for _, actual := range agentUriStat.GetEachUriStat() {
		expectedStat, ok := expected[actual.GetUri()]
		assert.True(t, ok, "unexpected uri=%s", actual.GetUri())
		if !ok {
			continue
		}

		assert.Equal(t, expectedStat.timestamp, actual.GetTimestamp())
		assertUriHistogram(t, expectedStat.total, actual.GetTotalHistogram())
		assertUriHistogram(t, expectedStat.failed, actual.GetFailedHistogram())
	}
}

func Test_makePUriHistogramReturnsEmptyForNoSamplesLikeJavaAgentMapper(t *testing.T) {
	histogram := makePUriHistogram(newStatHistogram())

	assert.Equal(t, int64(0), histogram.GetTotal())
	assert.Equal(t, int64(0), histogram.GetMax())
	assert.Empty(t, histogram.GetHistogram())
}

func Test_urlStatSnapshotIgnoresZeroEndTimeLikeJavaAgent(t *testing.T) {
	snapshot, _ := newUrlStatTestSnapshot(10, false)

	addTestUrlStat(snapshot, "/zero", "", 0, 10, time.Time{})

	assert.Empty(t, snapshot.urlMap)
}

func Test_urlStatFailureUsesStatusFailureOnly(t *testing.T) {
	snapshot, endTime := newUrlStatTestSnapshot(10, false)

	addTestUrlStat(snapshot, "/server-error-status", "", 0, 500, endTime)
	addTestUrlStat(snapshot, "/status-error", "", 1, 200, endTime)

	serverErrorStatus := findEachUrlStat(t, snapshot, "/server-error-status", endTime)
	assert.Equal(t, int32(1), histogramCount(serverErrorStatus.totalHistogram))
	assert.Equal(t, int32(0), histogramCount(serverErrorStatus.failedHistogram))

	statusError := findEachUrlStat(t, snapshot, "/status-error", endTime)
	assert.Equal(t, int32(1), histogramCount(statusError.totalHistogram))
	assert.Equal(t, int32(1), histogramCount(statusError.failedHistogram))
}

func Test_spanStatusErrIsSetOnlyBySetFailure(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))

	span.SetError(errors.New("application error"))
	assert.Equal(t, int32(ErrorCategoryException), span.err.Load())
	assert.Equal(t, int32(0), span.statusErr.Load())

	// SetFailure names no category, so it adds ErrorCategoryUnknown to the
	// exception bit already in the mask.
	span.SetFailure()
	assert.Equal(t, int32(ErrorCategoryException|ErrorCategoryUnknown), span.err.Load())
	assert.Equal(t, int32(1), span.statusErr.Load())
}

type urlStatSample struct {
	url       string
	statusErr int
	elapsed   int64
}

type expectedUrlStat struct {
	timestamp int64
	total     expectedUrlHistogram
	failed    expectedUrlHistogram
}

type expectedUrlHistogram struct {
	total     int64
	max       int64
	histogram []int32
}

func newUrlStatTestSnapshot(limit int, withMethod bool) (*urlStatSnapshot, time.Time) {
	config := defaultConfig()
	config.Set(CfgHttpUrlStatLimitSize, limit)
	config.Set(CfgHttpUrlStatWithMethod, withMethod)

	return newUrlStats(config).newSnapshot(), time.Unix(1700000000, 123000000).UTC()
}

func addTestUrlStat(snapshot *urlStatSnapshot, url string, method string, statusErr int, elapsed int64, endTime time.Time) {
	snapshot.add(&urlStat{
		entry: &UrlStatEntry{
			Url:    url,
			Method: method,
		},
		endTime:   endTime,
		elapsed:   elapsed,
		statusErr: statusErr,
	})
}

func findEachUrlStat(t *testing.T, snapshot *urlStatSnapshot, url string, endTime time.Time) *eachUrlStat {
	t.Helper()

	stat, ok := snapshot.urlMap[urlKey{url: url, tick: endTime.Truncate(urlStatCollectInterval)}]
	assert.True(t, ok, "url=%s", url)
	if !ok {
		return nil
	}
	return stat
}

func histogramCount(histogram *urlStatHistogram) int32 {
	var count int32
	for _, bucketCount := range histogram.histogram {
		count += bucketCount
	}
	return count
}

func makeExpectedUrlStats(samples []urlStatSample, endTime time.Time) map[string]*expectedUrlStat {
	expected := make(map[string]*expectedUrlStat)
	timestamp := endTime.Truncate(urlStatCollectInterval).UnixNano() / int64(time.Millisecond)

	for _, sample := range samples {
		stat := expected[sample.url]
		if stat == nil {
			stat = &expectedUrlStat{
				timestamp: timestamp,
				total:     newExpectedUrlHistogram(),
				failed:    newExpectedUrlHistogram(),
			}
			expected[sample.url] = stat
		}

		stat.total.add(sample.elapsed)
		if sample.statusErr != 0 {
			stat.failed.add(sample.elapsed)
		}
	}

	return expected
}

func newExpectedUrlHistogram() expectedUrlHistogram {
	return expectedUrlHistogram{
		histogram: make([]int32, urlStatBucketSize),
	}
}

func (histogram *expectedUrlHistogram) add(elapsed int64) {
	histogram.total += elapsed
	if histogram.max < elapsed {
		histogram.max = elapsed
	}
	histogram.histogram[getBucket(elapsed)]++
}

func assertUriHistogram(t *testing.T, expected expectedUrlHistogram, actual *pb.PUriHistogram) {
	t.Helper()

	assert.Equal(t, expected.total, actual.GetTotal())
	assert.Equal(t, expected.max, actual.GetMax())
	if expected.isEmpty() {
		assert.Empty(t, actual.GetHistogram())
		return
	}
	assert.Equal(t, expected.histogram, actual.GetHistogram())
}

func (histogram expectedUrlHistogram) isEmpty() bool {
	for _, count := range histogram.histogram {
		if count != 0 {
			return false
		}
	}
	return true
}

// Url stats accumulate in the agent's own urlStats, not in a package global: a
// second agent starts with an empty snapshot and cannot have its snapshot
// swapped out from under the first agent's worker.
func Test_agent_urlStatSnapshot_isPerAgent(t *testing.T) {
	first, second := newTestAgent(defaultConfig()), newTestAgent(defaultConfig())

	endTime := time.Unix(1700000000, 123000000).UTC()
	addTestUrlStat(first.urlStats.snapshot, "/only-on-first", "GET", 0, 100, endTime)

	assert.Equal(t, 1, first.urlStats.takeSnapshot(true).count, "first agent")
	assert.Equal(t, 0, second.urlStats.takeSnapshot(true).count, "second agent")

	// taking the snapshot leaves the agent a fresh one to keep filling
	assert.Equal(t, 0, first.urlStats.snapshot.count, "first agent after take")
}

// At the limit a new url pattern is dropped, and the drop is not silent: an
// operator looking for a missing url in the dashboard has to find the reason in
// the log at the default level. Aggregating an url already in the snapshot is
// not a drop and must stay quiet.
func Test_urlStatSnapshotWarnsWhenAPatternIsDroppedAtTheLimit(t *testing.T) {
	var buf bytes.Buffer
	defer captureLogAt(&buf, logrus.InfoLevel)() // the default level, not Warn
	urlStatLimitLog = logThrottle{src: "url stat"}

	snapshot, endTime := newUrlStatTestSnapshot(2, false)
	addTestUrlStat(snapshot, "/kept1", "", 0, 10, endTime)
	addTestUrlStat(snapshot, "/kept2", "", 0, 10, endTime)
	addTestUrlStat(snapshot, "/kept1", "", 0, 20, endTime)
	assert.Empty(t, buf.String(), "no drop, no warning")

	addTestUrlStat(snapshot, "/dropped", "", 0, 30, endTime)

	assert.Len(t, snapshot.urlMap, 2)
	assert.Equal(t, 2, snapshot.count)
	findEachUrlStat(t, snapshot, "/kept1", endTime)
	findEachUrlStat(t, snapshot, "/kept2", endTime)
	assert.Contains(t, buf.String(), "url stat limit reached")
	assert.Contains(t, buf.String(), `/dropped`)
	assert.Contains(t, buf.String(), "max 2 distinct urls")
}

// The limit is reached once and then held for as long as the traffic keeps
// bringing new patterns, so the warning must not log once per dropped request.
func Test_urlStatSnapshotThrottlesTheDropWarning(t *testing.T) {
	var buf bytes.Buffer
	defer captureLogAt(&buf, logrus.InfoLevel)()
	urlStatLimitLog = logThrottle{src: "url stat"}

	snapshot, endTime := newUrlStatTestSnapshot(1, false)
	addTestUrlStat(snapshot, "/kept", "", 0, 10, endTime)
	for i := 0; i < 1000; i++ {
		addTestUrlStat(snapshot, fmt.Sprintf("/dropped%d", i), "", 0, 10, endTime)
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "url stat limit reached"), buf.String())

	urlStatLimitLog.next.Store(0) // the interval elapses
	addTestUrlStat(snapshot, "/dropped-later", "", 0, 10, endTime)

	assert.Equal(t, 2, strings.Count(buf.String(), "url stat limit reached"))
	assert.Contains(t, buf.String(), "(999 similar warning(s) suppressed)")
	assert.Len(t, snapshot.urlMap, 1)
}

// A limit of 0 or less makes snapshot.count >= limit true before the first url,
// dropping every url stat entry. It has to recover the default instead.
func Test_configHttpUrlStatLimitSizeOutOfRangeRecoversTheDefault(t *testing.T) {
	for _, limit := range []int{0, -1, maxQueueSize + 1} {
		var buf bytes.Buffer
		restore := captureLogAt(&buf, logrus.InfoLevel)

		config := defaultConfig()
		config.Set(CfgHttpUrlStatLimitSize, limit)

		assert.Equal(t, 1024, config.Int(CfgHttpUrlStatLimitSize), "limit=%d", limit)
		assert.Contains(t, buf.String(), "Http.UrlStat.LimitSize", "limit=%d", limit)
		assert.Contains(t, buf.String(), "is out of range [1, 65536]", "limit=%d", limit)
		restore()
	}
}

// Only a closed tick is sent. The send interval is not aligned with the tick
// interval, so a send that took the tick in progress would put part of one
// (uri, tick) key in one message and the rest in the next - the collector
// stores the second write over the first instead of merging, so the counts of
// the first part are simply lost. Two sends inside one tick must therefore
// produce no message at all, and the tick must go out whole once it closes.
func Test_urlStatSendsOneTickInOneMessage(t *testing.T) {
	agent, stats := newUrlStatSendTestAgent(t)
	tick := time.Unix(1700000000, 0).UTC().Truncate(urlStatCollectInterval)
	// Both sends below happen while the clock is still inside the tick's own
	// window, so the newer entry at the end is the only thing that can close it.
	setNow := fixUrlStatClock(t, tick.Add(time.Second))

	agent.urlStats.add(newTestUrlStat("/a", 10, tick))
	agent.flushUrlStat(false)
	agent.urlStats.add(newTestUrlStat("/a", 20, tick.Add(time.Second)))
	agent.flushUrlStat(false)

	assert.Empty(t, stats(), "the tick is still open; nothing may go out yet")

	// A newer tick closes it, and the next send carries it whole.
	agent.urlStats.add(newTestUrlStat("/a", 30, tick.Add(urlStatCollectInterval)))
	setNow(tick.Add(urlStatCollectInterval + time.Second))
	agent.flushUrlStat(false)

	sent := stats()
	assert.Len(t, sent, 1)
	each := eachUriStatsByUri(t, sent[0])
	assert.Len(t, each, 1)
	assert.Equal(t, int64(30), each["/a"].GetTotalHistogram().GetTotal(), "both requests of the tick")
	assert.Equal(t, int64(20), each["/a"].GetTotalHistogram().GetMax())
	assert.Equal(t, tick.UnixMilli(), each["/a"].GetTimestamp())
}

// No traffic, no message. Java's UriStatCollectingJob leaves its poll loop on
// an empty queue rather than sending an empty PAgentUriStat.
func Test_urlStatSendsNothingWithoutTraffic(t *testing.T) {
	agent, stats := newUrlStatSendTestAgent(t)

	agent.flushUrlStat(false)
	agent.flushUrlStat(false)

	assert.Empty(t, stats())
}

// The boundary case the split regression above is the other half of: a tick
// closed just before the send goes out entirely in that send, and the tick that
// closed it stays behind.
func Test_urlStatSendsAClosedTickAndKeepsTheOpenOne(t *testing.T) {
	agent, stats := newUrlStatSendTestAgent(t)
	tick := time.Unix(1700000000, 0).UTC().Truncate(urlStatCollectInterval)
	// The clock sits inside the second tick's window: the first tick is over,
	// the second one is not.
	fixUrlStatClock(t, tick.Add(urlStatCollectInterval+time.Second))

	agent.urlStats.add(newTestUrlStat("/closed", 10, tick))
	agent.urlStats.add(newTestUrlStat("/open", 20, tick.Add(urlStatCollectInterval)))
	agent.flushUrlStat(false)

	sent := stats()
	assert.Len(t, sent, 1)
	each := eachUriStatsByUri(t, sent[0])
	assert.Len(t, each, 1, "only the closed tick")
	assert.Contains(t, each, "/closed")
	assert.Equal(t, tick.UnixMilli(), each["/closed"].GetTimestamp())

	assert.Len(t, agent.urlStats.snapshot.urlMap, 1, "the open tick keeps collecting")
}

// The tick in progress at shutdown has no later send to close it. Flushing it
// on the way out is the only thing standing between a clean stop and losing up
// to a full tick interval of traffic.
func Test_urlStatShutdownFlushesTheTickInProgress(t *testing.T) {
	agent, stats := newUrlStatSendTestAgent(t)
	tick := time.Unix(1700000000, 0).UTC().Truncate(urlStatCollectInterval)
	// Inside the second tick's window, so only the shutdown flush can take it.
	fixUrlStatClock(t, tick.Add(urlStatCollectInterval+time.Second))

	agent.urlStats.add(newTestUrlStat("/closed", 10, tick))
	agent.urlStats.add(newTestUrlStat("/in-progress", 20, tick.Add(urlStatCollectInterval)))
	agent.flushUrlStat(false)
	assert.Len(t, stats(), 1)

	agent.flushUrlStat(true)

	sent := stats()
	assert.Len(t, sent, 1)
	each := eachUriStatsByUri(t, sent[0])
	assert.Len(t, each, 1)
	assert.Contains(t, each, "/in-progress")
}

// A stats stream that never drains must not let the completed queue grow
// without bound. The oldest tick is dropped first, and the drop is reported
// once per throttle window rather than once per closed tick.
func Test_urlStatCompletedQueueDropsTheOldestTickAtTheCap(t *testing.T) {
	var buf bytes.Buffer
	defer captureLogAt(&buf, logrus.InfoLevel)()
	urlStatSnapshotDropLog = logThrottle{src: "url stat"}

	stats := newUrlStats(defaultConfig())
	tick := time.Unix(1700000000, 0).UTC().Truncate(urlStatCollectInterval)
	// Inside the last tick's window, so the take below drains the queue only.
	fixUrlStatClock(t, tick.Add(time.Duration(maxCompletedUrlStatSnapshots+2)*urlStatCollectInterval+time.Second))

	// maxCompletedUrlStatSnapshots+2 closed ticks, plus the one left open.
	for i := 0; i <= maxCompletedUrlStatSnapshots+2; i++ {
		stats.add(newTestUrlStat(fmt.Sprintf("/tick%d", i), 10, tick.Add(time.Duration(i)*urlStatCollectInterval)))
	}

	assert.Len(t, stats.completed, maxCompletedUrlStatSnapshots)
	// Two ticks were evicted, one line reported them: the throttle carries the
	// second one over to whatever line the next window grants.
	assert.Equal(t, 1, strings.Count(buf.String(), "url stat snapshot queue overflow"), buf.String())

	// Six ticks closed, the four newest survive: /tick0 and /tick1 are gone.
	snapshot := stats.takeSnapshot(false)
	assert.Len(t, snapshot.urlMap, maxCompletedUrlStatSnapshots)
	for i := 0; i <= maxCompletedUrlStatSnapshots+2; i++ {
		key := urlKey{url: fmt.Sprintf("/tick%d", i), tick: tick.Add(time.Duration(i) * urlStatCollectInterval)}
		if i < 2 {
			assert.NotContains(t, snapshot.urlMap, key, "oldest closed ticks are dropped first")
		} else if i <= maxCompletedUrlStatSnapshots+1 {
			assert.Contains(t, snapshot.urlMap, key)
		} else {
			assert.Contains(t, stats.snapshot.urlMap, key, "the last tick is still open")
		}
	}
}

// A straggler for an already-closed tick lands in the open snapshot under its
// own tick key, so both halves reach the same send and merge folds them back
// into one entry rather than dropping either.
func Test_urlStatMergeFoldsAStragglerBackIntoItsTick(t *testing.T) {
	stats := newUrlStats(defaultConfig())
	tick := time.Unix(1700000000, 0).UTC().Truncate(urlStatCollectInterval)

	stats.add(newTestUrlStat("/a", 10, tick))
	stats.add(newTestUrlStat("/a", 20, tick.Add(urlStatCollectInterval))) // closes the tick
	stats.add(newTestUrlStat("/a", 30, tick.Add(time.Second)))            // straggler for the closed tick

	snapshot := stats.takeSnapshot(true)
	folded, ok := snapshot.urlMap[urlKey{url: "/a", tick: tick}]
	assert.True(t, ok)
	assert.Equal(t, int64(40), folded.totalHistogram.total)
	assert.Equal(t, int64(30), folded.totalHistogram.max)
	assert.Equal(t, int32(2), histogramCount(folded.totalHistogram))
}

// The last tick of a burst has no newer entry coming to close it, so its own
// window elapsing has to be enough. Without that an agent whose traffic stopped
// holds its final tick until shutdown - and if traffic ever resumes, ships it
// stamped with the long-past tick it was collected in, backfilling a bucket the
// collector has already moved on from.
func Test_urlStatSendsTheLastTickOfABurstOnceItsWindowIsOver(t *testing.T) {
	agent, stats := newUrlStatSendTestAgent(t)
	tick := time.Unix(1700000000, 0).UTC().Truncate(urlStatCollectInterval)
	setNow := fixUrlStatClock(t, tick.Add(time.Second))

	agent.urlStats.add(newTestUrlStat("/a", 10, tick))

	agent.flushUrlStat(false)
	assert.Empty(t, stats(), "still inside the tick's window")

	// The window is over: nothing that can still belong to this tick is coming,
	// so taking it now is not a split.
	setNow(tick.Add(urlStatCollectInterval + time.Second))
	agent.flushUrlStat(false)

	sent := stats()
	require.Len(t, sent, 1)
	each := eachUriStatsByUri(t, sent[0])
	assert.Len(t, each, 1)
	assert.Equal(t, int64(10), each["/a"].GetTotalHistogram().GetTotal())
	assert.Equal(t, tick.UnixMilli(), each["/a"].GetTimestamp())

	// Taken, not copied: a later send must not report the same tick again.
	agent.flushUrlStat(false)
	assert.Empty(t, stats())
}

// fixUrlStatClock pins the clock urlStats reads and returns a setter that moves
// it, so a test can place the tick window boundary where it needs it instead of
// racing the wall clock. A plain variable is enough - these tests drive the
// agent from the one goroutine.
func fixUrlStatClock(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()

	prev := urlStatNow
	now := at
	urlStatNow = func() time.Time { return now }
	t.Cleanup(func() { urlStatNow = prev })

	return func(to time.Time) { now = to }
}

func newTestUrlStat(url string, elapsed int64, endTime time.Time) *urlStat {
	return &urlStat{
		entry:   &UrlStatEntry{Url: url},
		endTime: endTime,
		elapsed: elapsed,
	}
}

// newUrlStatSendTestAgent returns an agent whose stat queue is readable, and a
// function draining whatever url stat messages have been enqueued since the
// previous call.
func newUrlStatSendTestAgent(t *testing.T) (*agent, func() []*pb.PAgentUriStat) {
	t.Helper()

	config := defaultConfig()
	config.Set(CfgHttpUrlStatEnable, true)

	a := newTestAgent(config)
	a.statChan = make(chan *pb.PStatMessage, 16)

	return a, func() []*pb.PAgentUriStat {
		var sent []*pb.PAgentUriStat
		for {
			select {
			case msg := <-a.statChan:
				sent = append(sent, msg.GetAgentUriStat())
			default:
				return sent
			}
		}
	}
}

func eachUriStatsByUri(t *testing.T, stat *pb.PAgentUriStat) map[string]*pb.PEachUriStat {
	t.Helper()

	byUri := make(map[string]*pb.PEachUriStat)
	for _, each := range stat.GetEachUriStat() {
		_, dup := byUri[each.GetUri()]
		assert.False(t, dup, "uri=%s", each.GetUri())
		byUri[each.GetUri()] = each
	}
	return byUri
}
