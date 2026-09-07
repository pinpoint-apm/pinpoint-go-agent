package pinpoint

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_newSpanEvent(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			assert.Equal(t, se.operationName, tt.args.operationName, "operationName")
			assert.Equal(t, se.serviceType, int32(ServiceTypeGoFunction), "serviceType")
			assert.NotNil(t, se.startTime, "startTime")
		})
	}
}

func Test_spanEvent_end(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			tt.args.span.appendSpanEvent(se)
			assert.Equal(t, se.parentSpan.eventDepth.Load(), int32(2), "eventDepth")

			time.Sleep(100 * time.Millisecond)
			se.end()

			assert.Equal(t, se.operationName, tt.args.operationName, "operationName")
			assert.Equal(t, se.parentSpan.eventDepth.Load(), int32(1), "eventDepth")
			assert.Greater(t, se.endElapsed, int64(99), "endElapsed")
		})
	}
}

func Test_spanEvent_generateNextSpanId(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			id := se.generateNextSpanId()
			assert.Equal(t, se.operationName, tt.args.operationName, "operationName")
			assert.Equal(t, se.nextSpanId, id, "nextSpanId")
			assert.NotEqual(t, se.nextSpanId, int64(0), "nextSpanId")

			// the event path must avoid the parent span's ids as well
			se.parentSpan.spanId = 10
			se.parentSpan.parentSpanId = 20
			stubSpanIdGenerator(t, 10, 20, -1, 30)
			assert.Equal(t, int64(30), se.generateNextSpanId(), "nextSpanId on collision")
		})
	}
}

func Test_spanEvent_SetError(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.args.span.agent = newTestAgent(defaultConfig())
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			se.SetError(errors.New("TEST_ERROR"))
			assert.Equal(t, int32(1), se.errorFuncId, "errorFuncId")
			assert.Equal(t, "TEST_ERROR", se.errorString, "errorString")
		})
	}
}

func Test_SetError_AbbreviatesMessage(t *testing.T) {
	long := strings.Repeat("e", 300)
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{"short kept", "short", "short"},
		// Java's StringUtils.abbreviate(message, 256) marks the original size.
		{"long abbreviated", long, strings.Repeat("e", maxErrorMessageSize) + "...(300)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := defaultTestSpan()
			sp.agent = newTestAgent(defaultConfig())
			sp.SetError(errors.New(tt.msg))
			assert.Equal(t, tt.want, sp.errorString, "span errorString")

			se := newSpanEvent(sp, "t1")
			se.SetError(errors.New(tt.msg))
			assert.Equal(t, tt.want, se.errorString, "spanEvent errorString")
		})
	}
}

func Test_spanEvent_SetSQL(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.args.span.agent = newTestAgent(defaultConfig())
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			se.SetSQL("SELECT 1", "")
			assert.Equal(t, len(se.annotations.values), int(1), "annotations.len")
		})
	}
}

func Test_spanEvent_SetSQLBoundsAnnotationValues(t *testing.T) {
	const limit = 32
	cfg := defaultConfig()
	cfg.Set(CfgSQLMaxBindValueSize, limit)
	span := testSpanWithConfig(cfg)
	se := newSpanEvent(span, "query")
	literal := strings.Repeat("l", maxSqlSize+100)
	args := strings.Repeat("a", 1024)

	se.SetSQL("SELECT '"+literal+"'", args)

	assert.Len(t, se.annotations.values, 1)
	annotation := se.annotations.values[0]
	// Only the bind values are bounded: the normalized literal is a parameter
	// the server needs whole, and Java abbreviates neither. The allowance over
	// the limit is the room the bind value writers get for their own marker.
	assert.Equal(t, literal, annotation.s1)
	assert.Equal(t, args[:limit+maxBindValueMarkerSize]+"...(1024)", annotation.s2)
}

