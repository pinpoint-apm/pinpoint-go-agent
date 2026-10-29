package pinpoint

import (
	"bytes"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/spaolacci/murmur3"
)

type apiMeta struct {
	id         int32
	descriptor string
	apiType    int
}

// apiCacheKey identifies a cached API id by its descriptor and type.
type apiCacheKey struct {
	descriptor string
	apiType    int
}

type stringMeta struct {
	id       int32
	funcName string
}

// sqlMeta carries the text to publish, abbreviated to maxSqlSize, and the
// hash the id cache keys the statement by, so a failed send drops exactly the
// entry that published the id.
type sqlMeta struct {
	id  int32
	sql string
	key sqlHash
}

// sqlUidMeta carries the hash the UID cache keys the statement by, so a failed
// send drops exactly the entry that published the UID; the text is only the
// abbreviated copy to publish.
type sqlUidMeta struct {
	uid []byte
	sql string
	key sqlHash
}

type exceptionMeta struct {
	txId        TransactionId
	spanId      int64
	uriTemplate string
	exceptions  []*exception
}

type exception struct {
	exceptionId int64
	depth       int32  // 0 for the recorded error, 1..n down its cause chain
	className   string // SetError name or the error's Go type name
	callstack   *errorWithCallStack
}

func (agent *agent) sendMetaWorker() {
	Log("agent").Infof("start meta goroutine")

	// Metadata sends are pipelined: registration has no ordering requirement
	// (the collector accepts duplicates and metadata arriving after its
	// spans), while serial sends cap throughput at one item per round trip,
	// which falls behind when high error rates produce exception metadata
	// per span.
	permit := make(chan struct{}, agent.metaMaxConcurrentRequests())
	var inFlight sync.WaitGroup
	// Wait for the final sends after draining the queue, including those
	// dispatched during shutdown.
	defer func() {
		inFlight.Wait()
		// No later cycle will log what the rate limit holds back, so the last
		// report bypasses it, once the sends in flight can add no more.
		agent.metaDrops.reportAt.Store(0)
		agent.metaRetryDrops.reportAt.Store(0)
		agent.reportMetaDrops()
		Log("agent").Infof("end meta goroutine")
	}()

	stop := agent.stopSignal().Done()
	agent.metaRetry.init(metaRetryQueueSize)

	for {
		// Reported here rather than from the producers: enqueueMeta and the
		// send goroutines only bump a counter. Before the wait, so that a
		// total the rate limit holds back can bound it.
		held := agent.reportMetaDrops()

		// retry schedule: a retry is a second try at an id whose spans went
		// out a delay ago, a new item is an id whose spans are going out now.
		var item pendingMeta
		select {
		case md := <-agent.metaChan:
			item = pendingMeta{md: md}
		default:
			// Stop only after the queue is empty, even if shutdown completed
			// its phase transition before this worker was scheduled.
			if !agent.tracingEnabled() {
				return
			}
			var ok, stopped bool
			if item, ok, stopped = agent.awaitMeta(stop, held); stopped {
				continue // recheck the queue before exiting
			} else if !ok {
				continue
			}
		}

		// A parked release needs no permit.
		if item.releaseOnly {
			agent.releaseParkedMeta(item)
			continue
		}

		// Keep the dequeued item through shutdown. The shared shutdown deadline
		// closes the collector connection to unblock stalled sends.
		permit <- struct{}{}
		inFlight.Add(1)
		go func(item pendingMeta) {
			defer inFlight.Done()
			defer func() { <-permit }()

			recoverPanic("meta send", func() {
				agent.sendMetadataOnce(item)
			})
		}(item)
	}
}

