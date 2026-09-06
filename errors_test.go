package pinpoint

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	pkgError "github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// selfCausingError is the shape a buggy user error type can take: its Cause()
// returns itself, so an unbounded walk of the chain never terminates.
type selfCausingError struct{}

func (e *selfCausingError) Error() string                   { return "self" }
func (e *selfCausingError) Cause() error                    { return e }
func (e *selfCausingError) StackTrace() pkgError.StackTrace { return nil }

func TestSpan_TraceCallStackBoundsCauserCycle(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))

	done := make(chan int64, 1)
	go func() { done <- span.traceCallStack(&selfCausingError{}, "", 32, time.Now()) }()

	select {
	case id := <-done:
		assert.NotZero(t, id, "exception id")
	case <-time.After(5 * time.Second):
		t.Fatal("traceCallStack did not terminate on a self-causing error")
	}
}

// Each link of an error chain is recorded under one exception id with its
// 0-based depth and Go type name, as Java numbers a Throwable cause chain.
func TestSpan_TraceCallStackChainDepthAndClassName(t *testing.T) {
	inner := errors.New("inner")
	tests := []struct {
		name       string
		err        error
		classNames []string
	}{
		{"fmt.Errorf %w", fmt.Errorf("outer: %w", inner), []string{"fmt.wrapError", "errors.errorString"}},
		{"pkg/errors.WithStack", pkgError.WithStack(inner), []string{"errors.withStack", "errors.errorString"}},
		{"pkg/errors.Wrap", pkgError.Wrap(inner, "outer"), []string{"errors.withStack", "errors.withMessage", "errors.errorString"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := defaultSpan(newTestAgent(defaultConfig()))
			start := time.UnixMilli(1234)
			eid := span.traceCallStack(tt.err, "", 32, start)

			require.Len(t, span.errorChains, len(tt.classNames))
			for i, ec := range span.errorChains {
				assert.Equal(t, eid, ec.exceptionId, "exception id")
				assert.Equal(t, int32(i), ec.depth, "depth")
				assert.Equal(t, tt.classNames[i], ec.className, "class name")
				assert.Equal(t, start, ec.callstack.errorTime, "start time")
			}
			assert.Same(t, inner, span.errorChains[len(span.errorChains)-1].callstack.err)
			assertChainDepthsContiguous(t, span)
		})
	}
}

// errors.Join and any other Unwrap() []error error contribute their first
// element only: the exception chain is a single line of causes.
func TestSpan_TraceCallStackJoinedErrorFollowsFirstCause(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	span := defaultSpan(newTestAgent(defaultConfig()))
	span.traceCallStack(errors.Join(first, second), "", 32, time.Now())

	require.Len(t, span.errorChains, 2)
	assert.Same(t, first, span.errorChains[1].callstack.err, "first element recorded")
	assertChainDepthsContiguous(t, span)
}

// deepCall calls fn n frames below its caller, so the captured stack always
// has more frames available than the configured depth.
func deepCall(n int, fn func()) {
	if n == 0 {
		fn()
		return
	}
	deepCall(n-1, fn)
}

// An error without a stack of its own is captured with exactly
// Error.CallStackDepth frames - the buffer must not be padded with the frames
// runtime.Callers skips.
func TestSpan_TraceCallStackCollectsConfiguredDepth(t *testing.T) {
	for _, depth := range []int{1, 5, 32} {
		t.Run(fmt.Sprintf("depth %d", depth), func(t *testing.T) {
			span := defaultSpan(newTestAgent(defaultConfig()))
			deepCall(depth+8, func() {
				span.traceCallStack(errors.New("boom"), "", depth, time.Now())
			})

			require.Len(t, span.errorChains, 1)
			assert.Len(t, span.errorChains[0].callstack.callstack, depth, "frames captured")
		})
	}
}

// setErrorFrame stands in for SetError, the frame traceCallStack is called
// from: the recorded stack must start at its caller, with no agent frame in it.
//
//go:noinline
func setErrorFrame(span *span, err error) {
	span.traceCallStack(err, "", 8, time.Now())
}