// The server rebuilds the raw SQL by splitting param on ',' and indexing into
// it, so param must never be cut by SQL.MaxBindValueSize; only args is.
func Test_spanEvent_SetSQLKeepsParamOfLargeInList(t *testing.T) {
	const limit = 32
	cfg := defaultConfig()
	cfg.Set(CfgSQLMaxBindValueSize, limit)
	se := newSpanEvent(testSpanWithConfig(cfg), "query")
	nums := make([]string, 2000)
	for i := range nums {
		nums[i] = strconv.Itoa(i + 1)
	}
	param := strings.Join(nums, ",")
	args := strings.Repeat("a", 1024)

	se.SetSQL("SELECT * FROM t WHERE id IN ("+param+")", args)

	assert.Len(t, se.annotations.values, 1)
	assert.Equal(t, param, se.annotations.values[0].s1)
	assert.Equal(t, args[:limit+maxBindValueMarkerSize]+"...(1024)", se.annotations.values[0].s2)
}

// A negative SQL.MaxBindValueSize turns bind value tracing off and clamps the
// size to 0. Normalized literals are not bind values, so they must survive
// intact rather than collapse into an "...(0)" marker.
func Test_spanEvent_SetSQLKeepsParamWhenBindSizeIsZero(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSQLMaxBindValueSize, -1)
	se := newSpanEvent(testSpanWithConfig(cfg), "query")

	se.SetSQL("SELECT * FROM t WHERE id = 123", "")

	assert.Len(t, se.annotations.values, 1)
	assert.Equal(t, "123", se.annotations.values[0].s1)
	assert.Equal(t, "", se.annotations.values[0].s2)
}

// SetSQL leaves the size limit to the normalizer and the meta caches, so the
// published sql meta must stay bounded without SetSQL abbreviating again.
func Test_spanEvent_SetSQLPublishesBoundedSqlMeta(t *testing.T) {
	span := defaultTestSpan()
	se := newSpanEvent(span, "query")
	sql := "SELECT " + strings.Repeat("x", maxSqlSize*2)

	se.SetSQL(sql, "")

	var md sqlMeta
	for {
		if m, ok := (<-span.agent.metaChan).(sqlMeta); ok {
			md = m
			break
		}
	}
	assert.Equal(t, abbreviateString(sql, maxSqlSize), md.sql)
}

// SQL.ErrorCount ports the Java agent's DefaultSqlCountService: a span that
// executes the configured number of queries is marked failed, so an N+1 loop
// shows up as an error instead of just a slow trace.
func Test_spanEvent_SetSQLCountMarksFailedSpan(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		sql       string
		queries   int
		finished  bool
		wantErr   int32
		wantCount int32
	}{
		{"disabled", 0, "SELECT 1", 5, false, 0, 0},
		{"below limit", 3, "SELECT 1", 2, false, 0, 2},
		{"at limit", 3, "SELECT 1", 3, false, int32(ErrorCategorySql), 3},
		{"above limit", 3, "SELECT 1", 5, false, int32(ErrorCategorySql), 3},
		{"negative limit", -1, "SELECT 1", 5, false, 0, 0},
		// 1 is the smallest threshold that still counts: the first query marks.
		// Java reaches the same point with a non-positive count, which this
		// option cannot express because 0 means off (doc/config.md SQL.ErrorCount).
		{"limit one", 1, "SELECT 1", 2, false, int32(ErrorCategorySql), 1},
		// commit and rollback events reach SetSQL with no sql at all
		{"empty sql", 3, "", 5, false, 0, 0},
		// the span is already on its way to the sender goroutine
		{"finished span", 3, "SELECT 1", 5, true, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Set(CfgSQLErrorCount, tt.limit)
			sp := testSpanWithConfig(cfg)
			sp.finished.Store(tt.finished)

			for i := 0; i < tt.queries; i++ {
				newSpanEvent(sp, "query").SetSQL(tt.sql, "")
			}

			assert.Equal(t, tt.wantErr, sp.err.Load(), "span err")
			assert.Equal(t, tt.wantCount, sp.sqlCount.Load(), "sqlCount")
		})
	}
}

// Java returns before incrementing when the transaction already has an error
// code, so a failed span never counts and the count never resumes.
func Test_spanEvent_SetSQLCountSkipsFailedSpan(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSQLErrorCount, 3)
	sp := testSpanWithConfig(cfg)

	newSpanEvent(sp, "query").SetSQL("SELECT 1", "")
	assert.Equal(t, int32(1), sp.sqlCount.Load(), "sqlCount")

	newSpanEvent(sp, "query").SetError(errors.New("TEST_ERROR"))
	require.Equal(t, int32(ErrorCategoryException), sp.err.Load(), "span err")

	for i := 0; i < 5; i++ {
		newSpanEvent(sp, "query").SetSQL("SELECT 1", "")
	}
	assert.Equal(t, int32(1), sp.sqlCount.Load(), "sqlCount of a failed span")
}

