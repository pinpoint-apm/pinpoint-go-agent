package it

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

func connectionsTo(s Snapshot, e Endpoint) int {
	n := 0
	for _, c := range s.Connections {
		if c == e {
			n++
		}
	}
	return n
}

// With Collector.Grpc.ConnectionMaxAge and StreamMaxAge set, a real agent
// against a real collector rotates its connections and streams while traffic
// flows -- and every span still arrives, with no failed RPC: the switch is
// make-before-break, and a stream renewal is not a failure.
func TestRotatesConnectionsAndStreamsWithoutLosingSpans(t *testing.T) {
	cfg := defaultAgentConfig()
	cfg.grpcConnectionMaxAge = 100
	cfg.grpcStreamMaxAge = 150
	mc, agent := startStack(t, cfg)

	const spans = 100
	for i := 0; i < spans; i++ {
		handleInstrumentedRequest(agent, fmt.Sprintf("/renew/%d", i), i)
		time.Sleep(10 * time.Millisecond)
	}

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return countSpansByRpcPrefix(s, "/renew/") == spans
	}, longTimeout), "every span sent across the rotations must arrive")

	s := mc.Snapshot()
	// A second of span batches on a 100ms max age is several rotations; the
	// command stream renews every ~150ms and each reopen picks a connection,
	// so the agent endpoint rotates as well.
	assert.GreaterOrEqual(t, connectionsTo(s, EndpointSpan), 3, "span connections: %v", s.Connections)
	assert.GreaterOrEqual(t, connectionsTo(s, EndpointAgent), 3, "agent connections: %v", s.Connections)
	assert.GreaterOrEqual(t, len(s.CommandStreams), 3, "the command stream is renewed while it waits")
	assert.LessOrEqual(t, len(s.Connections), 60, "rotation must not open a connection per RPC")
	for _, r := range s.RpcResults {
		assert.Equal(t, codes.OK, r.Code, "%s failed during renewal: %s", r.Rpc, r.Message)
	}
	assert.True(t, agent.Enable())
}

// The defaults leave both renewals off: one connection per endpoint for the
// life of the agent, exactly as before the option existed.
func TestKeepsOneConnectionPerEndpointByDefault(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	for i := 0; i < 20; i++ {
		handleInstrumentedRequest(agent, fmt.Sprintf("/steady/%d", i), i)
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return countSpansByRpcPrefix(s, "/steady/") == 20 && len(s.Stats) > 0
	}, waitTimeout))

	s := mc.Snapshot()
	assert.Equal(t, 1, connectionsTo(s, EndpointSpan))
	assert.Equal(t, 1, connectionsTo(s, EndpointStat))
	// Agent and command share the agent port but dial separately.
	assert.Equal(t, 2, connectionsTo(s, EndpointAgent))
	assert.Len(t, s.CommandStreams, 1)
}