// awaitMeta waits for the next item to send: a new item from metaChan, or a
// retry come due. Nothing to send until one of those, or a retry scheduled
// while the queue was empty (wake), arrives. The timer is armed per wait: a
// retry is only ever appended behind the head, so the head's due time is
// fixed until it is popped. A held-back drop total arms it sooner, so a burst
// that ends in silence still gets logged: popDue then finds nothing due, ok
// is false, and the next cycle reports it. stopped reports the stop signal.
func (agent *agent) awaitMeta(stop <-chan struct{}, held time.Duration) (item pendingMeta, ok, stopped bool) {
	retry := &agent.metaRetry
	wait, armed := retry.headWait(time.Now())
	if held > 0 && (!armed || held < wait) {
		wait, armed = held, true
	}
	var due <-chan time.Time
	var timer *time.Timer
	if armed {
		timer = time.NewTimer(wait)
		due = timer.C
	}
	select {
	case <-stop:
		stopped = true
	case md := <-agent.metaChan:
		item, ok = pendingMeta{md: md}, true
	case <-retry.wake:
	case <-due:
		item, ok = retry.popDue(time.Now())
	}
	if timer != nil {
		timer.Stop()
	}
	return item, ok, stopped
}

// releaseParkedMeta handles a parked release come due: the drop a rejection
// earned, delayed by one retry interval (metaRejected), or a given-up item's,
// delayed by up to metaGiveUpParks of them unless the collector has delivered
// another item since - then it is parked once more.
func (agent *agent) releaseParkedMeta(item pendingMeta) {
	if item.parks > 0 && agent.metaDelivered.Load() == item.delivered {
		item.parks--
		agent.scheduleMetaRetry(item)
		return
	}
	agent.deleteMetaCache(item.md)
}

// reportMetaDrops warns about metadata lost to a full metaChan or retry
// schedule, and returns how long the rate limit holds back a total it did not
// log: the sooner of the two queues', so each is logged once its limit lifts.
func (agent *agent) reportMetaDrops() time.Duration {
	held := agent.metaDrops.report("meta", cap(agent.metaChan))
	retryHeld := agent.metaRetryDrops.report("meta retry", agent.metaRetry.capacity)
	if held <= 0 || (retryHeld > 0 && retryHeld < held) {
		held = retryHeld
	}
	return held
}

// metaMaxConcurrentRequests bounds how many metadata sends sendMetaWorker may
// have in flight at once: Collector.Grpc.SpanBatchMaxConcurrentRequests, the same budget
// the span batch sender has. A fixed four was the cap on exception metadata,
// which is one unary RPC per failed span with Error.TraceCallStack on: at a
// 20 ms round trip four permits carry some 200 a second, against the 1000 new
// chains a second Error.NewThroughput admits by default, and the rest
// overflowed metaChan. One option rather than a second one: an operator sizing
// the agent's concurrency toward the collector sizes both paths with it.
func (agent *agent) metaMaxConcurrentRequests() int {
	return agent.config.Int(CfgCollectorGrpcSpanBatchMaxConcurrentRequests)
}

// sendMetadataOnce makes one send of item and hands it on according to the
// verdict. The permit is held for the send alone: a retry waits in
// agent.metaRetry, not here, so a collector outage cannot pin the in-flight
// slots and stall sendMetaWorker on the permit while metaChan overflows.
func (agent *agent) sendMetadataOnce(item pendingMeta) {
	attempts := item.attempts + 1
	err := agent.agentGrpc.sendMetadata(item.md)
	switch metaVerdictOf(err, attempts) {
	case metaDelivered:
		agent.metaDelivered.Add(1)
		agent.metaGiveUps.Store(0)
	case metaRetryLater:
		agent.scheduleMetaRetry(pendingMeta{md: item.md, attempts: attempts})
	case metaGiveUp:
		// The first give-up since a delivery is released at once, so a
		// failure the collector is over by the next use re-registers
		// right away; only a run of them - an outage - waits (metaGiveUpParks).
		// Exception metadata is never cached, so there is nothing to wait with.
		if _, ok := item.md.(exceptionMeta); ok || agent.metaGiveUps.Add(1) == 1 {
			agent.deleteMetaCache(item.md)
			return
		}
		agent.scheduleMetaRetry(pendingMeta{md: item.md, releaseOnly: true,
			parks: metaGiveUpParks, delivered: agent.metaDelivered.Load()})
	case metaRejected:
		// Exception metadata is never cached, so there is nothing to park.
		if _, ok := item.md.(exceptionMeta); ok {
			return
		}
		agent.scheduleMetaRetry(pendingMeta{md: item.md, releaseOnly: true})
	}
}