// SQL.ErrorCount is dynamic, but a span keeps the snapshot it was born with:
// the new limit applies to the spans started after the reload.
func Test_spanEvent_SetSQLCountReloadsDynamically(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSQLErrorCount, 2)
	agent := newTestAgent(cfg)

	pinned := defaultSpan(agent)
	newSpanEvent(pinned, "query").SetSQL("SELECT 1", "")
	require.Equal(t, int32(0), pinned.err.Load(), "span err below the limit")

	cfg.Set(CfgSQLErrorCount, 0)

	newSpanEvent(pinned, "query").SetSQL("SELECT 1", "")
	assert.Equal(t, int32(ErrorCategorySql), pinned.err.Load(), "a live span must keep its pinned limit")

	reloaded := defaultSpan(agent)
	for i := 0; i < 5; i++ {
		newSpanEvent(reloaded, "query").SetSQL("SELECT 1", "")
	}
	assert.Equal(t, int32(0), reloaded.err.Load(), "span err after the count was disabled")
	assert.Equal(t, int32(0), reloaded.sqlCount.Load(), "sqlCount")
}

// A goroutine span event must carry an api id its own agent registered: ids
// come from the per-agent apiIdGen, so a process-global cache would make the
// second agent (Shutdown() + NewAgent()) reuse an id it never published.
func Test_newSpanEventGoroutine_apiIdIsPerAgent(t *testing.T) {
	publishedGoroutineApiId := func(a *agent) int32 {
		for {
			select {
			case md := <-a.metaChan:
				if api, ok := md.(apiMeta); ok && api.descriptor == "Goroutine Invocation" {
					assert.Equal(t, apiTypeInvocation, api.apiType, "apiType")
					return api.id
				}
			default:
				return 0
			}
		}
	}

	for _, name := range []string{"first agent", "second agent"} {
		t.Run(name, func(t *testing.T) {
			s := defaultTestSpan()
			se := newSpanEventGoroutine(s)

			assert.Equal(t, int32(ServiceTypeAsync), se.serviceType, "serviceType")
			assert.NotZero(t, se.apiId, "apiId")
			assert.Equal(t, se.apiId, publishedGoroutineApiId(s.agent), "registered apiId")

			// second event on the same agent reuses the id, publishing nothing
			assert.Equal(t, se.apiId, newSpanEventGoroutine(s).apiId, "cached apiId")
			assert.Zero(t, publishedGoroutineApiId(s.agent), "re-registered apiId")
		})
	}
}

// Exception chain ids are per-agent: a new agent (Shutdown() + NewAgent())
// numbers its chains from 1 instead of continuing the previous agent's count.
func Test_span_getExceptionChainId_isPerAgent(t *testing.T) {
	for _, name := range []string{"first agent", "second agent"} {
		t.Run(name, func(t *testing.T) {
			s := defaultTestSpan()

			id, isNew := s.getExceptionChainId(errors.New("boom"))
			assert.Equal(t, int64(1), id, "first chain id")
			assert.True(t, isNew, "isNew")

			next, _ := s.getExceptionChainId(errors.New("bang"))
			assert.Equal(t, int64(2), next, "second chain id")
		})
	}
}

