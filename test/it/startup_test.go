package it

import (
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// startWithUnavailableCollector keeps the configured ports but removes every
// listening server, modelling an agent process that starts while the collector
// is completely unavailable.
func startWithUnavailableCollector(t *testing.T) (*MockCollector, pinpoint.Agent) {
	t.Helper()
	mc := startCollector(t)
	mc.StopEndpoint(EndpointAgent)
	mc.StopEndpoint(EndpointSpan)
	mc.StopEndpoint(EndpointStat)
	return mc, startAgent(t, mc, defaultAgentConfig())
}

func restartCollector(t *testing.T, mc *MockCollector) {
	t.Helper()
	require.NoError(t, mc.StartEndpoint(EndpointSpan))
	require.NoError(t, mc.StartEndpoint(EndpointStat))
	require.NoError(t, mc.StartEndpoint(EndpointAgent))
}

func TestEnablesAndStartsAllGrpcWorkersAfterCollectorRecovery(t *testing.T) {
	mc, agent := startWithUnavailableCollector(t)

	// The connect goroutine may keep retrying indefinitely, but it must not
	// expose a half-started agent or start any downstream worker before
	// AgentInfo is accepted.
	time.Sleep(300 * time.Millisecond)
	assert.False(t, agent.Enable())
	outage := mc.Snapshot()
	assert.Empty(t, outage.AgentInfos)
	assert.Empty(t, outage.PingStreams)
	assert.Empty(t, outage.CommandStreams)
	assert.Empty(t, outage.StatStreams)

	restartCollector(t, mc)
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.AgentInfos) > 0 && len(s.Pings) > 0 &&
			len(s.CommandStreams) > 0 && len(s.StatStreams) > 0
	}, longTimeout))
	require.True(t, waitUntil(func() bool { return agent.Enable() }, longTimeout))

	// Verify more than registration: every independent collector channel must
	// carry fresh work after the full outage ends.
	statsBefore := len(mc.Snapshot().Stats)
	mc.SendEchoCommand(401, "collector-recovered")
	require.True(t, waitUntil(func() bool {
		tracer := agent.NewSpanTracer("collector.startup.recovery", "/collector-startup-recovery")
		tracer.EndSpan()
		s := mc.Snapshot()
		return findSpanByRpc(s, "/collector-startup-recovery") != nil &&
			hasApiMetadata(s, "collector.startup.recovery", apiTypeWebRequest) &&
			len(s.Stats) > statsBefore && hasEchoResponse(s, 401)
	}, longTimeout))
	assert.True(t, agent.Enable())
}

// Shutting down before registration completes must interrupt the collector
// wait instead of holding the process for a whole back-off interval.
func TestShutdownInterruptsInitialCollectorWait(t *testing.T) {
	mc, agent := startWithUnavailableCollector(t)

	// Give the connect goroutine time to enter its back-off wait while all
	// three collector endpoints remain unavailable.
	time.Sleep(300 * time.Millisecond)
	require.False(t, agent.Enable())

	started := time.Now()
	agent.Shutdown()
	elapsed := time.Since(started)

	assert.Less(t, elapsed, 3*time.Second,
		"shutdown must interrupt the initial collector wait, not wait out the back-off")
	assert.False(t, agent.Enable())

	// The agent never became enabled, but shutting it down must still release
	// the process-global singleton, or nothing in this process could ever
	// create an agent again.
	assert.Equal(t, pinpoint.NoopAgent(), pinpoint.GetAgent(),
		"shutdown must release the global agent even when registration never completed")

	s := mc.Snapshot()
	assert.Empty(t, s.AgentInfos)
	assert.Empty(t, s.StatStreams)
}

// Models an application that starts while the collector is unhealthy: the
// ports accept connections but every RPC keeps failing until EndOutage, so the
// rejected registration attempts stay visible in the collector records.
func TestServesNoopTracersDuringOutageAndEnablesTracingAfterRecovery(t *testing.T) {
	mc := startCollector(t)
	mc.BeginOutage()
	agent := startAgent(t, mc, defaultAgentConfig())

	// The agent keeps retrying registration against the failing collector
	// without ever coming online.
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(resultsFor(s, RpcAgentInfo)) >= 2
	}, longTimeout))
	assert.False(t, agent.Enable())

	// The application's own work proceeds normally; the disabled agent hands an
	// inert tracer to every request.
	for request := 0; request < 5; request++ {
		requireNoopTracer(t, agent.NewSpanTracer("startup.outage", "/startup-outage"))
		assert.Equal(t, request*2+1, handleInstrumentedRequest(agent, "/startup-outage", request))
	}

	// Nothing but the rejected registration attempts may have reached the
	// collector: no downstream worker starts before AgentInfo is accepted.
	s := mc.Snapshot()
	attempts := resultsFor(s, RpcAgentInfo)
	require.GreaterOrEqual(t, len(attempts), 2)
	for _, result := range attempts {
		assert.Equal(t, codes.Unavailable, result.Code)
	}
	assert.Empty(t, allSpanMessages(s))
	assert.Empty(t, s.PingStreams)
	assert.Empty(t, s.StatStreams)
	assert.Empty(t, s.CommandStreams)

	// Collector recovers: the ongoing retry loop must succeed and enable the agent.
	mc.EndOutage()
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return hasResultSuccess(s, RpcAgentInfo, codes.OK, true)
	}, longTimeout))
	require.True(t, waitUntil(func() bool { return agent.Enable() }, longTimeout))

	// Tracing now runs for real.
	recovered := agent.NewSpanTracer("startup.outage.recovered", "/startup-outage-recovered")
	require.True(t, recovered.IsSampled())
	assert.NotEqual(t, int64(0), recovered.SpanId())
	assert.Equal(t, itAgentID, recovered.TransactionId().AgentId)
	recovered.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/startup-outage-recovered") != nil && len(s.Pings) > 0
	}, longTimeout))
	assert.True(t, agent.Enable())
}
