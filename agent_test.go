package pinpoint

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func Test_agent_NewAgentError(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := NewAgent(nil)
			assert.Equal(t, NoopAgent(), a, "noop agent")
			assert.Error(t, err, "error")
		})
	}
}

func Test_agent_NewAgent(t *testing.T) {
	type args struct {
		config *Config
	}

	opts := []ConfigOption{
		WithAppName("test"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true

	tests := []struct {
		name string
		args args
	}{
		{"1", args{c}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.args.config
			a, err := NewAgent(c)
			agent := a.(*agent)
			assert.NoError(t, err, "NewAgent")
			assert.Equal(t, "test", agent.appName, "ApplicationName")
			assert.Len(t, agent.agentID, uidBase64Len, "AgentID")
			assert.Equal(t, int32(ServiceTypeGoApp), agent.appType, "ApplicationType")
			assert.Greater(t, agent.startTime, int64(0), "StartTime")
			assert.Equal(t, GetAgent(), a, "global agent")

			agent.startTime = 12345
			agent.enable.Store(true)
			assert.Equal(t, agent.agentID+"^12345^1", agent.generateTransactionId().String(), "generateTransactionId")

			a.Shutdown()
			assert.Equal(t, NoopAgent(), GetAgent(), "global agent")
			assert.Equal(t, false, a.Enable(), "Enable")

			span := agent.NewSpanTracer("test", "/")
			assert.Equal(t, NoopTracer(), span, "NewSpanTracer")
		})
	}
}

func Test_agent_GlobalAgent(t *testing.T) {
	type args struct {
		config *Config
	}

	opts := []ConfigOption{
		WithAppName("testGlobal"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)
	defer a.Shutdown()

	tests := []struct {
		name string
		args args
	}{
		{"1", args{c}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, GetAgent(), a, "global agent")
			assert.NotEqual(t, GetAgent(), NoopAgent(), "global agent")

			a, err := NewAgent(c)
			assert.Error(t, err, "NewAgent")
			assert.Equal(t, GetAgent(), a, "global agent")
		})
	}
}

func Test_agent_NewSpanTracer(t *testing.T) {
	type args struct {
		agent Agent
	}

	opts := []ConfigOption{
		WithAppName("test"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)
	defer a.Shutdown()

	tests := []struct {
		name string
		args args
	}{
		{"1", args{agent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := tt.args.agent
			span := ag.NewSpanTracer("test", "/")

			txid := span.TransactionId()
			assert.Equal(t, agent.agentID, txid.AgentId, "AgentId")
			assert.Greater(t, txid.StartTime, int64(0), "StartTime")
			assert.Greater(t, txid.Sequence, int64(0), "Sequence")

			spanid := span.SpanId()
			assert.NotEqual(t, int64(0), spanid, "spanId")
		})
	}
}

func Test_agent_NewSpanTracerWithReader(t *testing.T) {
	type args struct {
		agent  Agent
		reader DistributedTracingContextReader
	}

	opts := []ConfigOption{
		WithAppName("test"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)
	defer a.Shutdown()

	m := map[string]string{
		HeaderTraceId:      "t123456^12345^1",
		HeaderSpanId:       "67890",
		HeaderParentSpanId: "123",
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{agent, &DistributedTracingContextMap{m}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			span := agent.NewSpanTracerWithReader("test", "/", tt.args.reader)

			txId := span.TransactionId()
			assert.Equal(t, "t123456", txId.AgentId, "AgentId")
			assert.Equal(t, int64(12345), txId.StartTime, "StartTime")
			assert.Equal(t, int64(1), txId.Sequence, "Sequence")
			assert.Equal(t, int64(67890), span.SpanId(), "SpanId")
		})
	}
}

// An unparseable trace id must go through the new-trace sampler: Extract
// starts a new root transaction for it, and the continue sampler is
// unconditionally true, so a peer could otherwise defeat the sampling rate
// with any garbage Pinpoint-TraceID.
func Test_agent_NewSpanTracerWithReader_samplerByParseability(t *testing.T) {
	c, _ := NewConfig(
		WithAppName("test"),
		WithSamplingType("COUNTER"),
		WithSamplingCounterRate(100),
	)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)
	defer a.Shutdown()

	run := func(tid string) (sampled int) {
		for i := 0; i < 100; i++ {
			m := map[string]string{HeaderSpanId: "67890", HeaderParentSpanId: "123"}
			if tid != "" {
				m[HeaderTraceId] = tid
			}
			tr := agent.NewSpanTracerWithReader("test", "/", &DistributedTracingContextMap{m})
			if tr.IsSampled() {
				sampled++
			}
			tr.EndSpan()
		}
		return
	}

	assert.Equal(t, 1, run("garbage"), "malformed tid: new sampler at 1%")
	assert.Equal(t, 1, run(""), "empty tid: new sampler at 1%")
	assert.Equal(t, 100, run("t123456^12345^1"), "valid tid: continue sampler, always sampled")

	cs := agent.stats.readCounters()
	assert.Equal(t, int64(2), cs.sampleNew, "sampleNew")
	assert.Equal(t, int64(198), cs.unSampleNew, "unSampleNew")
	assert.Equal(t, int64(100), cs.sampleCont, "sampleCont")
	assert.Equal(t, int64(0), cs.unSampleCont, "unSampleCont")
	assert.Equal(t, int64(300), cs.sampleNew+cs.unSampleNew+cs.sampleCont+cs.unSampleCont+cs.skipNew+cs.skipCont, "total")
}

func Test_abbreviateString_RuneSafe(t *testing.T) {
	assert.Equal(t, "abc", abbreviateString("abc", 5))

	// "가" is 3 bytes; a limit landing mid-rune must back up to the rune
	// boundary, or protobuf rejects the string at marshal time and the whole
	// span/metadata send fails. The marker reports the original size, as
	s := strings.Repeat("가", 3)
	got := abbreviateString(s, 4)
	assert.Equal(t, "가...(9)", got)
	assert.True(t, utf8.ValidString(got))
	assert.Equal(t, "가가...(9)", abbreviateString(s, 6))
}

func Test_validUTF8(t *testing.T) {
	assert.Equal(t, "abc가", validUTF8("abc가"), "valid strings must pass through unchanged")
	assert.Equal(t, "a�b", validUTF8("a\xffb"))
	assert.True(t, utf8.ValidString(validUTF8("rowKey: \x9f\x03\xff")))
}

// Plugins feed network-origin bytes into span string fields (percent-decoded
// URL paths, binary row keys, raw query bodies, driver error strings). One
// invalid UTF-8 string fails proto.Marshal for the whole message, and a failed
// span stream Send cancels the stream - so the conversion boundary must
// sanitize every such field.
func Test_spanMessageBuilder_SanitizesInvalidUTF8(t *testing.T) {
	a := newTestAgent(defaultConfig())
	bad := "bad\xff\xfe"

	s := newSampledSpan(a, bad, "/"+bad)
	s.endPoint = bad
	s.remoteAddr = bad
	s.acceptorHost = bad
	s.parentAppName = bad
	s.errorString = bad
	s.annotations.AppendString(AnnotationHttpUrl, bad)

	se := newSpanEvent(s, bad)
	se.endPoint = bad
	se.destinationId = bad
	se.errorString = bad
	se.annotations.AppendStringString(AnnotationHttpUrl, bad, bad)
	s.spanEvents = append(s.spanEvents, se)

	chunk := s.newEventChunk(true)
	builder := acquireSpanMessageBuilder()
	defer releaseSpanMessageBuilder(builder)

	for name, msg := range map[string]*pb.PSpanMessage{
		"span":  builder.makePSpan(chunk),
		"chunk": builder.makePSpanChunk(chunk),
	} {
		_, err := proto.Marshal(msg)
		assert.NoError(t, err, "a %s carrying invalid UTF-8 must still marshal", name)
	}
}

// SQL.CacheSize must be the capacity the agent gives its SQL caches, and only
// those: the api and error caches keep the fixed default.
func TestNewAgent_SQLCacheSizeSizesOnlyTheSqlCaches(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSQLCacheSize, 64)
	a := newTestAgent(cfg)

	for name, c := range map[string]*metaCacheShard{
		"sqlCache":    &a.sqlCache.shards[0],
		"sqlUidCache": &a.sqlUidCache.shards[0],
		"rawSqlCache": &a.rawSqlCache.shards[0],
	} {
		assert.Equal(t, 64/metaCacheShardCount, c.cap, "%s takes SQL.CacheSize", name)
	}
	assert.Equal(t, cacheSize/metaCacheShardCount, a.apiCache.shards[0].cap, "apiCache keeps cacheSize")
	assert.Equal(t, cacheSize/metaCacheShardCount, a.errorCache.shards[0].cap, "errorCache keeps cacheSize")
}

// noSqlCacheBypassConfig turns SQL.CacheLengthLimit off, so a SQL of any length
// is cached.
func noSqlCacheBypassConfig() *Config {
	cfg := defaultConfig()
	cfg.Set(CfgSQLCacheLengthLimit, -1)
	return cfg
}

