package it

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

const (
	waitTimeout = 10 * time.Second
	longTimeout = 20 * time.Second

	itAppName   = "go-agent-it"
	itAgentName = "go-it-agent-name"
	itAppType   = int32(pinpoint.ServiceTypeGoApp)

	// api types, mirroring the agent's internal span.go constants.
	apiTypeDefault    = int32(0)
	apiTypeWebRequest = int32(100)
	apiTypeInvocation = int32(200)
)

// TestMain removes any PINPOINT_GO_* variable from the developer's environment.
// The agent's env prefix is fixed, so a stray override would silently replace
// the deterministic inline configuration every test relies on.
//
// Once the suite has run, every test's agent and collector have been shut down
// by their t.Cleanup, so nothing should still be parked on an agent channel:
// reportGoroutineLeaks checks that. It only does anything under
// GOEXPERIMENT=goroutineleakprofile, and only when the suite itself passed -
// a failing test leaves its own goroutines behind and would just add noise.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		if k := strings.SplitN(kv, "=", 2)[0]; strings.HasPrefix(k, "PINPOINT_GO_") {
			os.Unsetenv(k)
		}
	}
	code := m.Run()
	if code == 0 {
		code = reportGoroutineLeaks()
	}
	os.Exit(code)
}

// defaultOptions is the inline configuration every test starts from. Tests
// that need other values append their own pinpoint.With* options: a later
// option for the same key overwrites an earlier one.
func defaultOptions(mc *MockCollector) []pinpoint.ConfigOption {
	return []pinpoint.ConfigOption{
		pinpoint.WithAppName(itAppName),
		pinpoint.WithAgentName(itAgentName),
		pinpoint.WithAppType(itAppType),
		pinpoint.WithUidVersion("v3"),
		pinpoint.WithIsContainerEnv(true),
		pinpoint.WithLogLevel("error"),

		pinpoint.WithCollectorHost(mc.Host()),
		pinpoint.WithCollectorAgentPort(mc.AgentPort()),
		pinpoint.WithCollectorSpanPort(mc.SpanPort()),
		pinpoint.WithCollectorStatPort(mc.StatPort()),
		// AgentInfo refresh. A zero interval keeps the periodic re-send off, which
		// is the agent's default.
		pinpoint.WithCollectorAgentInfoRefreshInterval(0),
		pinpoint.WithCollectorAgentInfoSendRetryInterval(50),
		pinpoint.WithCollectorAgentInfoMaxTryPerAttempt(2),
		// Connection and stream renewal, in milliseconds. Zero keeps both off,
		// which is the agent default.
		pinpoint.WithCollectorGrpcConnectionMaxAge(0),
		pinpoint.WithCollectorGrpcStreamMaxAge(0),

		pinpoint.WithSamplingType("COUNTER"),
		pinpoint.WithSamplingCounterRate(1),
		pinpoint.WithSamplingPercentRate(100),

		pinpoint.WithSpanQueueSize(128),
		pinpoint.WithCollectorGrpcSpanBatchSize(4),
		// Short enough that a single span reaches the collector within a test's
		// patience, long enough that a batch of four is still assembled.
		pinpoint.WithCollectorGrpcSpanBatchFlushInterval(50),
		pinpoint.WithCollectorGrpcSpanBatchCollectDeadline(20),
		pinpoint.WithCollectorGrpcSpanBatchMaxConcurrentRequests(2),
		pinpoint.WithSpanEventChunkSize(2),
		pinpoint.WithSpanMaxCallStackDepth(16),
		pinpoint.WithSpanMaxCallStackSequence(128),

		// One agent-stat batch per tick at the configuration floor (1s), so the
		// statistics assertions do not wait for the production 5s/6-batch
		// cadence. A value below the floor silently falls back to 5s.
		pinpoint.WithStatCollectInterval(1000),
		pinpoint.WithStatBatchCount(1),

		pinpoint.WithSQLTraceQueryStat(true),
		pinpoint.WithSQLTraceBindValue(true),
		pinpoint.WithSQLMaxBindValueSize(1024),

		pinpoint.WithHttpUrlStatEnable(true),
		pinpoint.WithHttpUrlStatWithMethod(true),
		pinpoint.WithHttpUrlStatQueueSize(128),

		pinpoint.WithErrorTraceCallStack(true),
		pinpoint.WithErrorCallStackDepth(8),

		pphttp.WithHttpServerStatusCodeError([]string{"4xx", "5xx"}),
		pphttp.WithHttpServerExcludeUrl([]string{"/excluded/**"}),
		pphttp.WithHttpServerExcludeMethod([]string{"OPTIONS"}),
		pphttp.WithHttpServerRecordRequestHeader([]string{"x-request-id"}),
		pphttp.WithHttpServerRecordRequestCookie([]string{"session_id"}),
		pphttp.WithHttpServerRecordRespondHeader([]string{"x-response-id"}),
		pphttp.WithHttpClientRecordRequestHeader([]string{"x-client-request"}),
		pphttp.WithHttpClientRecordRequestCookie([]string{"client_session"}),
		pphttp.WithHttpClientRecordRespondHeader([]string{"x-client-response"}),
	}
}

