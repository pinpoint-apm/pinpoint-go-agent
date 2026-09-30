package pinpoint

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The logging plugins call SetLogging from whichever goroutine logs with the
// request's tracer, so two of them at once must be a plain store, not a data
// race (run under -race).
func TestSpan_SetLogging_ConcurrentCallsAreRaceFree(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	tracer := agent.NewSpanTracer("root", "/rpc")

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracer.Span().SetLogging(Logged)
		}()
	}
	wg.Wait()
	tracer.EndSpan()

	assert.Equal(t, int32(Logged), tracer.(*span).loggingInfo.Load())
}