// DefaultCachingSqlNormalizer does, and publish text abbreviated to maxSqlSize.
// An abbreviated key keeps no more than a 64KB prefix and the total length, so
// two statements agreeing on both would share one entry: the second would
// answer with the first's id and never publish its own metadata. The two texts
// differ past the cap, so the id meta cannot carry an abbreviated key and
// carries none: deleteMetaCache removes by id. The uid meta carries the key,
// which sqlCacheable already bounds.
func Test_agent_SQLCachesKeyTheWholeStatement(t *testing.T) {
	prefix := strings.Repeat("x", maxSqlSize)
	first, second := prefix+"select 1", prefix+"select 2"
	bounded := abbreviateString(first, maxSqlSize)
	require.Equal(t, bounded, abbreviateString(second, maxSqlSize), "the abbreviation is the collision")

	t.Run("sql id", func(t *testing.T) {
		a := newTestAgent(defaultConfig())
		firstID, secondID := a.cacheSql(first), a.cacheSql(second)

		assert.NotEqual(t, firstID, secondID, "each statement needs its own id")
		cached, ok := a.sqlCache.peek(second)
		assert.True(t, ok, "the whole statement is the key")
		assert.Equal(t, secondID, cached)
		_, abbreviated := a.sqlCache.peek(bounded)
		assert.False(t, abbreviated)

		assert.Len(t, a.metaChan, 2, "each statement publishes its own metadata")
		md := (<-a.metaChan).(sqlMeta)
		assert.Equal(t, bounded, md.sql, "the published text stays capped")

		a.deleteMetaCache(md)
		_, stillCached := a.sqlCache.peek(first)
		assert.False(t, stillCached, "a dropped meta must drop the entry that published its id")
	})

	t.Run("sql uid", func(t *testing.T) {
		a := newTestAgent(noSqlCacheBypassConfig())
		firstUid, secondUid := a.cacheSqlUid(first), a.cacheSqlUid(second)

		assert.NotEqual(t, firstUid, secondUid, "the uid hashes the whole statement")
		cached, ok := a.sqlUidCache.peek(second)
		assert.True(t, ok, "the whole statement is the key")
		assert.Equal(t, secondUid, cached)
		_, abbreviated := a.sqlUidCache.peek(bounded)
		assert.False(t, abbreviated)

		assert.Len(t, a.metaChan, 2, "each statement publishes its own metadata")
		md := (<-a.metaChan).(sqlUidMeta)
		assert.Equal(t, bounded, md.sql, "the published text stays capped")

		a.deleteMetaCache(md)
		_, stillCached := a.sqlUidCache.peek(first)
		assert.False(t, stillCached, "a dropped meta must drop the entry that published its uid")
	})
}

// A queued sql id meta holds no more than maxSqlSize of text. The id cache is
// keyed by the untruncated normalized statement, up to maxSqlNormalizeLength
// (1 MiB), and a queued copy of that key would have made the metadata queue
// hold up to its capacity x 1 MiB through a collector outage. The drop path
// still finds the entry, by id.
func Test_agent_QueuedSqlIdMetaHoldsNoUntruncatedKey(t *testing.T) {
	huge := "select " + strings.Repeat("x", maxSqlNormalizeLength-8)
	require.Less(t, len(huge), maxSqlNormalizeLength)

	a := newTestAgent(defaultConfig())
	id := a.cacheSql(huge)
	require.NotZero(t, id)

	md := (<-a.metaChan).(sqlMeta)
	assert.LessOrEqual(t, len(md.sql), maxSqlSize+16, "the published text is abbreviated")
	assert.Equal(t, id, md.id)

	a.deleteMetaCache(md)
	_, still := a.sqlCache.peek(huge)
	assert.False(t, still, "a dropped meta still evicts the entry that published its id")
}

// Cache membership is a flag on the meta, not the shape of its key. A SQL whose
// normalization is empty ("/* hint */" under Sql.RemoveComments) is cached
// under an empty key, so reading the empty key as "bypassed" skipped the
// eviction and left a UID cached that the collector never received.
func Test_agent_SQLUidCacheEvictsEmptyNormalizedStatement(t *testing.T) {
	nsql, _ := newSqlNormalizer("/* hint */", true).run()
	require.Empty(t, nsql, "the normalization of a lone comment is empty")

	a := newTestAgent(defaultConfig())
	require.NotNil(t, a.cacheSqlUid(nsql))
	_, cached := a.sqlUidCache.peek(nsql)
	require.True(t, cached, "an empty key is short enough to cache")

	md := (<-a.metaChan).(sqlUidMeta)
	assert.True(t, md.cached, "a cached statement must say so")

	a.deleteMetaCache(md)
	_, stillCached := a.sqlUidCache.peek(nsql)
	assert.False(t, stillCached, "a dropped meta must drop the entry that published its uid")
}

// A SQL at or above SQL.CacheLengthLimit is not cached by the hash-keyed caches:
// bypassLength. Caching them instead lets a few huge generated statements hold
// the cache - and their bytes - for the life of the process. The SQL-ID cache is
// exempt; see Test_agent_SQLIdCacheIgnoresLengthLimit.
func Test_agent_SQLCachesBypassKeysOverLengthLimit(t *testing.T) {
	sql := strings.Repeat("x", 3000)

	t.Run("sql uid", func(t *testing.T) {
		a := newTestAgent(defaultConfig())
		first := a.cacheSqlUid(sql)
		second := a.cacheSqlUid(sql)

		_, cached := a.sqlUidCache.peek(sql)
		assert.False(t, cached, "a sql over the length limit must not be cached")
		assert.Equal(t, first, second, "the uid hashes the sql, so it is stable")
		assert.Len(t, a.metaChan, 2, "metadata must be enqueued on every use")

		// Nothing was cached, so the item carries no key to evict by and its
		// size is bounded by the published text alone.
		md := (<-a.metaChan).(sqlUidMeta)
		assert.False(t, md.cached, "a bypassed statement has no entry to evict")
		assert.Empty(t, md.key, "and carries no key, so the queue item stays bounded")
		assert.LessOrEqual(t, len(md.sql), maxSqlSize)

		// deleteMetaCache must not reach the cache at all for a bypassed item.
		// An entry sitting at exactly the key and uid the item carries is what
		// an unguarded remove would delete - remove is a no-op for a missing
		// key, so evicting by md.key alone passes without the md.cached guard.
		_, existed := a.sqlUidCache.peekOrAdd(md.key, md.uid)
		require.False(t, existed, "the planted entry is the only one at that key")
		small := "select 1"
		a.cacheSqlUid(small)
		a.deleteMetaCache(md)
		_, stillPlanted := a.sqlUidCache.peek(md.key)
		assert.True(t, stillPlanted, "a bypassed statement must not touch the cache")
		_, stillCached := a.sqlUidCache.peek(small)
		assert.True(t, stillCached, "an uncached statement must not evict anything")
	})

	// The normalization memo holds the raw text as key and the normalized text
	// as value, so it pins the most memory per entry of the three.
	t.Run("normalized sql", func(t *testing.T) {
		a := newTestAgent(defaultConfig())
		a.normalizeSql(sql)
		_, cached := a.rawSqlCache.peek(sql)
		assert.False(t, cached, "a sql over the length limit must not be memoized")

		withinLimit := "select * from t where id = 1"
		a.normalizeSql(withinLimit)
		_, cached = a.rawSqlCache.peek(withinLimit)
		assert.True(t, cached, "a sql within the limit is still memoized")
	})
}

// SQL.CacheLengthLimit is fixed, read once in NewAgent like SQL.CacheSize.
// Lowering it after construction must change nothing: the entries already
// cached stay, and a statement within the limit the agent was built with keeps
// hitting the cache instead of re-sending its metadata on every use.
func Test_agent_SQLCacheLengthLimitIsFixedAtConstruction(t *testing.T) {
	sql := strings.Repeat("x", 1000)
	cfg := defaultConfig()
	a := newTestAgent(cfg)
	assert.Equal(t, defaultSqlCacheLengthLimit, a.sqlCacheLengthLimit)

	a.cacheSqlUid(sql)
	a.normalizeSql(sql)
	require.Len(t, a.metaChan, 1)

	cfg.Set(CfgSQLCacheLengthLimit, 10)
	assert.Equal(t, 10, cfg.Int(CfgSQLCacheLengthLimit), "the config itself still publishes the value")
	assert.True(t, a.sqlCacheable(sql), "the agent keeps the limit it was built with")

	a.cacheSqlUid(sql)
	a.normalizeSql(sql)
	assert.Len(t, a.metaChan, 1, "a cached statement must not be re-sent after a runtime Set")
	_, cached := a.sqlUidCache.peek(sql)
	assert.True(t, cached)
	_, cached = a.rawSqlCache.peek(sql)
	assert.True(t, cached)
}