// startCollector starts the in-process collector and stops it on test cleanup.
func startCollector(t *testing.T) *MockCollector {
	t.Helper()
	mc := NewMockCollector()
	require.NoError(t, mc.Start())
	require.Greater(t, mc.AgentPort(), 0)
	require.Greater(t, mc.SpanPort(), 0)
	require.Greater(t, mc.StatPort(), 0)
	t.Cleanup(mc.Shutdown)
	return mc
}

// startAgent builds an agent from defaultOptions plus opts and returns it
// without waiting for registration. The agent is shut down on test cleanup.
func startAgent(t *testing.T, mc *MockCollector, opts ...pinpoint.ConfigOption) pinpoint.Agent {
	t.Helper()
	config, err := pinpoint.NewConfig(append(defaultOptions(mc), opts...)...)
	require.NoError(t, err)
	agent, err := pinpoint.NewAgent(config)
	require.NoError(t, err, "a previous test left a global agent installed")
	t.Cleanup(agent.Shutdown)
	return agent
}

// startStack starts the collector and an agent, then blocks until the agent is
// registered and enabled.
func startStack(t *testing.T, opts ...pinpoint.ConfigOption) (*MockCollector, pinpoint.Agent) {
	t.Helper()
	return startArmedStack(t, nil, opts...)
}

// startArmedStack is startStack with a collector-side fault armed before the
// agent starts: NewAgent begins registration immediately.
func startArmedStack(t *testing.T, arm func(*MockCollector), opts ...pinpoint.ConfigOption) (*MockCollector, pinpoint.Agent) {
	t.Helper()
	mc := startCollector(t)
	if arm != nil {
		arm(mc)
	}
	agent := startAgent(t, mc, opts...)
	mc.WaitFor(t, func(s Snapshot) bool { return len(s.AgentInfos) > 0 }, waitTimeout,
		"the agent never registered with the collector")
	require.Eventually(t, func() bool { return agent.Enable() }, waitTimeout, 10*time.Millisecond,
		"the agent never came online")
	return mc, agent
}

// mapCarrier is a distributed-tracing carrier backed by a plain map.
type mapCarrier map[string]string

// Get reports a key held with an empty value as present, as a real header
// carrier does: the map is the only place the test can express that.
func (m mapCarrier) Get(key string) (string, bool) { v, ok := m[key]; return v, ok }
func (m mapCarrier) Set(key, value string)         { m[key] = value }
func (m mapCarrier) has(key string) bool           { _, ok := m[key]; return ok }

// handleInstrumentedRequest is the host application's "business logic": a fake
// request handler that must produce its result no matter what state the agent
// or the collector is in.
func handleInstrumentedRequest(agent pinpoint.Agent, rpc string, input int) int {
	tracer := agent.NewSpanTracer("app.request", rpc)
	tracer.NewSpanEvent("app.compute")
	result := input*2 + 1
	tracer.EndSpanEvent()
	tracer.Span().SetError(nil)
	tracer.EndSpan()
	return result
}

// requireNoopTracer asserts tracer is the inert tracer the agent hands out
// whenever tracing is impossible: nothing is recorded, no identifiers are
// minted, and outbound context injection stays empty so downstream services
// see an untraced call.
func requireNoopTracer(t *testing.T, tracer pinpoint.Tracer) {
	t.Helper()
	require.NotNil(t, tracer)
	assert.False(t, tracer.IsSampled())
	assert.Equal(t, int64(0), tracer.SpanId())
	assert.Equal(t, "Noop", tracer.TransactionId().AgentId)

	event := tracer.NewSpanEvent("noop.probe")
	outbound := mapCarrier{}
	event.Inject(outbound)
	assert.False(t, outbound.has(pinpoint.HeaderTraceId))
	assert.False(t, outbound.has(pinpoint.HeaderSpanId))
	tracer.EndSpanEvent()
	tracer.EndSpan()
}

// --- wire accessors --------------------------------------------------------

