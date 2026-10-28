package pinpoint

import (
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// dropReportInterval bounds how often a saturated queue may warn. Tests may
// shorten it.
var dropReportInterval = 60 * time.Second

// headDropEnqueue queues v on ch and, when ch is full, head-drops the oldest
// record to make room - the policy of the url stat and stat queues, and of
// tryEnqueueMeta. An overflow costs exactly one record: the evicted one, or v
// itself when another producer takes the freed slot first. It reports whether
// v was queued and how many records were lost.
func headDropEnqueue[T any](ch chan T, v T) (queued bool, dropped int64) {
	select {
	case ch <- v:
		return true, 0
	default:
	}
	select {
	case <-ch:
		dropped++
	default:
		// The consumer drained one meanwhile, so nothing had to be evicted.
	}
	select {
	case ch <- v:
		queued = true
	default:
		// Another producer took the freed slot: v is the record lost.
		dropped++
	}
	return queued, dropped
}

// dropReporter counts records lost to a full queue and rate-limits the warning
// about them.
type dropReporter struct {
	// dropped is the running total of lost records, reported the total the last
	// warning carried, and reportAt the unix nano before which the next warning
	// stays silent; it starts at zero so the first drop reports.
	dropped  atomic.Int64
	reported atomic.Int64
	reportAt atomic.Int64
}

// record counts n drops and nothing else - no clock read, no logging - so it
// is cheap enough for a producer hot path. The warning is left to report,
// which a consumer calls.
func (r *dropReporter) record(n int64) {
	r.dropped.Add(n)
}

// report logs the running total at most once per dropReportInterval, and only
// when drops have accumulated since the last warning. WARN so the data loss is
// visible at the default log level, rate-limited so a saturated queue cannot
// log once per dropped record. Like reportTotal, it returns how long the limit
// holds back a total it did not log.
func (r *dropReporter) report(queue string, queueSize int) time.Duration {
	return r.reportTotal(r.dropped.Load(), queue, queueSize)
}

// reportTotal is report for a queue that keeps its own drop counter: total is
// that queue's running total, so the reporter contributes only the rate limit.
// It returns how long that limit holds back a total it did not log, for a
// consumer that would otherwise go quiet before reporting it.
func (r *dropReporter) reportTotal(total int64, queue string, queueSize int) time.Duration {
	if total == r.reported.Load() {
		return 0
	}

	now := time.Now().UnixNano()
	next := r.reportAt.Load()
	if now < next || !r.reportAt.CompareAndSwap(next, now+int64(dropReportInterval)) {
		return time.Duration(r.reportAt.Load() - now)
	}
	r.reported.Store(total)
	Log("agent").Warnf(
		"%s queue overflow: %d dropped in total (oldest overwritten, max queue size %d)",
		queue, total, queueSize)
	return 0
}

// logThrottle rate-limits one warning site to a message per dropReportInterval,
// counting what it held back in between. For warnings a peer or the
// application can trigger once per request - a malformed header, an
// unbalanced span - where an unthrottled WARN is a log-flooding lever that
// covers the same sites.
type logThrottle struct {
	// src names the log source. The empty value logs under "span", where every
	// site but the url stat limit one lives.
	src        string
	next       atomic.Int64 // unix nano before which the site stays silent
	suppressed atomic.Int64
}

var (
	malformedTraceIdLog, malformedSpanIdLog, malformedParentSpanIdLog logThrottle
	endSpanTwiceLog, unclosedEventLog, noEventLog, sharedGoroutineLog logThrottle
	afterEndSpanLog                                                   logThrottle
	// Latched once per span as well (eventOverflowLog, errorChainDropLog), but
	// once a span is once a request for an endpoint that always overflows or
	// always reaches the entry cap, so these throttle across spans too.
	callStackOverflowLog, errorChainLimitLog, errorChainDroppedLog logThrottle
	addMetricTypeLog                                               logThrottle
)

// acquire reports whether the site may log now and, if so, how many calls it
// held back since it last did. A refused call is counted as held back.
func (t *logThrottle) acquire() (held int64, ok bool) {
	now := time.Now().UnixNano()
	next := t.next.Load()
	if now < next || !t.next.CompareAndSwap(next, now+int64(dropReportInterval)) {
		t.suppressed.Add(1)
		return 0, false
	}
	return t.suppressed.Swap(0), true
}

func (t *logThrottle) warnf(format string, args ...any) {
	t.logf(logrus.WarnLevel, format, args...)
}

func (t *logThrottle) errorf(format string, args ...any) {
	t.logf(logrus.ErrorLevel, format, args...)
}

func (t *logThrottle) infof(format string, args ...any) {
	t.logf(logrus.InfoLevel, format, args...)
}

// warnLazy is warnf for a site whose arguments are costly to produce - a
// registry size summed under every shard lock, a string built per call - so
// that args runs only for the line that is logged and never for a suppressed
// one, which at a throttled site is nearly every call.
func (t *logThrottle) warnLazy(format string, args func() []any) {
	n, ok := t.acquire()
	if !ok {
		return
	}
	t.emit(logrus.WarnLevel, format, args(), n)
}

func (t *logThrottle) logf(level logrus.Level, format string, args ...any) {
	n, ok := t.acquire()
	if !ok {
		return
	}
	t.emit(level, format, args, n)
}

// emit logs the line, noting the n calls the throttle held back before it.
func (t *logThrottle) emit(level logrus.Level, format string, args []any, n int64) {
	if n > 0 {
		format += " (%d similar warning(s) suppressed)"
		args = append(args, n)
	}
	src := t.src
	if src == "" {
		src = "span"
	}
	Log(src).log(level, format, args...)
}