// The SQL-ID cache is exempt from SQL.CacheLengthLimit: its ids come from an
// agent-local sequence, so a bypassed statement would burn a fresh id - and a
// fresh sqlMeta - on every execution, and the same query would show up in the UI
func Test_agent_SQLIdCacheIgnoresLengthLimit(t *testing.T) {
	sql := strings.Repeat("x", 3000)

	for _, limit := range []int{2048, 0, -1} {
		t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Set(CfgSQLCacheLengthLimit, limit)
			a := newTestAgent(cfg)

			first := a.cacheSql(sql)
			second := a.cacheSql(sql)

			assert.Equal(t, first, second, "the same sql must keep its id")
			assert.Equal(t, int32(1), atomic.LoadInt32(&a.sqlIdGen.id), "only one id may be issued")
			assert.Len(t, a.metaChan, 1, "metadata must be enqueued once")
		})
	}
}

// Past math.MaxInt32 a sequence wraps negative, and the ids the next lap hands
// out collide with the entries already registered under them. The collector
// keys its metadata by these ids, so a wrapped one is dropped, not published.
func Test_agent_MetadataCachesDropOverflowedIds(t *testing.T) {
	tests := []struct {
		name  string
		gen   func(*agent) *idGen
		cache func(*agent) int32
	}{
		{"sql", func(a *agent) *idGen { return &a.sqlIdGen },
			func(a *agent) int32 { return a.cacheSql("select 1") }},
		{"error", func(a *agent) *idGen { return &a.errorIdGen },
			func(a *agent) int32 { return a.cacheError("boom") }},
		{"api", func(a *agent) *idGen { return &a.apiIdGen },
			func(a *agent) int32 { return a.cacheSpanApi("op", apiTypeDefault) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAgent(defaultConfig())
			gen := tt.gen(a)
			gen.id = math.MaxInt32

			assert.Zero(t, tt.cache(a), "an overflowed id must not be recorded")
			assert.Empty(t, a.metaChan, "no metadata may be enqueued for a dropped id")

			// The wrap latches: left unlatched the sequence climbs back through
			// the negatives and starts reissuing ids that already name entries.
			assert.True(t, gen.wrapped.Load(), "the wrap must latch")
			gen.id = 0
			assert.Zero(t, tt.cache(a), "a latched sequence must not resume")
		})
	}
}

// A sequence that has not wrapped is untouched by the guard.
func Test_idGen_next(t *testing.T) {
	var g idGen
	assert.Equal(t, int32(1), g.next("test"))
	assert.Equal(t, int32(2), g.next("test"))

	g.id = math.MaxInt32
	assert.Zero(t, g.next("test"), "a wrapped sequence issues nothing")
	assert.Zero(t, g.next("test"), "and stays wrapped")
}

func Test_agent_tryEnqueueMetaReturnsWhenDropRaceLeavesQueueEmpty(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.metaChan = make(chan interface{})

	result := callBoolWithTimeout(t, "tryEnqueueMeta", func() bool {
		return agent.tryEnqueueMeta(stringMeta{id: 1, funcName: "error"})
	})

	assert.False(t, result)
}

// An unbuffered urlStatChan makes every send and receive miss: the eviction
// and the re-insert after it must both fall through to their default cases
// rather than block the request path.
func Test_agent_enqueueUrlStatReturnsWhenDropRaceLeavesQueueEmpty(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.urlStatChan = make(chan *urlStat)

	result := callBoolWithTimeout(t, "enqueueUrlStat", func() bool {
		return agent.enqueueUrlStat(&urlStat{})
	})

	assert.False(t, result)
	assert.EqualValues(t, 1, agent.urlStatDrops.dropped.Load(), "the rejected record is the one lost")
}

// An unbuffered statChan makes every send and receive miss: the eviction and
// the re-insert after it must both fall through to their default cases rather
// than block the producer.
func Test_agent_enqueueStatReturnsWhenDropRaceLeavesQueueEmpty(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.statChan = make(chan *pb.PStatMessage)

	result := callBoolWithTimeout(t, "enqueueStat", func() bool {
		return agent.enqueueStat(nil)
	})

	assert.False(t, result)
	assert.EqualValues(t, 1, agent.statDrops.dropped.Load(), "the rejected record is the one lost")
}

func callBoolWithTimeout(t *testing.T, name string, fn func() bool) bool {
	t.Helper()

	done := make(chan bool, 1)
	go func() {
		done <- fn()
	}()

	select {
	case result := <-done:
		return result
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("%s blocked", name)
		return false
	}
}

func Test_waitTimeout(t *testing.T) {
	var wg sync.WaitGroup
	assert.True(t, waitTimeout(&wg, time.Second), "already done")

	wg.Add(1)
	start := time.Now()
	assert.False(t, waitTimeout(&wg, 100*time.Millisecond), "timed out")
	assert.Less(t, time.Since(start), 500*time.Millisecond, "returns at the deadline")

	wg.Done()
	assert.True(t, waitTimeout(&wg, time.Second), "done before the deadline")
}

// Shutdown must not wait for a Done receiver after a ticker worker has already
// observed enable=false and exited.
func Test_agent_ShutdownAfterPingWorkerExited(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.agentGrpc = &agentGrpc{agent: agent}
	agent.statChan = make(chan *pb.PStatMessage)
	agent.urlStatChan = make(chan *urlStat)

	agent.enable.Store(false)
	agent.workerWg.Add(1)
	go agent.superviseWorker("ping", agent.sendPingWorker)
	if !waitTimeout(&agent.workerWg, time.Second) {
		t.Fatal("ping worker did not exit")
	}

	agent.config.offGrpc = false
	agent.enable.Store(true)
	done := make(chan struct{})
	go func() {
		agent.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Shutdown blocked signaling an exited worker")
	}
}

// A worker stuck on an unreachable collector must not hold Shutdown forever.
// startTestWorker spawns body as a supervised worker the way startWorkers
// does, state slot included, so shutdownAgent waits for it.
func startTestWorker(agent *agent, name string, body func()) {
	agent.workerStates = append(agent.workerStates, &workerState{name: name, done: make(chan struct{})})
	agent.workerWg.Add(1)
	go agent.superviseWorker(name, body)
}

func Test_agent_ShutdownDeadline(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("test"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)

	stuck := make(chan struct{})
	defer close(stuck)
	startTestWorker(agent, "stuck", func() { <-stuck })

	start := time.Now()
	a.Shutdown()
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, shutdownTimeout, "waits for the deadline")
	assert.Less(t, elapsed, shutdownTimeout+2*time.Second, "gives up at the deadline")
}

// Shutdown is serialized, so a concurrent second call waits for the first.
func Test_agent_ShutdownIsSerialized(t *testing.T) {
	c, _ := NewConfig(WithAppName("test"))
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)

	stuck := make(chan struct{})
	defer close(stuck)
	startTestWorker(agent, "stuck", func() { <-stuck })

	start := time.Now()
	second := make(chan time.Duration, 1)
	go func() {
		// Late enough that the first call is already inside its bounded drain.
		time.Sleep(50 * time.Millisecond)
		a.Shutdown()
		second <- time.Since(start)
	}()

	a.Shutdown()
	require.GreaterOrEqual(t, time.Since(start), shutdownTimeout,
		"the first call drains to its deadline")

	select {
	case elapsed := <-second:
		assert.GreaterOrEqual(t, elapsed, shutdownTimeout,
			"a concurrent Shutdown must wait for the teardown, not return into it")
	case <-time.After(2 * time.Second):
		require.FailNow(t, "the concurrent Shutdown never returned")
	}
}

