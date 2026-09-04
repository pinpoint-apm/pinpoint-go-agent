package it

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestRegistersAgentAndMaintainsPingAndCommandStreams(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.Pings) > 0 && len(s.CommandStreams) > 0
	}, waitTimeout))

	s := mc.Snapshot()
	require.NotEmpty(t, s.AgentInfos)
	info := s.AgentInfos[0].Message
	assert.Equal(t, itAppType, info.GetServiceType())
	assert.Greater(t, info.GetPid(), int32(0))
	assert.NotEmpty(t, info.GetHostname())
	assert.NotEmpty(t, info.GetAgentVersion())
	assert.Equal(t, runtime.Version(), info.GetVmVersion())
	assert.True(t, info.GetContainer())
	require.NotNil(t, info.GetServerMetaData())
	assert.Equal(t, "Go Application", info.GetServerMetaData().GetServerInfo())
	assert.Equal(t, os.Args[1:], info.GetServerMetaData().GetVmArg())

	// The single service-info entry lists the Go runtime and the build's deps.
	require.Len(t, info.GetServerMetaData().GetServiceInfo(), 1)
	assert.Contains(t, info.GetServerMetaData().GetServiceInfo()[0].GetServiceName(), runtime.GOOS)

	expectCommonMetadata(t, s.AgentInfos[0].Metadata, false)
	require.NotEmpty(t, s.PingStreams)
	expectCommonMetadata(t, s.PingStreams[0], true)
	require.NotEmpty(t, s.CommandStreams)
	expectCommonMetadata(t, s.CommandStreams[0], false)
	assert.True(t, agent.Enable())
}

func TestSendsV4IdentityAcrossGrpcAndTracePropagation(t *testing.T) {
	cfg := defaultAgentConfig()
	cfg.uidVersion = "v4"
	cfg.serviceName = "go-it-service"
	cfg.apiKey = "go-it-api-key"
	mc, agent := startStack(t, cfg)

	root := agent.NewSpanTracer("v4.server", "/v4-root")
	require.True(t, root.IsSampled())
	traceID := root.TransactionId().String()
	rootSpanID := root.SpanId()

	outbound := root.NewSpanEvent("v4.client")
	outbound.SpanEvent().SetServiceType(pinpoint.ServiceTypeGrpc)
	outbound.SpanEvent().SetDestination("v4-downstream")
	propagated := mapCarrier{}
	outbound.Inject(propagated)
	assert.Equal(t, itAppName, propagated[pinpoint.HeaderParentApplicationName])
	assert.Equal(t, fmt.Sprint(itAppType), propagated[pinpoint.HeaderParentApplicationType])
	assert.Equal(t, "go-it-service", propagated[pinpoint.HeaderParentServiceName])

	continued := agent.NewSpanTracerWithReader("v4.continued", "/v4-continued", propagated)
	require.True(t, continued.IsSampled())
	assert.Equal(t, traceID, continued.TransactionId().String())
	root.EndSpanEvent()
	continued.EndSpan()
	root.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/v4-root") != nil &&
			findSpanByRpc(s, "/v4-continued") != nil &&
			len(s.ApiMetadata) > 0 && len(s.SpanBatches) > 0 &&
			len(s.Stats) > 0 && len(s.StatStreams) > 0 &&
			len(s.PingStreams) > 0 && len(s.CommandStreams) > 0
	}, waitTimeout))

	s := mc.Snapshot()
	// v4 always mints its own 22-byte agent id, so the configured AgentID is
	// replaced; every channel must then carry that same generated id.
	agentID := s.AgentInfos[0].Metadata.ValueOr("agentid", "")
	require.Len(t, agentID, 22)
	startTime := s.AgentInfos[0].Metadata.ValueOr("starttime", "")
	require.NotEmpty(t, startTime)

	expectV4 := func(md RpcMetadata, expectSocketID bool) {
		assert.Equal(t, itAppName, md.ValueOr("applicationname", ""))
		assert.Equal(t, agentID, md.ValueOr("agentid", ""))
		assert.Equal(t, itAgentName, md.ValueOr("agentname", ""))
		assert.Equal(t, startTime, md.ValueOr("starttime", ""))
		assert.Equal(t, fmt.Sprint(itAppType), md.ValueOr("servicetype", ""))
		assert.Equal(t, "400", md.ValueOr("protocol.version", ""))
		assert.Equal(t, "go-it-service", md.ValueOr("servicename", ""))
		assert.Equal(t, "go-it-api-key", md.ValueOr("apikey", ""))
		assert.Equal(t, expectSocketID, md.Has("socketid"))
	}
	expectV4(s.AgentInfos[0].Metadata, false)
	expectV4(s.ApiMetadata[0].Metadata, false)
	expectV4(s.SpanBatches[0].Metadata, false)
	expectV4(s.StatStreams[0], false)
	expectV4(s.PingStreams[0], true)
	expectV4(s.CommandStreams[0], false)

	rootWire := findSpanByRpc(s, "/v4-root")
	require.NotNil(t, rootWire)
	assert.Equal(t, agentID, rootWire.GetTransactionId().GetAgentId())
	assert.Equal(t, rootSpanID, rootWire.GetSpanId())

	continuedWire := findSpanByRpc(s, "/v4-continued")
	require.NotNil(t, continuedWire)
	parent := continuedWire.GetAcceptEvent().GetParentInfo()
	require.NotNil(t, parent)
	assert.Equal(t, itAppName, parent.GetParentApplicationName())
	assert.Equal(t, itAppType, parent.GetParentApplicationType())
	assert.Equal(t, "go-it-service", parent.GetParentServiceName())
	assert.Equal(t, "v4-downstream", parent.GetAcceptorHost())

	// The API key is intentionally present in gRPC metadata but must never be
	// copied into the AgentInfo payload.
	raw, err := proto.Marshal(s.AgentInfos[0].Message)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "go-it-api-key")
}

