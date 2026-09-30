package ppgomemcache

import (
	"testing"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
)

// Client and its embedded *memcache.Client are exported, so a literal
// construction skips the store WrapClient makes; the first operation then
// dereferenced a nil box.
func TestClient_BuiltAsALiteralHasANoopTracer(t *testing.T) {
	c := &Client{Client: memcache.New("localhost:11211")}
	assert.NotPanics(t, func() {
		assert.Equal(t, pinpoint.NoopTracer(), c.currentTracer())
	})
}