// Shutdown must never sleep on the way out.
func Test_agent_ShutdownNoStartupDelay(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("test"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	a.(*agent).enable.Store(true)

	start := time.Now()
	a.Shutdown()

	assert.Less(t, time.Since(start), time.Second, "no unconditional sleep")
}

// A collector outage keeps registration retrying for as long as the outage
// lasts, so Shutdown must signal before it waits on connectWg - a wait that
// runs first pays its whole timeout during exactly the outage it was meant to
// survive.
func Test_agent_ShutdownDoesNotWaitOutRetryingRegistration(t *testing.T) {
	a := newTestAgent(defaultConfig())
	a.enable.Store(false)
	client := &mockAgentGrpcClient{failures: 1 << 30}
	agentGrpc := &agentGrpc{
		agentConn:          dialReadyConn(t),
		agentClient:        client,
		agent:              a,
		registerRetryDelay: time.Hour,
	}

	a.connectWg.Add(1)
	go func() {
		defer a.connectWg.Done()
		agentGrpc.registerAgentWithRetry()
	}()
	require.Eventually(t, func() bool { return len(client.sentAgentInfo()) == 1 }, time.Second, time.Millisecond)

	start := time.Now()
	a.Shutdown()
	assert.Less(t, time.Since(start), 500*time.Millisecond, "Shutdown sat through the registration retry")
}

// captureWarnLog redirects the agent log to buf until the returned func is called.
func captureWarnLog(buf *bytes.Buffer) func() {
	return captureLogAt(buf, logrus.WarnLevel)
}

// captureLogAt is captureWarnLog at an arbitrary level, for lines below Warn.
// It captures through the extra logger: NewConfig applies Log.Output and
// Log.Level to the default logger while it loads, which would undo a redirect
// of that logger under the test. The extra logger gets every line regardless.
func captureLogAt(buf *bytes.Buffer, level logrus.Level) func() {
	prev := logger.extra()
	capture := logrus.New()
	capture.SetOutput(buf)
	capture.SetLevel(level)
	SetExtraLogger(capture)
	return func() { logger.extraLogger.Store(prev) }
}

func Test_agent_enqueueUrlStatCountsEveryDroppedRecord(t *testing.T) {
	const queueSize, enqueued = 4, 100

	agent := newTestAgent(defaultConfig())
	agent.urlStatChan = make(chan *urlStat, queueSize)
	defer captureWarnLog(&bytes.Buffer{})()

	for i := 0; i < enqueued; i++ {
		agent.enqueueUrlStat(&urlStat{})
	}

	close(agent.urlStatChan)
	queued := 0
	for range agent.urlStatChan {
		queued++
	}

	// Nothing drained the queue while it filled, so every record that is not
	// still sitting in it was dropped: the oldest one evicted by each overflow.
	assert.Equal(t, int64(enqueued-queued), agent.urlStatDrops.dropped.Load(),
		"drop counter must account for every record that never reached the consumer")
	assert.Equal(t, queueSize, queued, "test must leave the queue full")
}

// Each overflow head-drops the oldest record and queues the new one, so a full
// queue costs exactly one record per enqueue and holds the newest records.
func Test_agent_enqueueUrlStatOverflowLosesExactlyOneRecord(t *testing.T) {
	const queueSize, enqueued = 4, 100

	agent := newTestAgent(defaultConfig())
	agent.urlStatChan = make(chan *urlStat, queueSize)
	defer captureWarnLog(&bytes.Buffer{})()

	stats := make([]*urlStat, enqueued)
	for i := range stats {
		stats[i] = &urlStat{}
		assert.True(t, agent.enqueueUrlStat(stats[i]), "a head-drop must make room for the new record")
	}

	assert.EqualValues(t, enqueued-queueSize, agent.urlStatDrops.dropped.Load(),
		"one record lost per overflow")
	close(agent.urlStatChan)
	i := enqueued - queueSize
	for stat := range agent.urlStatChan {
		assert.Same(t, stats[i], stat, "the newest records survive")
		i++
	}
	assert.Equal(t, enqueued, i)
}

func Test_agent_enqueueUrlStatCountsDropsFromConcurrentProducers(t *testing.T) {
	const producers, perProducer = 8, 250

	agent := newTestAgent(defaultConfig())
	agent.urlStatChan = make(chan *urlStat, 4)
	defer captureWarnLog(&bytes.Buffer{})()

	var wg sync.WaitGroup
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perProducer; j++ {
				agent.enqueueUrlStat(&urlStat{})
			}
		}()
	}
	wg.Wait()

	close(agent.urlStatChan)
	queued := 0
	for range agent.urlStatChan {
		queued++
	}

	assert.Equal(t, int64(producers*perProducer-queued), agent.urlStatDrops.dropped.Load())
}

func Test_agent_enqueueUrlStatRateLimitsOverflowWarning(t *testing.T) {
	const queueSize, enqueued = 4, 100

	agent := newTestAgent(defaultConfig())
	agent.urlStatChan = make(chan *urlStat, queueSize)

	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	for i := 0; i < enqueued; i++ {
		agent.enqueueUrlStat(&urlStat{})
	}

	assert.EqualValues(t, enqueued-queueSize, agent.urlStatDrops.dropped.Load(), "test did not saturate the queue")
	assert.Equal(t, 1, strings.Count(buf.String(), "url stat queue overflow"),
		"a saturated queue must warn once per report interval, not once per dropped record")

	// The next drop after the report interval elapses warns again, carrying the
	// running total rather than restarting the count.
	agent.urlStatDrops.reportAt.Store(0)
	agent.enqueueUrlStat(&urlStat{})

	assert.Equal(t, 2, strings.Count(buf.String(), "url stat queue overflow"))
	assert.Contains(t, buf.String(),
		fmt.Sprintf("%d dropped in total (oldest overwritten, max queue size %d)",
			agent.urlStatDrops.dropped.Load(), queueSize))
}

// Shutdown must not close the channels its producers send on. The producers
// (request-path goroutines for meta and url stat, ticker workers for stat)
// only check enable before sending, so a close races them into a "send on
// closed channel" panic - a raw send models a producer that passed that check
// just before Shutdown flipped it. Shutdown still stops consumers without
// closing producer channels.
func Test_agent_ShutdownDoesNotCloseProducerChannels(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.statChan = make(chan *pb.PStatMessage, 1)
	agent.urlStatChan = make(chan *urlStat, 1)

	agent.workerWg.Add(2)
	go agent.superviseWorker("meta", agent.sendMetaWorker)
	go agent.superviseWorker("collect uri stat", agent.collectUrlStatWorker)

	start := time.Now()
	agent.Shutdown()
	assert.Less(t, time.Since(start), shutdownTimeout, "consumers must stop on the shutdown signal")

	assert.NotPanics(t, func() { agent.metaChan <- stringMeta{} }, "metaChan")
	assert.NotPanics(t, func() { agent.statChan <- &pb.PStatMessage{} }, "statChan")
	assert.NotPanics(t, func() { agent.urlStatChan <- &urlStat{} }, "urlStatChan")
}

// With every permit held by a slow send, the worker parks on the permit
// acquisition; that wait must obey the stop signal, or the worker dispatches
// one more send after shutdown began.
func Test_agent_sendMetaWorkerStopsWhileAllPermitsHeld(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	blocking := &blockingMetaClient{release: make(chan struct{})}
	agent.agentGrpc = &agentGrpc{metaClient: blocking, agent: agent}

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	for i := 0; i < metaMaxConcurrentRequests; i++ {
		agent.metaChan <- stringMeta{id: int32(i), funcName: "f"}
	}
	assert.Eventually(t, func() bool { return blocking.inFlight() == metaMaxConcurrentRequests },
		5*time.Second, time.Millisecond, "all permits held")

	// One more item: the worker pulls it and parks on the permit acquisition.
	agent.metaChan <- stringMeta{id: 99, funcName: "f"}
	assert.Eventually(t, func() bool { return len(agent.metaChan) == 0 },
		5*time.Second, time.Millisecond, "worker pulled the extra item")

	agent.signalShutdown()
	close(blocking.release)

	assert.True(t, waitTimeout(&agent.workerWg, 5*time.Second), "worker exits")
	_, total := blocking.stats()
	assert.Equal(t, metaMaxConcurrentRequests, total, "no send dispatched after the stop signal")
}

// An agent that never finished registration must still release the global, so
// GetAgent stops handing out the dead agent and NewAgent can be retried.
func Test_agent_ShutdownReleasesGlobalWhenNeverRegistered(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("test"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, err := NewAgent(c)
	assert.NoError(t, err, "new agent")
	assert.False(t, a.(*agent).enable.Load(), "never registered")

	a.Shutdown()
	assert.Equal(t, NoopAgent(), GetAgent(), "global agent released")

	// A second Shutdown must not panic, nor unseat the agent created after it.
	c2, _ := NewConfig(opts...)
	c2.offGrpc = true
	a2, err := NewAgent(c2)
	assert.NoError(t, err, "agent creation can be retried")
	a.Shutdown()
	assert.Equal(t, a2, GetAgent(), "stale shutdown leaves the new agent alone")
	a2.Shutdown()
}

func Test_agent_GetAgentIsRaceFreeAgainstShutdown(t *testing.T) {
	c, _ := NewConfig(WithAppName("test"))
	c.offGrpc = true
	a, err := NewAgent(c)
	assert.NoError(t, err, "new agent")
	a.(*agent).enable.Store(true)

	// Request-path readers concurrent with the Shutdown swap; run under -race.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			GetAgent().Enable()
		}
	}()
	a.Shutdown()
	wg.Wait()

	assert.Equal(t, NoopAgent(), GetAgent(), "global agent released")
}

// A metadata item dropped by a full queue must not stay cached: its id was
// already handed to spans, so the entry has to be re-registered rather than
// left pointing at an id the collector never received. The queue refuses the
// newcomer (Java GrpcDataSender.send, C++ GrpcMetadata::enqueueMeta), so the
// item that loses its cache entry is the one just registered; the queued ones
// keep theirs.
func Test_agent_MetaCacheDropsEntryWhenQueueIsFull(t *testing.T) {
	a := newTestAgent(defaultConfig())
	a.metaChan = make(chan interface{}, 2)

	oldest := a.cacheError("oldest")
	a.cacheError("filler")

	first := a.cacheError("boom")
	second := a.cacheError("boom")
	assert.NotZero(t, first, "id minted")
	assert.NotEqual(t, first, second, "the refused item is re-registered with a new id")
	assert.Equal(t, oldest, a.cacheError("oldest"),
		"the queued item stays cached")
}

