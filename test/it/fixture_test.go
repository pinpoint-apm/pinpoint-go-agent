package it

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

const (
	waitTimeout = 10 * time.Second
	longTimeout = 20 * time.Second

	itAppName   = "go-agent-it"
	itAgentID   = "go-it-agent-id"
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
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		if k := strings.SplitN(kv, "=", 2)[0]; strings.HasPrefix(k, "PINPOINT_GO_") {
			os.Unsetenv(k)
		}
	}
	os.Exit(m.Run())
}

// agentConfig holds the knobs consumed by options(). Tests that need
// non-default values change the struct returned by defaultAgentConfig before
// calling startStack.
type agentConfig struct {
	samplingType        string
	samplingCounterRate int
	samplingPercentRate float32
	newThroughput       int
	continueThroughput  int

	uidVersion  string
	serviceName string
	apiKey      string

	spanQueueSize                  int
	spanBatchSize                  int
	spanBatchFlushInterval         int
	spanBatchCollectDeadline       int
	spanBatchMaxConcurrentRequests int
	spanEventChunkSize             int
	maxCallStackDepth              int
	maxCallStackSequence           int

	statCollectInterval int
	statBatchCount      int

	sqlTraceQueryStat   bool
	sqlTraceBindValue   bool
	sqlMaxBindValueSize int

	urlStatEnable     bool
	urlStatWithMethod bool

	errorTraceCallStack bool
	errorCallStackDepth int

	serverExcludeUrls     []string
	serverExcludeMethods  []string
	serverRequestHeaders  []string
	serverRequestCookies  []string
	serverResponseHeaders []string
	clientRequestHeaders  []string
	clientRequestCookies  []string
	clientResponseHeaders []string
	serverStatusCodeError []string
}

func defaultAgentConfig() *agentConfig {
	return &agentConfig{
		samplingType:        "COUNTER",
		samplingCounterRate: 1,
		samplingPercentRate: 100,

		uidVersion: "v3",

		spanQueueSize: 128,
		spanBatchSize: 4,
		// Short enough that a single span reaches the collector within a test's
		// patience, long enough that a batch of four is still assembled.
		spanBatchFlushInterval:         50,
		spanBatchCollectDeadline:       20,
		spanBatchMaxConcurrentRequests: 2,
		spanEventChunkSize:             2,
		maxCallStackDepth:              16,
		maxCallStackSequence:           128,

		// One agent-stat batch per tick, so the statistics assertions do not
		// wait for the production 5s/6-batch cadence.
		statCollectInterval: 200,
		statBatchCount:      1,

		sqlTraceQueryStat:   true,
		sqlTraceBindValue:   true,
		sqlMaxBindValueSize: 1024,

		urlStatEnable:     true,
		urlStatWithMethod: true,

		errorTraceCallStack: true,
		errorCallStackDepth: 8,

		serverExcludeUrls:     []string{"/excluded/**"},
		serverExcludeMethods:  []string{"OPTIONS"},
		serverRequestHeaders:  []string{"x-request-id"},
		serverRequestCookies:  []string{"session_id"},
		serverResponseHeaders: []string{"x-response-id"},
		clientRequestHeaders:  []string{"x-client-request"},
		clientRequestCookies:  []string{"client_session"},
		clientResponseHeaders: []string{"x-client-response"},
		serverStatusCodeError: []string{"4xx", "5xx"},
	}
}