func allSpanMessages(s Snapshot) []*pb.PSpanMessage {
	result := make([]*pb.PSpanMessage, 0, len(s.SpanMessages)+len(s.SpanBatches)*2)
	for _, r := range s.SpanMessages {
		result = append(result, r.Message)
	}
	for _, r := range s.SpanBatches {
		result = append(result, r.Message.GetSpan()...)
	}
	return result
}

func findSpanByRpc(s Snapshot, rpc string) *pb.PSpan {
	for _, m := range allSpanMessages(s) {
		if span := m.GetSpan(); span != nil && span.GetAcceptEvent().GetRpc() == rpc {
			return span
		}
	}
	return nil
}

func countSpansByRpc(s Snapshot, rpc string) int {
	count := 0
	for _, m := range allSpanMessages(s) {
		if span := m.GetSpan(); span != nil && span.GetAcceptEvent().GetRpc() == rpc {
			count++
		}
	}
	return count
}

func eventsForSpan(s Snapshot, spanID int64) []*pb.PSpanEvent {
	result := make([]*pb.PSpanEvent, 0)
	for _, m := range allSpanMessages(s) {
		if span := m.GetSpan(); span != nil && span.GetSpanId() == spanID {
			result = append(result, span.GetSpanEvent()...)
		}
		if chunk := m.GetSpanChunk(); chunk != nil && chunk.GetSpanId() == spanID {
			result = append(result, chunk.GetSpanEvent()...)
		}
	}
	return result
}

// asyncChunksFor returns the PSpanChunks a span's async spans produced. They
// are the only chunks carrying a localAsyncId.
func asyncChunksFor(s Snapshot, spanID int64) []*pb.PSpanChunk {
	result := make([]*pb.PSpanChunk, 0)
	for _, m := range allSpanMessages(s) {
		chunk := m.GetSpanChunk()
		if chunk != nil && chunk.GetSpanId() == spanID && chunk.GetLocalAsyncId() != nil {
			result = append(result, chunk)
		}
	}
	return result
}

func findAnnotation(list []*pb.PAnnotation, key int32) *pb.PAnnotation {
	for _, a := range list {
		if a.GetKey() == key {
			return a
		}
	}
	return nil
}

func hasStringPairAnnotation(list []*pb.PAnnotation, key int32, first, second string) bool {
	for _, a := range list {
		if a.GetKey() != key {
			continue
		}
		pair := a.GetValue().GetStringStringValue()
		if pair.GetStringValue1().GetValue() == first && pair.GetStringValue2().GetValue() == second {
			return true
		}
	}
	return false
}

func agentStats(s Snapshot) []*pb.PAgentStat {
	result := make([]*pb.PAgentStat, 0)
	for _, r := range s.Stats {
		if batch := r.Message.GetAgentStatBatch(); batch != nil {
			result = append(result, batch.GetAgentStat()...)
		}
	}
	return result
}

type transactionTotals struct {
	sampledNew          int64
	sampledContinuation int64
	unsampledNew        int64
	unsampledCont       int64
	skippedNew          int64
	skippedCont         int64
}

// transactionTotalsAfter sums the transaction counters of every agent stat
// past the first skip entries. Statistics flush on a fixed tick, so a baseline
// taken before the traced work must be skipped rather than subtracted.
func transactionTotalsAfter(s Snapshot, skip int) transactionTotals {
	var totals transactionTotals
	for i, stat := range agentStats(s) {
		tx := stat.GetTransaction()
		if i < skip || tx == nil {
			continue
		}
		totals.sampledNew += tx.GetSampledNewCount()
		totals.sampledContinuation += tx.GetSampledContinuationCount()
		totals.unsampledNew += tx.GetUnsampledNewCount()
		totals.unsampledCont += tx.GetUnsampledContinuationCount()
		totals.skippedNew += tx.GetSkippedNewCount()
		totals.skippedCont += tx.GetSkippedContinuationCount()
	}
	return totals
}

func maxResponseTimeAfter(s Snapshot, skip int) int64 {
	var m int64
	for i, stat := range agentStats(s) {
		if i >= skip {
			m = max(m, stat.GetResponseTime().GetMax())
		}
	}
	return m
}

// sampledNewAgentStat returns the first agent stat that actually carries a
// sampled-new transaction count. Batches whose interval closed before a
// sampled transaction started carry a zero count.
func sampledNewAgentStat(s Snapshot) *pb.PAgentStat {
	for _, stat := range agentStats(s) {
		if stat.GetTransaction().GetSampledNewCount() >= 1 {
			return stat
		}
	}
	return nil
}