// One overflow costs exactly one cache entry: the newcomer's. The head of the
// queue has been reused by every span that hit its entry while the pipeline
// stalled, so evicting it would orphan the most-referenced id.
func Test_agent_MetaOverflowInvalidatesOneCacheEntryPerOverflow(t *testing.T) {
	const queueSize = 4

	a := newTestAgent(defaultConfig())
	a.metaChan = make(chan interface{}, queueSize)

	names := make([]string, queueSize)
	for i := range names {
		names[i] = fmt.Sprintf("error-%d", i)
		a.cacheError(names[i])
	}
	assert.Len(t, a.metaChan, queueSize, "test must fill the queue")

	a.cacheError("newcomer")

	for _, name := range names {
		_, ok := a.errorCache.peek(name)
		assert.True(t, ok, "%s: a queued item keeps its cache entry", name)
	}
	assert.EqualValues(t, 1, a.metaDrops.dropped.Load())
	_, cached := a.errorCache.peek("newcomer")
	assert.False(t, cached, "the refused newcomer loses its entry and is re-registered next time")
	assert.Len(t, a.metaChan, queueSize, "the queue is untouched")
}

// The same invariant over a long run: every item that never reached the
// consumer lost its cache entry, and nothing else did.
func Test_agent_MetaOverflowInvalidatesOneCacheEntryPerDrop(t *testing.T) {
	const queueSize, enqueued = 4, 100

	a := newTestAgent(defaultConfig())
	a.metaChan = make(chan interface{}, queueSize)

	for i := 0; i < enqueued; i++ {
		a.cacheError(fmt.Sprintf("error-%d", i))
	}
	assert.Len(t, a.metaChan, queueSize, "the queue stays full")

	close(a.metaChan)
	queued := 0
	for range a.metaChan {
		queued++
	}

	// Nothing drained the queue while it filled, so every item that is not
	// still sitting in it was dropped - and every dropped item lost its cache
	// entry, no more and no less.
	assert.Equal(t, int64(enqueued-queued), a.metaDrops.dropped.Load(),
		"one drop per item that never reached the consumer")

	cached := 0
	for i := 0; i < enqueued; i++ {
		if _, ok := a.errorCache.peek(fmt.Sprintf("error-%d", i)); ok {
			cached++
		}
	}
	assert.Equal(t, queued, cached,
		"cache invalidations must match drops 1:1, not double-count them")
}

// Concurrent producers must not lose more cache entries than items: whichever
// producer wins the freed slot, the loser drops exactly its own item.
func Test_agent_MetaOverflowCountsDropsFromConcurrentProducers(t *testing.T) {
	const producers, perProducer = 8, 250

	a := newTestAgent(defaultConfig())
	a.metaChan = make(chan interface{}, 4)

	var wg sync.WaitGroup
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for j := 0; j < perProducer; j++ {
				a.cacheError(fmt.Sprintf("error-%d-%d", p, j))
			}
		}(i)
	}
	wg.Wait()

	close(a.metaChan)
	queued := 0
	for range a.metaChan {
		queued++
	}

	assert.Equal(t, int64(producers*perProducer-queued), a.metaDrops.dropped.Load())

	cached := 0
	for p := 0; p < producers; p++ {
		for j := 0; j < perProducer; j++ {
			if _, ok := a.errorCache.peek(fmt.Sprintf("error-%d-%d", p, j)); ok {
				cached++
			}
		}
	}
	assert.Equal(t, queued, cached, "one cache invalidation per dropped item")
}

// The overflow warning is rate-limited and comes from the consumer, so a
// saturated queue costs the request path a counter bump and nothing else.
func Test_agent_MetaOverflowRateLimitsWarning(t *testing.T) {
	const queueSize, enqueued = 4, 100

	a := newTestAgent(defaultConfig())
	a.metaChan = make(chan interface{}, queueSize)

	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	for i := 0; i < enqueued; i++ {
		a.cacheError(fmt.Sprintf("error-%d", i))
	}
	assert.Greater(t, a.metaDrops.dropped.Load(), int64(1), "test did not saturate the queue")
	assert.Empty(t, buf.String(), "the producer path must not log")

	a.metaDrops.report("meta", queueSize)
	a.metaDrops.report("meta", queueSize)
	assert.Equal(t, 1, strings.Count(buf.String(), "meta queue overflow"),
		"a saturated queue must warn once per report interval, not once per drop")
	assert.Contains(t, buf.String(),
		fmt.Sprintf("%d dropped in total (oldest overwritten, max queue size %d)",
			a.metaDrops.dropped.Load(), queueSize))

	// Once the interval elapses, a report with no new drops stays silent.
	a.metaDrops.reportAt.Store(0)
	a.metaDrops.report("meta", queueSize)
	assert.Equal(t, 1, strings.Count(buf.String(), "meta queue overflow"))
}

// shortWorkerRestartDelay shortens the supervisor's restart pacing for the
// duration of a test.
func shortWorkerRestartDelay(t *testing.T) {
	prev := workerRestartDelay
	workerRestartDelay = 10 * time.Millisecond
	t.Cleanup(func() { workerRestartDelay = prev })
}

// An agent bug must not take the host process down: a worker body that panics
// is recovered and the worker is restarted after the delay, then stops
// normally on the shutdown signal.
func Test_agent_superviseWorkerRecoversAndRestarts(t *testing.T) {
	shortWorkerRestartDelay(t)
	agent := newTestAgent(defaultConfig())
	stop := agent.stopSignal().Done()

	var runs atomic.Int32
	agent.workerWg.Add(1)
	go agent.superviseWorker("test", func() {
		if runs.Add(1) == 1 {
			panic("worker bug")
		}
		<-stop
	})

	assert.Eventually(t, func() bool { return runs.Load() == 2 },
		5*time.Second, time.Millisecond, "worker must be restarted after the panic")

	agent.signalShutdown()
	assert.True(t, waitTimeout(&agent.workerWg, 5*time.Second), "worker exits on the stop signal")
	assert.EqualValues(t, 2, runs.Load(), "a normal return is not restarted")
}

// A panic while the agent is stopping ends the worker like a normal return:
// no restart, whether shutdown is seen through the stop signal or the enable
// flag.
func Test_agent_superviseWorkerDoesNotRestartWhileStopping(t *testing.T) {
	for name, stopping := range map[string]func(*agent){
		"stop signal": func(a *agent) { a.signalShutdown() },
		"disabled":    func(a *agent) { a.enable.Store(false) },
	} {
		t.Run(name, func(t *testing.T) {
			shortWorkerRestartDelay(t)
			agent := newTestAgent(defaultConfig())

			var runs atomic.Int32
			agent.workerWg.Add(1)
			go agent.superviseWorker("test", func() {
				runs.Add(1)
				stopping(agent)
				panic("worker bug during shutdown")
			})

			assert.True(t, waitTimeout(&agent.workerWg, 5*time.Second), "a panic while stopping must end the worker")
			assert.EqualValues(t, 1, runs.Load(), "worker must not be restarted while stopping")
		})
	}
}

// workerTableCases are the configuration combinations that select different
// rows of the worker table: span vs span batch, and whether the agent info
// refresh worker runs.
var workerTableCases = []struct {
	name            string
	spanBatch       bool
	refreshInterval int
	want            []string
}{
	{"batch on, refresh on", true, 1000, []string{"ping", "span batch", "command", "meta", "collect agent stat", "collect uri stat", "send uri stat", "send stats", "agent info refresh"}},
	{"batch on, refresh off", true, 0, []string{"ping", "span batch", "command", "meta", "collect agent stat", "collect uri stat", "send uri stat", "send stats"}},
	{"batch off, refresh on", false, 1000, []string{"ping", "span", "command", "meta", "collect agent stat", "collect uri stat", "send uri stat", "send stats", "agent info refresh"}},
	{"batch off, refresh off", false, 0, []string{"ping", "span", "command", "meta", "collect agent stat", "collect uri stat", "send uri stat", "send stats"}},
}

// workerTableConfig builds a config selecting one worker table case.
func workerTableConfig(spanBatch bool, refreshInterval int) *Config {
	cfg := defaultConfig()
	cfg.Set(CfgSpanBatchEnable, spanBatch)
	cfg.Set(CfgCollectorAgentInfoRefreshInterval, refreshInterval)
	return cfg
}

// activeWorkerNames evaluates the table's predicates the way startWorkers does.
func activeWorkerNames(table []worker) []string {
	var names []string
	for _, w := range table {
		if w.when() {
			names = append(names, w.name)
		}
	}
	return names
}

// stubWorkers keeps the table's names and predicates but replaces every body
// with one that parks on the stop signal and records that it ran, so the
// spawn loop can be exercised without a collector.
func stubWorkers(agent *agent, table []worker, started *atomic.Int32) []worker {
	stop := agent.stopSignal().Done()
	stubs := make([]worker, len(table))
	for i, w := range table {
		stubs[i] = worker{name: w.name, when: w.when, body: func() {
			started.Add(1)
			<-stop
		}}
	}
	return stubs
}

// The table's predicates must reproduce exactly the worker set the old
// hand-written go statements produced, under every configuration that selects
// different rows: span and span batch are mutually exclusive, and agent info
// refresh runs only for a positive interval. Names are the log contract, so
// they are pinned by value and must be unique.
func Test_agent_workerTableSelectsWorkersByConfig(t *testing.T) {
	for _, tc := range workerTableCases {
		t.Run(tc.name, func(t *testing.T) {
			agent := newTestAgent(workerTableConfig(tc.spanBatch, tc.refreshInterval))
			table := agent.workerTable()

			assert.Equal(t, tc.want, activeWorkerNames(table))

			seen := map[string]bool{}
			for _, w := range table {
				assert.False(t, seen[w.name], "duplicate worker name %q", w.name)
				seen[w.name] = true
				assert.NotNil(t, w.body, "%s has no body", w.name)
				assert.NotNil(t, w.when, "%s has no predicate", w.name)
			}
		})
	}
}

