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