func TestSpan_TraceCallStackSkipsAgentFrames(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))
	setErrorFrame(span, errors.New("boom"))

	require.Len(t, span.errorChains, 1)
	frames := span.errorChains[0].callstack.stackTrace()
	require.NotEmpty(t, frames)
	assert.Equal(t, "TestSpan_TraceCallStackSkipsAgentFrames", frames[0].funcName)
}

// A name passed to SetError wins over the type name for the recorded error.
func TestSpan_TraceCallStackUsesGivenClassName(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))
	span.traceCallStack(errors.New("x"), "MyError", 32, time.Now())
	require.Len(t, span.errorChains, 1)
	assert.Equal(t, "MyError", span.errorChains[0].className)
}

// Error.NewThroughput limits new exception chains, like the Java agent's
// ExceptionChainSampler; 0 or less means unlimited. The burst is one second of
// permits, so the first tps chains are recorded and the rest denied without
// waiting for a refill.
func TestSpan_TraceCallStackLimitsNewChains(t *testing.T) {
	tests := []struct {
		name       string
		throughput int
		recorded   int
	}{
		{"unlimited", 0, 3},
		{"negative is unlimited", -1, 3},
		{"limited to the burst", 2, 2},
		{"limited to one", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Set(CfgErrorNewThroughput, tt.throughput)
			span := testSpanWithConfig(cfg)

			ids := make([]int64, 0, 3)
			for i := 0; i < 3; i++ {
				ids = append(ids, span.traceCallStack(fmt.Errorf("boom %d", i), "", 32, time.Now()))
			}

			assert.Len(t, span.errorChains, tt.recorded, "recorded chains")
			for i, id := range ids {
				if i < tt.recorded {
					assert.NotEqual(t, int64(noExceptionChainId), id, "chain id %d", i)
				} else {
					assert.Equal(t, int64(noExceptionChainId), id, "denied chain id %d", i)
				}
			}
		})
	}
}

// A denied chain records nothing at all - not the error, not its causes - and
// does not burn an id, as Java asks isNewSampled() before nextErrorId().
func TestSpan_TraceCallStackDeniedRecordsNothing(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgErrorNewThroughput, 1)
	span := testSpanWithConfig(cfg)

	require.NotEqual(t, int64(noExceptionChainId), span.traceCallStack(errors.New("first"), "", 32, time.Now()))
	require.Len(t, span.errorChains, 1)

	denied := span.traceCallStack(fmt.Errorf("outer: %w", errors.New("inner")), "", 32, time.Now())
	assert.Equal(t, int64(noExceptionChainId), denied, "denied chain id")
	assert.Len(t, span.errorChains, 1, "denied chain recorded on the span")
	assert.Equal(t, int64(1), span.agent.exceptionIdGen.Load(), "denied chain burned an id")
}

// Only a new chain asks the limiter: an error already recorded on the span, or
// one whose cause is, keeps reporting its id after the burst is exhausted.
func TestSpan_TraceCallStackContinuesChainWhenLimiterExhausted(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgErrorNewThroughput, 1)
	span := testSpanWithConfig(cfg)

	inner := errors.New("inner")
	outer := fmt.Errorf("outer: %w", inner)
	eid := span.traceCallStack(outer, "", 32, time.Now())
	require.NotEqual(t, int64(noExceptionChainId), eid)
	require.Len(t, span.errorChains, 2, "error and its cause")

	assert.Equal(t, eid, span.traceCallStack(outer, "", 32, time.Now()), "same error")
	assert.Equal(t, eid, span.traceCallStack(fmt.Errorf("again: %w", outer), "", 32, time.Now()), "recorded head")

	require.Len(t, span.errorChains, 3, "error, its cause and the joined wrapper")
	assert.ElementsMatch(t, []int32{0, 1, 2}, chainDepths(span), "unique depth per entry")
	assertChainDepthsContiguous(t, span)
}

func chainDepths(span *span) []int32 {
	depths := make([]int32, 0, len(span.errorChains))
	for _, ec := range span.errorChains {
		depths = append(depths, ec.depth)
	}
	return depths
}