// startWorkers must start exactly one goroutine per active table entry, and
// count exactly that many into workerWg: the drain then completes as soon as
// the workers exit. An Add that drifted from the go statements would either
// leave the wait unsatisfied (too large) or panic the WaitGroup (too small).
func Test_agent_startWorkersCountMatchesTable(t *testing.T) {
	for _, tc := range workerTableCases {
		t.Run(tc.name, func(t *testing.T) {
			agent := newTestAgent(workerTableConfig(tc.spanBatch, tc.refreshInterval))
			var started atomic.Int32
			table := agent.workerTable()
			agent.startWorkers(stubWorkers(agent, table, &started))

			want := int32(len(activeWorkerNames(table)))
			require.Eventually(t, func() bool { return started.Load() == want },
				time.Second, time.Millisecond, "every active worker must start")
			// The counter is at the target and every body is parked on the stop
			// signal, so an over-counted Add is the only way this wait fails.
			assert.False(t, waitTimeout(&agent.workerWg, 50*time.Millisecond),
				"workers are still running before the signal")

			agent.signalShutdown()
			assert.True(t, waitTimeout(&agent.workerWg, shutdownTimeout),
				"workerWg must drain once every started worker exits")
			assert.Equal(t, want, started.Load(), "no worker started twice")
		})
	}
}

// Shutdown of an agent running the full worker set, under each configuration,
// must finish inside shutdownTimeout - the direct symptom of a workerWg count
// that exceeds the goroutines is a Shutdown that always waits out its deadline.
func Test_agent_ShutdownDrainsWorkerTableWithinDeadline(t *testing.T) {
	for _, tc := range workerTableCases {
		t.Run(tc.name, func(t *testing.T) {
			agent := newTestAgent(workerTableConfig(tc.spanBatch, tc.refreshInterval))
			agent.config.offGrpc = false
			var started atomic.Int32
			table := agent.workerTable()
			agent.startWorkers(stubWorkers(agent, table, &started))
			require.Eventually(t, func() bool { return int(started.Load()) == len(activeWorkerNames(table)) },
				time.Second, time.Millisecond)

			start := time.Now()
			agent.Shutdown()
			assert.Less(t, time.Since(start), shutdownTimeout, "Shutdown waited out the deadline")
			assert.True(t, waitTimeout(&agent.workerWg, time.Second), "every worker slot released")
		})
	}
}

// shortShutdownTimeout shortens Shutdown's worker drain deadline for the
// duration of a test.
func shortShutdownTimeout(t *testing.T) {
	prev := shutdownTimeout
	shutdownTimeout = 20 * time.Millisecond
	t.Cleanup(func() { shutdownTimeout = prev })
}

// stuckWorkers is stubWorkers with the named workers parked on release
// instead of the stop signal, so they outlive the shutdown deadline.
func stuckWorkers(agent *agent, table []worker, started *atomic.Int32, release chan struct{}, stuck ...string) []worker {
	stubs := stubWorkers(agent, table, started)
	for i := range stubs {
		for _, name := range stuck {
			if stubs[i].name == name {
				stubs[i].body = func() {
					started.Add(1)
					<-release
				}
			}
		}
	}
	return stubs
}

// A Shutdown that overruns its deadline must name the workers still running:
// a WaitGroup alone cannot say which - or even how many - are left, and the
// names are the only lead for investigating a slow shutdown.
func Test_agent_ShutdownTimeoutNamesRunningWorkers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stuck []string
	}{
		{"one worker", []string{"send stats"}},
		{"agent info refresh", []string{"agent info refresh"}},
		{"two workers", []string{"ping", "agent info refresh"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortShutdownTimeout(t)
			agent := newTestAgent(workerTableConfig(true, 1000))
			agent.config.offGrpc = false
			release := make(chan struct{})
			defer close(release)

			var started atomic.Int32
			table := agent.workerTable()
			agent.startWorkers(stuckWorkers(agent, table, &started, release, tc.stuck...))
			require.Eventually(t, func() bool { return int(started.Load()) == len(activeWorkerNames(table)) },
				time.Second, time.Millisecond)

			var buf bytes.Buffer
			restore := captureWarnLog(&buf)
			agent.Shutdown()
			restore()

			require.Contains(t, buf.String(), "shutdown timeout", "the deadline must have been exceeded")
			for _, name := range tc.stuck {
				assert.Contains(t, buf.String(), name, "the stuck worker must be named")
			}
			// Exactly the stuck workers, none of the ones that exited in time.
			// logrus quotes the message: ... workers: a, b" module=pinpoint ...
			const marker = "abandon in-flight workers: "
			rest := buf.String()[strings.Index(buf.String(), marker)+len(marker):]
			listed := rest[:strings.IndexByte(rest, '"')]
			assert.ElementsMatch(t, tc.stuck, strings.Split(listed, ", "))
		})
	}
}

// A Shutdown that drains inside its deadline logs nothing about workers.
func Test_agent_ShutdownInTimeLogsNoWorkerNames(t *testing.T) {
	agent := newTestAgent(workerTableConfig(true, 1000))
	agent.config.offGrpc = false
	var started atomic.Int32
	table := agent.workerTable()
	agent.startWorkers(stubWorkers(agent, table, &started))
	require.Eventually(t, func() bool { return int(started.Load()) == len(activeWorkerNames(table)) },
		time.Second, time.Millisecond)

	var buf bytes.Buffer
	restore := captureWarnLog(&buf)
	agent.Shutdown()
	restore()

	assert.NotContains(t, buf.String(), "shutdown timeout")
	assert.NotContains(t, buf.String(), "in-flight workers")
	assert.Empty(t, agent.runningWorkerNames(), "every started worker has released its flag")
}

// A panic inside a metadata send must not escape the per-item goroutine: the
// worker keeps running and still exits cleanly on shutdown.
func Test_agent_sendMetaWorkerSurvivesPanicInSend(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.agentGrpc = nil // every send dereferences it: nil pointer panic

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	agent.metaChan <- stringMeta{id: 1, funcName: "f"}
	assert.Eventually(t, func() bool { return len(agent.metaChan) == 0 },
		5*time.Second, time.Millisecond, "worker pulled the item")

	agent.signalShutdown()
	assert.True(t, waitTimeout(&agent.workerWg, 5*time.Second), "worker exits after the recovered send panic")
}

// shortDropReportInterval shortens the overflow warning's rate limit for the
// duration of a test.
func shortDropReportInterval(t *testing.T, d time.Duration) {
	prev := dropReportInterval
	dropReportInterval = d
	t.Cleanup(func() { dropReportInterval = prev })
}

// The reporter's whole job is the rate limit: repeated reports inside one
// interval collapse to a single warning, a report with nothing new to say
// stays silent, and the total it carries keeps accumulating across intervals
// rather than restarting.
func Test_dropReporter_rateLimitsAndAccumulates(t *testing.T) {
	shortDropReportInterval(t, time.Hour) // only the explicit reportAt resets advance time

	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	var r dropReporter

	// Nothing dropped yet: no warning, and no interval consumed either.
	r.report("test", 8)
	assert.Empty(t, buf.String(), "a reporter with no drops must stay silent")

	r.record(3)
	for i := 0; i < 10; i++ {
		r.report("test", 8)
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "test queue overflow"),
		"repeated reports inside one interval must warn once")
	assert.Contains(t, buf.String(), "3 dropped in total (oldest overwritten, max queue size 8)")

	// Interval elapsed but no new drops: still silent.
	r.reportAt.Store(0)
	r.report("test", 8)
	assert.Equal(t, 1, strings.Count(buf.String(), "test queue overflow"),
		"an elapsed interval with no new drops must not warn")

	// New drops after the interval carry the running total, not a fresh count.
	r.record(4)
	r.report("test", 8)
	assert.Equal(t, 2, strings.Count(buf.String(), "test queue overflow"))
	assert.Contains(t, buf.String(), "7 dropped in total (oldest overwritten, max queue size 8)")
	assert.EqualValues(t, 7, r.dropped.Load(), "the total must never be reset by a report")
}

// enqueueStat counts what a full queue costs: the rejected snapshot plus the
// queued one evicted to make room for the next.
func Test_agent_enqueueStatCountsEveryDroppedRecord(t *testing.T) {
	const queueSize, enqueued = 4, 100

	agent := newTestAgent(defaultConfig())
	agent.statChan = make(chan *pb.PStatMessage, queueSize)

	for i := 0; i < enqueued; i++ {
		agent.enqueueStat(&pb.PStatMessage{})
	}
	close(agent.statChan)
	queued := 0
	for range agent.statChan {
		queued++
	}

	// Nothing drained the queue while it filled, so every record that is not
	// still sitting in it was lost: the oldest one evicted by each overflow.
	assert.EqualValues(t, enqueued-queued, agent.statDrops.dropped.Load(),
		"every record the collector will never see must be counted once")
	assert.Equal(t, queueSize, queued, "test must leave the queue full")
}