// scheduleMetaRetry parks item for one retry delay. The evicted item, if the
// schedule was full, has its cache entry released here: the same policy the
// metaChan overflow applies, so a full schedule costs exactly one cache entry
// and the id is registered again on its next use.
func (agent *agent) scheduleMetaRetry(item pendingMeta) {
	if agent.stopping() {
		return
	}
	item.dueAt = time.Now().Add(agent.agentGrpc.retryDelay)
	if evicted, ok := agent.metaRetry.push(item); ok {
		agent.deleteMetaCache(evicted.md)
		agent.metaRetryDrops.record(1)
	}
}

// pendingMeta is a metadata item on the retry schedule.
type pendingMeta struct {
	md any
	// attempts counts the sends made so far.
	attempts int
	dueAt    time.Time
	// releaseOnly parks a rejected or given-up item: when it comes due only
	// its cache entry is released, nothing is sent (metaRejected). A given-up
	// one is parked again while parks remain and nothing was delivered since
	// the delivered count it was parked at (metaGiveUpParks).
	releaseOnly bool
	parks       int
	delivered   int64
}

// metaRetryQueue is the time-ordered retry schedule. Every item waits the same
// delay, so the newest is always due last and a slice kept in push order is
// kept in due order. The schedule has its own bound, separate from metaChan's
// (see metaRetryQueueSize), and a full one head-drops: the incoming item is the
// last one due, so dropping it would freeze the schedule on whatever entered
// first and deny every later failure a retry, while dropping the oldest keeps
// the freshest failures - the ones whose spans the collector is still
// receiving - and keeps the schedule moving under a sustained outage.
type metaRetryQueue struct {
	mu       sync.Mutex
	items    []pendingMeta
	capacity int
	// wake tells sendMetaWorker a retry was scheduled while it waited with
	// an empty schedule, so it re-arms its timer. Buffered so a push never
	// blocks on a worker that is busy elsewhere.
	wake chan struct{}
}

// init sets the bound and creates the wake channel once; a capacity set
// before it (by a test) is kept.
func (q *metaRetryQueue) init(capacity int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.capacity <= 0 {
		q.capacity = capacity
	}
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
}

// push appends item, evicting and returning the oldest entry when the
// schedule is full. The caller releases the evicted item's cache entry
// outside the lock: the caches have locks of their own, and nesting them
// under this one would put the request path behind the retry schedule.
func (q *metaRetryQueue) push(item pendingMeta) (evicted pendingMeta, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.capacity > 0 && len(q.items) >= q.capacity {
		evicted, ok = q.items[0], true
		q.items[0] = pendingMeta{}
		q.items = q.items[1:]
	}
	q.items = append(q.items, item)
	if q.wake != nil {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
	return evicted, ok
}

// headWait returns how long until the oldest entry is due, or false when the
// schedule is empty.
func (q *metaRetryQueue) headWait(now time.Time) (time.Duration, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return 0, false
	}
	return q.items[0].dueAt.Sub(now), true
}

// popDue removes and returns the oldest entry if it is due.
func (q *metaRetryQueue) popDue(now time.Time) (pendingMeta, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 || q.items[0].dueAt.After(now) {
		return pendingMeta{}, false
	}
	item := q.items[0]
	q.items[0] = pendingMeta{}
	q.items = q.items[1:]
	return item, true
}