// assertChainDepthsContiguous asserts the chain invariant: within one
// exception id the depths are 0..n-1 with no duplicate.
func assertChainDepthsContiguous(t *testing.T, span *span) {
	t.Helper()
	byId := map[int64][]int32{}
	for _, ec := range span.errorChains {
		byId[ec.exceptionId] = append(byId[ec.exceptionId], ec.depth)
	}
	for eid, depths := range byId {
		want := make([]int32, len(depths))
		for i := range want {
			want[i] = int32(i)
		}
		assert.ElementsMatch(t, want, depths, "exception id %d: depths 0..n-1, no duplicates", eid)
	}
}

// Wrapping a cause that is not the head of its chain - a sentinel wrapped
// again at another call site - starts a new chain, as Java's
// ExceptionRecordingState.stateOf only joins when the previously recorded
// throwable is in the new one's cause chain. Joining the old chain would put
// its head below the new error although it is not a cause of it.
func TestSpan_TraceCallStackWrappedInnerCauseStartsNewChain(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))

	base := errors.New("base")
	db := fmt.Errorf("db: %w", base)
	cache := fmt.Errorf("cache: %w", base)
	eid1 := span.traceCallStack(db, "", 32, time.Now())
	eid2 := span.traceCallStack(cache, "", 32, time.Now())

	assert.NotEqual(t, eid1, eid2, "distinct chains")
	require.Len(t, span.errorChains, 3, "db, base, cache: base is not recorded twice")
	assert.Equal(t, []int32{0, 1, 0}, chainDepths(span))
	assert.Equal(t, eid1, span.errorChains[0].exceptionId)
	assert.Equal(t, eid1, span.errorChains[1].exceptionId)
	assert.Equal(t, eid2, span.errorChains[2].exceptionId)
	assertChainDepthsContiguous(t, span)
}

// A chain joined one link at a time stays one chain in cause order.
func TestSpan_TraceCallStackJoinsThreeLevels(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))

	c := errors.New("c")
	b := fmt.Errorf("b: %w", c)
	a := fmt.Errorf("a: %w", b)
	eid := span.traceCallStack(c, "", 32, time.Now())
	assert.Equal(t, eid, span.traceCallStack(b, "", 32, time.Now()))
	assert.Equal(t, eid, span.traceCallStack(a, "", 32, time.Now()))

	require.Len(t, span.errorChains, 3)
	assert.Equal(t, []int32{2, 1, 0}, chainDepths(span), "c, b, a in record order")
	assertChainDepthsContiguous(t, span)
}

// An error recorded after one of its causes joins that chain, and the chain is
// renumbered so the outermost error is depth 0 - the numbering it would have
// got had it been recorded first. Two entries at depth 0 leave the collector
// no way to order the chain.
func TestSpan_TraceCallStackRenumbersJoinedChain(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))

	// An inner span event fails first, then an outer one fails with the error
	// wrapped twice on the way out.
	inner := errors.New("inner")
	eid := span.traceCallStack(inner, "", 32, time.Now())
	require.Len(t, span.errorChains, 1)

	mid := fmt.Errorf("mid: %w", inner)
	outer := fmt.Errorf("outer: %w", mid)
	assert.Equal(t, eid, span.traceCallStack(outer, "", 32, time.Now()), "joined chain id")

	require.Len(t, span.errorChains, 3)
	assert.Equal(t, []int32{2, 0, 1}, chainDepths(span), "outermost error at depth 0")
	assert.Same(t, inner, span.errorChains[0].callstack.err, "depth 2")
	assert.Same(t, outer, span.errorChains[1].callstack.err, "depth 0")
	assert.Same(t, mid, span.errorChains[2].callstack.err, "depth 1")
	assertChainDepthsContiguous(t, span)
}

// Error.MaxChainDepth counts the links recorded, the error itself included.
func TestSpan_TraceCallStackMaxChainDepth(t *testing.T) {
	for _, depth := range []int{1, 3, 0} {
		t.Run(fmt.Sprintf("depth %d", depth), func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Set(CfgErrorMaxChainDepth, depth)
			span := testSpanWithConfig(cfg)

			err := error(errors.New("root"))
			for i := 0; i < 8; i++ {
				err = fmt.Errorf("wrap %d: %w", i, err)
			}
			span.traceCallStack(err, "", 32, time.Now())

			want := depth
			if depth < 1 {
				want = 9 // 0 is unlimited: every link of this chain
			}
			assert.Len(t, span.errorChains, want, "links recorded")
			assertChainDepthsContiguous(t, span)
		})
	}
}