// Each overflow head-drops the oldest record and queues the new one, so a full
// queue costs exactly one record per enqueue and holds the newest records.
func Test_agent_enqueueStatOverflowLosesExactlyOneRecord(t *testing.T) {
	const queueSize, enqueued = 4, 100

	agent := newTestAgent(defaultConfig())
	agent.statChan = make(chan *pb.PStatMessage, queueSize)
	defer captureWarnLog(&bytes.Buffer{})()

	stats := make([]*pb.PStatMessage, enqueued)
	for i := range stats {
		stats[i] = &pb.PStatMessage{}
		assert.True(t, agent.enqueueStat(stats[i]), "a head-drop must make room for the new record")
	}

	assert.EqualValues(t, enqueued-queueSize, agent.statDrops.dropped.Load(),
		"one record lost per overflow")
	close(agent.statChan)
	i := enqueued - queueSize
	for stat := range agent.statChan {
		assert.Same(t, stats[i], stat, "the newest records survive")
		i++
	}
	assert.Equal(t, enqueued, i)
}

// The producer reports queue overflow even while the collector is unavailable.
func Test_agent_enqueueStatWarnsWithoutAConsumer(t *testing.T) {
	const queueSize, enqueued = 4, 100

	agent := newTestAgent(defaultConfig())
	agent.statChan = make(chan *pb.PStatMessage, queueSize)

	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	for i := 0; i < enqueued; i++ {
		agent.enqueueStat(&pb.PStatMessage{})
	}

	assert.EqualValues(t, enqueued-queueSize, agent.statDrops.dropped.Load(), "test did not saturate the queue")
	assert.Equal(t, 1, strings.Count(buf.String(), "stat queue overflow"),
		"a saturated queue must warn once per report interval, not once per dropped record")
	assert.Contains(t, buf.String(), fmt.Sprintf("max queue size %d", queueSize))
}

func Test_sqlUid_MatchesJavaGuavaMurmur3_128(t *testing.T) {
	// Golden values were computed with Guava 33.6.0 Hashing.murmur3_128().hashBytes(sql.getBytes(UTF_8)).asBytes()
	tests := []struct {
		name string
		sql  string
		hex  string
	}{
		{"normalized sql", "select * from t where a = 0#", "d54242c8a741d4cc7bae4fa28d0c1ef1"},
		{"empty", "", "00000000000000000000000000000000"},
		{"multibyte", "select * from 테이블 where 이름 = 0#", "1bedb2a46cf838f0366394c28a886411"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.hex, hex.EncodeToString(sqlUid(tt.sql)))
		})
	}
}

// things that must move together: which sampler ran, whether the parent span id
// was adopted, and whether the transaction id was inherited or generated.
//
// Rows 2-4 failed before this table's change: a trace id on its own took the
// continue sampler and left parentSpanId at its default with no parent node in
// the trace.
func Test_agent_continueHeaders_table(t *testing.T) {
	const validTid = "t123456^12345^1"

	tests := []struct {
		name      string
		headers   map[string]string
		continued bool
	}{
		{"tid+spanid+pspanid", map[string]string{HeaderTraceId: validTid, HeaderSpanId: "67890", HeaderParentSpanId: "123"}, true},
		{"tid+pspanid, no spanid", map[string]string{HeaderTraceId: validTid, HeaderParentSpanId: "123"}, false},
		{"tid+spanid, no pspanid", map[string]string{HeaderTraceId: validTid, HeaderSpanId: "67890"}, false},
		{"tid only", map[string]string{HeaderTraceId: validTid}, false},
		{"no tid", map[string]string{HeaderSpanId: "67890", HeaderParentSpanId: "123"}, false},
		{"blank tid", map[string]string{HeaderTraceId: "", HeaderSpanId: "67890", HeaderParentSpanId: "123"}, false},
		{"malformed tid", map[string]string{HeaderTraceId: "garbage", HeaderSpanId: "67890", HeaderParentSpanId: "123"}, false},
		{"malformed spanid", map[string]string{HeaderTraceId: validTid, HeaderSpanId: "garbage", HeaderParentSpanId: "123"}, true},
		// A proxy that blanks a header instead of dropping it still describes
		{"blank spanid", map[string]string{HeaderTraceId: validTid, HeaderSpanId: "", HeaderParentSpanId: "123"}, true},
	}

	// Counter rate 1 samples every new trace, so a span exists to inspect on
	// every row and the stat counters alone say which sampler ran.
	c, _ := NewConfig(
		WithAppName("test"),
		WithSamplingType("COUNTER"),
		WithSamplingCounterRate(1),
	)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)
	defer a.Shutdown()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := agent.stats.readCounters()
			reader := &DistributedTracingContextMap{m: tt.headers}

			_, continued := continueHeaders(reader)
			assert.Equal(t, tt.continued, continued, "continueHeaders")

			tr := agent.NewSpanTracerWithReader("test", "/", reader)
			s, ok := tr.(*span)
			assert.True(t, ok, "sampled span")
			defer tr.EndSpan()

			after := agent.stats.readCounters()
			// (a) which sampler ran
			contSampler := after.sampleCont > before.sampleCont
			newSampler := after.sampleNew > before.sampleNew
			// (b) parent adopted, or root
			adoptedParent := s.parentSpanId != -1
			// (c) transaction id inherited, or generated here
			inheritedTxId := s.txId.AgentId == "t123456"

			assert.Equal(t, tt.continued, contSampler, "continue sampler")
			assert.Equal(t, !tt.continued, newSampler, "new sampler")
			assert.Equal(t, tt.continued, adoptedParent, "parent span id adopted")
			assert.Equal(t, tt.continued, inheritedTxId, "transaction id inherited")

			// The invariant this change exists for: the sampler choice and the
			// context extraction never disagree about which trace this is.
			assert.Equal(t, contSampler, adoptedParent && inheritedTxId,
				"sampler choice and extracted context disagree")

			if tt.continued {
				assert.Equal(t, int64(123), s.parentSpanId, "parent span id")
				assert.NotEqual(t, int64(0), s.spanId, "span id")
			} else {
				assert.Equal(t, agent.agentID, s.txId.AgentId, "generated transaction id")
			}
		})
	}
}

// A blank Pinpoint-pSpanID continues the trace like a blank Pinpoint-SpanID:
// the transaction is inherited. The span stays a root, because an unparseable
// takes (Test_span_Extract_malformedSpanIds), which the shared table cannot
// express since it asserts an adopted parent for every continued row.
func Test_agent_continueHeaders_blankParentSpanId(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	reader := &DistributedTracingContextMap{m: map[string]string{
		HeaderTraceId:      "t123456^12345^1",
		HeaderSpanId:       "67890",
		HeaderParentSpanId: "",
	}}

	txId, continued := continueHeaders(reader)
	assert.True(t, continued, "blank parent span id still describes a hop")
	assert.Equal(t, "t123456", txId.AgentId, "transaction id inherited")

	span := defaultSpan(agent)
	span.Extract(reader)
	assert.Equal(t, "t123456", span.txId.AgentId, "extracted transaction id")
	assert.Equal(t, int64(-1), span.parentSpanId, "unparseable parent span id stays root")
}

// A carrier over a source with no presence information reports a blank header
// as absent, and the request starts a new transaction - the reading every
// carrier had before Get reported presence.
func Test_agent_continueHeaders_valueOnlyCarrier(t *testing.T) {
	blank := map[string]string{
		HeaderTraceId:      "t123456^12345^1",
		HeaderSpanId:       "",
		HeaderParentSpanId: "123",
	}

	_, continued := continueHeaders(valueOnlyCarrier(blank))
	assert.False(t, continued, "a value-only carrier cannot tell blank from absent")

	_, continued = continueHeaders(&DistributedTracingContextMap{m: blank})
	assert.True(t, continued, "the same headers continue from a carrier that can")
}

// net/http.Header is what an http server holds the inbound headers in, and
// HttpHeaderReader is how it reaches the agent - a stdlib type cannot carry the
// interface's two-result Get. Its map keeps a blanked header, which is the
// header shape a proxy breaks the trace with.
func Test_agent_continueHeaders_httpHeaderReader(t *testing.T) {
	h := http.Header{}
	h.Set(HeaderTraceId, "t123456^12345^1")
	h.Set(HeaderParentSpanId, "123")

	_, continued := continueHeaders(HttpHeaderReader(h))
	assert.False(t, continued, "no span id header at all: new transaction")

	h.Set(HeaderSpanId, "")
	txId, continued := continueHeaders(HttpHeaderReader(h))
	assert.True(t, continued, "blank span id header is still a hop")
	assert.Equal(t, "t123456", txId.AgentId, "transaction id inherited")

	// Header names reach the carrier in the case the caller wrote them, not
	// the canonical case the map stores.
	v, ok := HttpHeaderReader(h).Get(HeaderParentSpanId)
	assert.True(t, ok, "a canonicalized key must still be found")
	assert.Equal(t, "123", v)
}