func TestReconnectsPingStreamAfterResponseError(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig(), func(mc *MockCollector) {
		mc.FailNext(RpcPingSession, codes.Unavailable, "first ping stream disconnected", 1)
	})

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.PingStreams) >= 2 && len(s.Pings) > 0 &&
			hasResultSuccess(s, RpcPingSession, codes.Unavailable, false)
	}, waitTimeout))

	// Each ping stream carries a fresh socket id so the collector can tell a
	// reconnect from a duplicate registration.
	s := mc.Snapshot()
	require.GreaterOrEqual(t, len(s.PingStreams), 2)
	first, ok := s.PingStreams[0].Int64("socketid")
	require.True(t, ok)
	second, ok := s.PingStreams[1].Int64("socketid")
	require.True(t, ok)
	assert.Equal(t, first+1, second)
	assert.True(t, agent.Enable())
}

func TestRecyclesPingStreamWhenCollectorNeverResponds(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig(), func(mc *MockCollector) {
		mc.TimeoutNext(RpcPingSession)
	})

	// sendStreamWithTimeout cancels the stalled stream after sendStreamTimeOut,
	// which the collector observes as a cancellation of the RPC.
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(s.PingStreams) >= 2 &&
			(hasResult(s, RpcPingSession, codes.Canceled) ||
				hasResult(s, RpcPingSession, codes.DeadlineExceeded))
	}, longTimeout))
	assert.True(t, agent.Enable())
}

func TestRetriesAgentRegistrationAfterInitialFailure(t *testing.T) {
	// startStack blocks until registration succeeded and the agent came
	// online, so by now the failed first attempt and its retry are on record.
	mc, agent := startStack(t, defaultAgentConfig(), func(mc *MockCollector) {
		mc.FailNext(RpcAgentInfo, codes.Unavailable, "first registration attempt rejected")
	})

	s := mc.Snapshot()
	require.GreaterOrEqual(t, len(s.AgentInfos), 2)
	assert.Equal(t, s.AgentInfos[0].Message.GetAgentVersion(), s.AgentInfos[1].Message.GetAgentVersion())

	results := resultsFor(s, RpcAgentInfo)
	require.GreaterOrEqual(t, len(results), 2)
	assert.Equal(t, codes.Unavailable, results[0].Code)
	assert.False(t, results[0].Success)
	assert.Equal(t, codes.OK, results[1].Code)
	assert.True(t, results[1].Success)
	assert.True(t, agent.Enable())
}