// A chain the Error.NewThroughput limiter denies carries no exception id and no
// EXCEPTION_CHAIN_ID annotation, as Java's DISABLED sampling state records
// neither. The span is still marked failed either way.
func Test_spanEvent_SetErrorRateLimitsExceptionChain(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgErrorTraceCallStack, true)
	cfg.Set(CfgErrorNewThroughput, 1)

	// Each subtest gets its own span so that span.err has to be set by its own
	// SetError instead of being read back from the previous subtest. The
	// limiter lives in the shared config snapshot, which newTestAgent does not
	// republish, so the burst is still exhausted in order.
	tests := []struct {
		name    string
		sampled bool
	}{
		{"first error is sampled", true},
		{"second error is denied", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := testSpanWithConfig(cfg)
			se := newSpanEvent(span, "query")
			se.SetError(errors.New(tt.name))

			assert.Equal(t, int32(ErrorCategoryException), span.err.Load(), "span failure marking")
			if tt.sampled {
				assert.NotZero(t, se.exceptionId, "exceptionId")
				require.Len(t, se.annotations.values, 1)
				assert.Equal(t, int32(AnnotationExceptionChainId), se.annotations.values[0].key)
				assert.Equal(t, se.exceptionId, se.annotations.values[0].l)
			} else {
				assert.Zero(t, se.exceptionId, "exceptionId")
				assert.Empty(t, se.annotations.values, "annotations")
			}
		})
	}
}

func Test_spanEvent_SetErrorFailsTransaction(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgHttpUrlStatEnable, true)
	agent := newTestAgent(cfg)
	agent.urlStatChan = make(chan *urlStat, 1)
	span := newSampledSpan(agent, "op", "/rpc")
	span.collectUrlStat(&UrlStatEntry{Url: "/users/{id}", Method: "GET"})

	span.NewSpanEvent("query")
	span.SpanEvent().SetError(errors.New("db error"))
	span.EndSpanEvent()
	assert.Equal(t, int32(ErrorCategoryException), span.err.Load())
	assert.Equal(t, int32(0), span.statusErr.Load(), "statusErr stays reserved for SetFailure")

	span.EndSpan()

	chunk, ok := agent.spanQueue.tryDequeue()
	require.True(t, ok)
	builder := acquireSpanMessageBuilder()
	defer releaseSpanMessageBuilder(builder)
	assert.Equal(t, int32(ErrorCategoryException), builder.makePSpan(chunk).GetSpan().GetErr())

	select {
	case stat := <-agent.urlStatChan:
		snapshot, endTime := newUrlStatTestSnapshot(10, false)
		stat.endTime = endTime
		snapshot.add(stat)
		each := findEachUrlStat(t, snapshot, "/users/{id}", endTime)
		assert.Equal(t, int32(1), histogramCount(each.failedHistogram))
	default:
		t.Fatal("url stat not enqueued")
	}

	// A recorder retained past EndSpan must not touch the finished span.
	chunk.eventChunk[0].SetError(errors.New("late"))
	assert.Equal(t, "db error", chunk.eventChunk[0].errorString)
}

// Span.IgnoreErrors: a matched error keeps its exception info but does not
// fail the span (Java profiler.ignore-error-handler).
func Test_SetError_IgnoreErrors(t *testing.T) {
	newConfig := func(rules ...string) *Config {
		c, err := NewConfig(WithAppName("ignoreErrApp"), WithSpanIgnoreErrors(rules...))
		assert.NoError(t, err)
		return c
	}
	notFound := errors.New("user not found")

	tests := []struct {
		name    string
		rules   []string
		err     error
		errName string
		ignored bool
	}{
		{"type and message", []string{"*errors.errorString:not found"}, notFound, "", true},
		{"wrapped error unwrapped", []string{"*errors.errorString:not found"}, fmt.Errorf("get user: %w", notFound), "", true},
		{"message mismatch", []string{"*errors.errorString:timeout"}, notFound, "", false},
		{"type mismatch", []string{"*fs.PathError:not found"}, notFound, "", false},
		{"type only", []string{"*fs.PathError"}, &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist}, "", true},
		{"message only", []string{":not found"}, notFound, "", true},
		{"errorName as type", []string{"panic"}, notFound, "panic", true},
		{"no rules", nil, notFound, "", false},
		{"empty entries", []string{"", " : "}, notFound, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := testSpanWithConfig(newConfig(tt.rules...))
			se := newSpanEvent(span, "t1")
			if tt.errName != "" {
				se.SetError(tt.err, tt.errName)
			} else {
				se.SetError(tt.err)
			}
			assert.Equal(t, int32(1), se.errorFuncId, "errorFuncId")
			assert.Equal(t, tt.err.Error(), se.errorString, "errorString")
			assert.Equal(t, !tt.ignored, span.err.Load() == int32(ErrorCategoryException), "span.err from event")

			// span.SetError records under the error type name.
			if tt.errName == "" {
				span = testSpanWithConfig(newConfig(tt.rules...))
				span.SetError(tt.err)
				assert.Equal(t, tt.err.Error(), span.errorString, "span.errorString")
				assert.Equal(t, !tt.ignored, span.err.Load() == int32(ErrorCategoryException), "span.err")
			}
		})
	}
}

