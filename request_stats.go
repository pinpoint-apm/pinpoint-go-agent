package pinpoint

import (
	"math/rand/v2"
	"sync/atomic"
	"time"
)

// requestStats is the statistics state a request's span carries whether or
// not it is sampled: its place in the active span registry and, while a
// real-time viewer is attached, in the real-time map; the URL statistics entry
// it records; and the failure flag those statistics read. span and noopSpan
// embed it, so the sampled and the unsampled path share one implementation.
type requestStats struct {
	startTime time.Time

	// urlStat is nil until collectUrlStat records an entry; it then points at
	// urlStatBuf, and endUrlStat queues urlStatRecord. All three live in the
	// span so a request with URL statistics costs no allocation beyond the
	// span: the entry copy and the record were two more per request.
	urlStat       *UrlStatEntry
	urlStatBuf    UrlStatEntry
	urlStatRecord urlStat

	// statusErr is the failure the URL statistics report. Atomic: an event
	// setter run from another goroutine of the call stack races the end
	// reading it.
	statusErr atomic.Int32

	// activeKey is this span's key in the active span registry, drawn locally
	// on first registration (0: not drawn yet). Not spanId: a continued span
	// takes its id from the upstream header, so two in-flight requests carrying
	// the same headers - a retrying proxy, a header-copying client - shared one
	// entry, the first EndSpan removed it from under the other, and every such
	// request locked the same shard.
	activeKey int64

	// goroutineId is the goroutine the request runs on: the real-time map's
	// key, and on a sampled span what NewSpanEvent's goroutine-sharing check
	// and OnTracerGoroutine compare against. begin sets it; a goroutine tracer,
	// which never begins, holds -1 until its first NewSpanEvent sets it.
	goroutineId atomic.Int64

	// realTimeTracked records that begin stored this span in
	// agent.realTimeActiveSpan, so end deletes only what was stored: the store
	// is gated by atcStreamCount at start, and a viewer that attached or left
	// in between makes the count at end time no guide.
	realTimeTracked atomic.Bool
}

// begin registers the request in the active span registry and, while a
// real-time viewer is attached (atcStreamCount), in the real-time map under
// its goroutine id, where txId and entryPoint describe a sampled request. The
// key is drawn once, so a second begin - a continued span extracted again -
// re-registers the same entry instead of leaking the first.
func (s *requestStats) begin(agent *agent, txId TransactionId, entryPoint string, sampled bool) {
	if s.activeKey == 0 {
		// The top bit keeps it non-zero without touching the low bits the
		// shard is picked by.
		s.activeKey = int64(rand.Uint64() | 1<<63)
	}
	agent.stats.activeSpan.store(s.activeKey, s.startTime)

	// Pinned where the request starts, not by its first span event:
	// OnTracerGoroutine is asked before any event may exist, and a library
	// callback off this goroutine - a gocql speculative attempt, a go-redis
	// AutoPipeliner command - was otherwise taken for it, claimed the span,
	// and made this goroutine read as a foreign one.
	if goIdOffset > 0 {
		s.goroutineId.Store(goIdFromG())
	}

	if agent.atcStreamCount.Load() > 0 {
		txIdString := ""
		if sampled {
			txIdString = txId.String()
		}
		gid := curGoroutineID()
		s.goroutineId.Store(gid)
		agent.realTimeActiveSpan.Store(gid, &activeSpanInfo{s.startTime, txIdString, entryPoint, sampled})
		s.realTimeTracked.Store(true)
	}
}

// end removes what begin registered. The real-time entry goes by the span's
// own flag rather than atcStreamCount: the count may have changed since the
// start, and a sync.Map lookup per EndSpan for a feature that is off nearly
// all the time is a fixed cost the flag avoids.
func (s *requestStats) end(agent *agent) {
	agent.stats.activeSpan.remove(s.activeKey)
	if s.realTimeTracked.Load() {
		agent.realTimeActiveSpan.Delete(s.goroutineId.Load())
	}
}

// collectUrlStat records stat as the request's URL statistics entry when cfg
// collects them; see mergeUrlStat for how a later entry combines with it.
func (s *requestStats) collectUrlStat(cfg *configSnapshot, stat *UrlStatEntry) {
	if cfg.collectUrlStat {
		mergeUrlStat(&s.urlStatBuf, s.urlStat != nil, stat)
		s.urlStat = &s.urlStatBuf
	}
}

// endUrlStat queues the URL statistics record of a request that ended at
// endTime after elapsed milliseconds, failed when statusErr is set. A request
// that recorded no entry queues nothing.
func (s *requestStats) endUrlStat(agent *agent, endTime time.Time, elapsed int64, statusErr int32) {
	if s.urlStat == nil {
		return
	}
	s.urlStatRecord = urlStat{entry: s.urlStat, endTime: endTime, elapsed: elapsed, statusErr: int(statusErr)}
	agent.enqueueUrlStat(&s.urlStatRecord)
}
