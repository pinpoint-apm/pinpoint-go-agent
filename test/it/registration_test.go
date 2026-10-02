package it

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestRegistersAgentAndMaintainsPingAndCommandStreams(t *testing.T) {
	mc, agent := startStack(t)

	mc.WaitFor(t, func(s Snapshot) bool {
		return len(s.Pings) > 0 && len(s.CommandStreams) > 0
	}, waitTimeout)

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
	// HandleCommandV2 registers the connection from this header alone: the
	// supported codes, ";"-separated and ascending, with no handshake message.
	assert.Equal(t, "710;730;740;750", s.CommandStreams[0].Get("supportcommandcode"))
	assert.False(t, s.AgentInfos[0].Metadata.Has("supportcommandcode"), "only the command stream carries the header")
	assert.True(t, agent.Enable())
}

func TestSendsV4IdentityAcrossGrpcAndTracePropagation(t *testing.T) {
	mc, agent := startStack(t,
		pinpoint.WithUidVersion("v4"),
		pinpoint.WithServiceName("go-it-service"),
		pinpoint.WithApiKey("go-it-api-key"))

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

	mc.WaitFor(t, func(s Snapshot) bool {
		return findSpanByRpc(s, "/v4-root") != nil &&
			findSpanByRpc(s, "/v4-continued") != nil &&
			len(s.ApiMetadata) > 0 && len(s.SpanBatches) > 0 &&
			len(s.Stats) > 0 && len(s.StatStreams) > 0 &&
			len(s.PingStreams) > 0 && len(s.CommandStreams) > 0
	}, waitTimeout)

	s := mc.Snapshot()
	// The agent always mints its own 22-byte agent id; every channel must
	// carry that same generated id.
	agentID := s.AgentInfos[0].Metadata.Get("agentid")
	require.Len(t, agentID, generatedAgentIDLen)
	startTime := s.AgentInfos[0].Metadata.Get("starttime")
	require.NotEmpty(t, startTime)

	expectV4 := func(md RpcMetadata, expectSocketID bool) {
		assert.Equal(t, itAppName, md.Get("applicationname"))
		assert.Equal(t, agentID, md.Get("agentid"))
		assert.Equal(t, itAgentName, md.Get("agentname"))
		assert.Equal(t, startTime, md.Get("starttime"))
		assert.Equal(t, fmt.Sprint(itAppType), md.Get("servicetype"))
		assert.Equal(t, "400", md.Get("protocol.version"))
		assert.Equal(t, "go-it-service", md.Get("servicename"))
		assert.Equal(t, "go-it-api-key", md.Get("apikey"))
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
	mc, agent := startArmedStack(t, func(mc *MockCollector) {
		mc.FailNext(RpcPingSession, codes.Unavailable, "first ping stream disconnected", 1)
	})

	mc.WaitFor(t, func(s Snapshot) bool {
		return len(s.PingStreams) >= 2 && len(s.Pings) > 0 &&
			hasResultSuccess(s, RpcPingSession, codes.Unavailable, false)
	}, waitTimeout)

	// Each ping stream carries a fresh socket id so the collector can tell a
	// reconnect from a duplicate registration.
	s := mc.Snapshot()
	require.GreaterOrEqual(t, len(s.PingStreams), 2)
	first, err := strconv.ParseInt(s.PingStreams[0].Get("socketid"), 10, 64)
	require.NoError(t, err)
	second, err := strconv.ParseInt(s.PingStreams[1].Get("socketid"), 10, 64)
	require.NoError(t, err)
	assert.Equal(t, first+1, second)
	assert.True(t, agent.Enable())
}

func TestRecyclesPingStreamWhenCollectorNeverResponds(t *testing.T) {
	mc, agent := startArmedStack(t, func(mc *MockCollector) {
		mc.TimeoutNext(RpcPingSession)
	})

	// sendStreamWithTimeout cancels the stalled stream after sendStreamTimeOut,
	// which the collector observes as a cancellation of the RPC.
	mc.WaitFor(t, func(s Snapshot) bool {
		return len(s.PingStreams) >= 2 &&
			(hasResult(s, RpcPingSession, codes.Canceled) ||
				hasResult(s, RpcPingSession, codes.DeadlineExceeded))
	}, longTimeout)
	assert.True(t, agent.Enable())
}

// Retrying is not the same as running: for as long as the collector keeps
// rejecting, the agent must stay disabled and open no stream. Registration is
// the precondition for tracing here (see doc/development.md), and an agent that
// reported itself enabled while stuck in this loop would look healthy while
// reporting nothing.
//
// A PResult.success=false answer is still retried like a transport error
// rather than treated as permanent: a collector answers that way while it is
// initializing or briefly refusing, and giving up would leave the process
// untraced until someone restarts it.
func TestStaysDisabledWhileRegistrationIsRejected(t *testing.T) {
	mc := startCollector(t)
	for i := 0; i < 3; i++ {
		mc.RejectNext(RpcAgentInfo, "collector rejected this agent")
	}
	// Slow enough that the rejections below cover the assertions comfortably.
	agent := startAgent(t, mc, pinpoint.WithCollectorAgentInfoSendRetryInterval(300))

	mc.WaitFor(t, func(s Snapshot) bool {
		return len(resultsFor(s, RpcAgentInfo)) >= 2
	}, waitTimeout, "the rejected registration was not retried")

	assert.False(t, agent.Enable(), "a rejected registration must not enable the agent")
	s := mc.Snapshot()
	assert.Empty(t, s.PingStreams)
	assert.Empty(t, s.StatStreams)
	assert.Empty(t, s.CommandStreams)

	require.Eventually(t, agent.Enable, longTimeout, 10*time.Millisecond,
		"the agent never registered once the collector stopped rejecting")
}

// Runs without the fixture: a disabled configuration must produce the noop
// agent, which needs no collector and never becomes the global agent.
func TestCreatesNoopAgentWhenDisabledByConfig(t *testing.T) {
	config, err := pinpoint.NewConfig(
		pinpoint.WithAppName("noop-agent-it"),
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
	// Short enough that a refresh lands during the test; the retry interval and
	// attempt count come from the fixture (50ms, 2 tries).
	mc, agent := startStack(t, pinpoint.WithCollectorAgentInfoRefreshInterval(200))

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
	mc.WaitFor(t, func(s Snapshot) bool {
		results := resultsFor(s, RpcAgentInfo)
		failed := findFailed(results)
		return failed >= 0 && failed+1 < len(results)
	}, waitTimeout)

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
	mc, agent := startStack(t)
	require.Zero(t, agent.Config().Int(pinpoint.CfgCollectorAgentInfoRefreshInterval))

	require.Len(t, mc.Snapshot().AgentInfos, 1)
	// Several refresh intervals' worth of time for a worker that must not exist.
	time.Sleep(time.Second)
	assert.Len(t, mc.Snapshot().AgentInfos, 1)
	assert.True(t, agent.Enable())
}
