package pinpoint

import (
	"errors"
	"fmt"
	"testing"
	"time"

	pkgError "github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func Test_splitName_NoDot(t *testing.T) {
	module, fn := splitName("main")
	assert.Equal(t, "unknown", module, "module name")
	assert.Equal(t, "main", fn, "func name")

	module, fn = splitName("pkg.Func")
	assert.Equal(t, "pkg", module, "module name")
	assert.Equal(t, "Func", fn, "func name")
}