type uriStatTotals struct {
	totalElapsed  int64
	failedElapsed int64
	maxElapsed    int64
	failedMax     int64
	totalCount    int64
	failedCount   int64
	entries       int
}

func uriStatTotalsFor(s Snapshot, uri string) uriStatTotals {
	var totals uriStatTotals
	for _, r := range s.Stats {
		uriStat := r.Message.GetAgentUriStat()
		if uriStat == nil {
			continue
		}
		for _, each := range uriStat.GetEachUriStat() {
			if each.GetUri() != uri {
				continue
			}
			totals.entries++
			totals.totalElapsed += each.GetTotalHistogram().GetTotal()
			totals.failedElapsed += each.GetFailedHistogram().GetTotal()
			totals.maxElapsed = max(totals.maxElapsed, each.GetTotalHistogram().GetMax())
			totals.failedMax = max(totals.failedMax, each.GetFailedHistogram().GetMax())
			for _, c := range each.GetTotalHistogram().GetHistogram() {
				totals.totalCount += int64(c)
			}
			for _, c := range each.GetFailedHistogram().GetHistogram() {
				totals.failedCount += int64(c)
			}
		}
	}
	return totals
}

func resultsFor(s Snapshot, rpc Rpc) []RpcResult {
	return slices.DeleteFunc(slices.Clone(s.RpcResults), func(r RpcResult) bool { return r.Rpc != rpc })
}

func hasResult(s Snapshot, rpc Rpc, code codes.Code) bool { return countResults(s, rpc, code) > 0 }

func hasResultSuccess(s Snapshot, rpc Rpc, code codes.Code, success bool) bool {
	return slices.ContainsFunc(s.RpcResults, func(r RpcResult) bool {
		return r.Rpc == rpc && r.Code == code && r.Success == success
	})
}

func countResults(s Snapshot, rpc Rpc, code codes.Code) int {
	count := 0
	for _, r := range s.RpcResults {
		if r.Rpc == rpc && r.Code == code {
			count++
		}
	}
	return count
}

// acceptedApiIds returns the api ids the collector accepted metadata for under
// apiInfo. A span resolves only when its api id is among them.
func acceptedApiIds(s Snapshot, apiInfo string) map[int32]bool {
	ids := make(map[int32]bool)
	for _, r := range resultsFor(s, RpcApiMetadata) {
		if m, ok := r.Request.(*pb.PApiMetaData); ok && r.Success && m.GetApiInfo() == apiInfo {
			ids[m.GetApiId()] = true
		}
	}
	return ids
}

// apiIdsFor returns every api id the agent sent metadata for under apiInfo,
// accepted or not.
func apiIdsFor(s Snapshot, apiInfo string) map[int32]bool {
	ids := make(map[int32]bool)
	for _, r := range s.ApiMetadata {
		if r.Message.GetApiInfo() == apiInfo {
			ids[r.Message.GetApiId()] = true
		}
	}
	return ids
}

// assertNoSpanSentTwice checks the span sender's drop policy: a batch that
// failed is lost, never re-sent, so no span reaches the collector twice.
func assertNoSpanSentTwice(t *testing.T, s Snapshot) {
	t.Helper()
	sent := make(map[int64]int)
	for _, m := range allSpanMessages(s) {
		if span := m.GetSpan(); span != nil {
			sent[span.GetSpanId()]++
		}
	}
	for id, n := range sent {
		assert.Equalf(t, 1, n, "span %d reached the collector %d times", id, n)
	}
}

func hasApiMetadata(s Snapshot, apiInfo string, apiType int32) bool {
	for _, r := range s.ApiMetadata {
		if r.Message.GetApiInfo() == apiInfo && r.Message.GetType() == apiType {
			return true
		}
	}
	return false
}

func countApiMetadata(s Snapshot, apiInfo string) int {
	count := 0
	for _, r := range s.ApiMetadata {
		if r.Message.GetApiInfo() == apiInfo {
			count++
		}
	}
	return count
}

func countActiveThreadResponses(s Snapshot, responseID int32, sequenceID ...int32) int {
	count := 0
	for _, r := range s.ActiveThreadCountResponses {
		common := r.Message.GetCommonStreamResponse()
		if common.GetResponseId() != responseID {
			continue
		}
		if len(sequenceID) > 0 && common.GetSequenceId() != sequenceID[0] {
			continue
		}
		count++
	}
	return count
}

