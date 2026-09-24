package ppgohbase

import (
	"testing"

	"github.com/stretchr/testify/assert"
	hbase "github.com/tsuna/gohbase"
)

// wrapTestClient stands in for a client the application created itself; only
// its identity matters here.
type wrapTestClient struct{ hbase.Client }

// WrapClient is NewClient for a client created elsewhere (compile-time
// instrumentation); wrapping twice must not stack two layers.
func TestWrapClient(t *testing.T) {
	base := &wrapTestClient{}
	c := WrapClient(base, "zk1,zk2")
	assert.Equal(t, "zk1,zk2", c.host)
	assert.Same(t, base, c.Client)
	assert.Same(t, c, WrapClient(c, "other"), "an already wrapped client is returned as it is")
}
