package pinpoint

import (
	"testing"
	"time"

	pkgError "github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
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
	go func() { done <- span.traceCallStack(&selfCausingError{}, 32) }()

	select {
	case id := <-done:
		assert.NotZero(t, id, "exception id")
	case <-time.After(5 * time.Second):
		t.Fatal("traceCallStack did not terminate on a self-causing error")
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