func hasEchoResponse(s Snapshot, responseID int32) bool {
	for _, r := range s.EchoResponses {
		if r.Message.GetCommonResponse().GetResponseId() == responseID {
			return true
		}
	}
	return false
}

// generatedAgentIDLen is the length of the agent id the agent always mints for
// itself: base64url of a UUIDv7 without padding. The id is not configurable.
const generatedAgentIDLen = 22

// registeredAgentID returns the generated agent id the collector saw on the
// first AgentInfo registration.
func registeredAgentID(t *testing.T, mc *MockCollector) string {
	t.Helper()
	s := mc.Snapshot()
	require.NotEmpty(t, s.AgentInfos)
	id := s.AgentInfos[0].Metadata.ValueOr("agentid", "")
	require.Len(t, id, generatedAgentIDLen)
	return id
}

// expectCommonMetadata asserts the agent identity headers every collector
// channel must carry. Only the ping and active-thread-count streams add a
// socket id.
func expectCommonMetadata(t *testing.T, md RpcMetadata, expectSocketID bool) {
	t.Helper()
	assert.Equal(t, itAppName, md.ValueOr("applicationname", ""))
	assert.Len(t, md.ValueOr("agentid", ""), generatedAgentIDLen)
	assert.Equal(t, itAgentName, md.ValueOr("agentname", ""))
	assert.Equal(t, fmt.Sprint(itAppType), md.ValueOr("servicetype", ""))
	assert.Equal(t, "100", md.ValueOr("protocol.version", ""))
	assert.NotEmpty(t, md.ValueOr("starttime", ""))
	assert.Equal(t, expectSocketID, md.Has("socketid"))
}

// findFailMessage returns the command-stream rejection recorded for responseID,
// which is the only channel the protocol offers for refusing a command.
func findFailMessage(s Snapshot, responseID int32) *pb.PCmdResponse {
	for _, r := range s.CommandStreamMessages {
		if fail := r.Message.GetFailMessage(); fail != nil && fail.GetResponseId() == responseID {
			return fail
		}
	}
	return nil
}

func findExceptionForSpan(s Snapshot, spanID int64) *pb.PExceptionMetaData {
	for _, r := range s.ExceptionMetadata {
		if r.Message.GetSpanId() == spanID {
			return r.Message
		}
	}
	return nil
}

func findSqlUidMetadata(s Snapshot, normalizedSQL string) *pb.PSqlUidMetaData {
	for _, r := range s.SqlUidMetadata {
		if r.Message.GetSql() == normalizedSQL {
			return r.Message
		}
	}
	return nil
}

func countSqlUidMetadata(s Snapshot, normalizedSQL string) int {
	count := 0
	for _, r := range s.SqlUidMetadata {
		if r.Message.GetSql() == normalizedSQL {
			count++
		}
	}
	return count
}

func findSqlMetadata(s Snapshot, normalizedSQL string) *pb.PSqlMetaData {
	for _, r := range s.SqlMetadata {
		if r.Message.GetSql() == normalizedSQL {
			return r.Message
		}
	}
	return nil
}

// driveSamplingPattern issues one request per entry in expected, asserts the
// sampling decision it got, and returns the trace id of the first sampled one.
func driveSamplingPattern(t *testing.T, agent pinpoint.Agent, operation, rpcPrefix string,
	expected []bool, parent mapCarrier) string {
	t.Helper()
	var firstSampled string
	for i, want := range expected {
		rpc := rpcPrefix + fmt.Sprint(i)
		var tracer pinpoint.Tracer
		if parent == nil {
			tracer = agent.NewSpanTracer(operation, rpc)
		} else {
			tracer = agent.NewSpanTracerWithReader(operation, rpc, parent)
		}
		assert.Equal(t, want, tracer.IsSampled(), rpc)
		if tracer.IsSampled() && firstSampled == "" {
			firstSampled = tracer.TransactionId().String()
		}
		tracer.EndSpan()
	}
	return firstSampled
}

func expectSamplingPattern(t *testing.T, s Snapshot, rpcPrefix string, expected []bool) {
	t.Helper()
	for i, want := range expected {
		rpc := rpcPrefix + fmt.Sprint(i)
		count := 0
		if want {
			count = 1
		}
		assert.Equal(t, count, countSpansByRpc(s, rpc), rpc)
	}
}

func countSpansByRpcPrefix(s Snapshot, prefix string) int {
	count := 0
	for _, m := range allSpanMessages(s) {
		if span := m.GetSpan(); span != nil &&
			strings.HasPrefix(span.GetAcceptEvent().GetRpc(), prefix) {
			count++
		}
	}
	return count
}
