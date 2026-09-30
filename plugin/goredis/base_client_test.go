package ppgoredis

import (
	"context"
	"testing"

	"github.com/go-redis/redis"
	"github.com/stretchr/testify/assert"
)

// Every copy derives from the client NewClient made, never from another copy:
// go-redis copies the process wrappers along with the client, so deriving from
// a copy stacked a second wrapper and recorded two events per command.
func TestClient_WithContextDerivesFromTheBaseClient(t *testing.T) {
	c := NewClient(&redis.Options{Addr: "localhost:6379"})
	first := c.WithContext(context.Background())
	second := first.WithContext(context.Background())

	assert.Same(t, c.Client, c.base)
	assert.Same(t, c.base, first.base)
	assert.Same(t, c.base, second.base, "a copy of a copy is still derived from the base client")
	assert.NotSame(t, first.Client, second.Client)

	cc := NewClusterClient(&redis.ClusterOptions{Addrs: []string{"localhost:7000"}})
	assert.Same(t, cc.base, cc.WithContext(context.Background()).WithContext(context.Background()).base)
}
