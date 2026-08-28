package ppgoredis

import (
	"context"
	"sync"
	"testing"

	"github.com/go-redis/redis"
)

// WithContext must hand each request its own copy: the wrapped client is
// shared, and rebinding it in place raced the field write and recorded one
// request's commands on another request's tracer. Run under -race.
func TestClient_WithContextReturnsCopy(t *testing.T) {
	rc := NewClient(&redis.Options{Addr: "localhost:1"})
	orig := rc.Client

	c := rc.WithContext(context.Background())
	if c == rc {
		t.Error("WithContext returned the shared wrapper, want a copy")
	}
	if rc.Client != orig {
		t.Error("WithContext mutated the shared wrapper")
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				rc.WithContext(context.Background())
			}
		}()
	}
	wg.Wait()
}