// length reports how many entries are scheduled.
func (q *metaRetryQueue) length() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// deleteMetaCache drops the cache entry whose metadata md failed to reach the
// collector. The entry is matched on its value as well as its key: between the
// failed send and this call the key may have been evicted and re-registered
// under a new id whose metadata did go out, and removing that entry would
// re-issue the id a third time for nothing.
func (agent *agent) deleteMetaCache(md any) {
	switch md := md.(type) {
	case apiMeta:
		agent.apiCache.remove(apiCacheKey{md.descriptor, md.apiType}, func(id int32) bool { return id == md.id })
	case stringMeta:
		agent.errorCache.remove(md.funcName, func(id int32) bool { return id == md.id })
	case sqlMeta:
		agent.sqlCache.remove(md.key, func(id int32) bool { return id == md.id })
	case sqlUidMeta:
		agent.sqlUidCache.remove(md.key, func(uid []byte) bool { return bytes.Equal(uid, md.uid) })
	}
}

// enqueueMeta queues md for the metadata sender, dropping the cache entry that
// published its id when the queue cannot take it. The id was already handed to
// the spans referencing it, so leaving the entry cached would keep every later
// span pointing at an id the collector never received; dropping it makes the
// next span register the metadata again. Same policy the send-failure path
// uses.
func (agent *agent) enqueueMeta(md any) {
	if !agent.tryEnqueueMeta(md) {
		agent.deleteMetaCache(md)
	}
}

// metaQueueFull reports whether the metadata queue has no room, counting the
// call as a drop when it has none. A cache miss asks before minting an id: an
// id minted for an item the queue then refuses is spent for nothing - the
// entry is released and the next use mints another - and under a collector
// outage that loop ran at the request rate, wrapping the int32 sequence
// within hours on a busy service and switching the metadata off for the rest
// of the process (idGen). Refusing here records nothing for this one use and
// the next use tries again. Two producers racing for the last slot still reach
// enqueueMeta's release, which is the backstop, not the common path.
func (agent *agent) metaQueueFull() bool {
	if len(agent.metaChan) < cap(agent.metaChan) {
		return false
	}
	agent.metaDrops.record(1)
	return true
}

// tryEnqueueMeta queues md, refusing a new item when the queue is full.
// Unlike a span, metadata has no recency value, and what differs between
// the oldest and the newest item is how many spans already reference the id.
// The producer registers the id in the cache before enqueueing, so the item
// at the head has been reused by every span that hit its entry while the
// pipeline stalled; dropping it (and releasing its entry, which the caller
// does) orphans all of them, while the newcomer is referenced by the one span
// that created it.
func (agent *agent) tryEnqueueMeta(md any) bool {
	if !agent.tracingEnabled() {
		return false
	}

	select {
	case agent.metaChan <- md:
		return true
	default:
		agent.metaDrops.record(1)
		return false
	}
}

// idGen is an agent-local metadata id sequence. The collector keys its API,
// string and SQL metadata by these int32 ids, so once the sequence wraps past
// math.MaxInt32 its next lap recycles ids that already name other entries, and
// the spans carrying them would point at another entry's text. next refuses to
// issue anything past the wrap instead.
type idGen struct {
	id int32
	// wrapped latches the wrap: without it the sequence would climb back
	// through the negatives and start handing out colliding ids again. Its CAS
	// also keeps the warning to one line per process.
	wrapped atomic.Bool
}

// next returns the next id in the sequence, or 0 once it has wrapped - the same
// 0 the caches return while the agent is disabled, which every consumer already
// reads as "no metadata". kind names the sequence in the overflow warning.
func (g *idGen) next(kind string) int32 {
	if g.wrapped.Load() {
		return 0
	}
	id := atomic.AddInt32(&g.id, 1)
	if id > 0 {
		return id
	}
	if g.wrapped.CompareAndSwap(false, true) {
		Log("agent").Warnf("%s id generator overflowed; no further %s metadata is recorded", kind, kind)
	}
	return 0
}