func Test_splitName_NoDot(t *testing.T) {
	module, fn := splitName("main")
	assert.Equal(t, "unknown", module, "module name")
	assert.Equal(t, "main", fn, "func name")

	module, fn = splitName("pkg.Func")
	assert.Equal(t, "pkg", module, "module name")
	assert.Equal(t, "Func", fn, "func name")
}

// uncomparableError is the shape of a common user error type - Go's own
// errors.Join value and validator.ValidationErrors are both slices - whose
// dynamic type cannot be compared with ==.
type uncomparableError []string

func (e uncomparableError) Error() string { return strings.Join(e, ",") }

// Recording two errors of the same uncomparable type on one span used to
// panic on the request goroutine inside findError.
func TestSpan_TraceCallStackUncomparableErrorType(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))

	first := span.traceCallStack(uncomparableError{"a"}, "", 32, time.Now())
	second := span.traceCallStack(uncomparableError{"b"}, "", 32, time.Now())

	assert.NotZero(t, first, "first exception id")
	assert.NotZero(t, second, "second exception id")
	assert.NotEqual(t, first, second, "two distinct errors get their own chain")
	require.Len(t, span.errorChains, 2)
	assert.Equal(t, "pinpoint.uncomparableError", span.errorChains[0].className)
}

func Test_sameError(t *testing.T) {
	comparable := errors.New("x")
	tests := []struct {
		name string
		a, b error
		want bool
	}{
		{"identical comparable", comparable, comparable, true},
		{"distinct comparable", comparable, errors.New("x"), false},
		{"both nil", nil, nil, true},
		{"one nil", comparable, nil, false},
		{"different types", comparable, uncomparableError{"a"}, false},
		{"same uncomparable type", uncomparableError{"a"}, uncomparableError{"a"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sameError(tt.a, tt.b))
		})
	}
}

// A chain the limiter refused stays refused on the span: a later error whose
// cause chain reaches the refused head is Java's CONTINUED state, which reuses
// the stored DISABLED state without asking the sampler again. The limiter is
// swapped for one holding a permit after the refusal, so a continuation that
// asked it would be admitted; an unrelated new chain still is.
func TestSpan_TraceCallStackRefusedChainDoesNotReaskLimiter(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgErrorNewThroughput, 1)
	span := testSpanWithConfig(cfg)

	require.NotEqual(t, int64(noExceptionChainId), span.traceCallStack(errors.New("first"), "", 32, time.Now()))
	refused := errors.New("refused")
	require.Equal(t, int64(noExceptionChainId), span.traceCallStack(refused, "", 32, time.Now()))

	refilled := *span.cfg
	refilled.newExceptionLimiter = rate.NewLimiter(1, 1)
	span.cfg = &refilled

	assert.Equal(t, int64(noExceptionChainId), span.traceCallStack(fmt.Errorf("outer: %w", refused), "", 32, time.Now()), "continuation of the refused chain asked the limiter")
	assert.Equal(t, int64(noExceptionChainId), span.traceCallStack(refused, "", 32, time.Now()), "refused head itself asked the limiter")
	assert.NotEqual(t, int64(noExceptionChainId), span.traceCallStack(errors.New("unrelated"), "", 32, time.Now()), "an unrelated new chain is still asked")
	assert.Len(t, span.errorChains, 2, "first and unrelated only")
}