// Run under -race: a plugin marks the span failed from another goroutine of the
// call stack while the sender serializes a chunk the event buffer already
// flushed - the write path this file's setters share with span.err.
func TestSpanEvent_ErrSetterConcurrentWithSenderIsRaceFree(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSQLErrorCount, 1)
	span := testSpanWithConfig(cfg)

	span.NewSpanEvent("event")
	span.EndSpanEvent()
	span.spanEventLock.Lock()
	chunk := span.newEventChunk(false) // what an event buffer overflow flushes
	span.spanEventLock.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			span.NewSpanEvent("query")
			span.SpanEvent().SetSQL("SELECT 1", "")
			span.SpanEvent().SetError(errors.New("late"))
			span.EndSpanEvent()
		}
	}()
	go func() {
		defer wg.Done()
		b := &spanMessageBuilder{}
		for i := 0; i < 100; i++ {
			b.makePSpan(chunk)
		}
	}()
	wg.Wait()

	// The first query reaches the limit of 1 and every event records an error,
	// so both causes are OR-ed into the mask.
	assert.Equal(t, int32(ErrorCategorySql|ErrorCategoryException), span.err.Load(), "span marked failed")
}

// Span.ErrorMarkExclude drops the verdict, not the counting: Java applies the
// enabled-category filter inside the recorder, downstream of
// DefaultSqlCountService, so an operator who does not want an N+1 pattern to
// fail a transaction can drop that one cause and keep every other failure.
func Test_spanEvent_SetSQLCountExcludedCategoryLeavesTheTransactionClean(t *testing.T) {
	cfg, err := NewConfig(WithAppName("sqlMarkExcludeApp"), WithSpanErrorMarkExclude("sql"))
	require.NoError(t, err)
	defer cfg.Close()
	cfg.Set(CfgSQLErrorCount, 2)
	sp := testSpanWithConfig(cfg)

	for i := 0; i < 20; i++ {
		newSpanEvent(sp, "query").SetSQL("SELECT 1", "")
	}

	assert.Equal(t, int32(0), sp.err.Load(), "an excluded sql cause must not fail the transaction")
	assert.Equal(t, int32(20), sp.sqlCount.Load(), "the statements are still counted")

	// An exception is a different cause, and still fails the transaction.
	newSpanEvent(sp, "query").SetError(errors.New("TEST_ERROR"))
	assert.Equal(t, int32(ErrorCategoryException), sp.err.Load(), "span err")
}

// A statement past maxSqlNormalizeLength is dropped whole: not normalized, not
// annotated, no metadata queued, and not counted toward SQL.ErrorCount. The
// alternative - cut and normalize the rest, as the C++ agent does - yields a
// SQL id / UID no other agent computes when the cut lands inside a literal.
func Test_spanEvent_SetSQLDropsAStatementPastTheNormalizationCap(t *testing.T) {
	for _, queryStat := range []bool{false, true} {
		t.Run(fmt.Sprintf("TraceQueryStat=%v", queryStat), func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Set(CfgSQLTraceQueryStat, queryStat)
			cfg.Set(CfgSQLErrorCount, 1)
			span := testSpanWithConfig(cfg)
			se := newSpanEvent(span, "query")

			se.SetSQL("select '"+strings.Repeat("x", maxSqlNormalizeLength)+"'", "")

			assert.Empty(t, se.annotations.values, "no SQL annotation")
			for len(span.agent.metaChan) > 0 {
				switch md := (<-span.agent.metaChan).(type) {
				case sqlMeta, sqlUidMeta:
					t.Errorf("SQL metadata queued: %T", md)
				}
			}
			assert.Equal(t, int32(0), span.sqlCount.Load(), "not counted")
			assert.Equal(t, int32(0), span.err.Load(), "the span stays clean")
		})
	}
}