// registerMeta is the metadata registration every id cache follows: a hit on
// key returns the cached value; a miss mints one, publishes it in cache and
// queues the metadata md builds for it. The zero value comes back, and
// nothing is minted, while tracing is off, when the metadata queue is full
// (metaQueueFull) or when mint declines - an id sequence that wrapped. Two
// goroutines missing on the same key both mint, and the one peekOrAdd admits
// is the value both return.
func registerMeta[K comparable, V any](agent *agent, cache *metaCache[K, V], kind string, key K,
	mint func() (V, bool), md func(V) any) V {
	var zero V
	if !agent.tracingEnabled() {
		return zero
	}
	if v, ok := cache.peek(key); ok {
		return v
	}
	if agent.metaQueueFull() {
		return zero
	}
	v, ok := mint()
	if !ok {
		return zero
	}
	if cached, ok := cache.peekOrAdd(key, v); ok {
		return cached
	}

	m := md(v)
	agent.enqueueMeta(m)
	// Debug, not info: a miss runs on the request goroutine, and a workload
	// whose cardinality exceeds the cache would otherwise log per request.
	if IsDebugLogLevelEnabled() {
		Log("agent").Debugf("register %s metadata: %+v", kind, m)
	}
	return v
}

func (agent *agent) cacheError(errorName string) int32 {
	return registerMeta(agent, agent.errorCache, "error", errorName,
		func() (int32, bool) { id := agent.errorIdGen.next("error"); return id, id != 0 },
		func(id int32) any { return stringMeta{id: id, funcName: errorName} })
}

// sqlCacheable reports whether a raw SQL text is short enough to memoize its
// normalization (rawSqlCache), which holds the text as key and value. Anything
// longer is normalized on every use, so a handful of huge generated statements
// cannot pin megabytes of cache for the life of the process. The limit
// (SQL.CacheLengthLimit) is in bytes.
//
// The id and UID caches are not bounded by it: both key on the statement's
// 128-bit hash (sqlHash), so an entry costs the same whatever the statement's
// length, and a bypass would re-send the metadata - and for the id cache mint
// a fresh id, a new UI entry per execution - on every use of a long statement.
func (agent *agent) sqlCacheable(sql string) bool {
	return len(sql) < agent.sqlCacheLengthLimit
}

func (agent *agent) cacheSql(sql string) int32 {
	// Keyed on a hash of the untruncated statement, not on the abbreviated
	// text: an abbreviated key keeps no more than a 64KB prefix and the total
	// length, so two statements agreeing on both would share one id and the
	// second would never publish its own metadata. The hash and not the text
	// itself, because the text is kept for the life of the entry and is
	// bounded only by maxSqlNormalizeLength: SQL.CacheSize statements of up to
	// 1 MiB each pinned up to a gigabyte, where the hash pins 16 bytes.
	//
	// Bounded by maxSqlNormalizeLength: SetSQL drops a raw statement past it,
	// but literal-heavy SQL normalizes larger than it came in, so the key is
	// checked here as well - a key past the cap is refused (no id, so SetSQL
	// records no annotation) rather than admitted to the cache and metaChan.
	if !sqlNormalizable(sql) {
		return 0
	}
	key := sqlHashOf(sql)
	return registerMeta(agent, agent.sqlCache, "sql", key,
		// A wrapped sequence records no SQL: SetSQL skips a zero id.
		func() (int32, bool) { id := agent.sqlIdGen.next("sql"); return id, id != 0 },
		func(id int32) any { return sqlMeta{id: id, sql: abbreviateString(sql, maxSqlSize), key: key} })
}

func (agent *agent) cacheSqlUid(sql string) []byte {
	// The UID is the hash of the untruncated statement, and the cache keys on
	// that same hash: a text key was bounded by SQL.CacheLengthLimit, so every
	// execution of a statement past it - 2 KB by default, which an ORM reaches
	// easily - re-sent its metadata and re-normalized the text. The hash keeps
	// 16 bytes per statement whatever its length, and only the published text
	// is abbreviated. Nothing past maxSqlNormalizeLength gets a UID at all (see
	// cacheSql).
	if !sqlNormalizable(sql) {
		return nil
	}
	key := sqlHashOf(sql)
	return registerMeta(agent, agent.sqlUidCache, "sql uid", key,
		func() ([]byte, bool) { return key[:], true },
		func(uid []byte) any {
			return sqlUidMeta{uid: uid, sql: abbreviateString(sql, maxSqlSize), key: key}
		})
}