// Two chains refused back to back must both stay latched. With a single slot
// the older head is overwritten, and the rest of that chain's links are then
// charged as brand new chains - under a burst those crowd out chains Java
// would have admitted.
func TestSpan_TraceCallStackKeepsEveryRefusedChainHead(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgErrorNewThroughput, 0)
	span := testSpanWithConfig(cfg)
	span.cfg.newExceptionLimiter = rate.NewLimiter(0, 0) // refuses everything

	first := errors.New("first")
	second := errors.New("second")
	require.Equal(t, int64(noExceptionChainId), span.traceCallStack(first, "", 32, time.Now()))
	require.Equal(t, int64(noExceptionChainId), span.traceCallStack(second, "", 32, time.Now()))

	// A permit is available again: only a chain that forgot its refusal asks
	// for it, and getting one proves the latch was lost.
	span.cfg.newExceptionLimiter = rate.NewLimiter(rate.Inf, 1)

	assert.Equal(t, int64(noExceptionChainId), span.traceCallStack(fmt.Errorf("more: %w", first), "", 32, time.Now()), "the first refused chain was recharged as a new chain")
	assert.Equal(t, int64(noExceptionChainId), span.traceCallStack(fmt.Errorf("more: %w", second), "", 32, time.Now()), "the second refused chain was recharged as a new chain")
	assert.Empty(t, span.errorChains, "a refused chain recorded entries")
}

// The latch cannot grow without bound: past maxRefusedChainHeads the oldest
// head is evicted, and only that head loses its refusal.
func TestSpan_TraceCallStackRefusedChainHeadsAreBounded(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgErrorNewThroughput, 0)
	span := testSpanWithConfig(cfg)
	span.cfg.newExceptionLimiter = rate.NewLimiter(0, 0)

	refused := make([]error, maxRefusedChainHeads+1)
	for i := range refused {
		refused[i] = fmt.Errorf("refused %d", i)
		require.Equal(t, int64(noExceptionChainId), span.traceCallStack(refused[i], "", 32, time.Now()))
	}
	assert.Len(t, span.refusedChainHeads, maxRefusedChainHeads, "the latch grew past its bound")

	span.cfg.newExceptionLimiter = rate.NewLimiter(rate.Inf, 1)
	assert.NotEqual(t, int64(noExceptionChainId), span.traceCallStack(refused[0], "", 32, time.Now()), "the evicted head kept its refusal")
	for _, err := range refused[1:] {
		assert.Equal(t, int64(noExceptionChainId), span.traceCallStack(err, "", 32, time.Now()), "a head still in the ring lost its refusal")
	}
}

// The per-span entry cap follows Error.MaxChainDepth, so a chain as long as the
// option allows is recorded in full; the cap floors at minErrorChainEntry.
// The cap is checked where SetError records, so this goes through a span event.
func TestSpanEvent_SetErrorRecordsMaxChainDepthLinks(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgErrorTraceCallStack, true)
	cfg.Set(CfgErrorMaxChainDepth, 20)
	span := testSpanWithConfig(cfg)
	require.Equal(t, 20, span.cfg.errorMaxChainDepth)

	err := errors.New("root")
	for i := 1; i < 25; i++ {
		err = fmt.Errorf("link %d: %w", i, err)
	}
	newSpanEvent(span, "first").SetError(err)
	assert.Len(t, span.errorChains, 20, "links recorded")
	assert.False(t, span.errorChainDropLog.Load(), "cap reached before the chain limit")

	newSpanEvent(span, "second").SetError(errors.New("one more"))
	newSpanEvent(span, "third").SetError(errors.New("and another"))
	assert.Len(t, span.errorChains, 20, "entry beyond the cap recorded")
	assert.True(t, span.errorChainDropLog.Load(), "drop not logged")
}

// The drop must be visible at the default log level (info) - the comment on
// errorChainDropLog claims parity with eventOverflowLog, which warns. Captured
// on the global logger Log("span") actually writes to, not a local instance.
func TestSpanEvent_SetErrorLogsDropAtDefaultLevel(t *testing.T) {
	var buf bytes.Buffer
	defer captureLogAt(&buf, logrus.InfoLevel)()

	cfg := defaultConfig()
	cfg.Set(CfgErrorTraceCallStack, true)
	cfg.Set(CfgErrorMaxChainDepth, 1)
	span := testSpanWithConfig(cfg)

	for i := 0; i < minErrorChainEntry+2; i++ {
		newSpanEvent(span, "event").SetError(fmt.Errorf("boom %d", i))
	}
	require.True(t, span.errorChainDropLog.Load(), "the cap was never reached")

	assert.Contains(t, buf.String(), "exception entry limit reached", "the drop was silent at the default level")
	assert.Equal(t, 1, strings.Count(buf.String(), "exception entry limit reached"), "logged more than once a span")
}