// A registration the collector answers with PResult.success=false is a
// permanent rejection, not a transport error: the agent must stop retrying and
// stay disabled rather than loop forever against a collector that refuses it.
func TestStopsRegistrationAfterApplicationRejection(t *testing.T) {
	mc := startCollector(t)
	mc.RejectNext(RpcAgentInfo, "collector rejected this agent")
	agent := startAgent(t, mc, defaultAgentConfig())

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return hasResultSuccess(s, RpcAgentInfo, codes.OK, false)
	}, waitTimeout))

	assert.False(t, waitUntil(func() bool { return agent.Enable() }, 500*time.Millisecond),
		"a rejected registration must not enable the agent")
	s := mc.Snapshot()
	assert.Len(t, resultsFor(s, RpcAgentInfo), 1)
	assert.Empty(t, s.PingStreams)
	assert.Empty(t, s.StatStreams)
	assert.Empty(t, s.CommandStreams)
}

// Runs without the fixture: a disabled configuration must produce the noop
// agent, which needs no collector and never becomes the global agent.
func TestCreatesNoopAgentWhenDisabledByConfig(t *testing.T) {
	config, err := pinpoint.NewConfig(
		pinpoint.WithAppName("noop-agent-it"),
		pinpoint.WithAgentId("noop-agent-it"),
		pinpoint.WithEnable(false),
	)
	require.NoError(t, err)
	agent, err := pinpoint.NewAgent(config)
	require.NoError(t, err)
	assert.False(t, agent.Enable())
	assert.Equal(t, pinpoint.NoopAgent(), agent)
	assert.Equal(t, pinpoint.NoopAgent(), pinpoint.GetAgent())

	requireNoopTracer(t, agent.NewSpanTracer("noop.operation", "/noop"))

	// The noop agent's lifecycle entry points must be inert and safe.
	agent.Shutdown()
	assert.False(t, agent.Enable())
}

// Boot registration retries forever, but the periodic AgentInfo re-sender is
// bounded by Collector.AgentInfo.MaxTryPerAttempt and a failed cycle is
// best-effort: it must retry within the cycle and leave the agent enabled
// either way. The other retry tests here all cover the boot path, which is a
// different loop.
func TestRetriesPeriodicAgentInfoResendAfterFailure(t *testing.T) {
	cfg := defaultAgentConfig()
	// Short enough that a refresh lands during the test; the retry interval and
	// attempt count come from the fixture (50ms, 2 tries).
	cfg.agentInfoRefreshInterval = 200
	mc, agent := startStack(t, cfg)

	require.GreaterOrEqual(t, len(mc.Snapshot().AgentInfos), 1)

	// Armed only now, so boot registration keeps its own success and the fault
	// lands on a re-send instead.
	mc.FailNext(RpcAgentInfo, codes.Unavailable, "periodic re-send rejected")

	// The failed attempt and its retry both reach the collector. Locate the
	// injected failure instead of assuming its index: a periodic re-send can
	// land between the snapshot above and FailNext arming, shifting every later
	// entry by one.
	findFailed := func(results []RpcResult) int {
		for i, r := range results {
			if r.Code == codes.Unavailable {
				return i
			}
		}
		return -1
	}
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		results := resultsFor(s, RpcAgentInfo)
		failed := findFailed(results)
		return failed >= 0 && failed+1 < len(results)
	}, waitTimeout))

	results := resultsFor(mc.Snapshot(), RpcAgentInfo)
	failed := findFailed(results)
	require.GreaterOrEqual(t, failed, 0)
	assert.False(t, results[failed].Success)
	retried := results[failed+1]
	assert.Equal(t, codes.OK, retried.Code)
	assert.True(t, retried.Success)
	// A best-effort cycle must never take the agent offline.
	assert.True(t, agent.Enable())
}

// The fixture sets the refresh interval to zero, which keeps the periodic
// re-sender off and registers exactly once (the library default is 24h).
func TestSendsAgentInfoOnceWhenRefreshDisabled(t *testing.T) {
	cfg := defaultAgentConfig()
	require.Zero(t, cfg.agentInfoRefreshInterval)
	mc, agent := startStack(t, cfg)

	require.Len(t, mc.Snapshot().AgentInfos, 1)
	// Several refresh intervals' worth of time for a worker that must not exist.
	time.Sleep(time.Second)
	assert.Len(t, mc.Snapshot().AgentInfos, 1)
	assert.True(t, agent.Enable())
}