// The headers Inject writes must be readable as a continued trace by the other
// side - proof that Inject really emits all three headers the new check needs.
func Test_agent_continueHeaders_roundTrip(t *testing.T) {
	c, _ := NewConfig(WithAppName("test"))
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)
	defer a.Shutdown()

	caller := agent.NewSpanTracer("test", "/")
	caller.NewSpanEvent("call")
	m := map[string]string{}
	caller.Inject(&DistributedTracingContextMap{m})
	caller.EndSpanEvent()
	caller.EndSpan()

	txId, continued := continueHeaders(&DistributedTracingContextMap{m})
	assert.True(t, continued, "injected headers must continue the trace: %v", m)
	assert.Equal(t, caller.TransactionId(), txId, "transaction id")
}

// The normalized text is what both caches key on and what every queued meta
// carries in key, so it is bounded here too: literal-heavy SQL normalizes
// larger than it came in, and a key past maxSqlNormalizeLength is refused
// rather than admitted to the cache or metaChan.
func Test_agent_SQLCachesRefuseAKeyPastTheNormalizationCap(t *testing.T) {
	within := strings.Repeat("x", maxSqlNormalizeLength)
	past := within + "x"

	t.Run("sql id", func(t *testing.T) {
		a := newTestAgent(defaultConfig())
		assert.Equal(t, int32(0), a.cacheSql(past), "no id for a key past the cap")
		_, cached := a.sqlCache.peek(past)
		assert.False(t, cached)
		assert.Empty(t, a.metaChan, "nothing queued for a refused key")

		assert.NotEqual(t, int32(0), a.cacheSql(within), "a key at the cap is admitted")
	})

	t.Run("sql uid", func(t *testing.T) {
		a := newTestAgent(noSqlCacheBypassConfig())
		assert.Nil(t, a.cacheSqlUid(past), "no uid for a key past the cap")
		_, cached := a.sqlUidCache.peek(past)
		assert.False(t, cached)
		assert.Empty(t, a.metaChan, "nothing queued for a refused key")

		assert.Equal(t, sqlUid(within), a.cacheSqlUid(within), "a key at the cap is admitted")
	})
}

// shutdownCounter is an Agent whose Shutdown only counts its calls, so the
// signal tests can observe the call without a collector.
type shutdownCounter struct {
	Agent
	calls atomic.Int32
}

func (a *shutdownCounter) Shutdown() { a.calls.Add(1) }

// replaceRaiseSignal swaps the re-raise for a recorder. The tests deliver
// SIGUSR1, whose default disposition terminates the process: a real re-raise
// after signal.Stop would kill the test binary.
func replaceRaiseSignal(t *testing.T) *[]os.Signal {
	t.Helper()
	var mu sync.Mutex
	raised := &[]os.Signal{}
	orig := raiseSignal
	raiseSignal = func(sig os.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		*raised = append(*raised, sig)
		return nil
	}
	t.Cleanup(func() { raiseSignal = orig })
	return raised
}

// A watched signal must run Shutdown and then be re-raised, so the process
// still dies of it with the usual 128+signum status once the agent is down.
func Test_ShutdownOnSignal_ShutsDownAndReRaises(t *testing.T) {
	raised := replaceRaiseSignal(t)
	a := &shutdownCounter{Agent: NoopAgent()}

	stop := ShutdownOnSignal(a, syscall.SIGUSR1)
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGUSR1))

	assert.Eventually(t, func() bool { return a.calls.Load() == 1 },
		5*time.Second, time.Millisecond, "Shutdown called on the signal")
	stop() // returns once the watcher goroutine has exited
	assert.Equal(t, []os.Signal{syscall.SIGUSR1}, *raised, "the signal is re-raised after Shutdown")
}

// After the returned stop, a later signal must not reach Shutdown, and the
// watcher goroutine must be gone. The test keeps its own Notify on SIGUSR1 so
// signal.Stop does not restore the default disposition, which would kill the
// process; it also shows stop leaves the host's channel untouched.
func Test_ShutdownOnSignal_StopEndsTheWatch(t *testing.T) {
	raised := replaceRaiseSignal(t)
	host := make(chan os.Signal, 1)
	signal.Notify(host, syscall.SIGUSR1)
	defer signal.Stop(host)

	a := &shutdownCounter{Agent: NoopAgent()}
	before := runtime.NumGoroutine()
	stop := ShutdownOnSignal(a, syscall.SIGUSR1)
	stop()
	stop() // idempotent
	// stop waited on the watcher's exit channel, so only the goroutine's own
	// final return can still be outstanding. Polled by hand: assert.Eventually
	// runs its condition on a goroutine of its own, which would skew the count.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), before, "the watcher goroutine exits on stop")

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGUSR1))
	select {
	case <-host:
	case <-time.After(5 * time.Second):
		t.Fatal("the host's own channel must still receive the signal")
	}
	assert.Zero(t, a.calls.Load(), "no Shutdown after stop")
	assert.Empty(t, *raised, "nothing re-raised after stop")
}

// Signal handling is opt-in: an agent that is created, used and shut down
// without ShutdownOnSignal must never call signal.Notify, because Notify
// changes the process-wide disposition of the signals it is given.
func Test_agent_DefaultNeverCallsSignalNotify(t *testing.T) {
	var notifies atomic.Int32
	orig := signalNotify
	signalNotify = func(chan<- os.Signal, ...os.Signal) { notifies.Add(1) }
	defer func() { signalNotify = orig }()

	c, _ := NewConfig(WithAppName("test"))
	c.offGrpc = true
	a, err := NewAgent(c)
	require.NoError(t, err, "new agent")
	a.NewSpanTracer("op", "/rpc").EndSpan()
	a.Shutdown()

	assert.Zero(t, notifies.Load(), "signal.Notify must not be called unless ShutdownOnSignal is used")

	// And the opt-in is what calls it, with exactly the signals given.
	stop := ShutdownOnSignal(a, syscall.SIGUSR1)
	stop()
	assert.Equal(t, int32(1), notifies.Load(), "ShutdownOnSignal is the only caller")
}

// The signal path and the host's own deferred Shutdown may both run; the
// teardown is serialized by shutdownOnce, so the second call is a no-op.
func Test_agent_ShutdownTwiceViaSignalAndCall(t *testing.T) {
	replaceRaiseSignal(t)
	c, _ := NewConfig(WithAppName("test"))
	c.offGrpc = true
	a, err := NewAgent(c)
	require.NoError(t, err, "new agent")

	stop := ShutdownOnSignal(a, syscall.SIGUSR1)
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGUSR1))
	assert.NotPanics(t, a.Shutdown, "explicit Shutdown concurrent with the signal path")
	stop()
	assert.NotPanics(t, a.Shutdown, "a third call after both is still safe")
	assert.Equal(t, NoopAgent(), GetAgent(), "global agent released exactly once")
}

// Shutdown must send what is still queued: this is the loss ShutdownOnSignal
// exists to prevent, and the reason the docs insist on calling Shutdown.
func Test_agent_ShutdownSendsQueuedSpans(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.spanGrpc = newMockSpanGrpc(agent)
	client := agent.spanGrpc.spanClient.(*mockSpanGrpcClient)

	startTestWorker(agent, "span batch", agent.sendSpanBatchWorker)

	const spans = 3
	for i := 0; i < spans; i++ {
		span := defaultSpan(agent)
		span.spanId = int64(i + 1)
		require.True(t, agent.enqueueSpan(span.newEventChunk(true)))
	}
	agent.Shutdown()

	sent := 0
	for _, req := range client.requests {
		sent += len(req.GetSpan())
	}
	assert.Equal(t, spans, sent, "every span queued before Shutdown reaches the collector")
}

// A Shutdown that overruns its deadline leaves only the stuck worker behind.
func Test_agent_ShutdownTimeoutLeavesOnlyTheStuckWorker(t *testing.T) {
	shortShutdownTimeout(t)
	release := make(chan struct{})
	var agents []*agent

	runtime.GC()
	before := runtime.NumGoroutine()
	const cycles = 8
	for i := 0; i < cycles; i++ {
		agent := newTestAgent(workerTableConfig(true, 1000))
		agent.config.offGrpc = false
		var started atomic.Int32
		table := agent.workerTable()
		agent.startWorkers(stuckWorkers(agent, table, &started, release, "send stats"))
		require.Eventually(t, func() bool { return int(started.Load()) == len(activeWorkerNames(table)) },
			time.Second, time.Millisecond)
		agent.Shutdown()
		agents = append(agents, agent)
	}
	// One abandoned worker per cycle is the intended residue; anything on top
	// of that is the wait leaking. Slack for goroutines the runtime or logger
	// may be spinning up or down around the measurement.
	after := runtime.NumGoroutine()
	assert.LessOrEqual(t, after-before, cycles+2, "goroutines grew per overrun beyond the stuck worker")

	close(release)
	for _, agent := range agents {
		require.True(t, waitTimeout(&agent.workerWg, time.Second), "stuck worker did not exit on release")
	}
}