// sqlHash is the murmur3 x64 128 hash (seed 0) of a normalized statement, its
// two words laid out little-endian: the byte order the collector expects of a
// SQL UID, where murmur3's own Sum() writes them big-endian and would yield a
// different UID for the same SQL. It is the SQL id cache's key as well: two
// statements share a key with probability 2^-128, which is no collision in
// practice, and the cache holds 16 bytes per statement instead of the statement.
type sqlHash [16]byte

func sqlHashOf(sql string) sqlHash {
	// The string's bytes are hashed in place: Sum128 only reads them and
	// retains nothing, and the copy []byte(sql) made was the size of the
	// statement on every cache miss and every execution of a statement past
	// SQL.CacheLengthLimit.
	h1, h2 := murmur3.Sum128(unsafe.Slice(unsafe.StringData(sql), len(sql)))
	var h sqlHash
	binary.LittleEndian.PutUint64(h[0:8], h1)
	binary.LittleEndian.PutUint64(h[8:16], h2)
	return h
}

// normalizedSql is the immutable result of normalizing one raw SQL text.
// Both fields are strings, so a cached value can be handed to any number of
// callers without copying.
type normalizedSql struct {
	sql   string
	param string
}

// normalizeSql returns the normalized SQL and extracted parameters for sql,
// memoized by the raw SQL text so repeated statements skip re-parsing. It uses
// the same sharded metaCache as the id caches above, so a hot statement is a
// lock-free lookup and stays resident under aged promotion.
func (agent *agent) normalizeSql(sql string) (string, string) {
	// SQL.CacheLengthLimit applies here as it does to the metadata caches: the
	// raw text is both key and value, so an over-limit statement would pin twice
	// its size per entry - the memory the limit exists to cap.
	// SQL.RemoveComments is startup-only, so entries already in the cache stay
	// consistent with the value read here.
	removeComments := agent.config.load().sqlRemoveComments
	if len(sql) > maxSqlSize || !agent.sqlCacheable(sql) {
		return newSqlNormalizer(sql, removeComments).run()
	}
	if n, ok := agent.rawSqlCache.peek(sql); ok {
		return n.sql, n.param
	}
	nsql, param := newSqlNormalizer(sql, removeComments).run()
	agent.rawSqlCache.peekOrAdd(sql, normalizedSql{sql: nsql, param: param})
	return nsql, param
}

func (agent *agent) cacheSpanApi(descriptor string, apiType int) int32 {
	key := apiCacheKey{descriptor, apiType}
	return registerMeta(agent, agent.apiCache, "api", key,
		func() (int32, bool) { id := agent.apiIdGen.next("api"); return id, id != 0 },
		func(id int32) any { return apiMeta{id: id, descriptor: descriptor, apiType: apiType} })
}

// enqueueExceptionMeta sends chains, taken from span under errorChainsLock by
// EndSpan; span is read for its identity only.
func (agent *agent) enqueueExceptionMeta(span *span, chains []*exception) {
	if !agent.tracingEnabled() || !span.cfg.errorTraceCallStack {
		return
	}

	md := exceptionMeta{
		txId:       span.txId,
		spanId:     span.spanId,
		exceptions: chains,
	}
	if span.urlStat != nil {
		md.uriTemplate = span.urlStat.Url
	} else {
		md.uriTemplate = "NULL"
	}

	agent.enqueueMeta(md)
	if IsDebugLogLevelEnabled() {
		Log("agent").Debugf("enqueue exception meta: %v", md)
	}
}
