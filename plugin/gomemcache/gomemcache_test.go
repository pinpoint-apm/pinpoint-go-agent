package ppgomemcache

import (
	"context"
	"sync"
	"testing"
)

// WithContext must hand each request its own copy and keep the shared
// receiver's tracer rebind race-free. Run under -race.
func TestClient_WithContextIsConcurrencySafe(t *testing.T) {
	mc := NewClient("localhost:1")

	c := mc.WithContext(context.Background())
	if c == mc {
		t.Error("WithContext returned the shared wrapper, want a copy")
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				c := mc.WithContext(context.Background())
				_, _ = c.Get("foo") // no server: errors fast, still records the span event
			}
		}()
	}
	wg.Wait()
}
