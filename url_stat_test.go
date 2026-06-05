package pinpoint

import (
	"errors"
	"testing"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
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
	span := defaultSpan()
	span.agent = newTestAgent(defaultConfig())

	span.SetError(errors.New("application error"))
	assert.Equal(t, 1, span.err)
	assert.Equal(t, 0, span.statusErr)

	span.SetFailure()
	assert.Equal(t, 1, span.err)
	assert.Equal(t, 1, span.statusErr)
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
	config.urlStatLimitSize = limit
	config.urlStatWithMethod = withMethod
	agent := newTestAgent(config)
	clock = newTickClock(urlStatCollectInterval)

	return agent.newUrlStatSnapshot(), time.Unix(1700000000, 123000000).UTC()
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

	stat, ok := snapshot.urlMap[urlKey{url: url, tick: clock.tick(endTime)}]
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
	timestamp := clock.tick(endTime).UnixNano() / int64(time.Millisecond)

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
		if urlStatStatus(sample.statusErr) == urlStatusFail {
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