func (c *agentConfig) options(mc *MockCollector) []pinpoint.ConfigOption {
	return []pinpoint.ConfigOption{
		pinpoint.WithAppName(itAppName),
		pinpoint.WithAgentId(itAgentID),
		pinpoint.WithAgentName(itAgentName),
		pinpoint.WithAppType(itAppType),
		pinpoint.WithUidVersion(c.uidVersion),
		pinpoint.WithServiceName(c.serviceName),
		pinpoint.WithApiKey(c.apiKey),
		pinpoint.WithIsContainerEnv(true),
		pinpoint.WithLogLevel("error"),

		pinpoint.WithCollectorHost(mc.Host()),
		pinpoint.WithCollectorAgentPort(mc.AgentPort()),
		pinpoint.WithCollectorSpanPort(mc.SpanPort()),
		pinpoint.WithCollectorStatPort(mc.StatPort()),

		pinpoint.WithSamplingType(c.samplingType),
		pinpoint.WithSamplingCounterRate(c.samplingCounterRate),
		pinpoint.WithSamplingPercentRate(c.samplingPercentRate),
		pinpoint.WithSamplingNewThroughput(c.newThroughput),
		pinpoint.WithSamplingContinueThroughput(c.continueThroughput),

		pinpoint.WithSpanQueueSize(c.spanQueueSize),
		pinpoint.WithSpanBatchEnable(true),
		pinpoint.WithSpanBatchSize(c.spanBatchSize),
		pinpoint.WithSpanBatchFlushInterval(c.spanBatchFlushInterval),
		pinpoint.WithSpanBatchCollectDeadline(c.spanBatchCollectDeadline),
		pinpoint.WithSpanBatchMaxConcurrentRequests(c.spanBatchMaxConcurrentRequests),
		pinpoint.WithSpanEventChunkSize(c.spanEventChunkSize),
		pinpoint.WithSpanMaxCallStackDepth(c.maxCallStackDepth),
		pinpoint.WithSpanMaxCallStackSequence(c.maxCallStackSequence),

		pinpoint.WithStatCollectInterval(c.statCollectInterval),
		pinpoint.WithStatBatchCount(c.statBatchCount),

		pinpoint.WithSQLTraceQueryStat(c.sqlTraceQueryStat),
		pinpoint.WithSQLTraceBindValue(c.sqlTraceBindValue),
		pinpoint.WithSQLMaxBindValueSize(c.sqlMaxBindValueSize),

		pinpoint.WithHttpUrlStatEnable(c.urlStatEnable),
		pinpoint.WithHttpUrlStatWithMethod(c.urlStatWithMethod),

		pinpoint.WithErrorTraceCallStack(c.errorTraceCallStack),
		pinpoint.WithErrorCallStackDepth(c.errorCallStackDepth),

		pphttp.WithHttpServerStatusCodeError(c.serverStatusCodeError),
		pphttp.WithHttpServerExcludeUrl(c.serverExcludeUrls),
		pphttp.WithHttpServerExcludeMethod(c.serverExcludeMethods),
		pphttp.WithHttpServerRecordRequestHeader(c.serverRequestHeaders),
		pphttp.WithHttpServerRecordRequestCookie(c.serverRequestCookies),
		pphttp.WithHttpServerRecordRespondHeader(c.serverResponseHeaders),
		pphttp.WithHttpClientRecordRequestHeader(c.clientRequestHeaders),
		pphttp.WithHttpClientRecordRequestCookie(c.clientRequestCookies),
		pphttp.WithHttpClientRecordRespondHeader(c.clientResponseHeaders),
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

// startAgent builds an agent from cfg and returns it without waiting for
// registration. The agent is shut down on test cleanup.
func startAgent(t *testing.T, mc *MockCollector, cfg *agentConfig) pinpoint.Agent {
	t.Helper()
	config, err := pinpoint.NewConfig(cfg.options(mc)...)
	require.NoError(t, err)
	agent, err := pinpoint.NewAgent(config)
	require.NoError(t, err, "a previous test left a global agent installed")
	t.Cleanup(agent.Shutdown)
	return agent
}

// startStack starts the collector and an agent, then blocks until the agent is
// registered and enabled.
func startStack(t *testing.T, cfg *agentConfig, arm ...func(*MockCollector)) (*MockCollector, pinpoint.Agent) {
	t.Helper()
	mc := startCollector(t)
	// Collector-side faults must be armed before the agent starts: NewAgent
	// begins registration immediately.
	for _, fn := range arm {
		fn(mc)
	}
	agent := startAgent(t, mc, cfg)
	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.AgentInfos) > 0 }, waitTimeout),
		"the agent never registered with the collector")
	require.True(t, waitUntil(func() bool { return agent.Enable() }, waitTimeout),
		"the agent never came online")
	return mc, agent
}

func waitUntil(predicate func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return predicate()
}

// mapCarrier is a distributed-tracing carrier backed by a plain map.
type mapCarrier map[string]string

func (m mapCarrier) Get(key string) string { return m[key] }
func (m mapCarrier) Set(key, value string) { m[key] = value }
func (m mapCarrier) has(key string) bool   { _, ok := m[key]; return ok }

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

func agentStatCount(s Snapshot) int { return len(agentStats(s)) }

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
	var max int64
	for i, stat := range agentStats(s) {
		if i < skip {
			continue
		}
		if v := stat.GetResponseTime().GetMax(); v > max {
			max = v
		}
	}
	return max
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
			if v := each.GetTotalHistogram().GetMax(); v > totals.maxElapsed {
				totals.maxElapsed = v
			}
			if v := each.GetFailedHistogram().GetMax(); v > totals.failedMax {
				totals.failedMax = v
			}
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

func hasUriStat(s Snapshot, uri string) bool {
	return uriStatTotalsFor(s, uri).entries > 0
}

func resultsFor(s Snapshot, rpc Rpc) []RpcResult {
	result := make([]RpcResult, 0)
	for _, r := range s.RpcResults {
		if r.Rpc == rpc {
			result = append(result, r)
		}
	}
	return result
}

func hasResult(s Snapshot, rpc Rpc, code codes.Code) bool {
	for _, r := range s.RpcResults {
		if r.Rpc == rpc && r.Code == code {
			return true
		}
	}
	return false
}

func hasResultSuccess(s Snapshot, rpc Rpc, code codes.Code, success bool) bool {
	for _, r := range s.RpcResults {
		if r.Rpc == rpc && r.Code == code && r.Success == success {
			return true
		}
	}
	return false
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

// expectCommonMetadata asserts the agent identity headers every collector
// channel must carry. Only the ping and active-thread-count streams add a
// socket id.
func expectCommonMetadata(t *testing.T, md RpcMetadata, expectSocketID bool) {
	t.Helper()
	assert.Equal(t, itAppName, md.ValueOr("applicationname", ""))
	assert.Equal(t, itAgentID, md.ValueOr("agentid", ""))
	assert.Equal(t, itAgentName, md.ValueOr("agentname", ""))
	assert.Equal(t, fmt.Sprint(itAppType), md.ValueOr("servicetype", ""))
	assert.Equal(t, "100", md.ValueOr("protocol.version", ""))
	assert.NotEmpty(t, md.ValueOr("starttime", ""))
	assert.Equal(t, expectSocketID, md.Has("socketid"))
}

// isolate re-executes the calling test in a child process and reports whether
// this process is that child.
//
// Some scenarios deliberately leave a never-enabled agent installed as the
// process-global singleton: Agent.Shutdown clears the global only when the
// agent was enabled, so a later NewAgent in the same process is refused.
// Running those in their own process keeps the rest of the suite
// order-independent.
func isolate(t *testing.T) bool {
	if os.Getenv("PINPOINT_IT_ISOLATED") == t.Name() {
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.timeout=5m")
	cmd.Env = append(os.Environ(), "PINPOINT_IT_ISOLATED="+t.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated run failed: %v\n%s", err, out)
	}
	return false
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
