package pinpoint

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func Test_agent_NewAgentError(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := NewAgent(nil)
			assert.Equal(t, NoopAgent(), a, "noop agent")
			assert.Error(t, err, "error")
		})
	}
}

func Test_agent_NewAgent(t *testing.T) {
	type args struct {
		config *Config
	}

	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true

	tests := []struct {
		name string
		args args
	}{
		{"1", args{c}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.args.config
			a, err := NewAgent(c)
			agent := a.(*agent)
			assert.NoError(t, err, "NewAgent")
			assert.Equal(t, "test", agent.appName, "ApplicationName")
			assert.Equal(t, "testagent", agent.agentID, "AgentID")
			assert.Equal(t, int32(ServiceTypeGoApp), agent.appType, "ApplicationType")
			assert.Greater(t, agent.startTime, int64(0), "StartTime")
			assert.Equal(t, GetAgent(), a, "global agent")

			agent.startTime = 12345
			agent.enable.Store(true)
			assert.Equal(t, "testagent^12345^1", agent.generateTransactionId().String(), "generateTransactionId")

			a.Shutdown()
			assert.Equal(t, NoopAgent(), GetAgent(), "global agent")
			assert.Equal(t, false, a.Enable(), "Enable")

			span := agent.NewSpanTracer("test", "/")
			assert.Equal(t, NoopTracer(), span, "NewSpanTracer")
		})
	}
}

func Test_agent_GlobalAgent(t *testing.T) {
	type args struct {
		config *Config
	}

	opts := []ConfigOption{
		WithAppName("testGlobal"),
		WithAgentId("testGlobalAgent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)
	defer a.Shutdown()

	tests := []struct {
		name string
		args args
	}{
		{"1", args{c}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, GetAgent(), a, "global agent")
			assert.NotEqual(t, GetAgent(), NoopAgent(), "global agent")

			a, err := NewAgent(c)
			assert.Error(t, err, "NewAgent")
			assert.Equal(t, GetAgent(), a, "global agent")
		})
	}
}

func Test_agent_NewSpanTracer(t *testing.T) {
	type args struct {
		agent Agent
	}

	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)
	defer a.Shutdown()

	tests := []struct {
		name string
		args args
	}{
		{"1", args{agent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			span := agent.NewSpanTracer("test", "/")

			txid := span.TransactionId()
			assert.Equal(t, "testagent", txid.AgentId, "AgentId")
			assert.Greater(t, txid.StartTime, int64(0), "StartTime")
			assert.Greater(t, txid.Sequence, int64(0), "Sequence")

			spanid := span.SpanId()
			assert.NotEqual(t, int64(0), spanid, "spanId")
		})
	}
}

func Test_agent_NewSpanTracerWithReader(t *testing.T) {
	type args struct {
		agent  Agent
		reader DistributedTracingContextReader
	}

	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)
	defer a.Shutdown()

	m := map[string]string{
		HeaderTraceId:      "t123456^12345^1",
		HeaderSpanId:       "67890",
		HeaderParentSpanId: "123",
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{agent, &DistributedTracingContextMap{m}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			span := agent.NewSpanTracerWithReader("test", "/", tt.args.reader)

			txId := span.TransactionId()
			assert.Equal(t, "t123456", txId.AgentId, "AgentId")
			assert.Equal(t, int64(12345), txId.StartTime, "StartTime")
			assert.Equal(t, int64(1), txId.Sequence, "Sequence")
			assert.Equal(t, int64(67890), span.SpanId(), "SpanId")
		})
	}
}

func Test_abbreviateString_RuneSafe(t *testing.T) {
	assert.Equal(t, "abc", abbreviateString("abc", 5))

	// "가" is 3 bytes; a limit landing mid-rune must back up to the rune
	// boundary, or protobuf rejects the string at marshal time and the whole
	// span/metadata send fails.
	s := strings.Repeat("가", 3)
	got := abbreviateString(s, 4)
	assert.Equal(t, "가...(4)", got)
	assert.True(t, utf8.ValidString(got))
	assert.Equal(t, "가가...(6)", abbreviateString(s, 6))
}

func Test_validUTF8(t *testing.T) {
	assert.Equal(t, "abc가", validUTF8("abc가"), "valid strings must pass through unchanged")
	assert.Equal(t, "a�b", validUTF8("a\xffb"))
	assert.True(t, utf8.ValidString(validUTF8("rowKey: \x9f\x03\xff")))
}

// Plugins feed network-origin bytes into span string fields (percent-decoded
// URL paths, binary row keys, raw query bodies, driver error strings). One
// invalid UTF-8 string fails proto.Marshal for the whole message, and a failed
// span stream Send cancels the stream - so the conversion boundary must
// sanitize every such field.
func Test_spanMessageBuilder_SanitizesInvalidUTF8(t *testing.T) {
	a := newTestAgent(defaultConfig())
	bad := "bad\xff\xfe"

	s := newSampledSpan(a, bad, "/"+bad)
	s.endPoint = bad
	s.remoteAddr = bad
	s.acceptorHost = bad
	s.parentAppName = bad
	s.errorString = bad
	s.annotations.AppendString(AnnotationHttpUrl, bad)

	se := newSpanEvent(s, bad)
	se.endPoint = bad
	se.destinationId = bad
	se.errorString = bad
	se.annotations.AppendStringString(AnnotationHttpUrl, bad, bad)
	s.spanEvents = append(s.spanEvents, se)

	chunk := s.newEventChunk(true)
	builder := acquireSpanMessageBuilder()
	defer releaseSpanMessageBuilder(builder)

	for name, msg := range map[string]*pb.PSpanMessage{
		"span":  builder.makePSpan(chunk),
		"chunk": builder.makePSpanChunk(chunk),
	} {
		_, err := proto.Marshal(msg)
		assert.NoError(t, err, "a %s carrying invalid UTF-8 must still marshal", name)
	}
}

func Test_agent_SQLCachesBoundKeys(t *testing.T) {
	sql := strings.Repeat("x", maxSqlSize*2)
	bounded := abbreviateString(sql, maxSqlSize)

	t.Run("sql id", func(t *testing.T) {
		a := newTestAgent(defaultConfig())
		id := a.cacheSql(sql)

		cached, ok := a.sqlCache.peek(bounded)
		assert.True(t, ok)
		assert.Equal(t, id, cached)
		_, retainedFullSQL := a.sqlCache.peek(sql)
		assert.False(t, retainedFullSQL)

		md := (<-a.metaChan).(sqlMeta)
		assert.Equal(t, bounded, md.sql)
	})

	t.Run("sql uid", func(t *testing.T) {
		a := newTestAgent(defaultConfig())
		uid := a.cacheSqlUid(sql)

		cached, ok := a.sqlUidCache.peek(bounded)
		assert.True(t, ok)
		assert.Equal(t, uid, cached)
		_, retainedFullSQL := a.sqlUidCache.peek(sql)
		assert.False(t, retainedFullSQL)

		md := (<-a.metaChan).(sqlUidMeta)
		assert.Equal(t, bounded, md.sql)
	})
}

func Test_agent_tryEnqueueMetaReturnsWhenDropRaceLeavesQueueEmpty(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.metaChan = make(chan interface{})

	result := callBoolWithTimeout(t, "tryEnqueueMeta", func() bool {
		return agent.tryEnqueueMeta(stringMeta{id: 1, funcName: "error"})
	})

	assert.False(t, result)
}

func Test_agent_enqueueUrlStatReturnsWhenDropRaceLeavesQueueEmpty(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.urlStatChan = make(chan *urlStat)

	result := callBoolWithTimeout(t, "enqueueUrlStat", func() bool {
		return agent.enqueueUrlStat(&urlStat{})
	})

	assert.False(t, result)
}

func Test_agent_enqueueStatReturnsWhenDropRaceLeavesQueueEmpty(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.statChan = make(chan *pb.PStatMessage)

	result := callBoolWithTimeout(t, "enqueueStat", func() bool {
		return agent.enqueueStat(nil)
	})

	assert.False(t, result)
}

func callBoolWithTimeout(t *testing.T, name string, fn func() bool) bool {
	t.Helper()

	done := make(chan bool, 1)
	go func() {
		done <- fn()
	}()

	select {
	case result := <-done:
		return result
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("%s blocked", name)
		return false
	}
}

func Test_waitTimeout(t *testing.T) {
	var wg sync.WaitGroup
	assert.True(t, waitTimeout(&wg, time.Second), "already done")

	wg.Add(1)
	start := time.Now()
	assert.False(t, waitTimeout(&wg, 100*time.Millisecond), "timed out")
	assert.Less(t, time.Since(start), 500*time.Millisecond, "returns at the deadline")

	wg.Done()
	assert.True(t, waitTimeout(&wg, time.Second), "done before the deadline")
}

// Shutdown must not wait for a Done receiver after a ticker worker has already
// observed enable=false and exited.
func Test_agent_ShutdownAfterPingWorkerExited(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.agentGrpc = &agentGrpc{agent: agent}
	agent.statChan = make(chan *pb.PStatMessage)
	agent.urlStatChan = make(chan *urlStat)

	agent.enable.Store(false)
	agent.workerWg.Add(1)
	go agent.superviseWorker("ping", agent.sendPingWorker)
	if !waitTimeout(&agent.workerWg, time.Second) {
		t.Fatal("ping worker did not exit")
	}

	agent.config.offGrpc = false
	agent.enable.Store(true)
	done := make(chan struct{})
	go func() {
		agent.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Shutdown blocked signaling an exited worker")
	}
}

// A worker stuck on an unreachable collector must not hold Shutdown forever.
func Test_agent_ShutdownDeadline(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable.Store(true)

	stuck := make(chan struct{})
	defer close(stuck)
	agent.workerWg.Add(1)
	go func() {
		defer agent.workerWg.Done()
		<-stuck
	}()

	start := time.Now()
	a.Shutdown()
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, shutdownTimeout, "waits for the deadline")
	assert.Less(t, elapsed, shutdownTimeout+2*time.Second, "gives up at the deadline")
}

// The normal path must not pay the startup grace delay.
func Test_agent_ShutdownNoStartupDelay(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	a.(*agent).enable.Store(true)

	start := time.Now()
	a.Shutdown()

	assert.Less(t, time.Since(start), connectGraceTimeout, "no unconditional sleep")
}

// captureWarnLog redirects the agent log to buf until the returned func is called.
func captureWarnLog(buf *bytes.Buffer) func() {
	prevOut, prevLevel := logger.defaultLogger.Out, logger.defaultLogger.GetLevel()
	logger.defaultLogger.SetOutput(buf)
	logger.defaultLogger.SetLevel(logrus.WarnLevel)
	return func() {
		logger.defaultLogger.SetOutput(prevOut)
		logger.defaultLogger.SetLevel(prevLevel)
	}
}

func Test_agent_enqueueUrlStatCountsEveryDroppedRecord(t *testing.T) {
	const queueSize, enqueued = 4, 100

	agent := newTestAgent(defaultConfig())
	agent.urlStatChan = make(chan *urlStat, queueSize)
	defer captureWarnLog(&bytes.Buffer{})()

	for i := 0; i < enqueued; i++ {
		agent.enqueueUrlStat(&urlStat{})
	}

	close(agent.urlStatChan)
	queued := 0
	for range agent.urlStatChan {
		queued++
	}

	// Nothing drained the queue while it filled, so every record that is not
	// still sitting in it was dropped - both the rejected enqueues and the
	// oldest entries evicted to make room for them.
	assert.Equal(t, int64(enqueued-queued), agent.urlStatDrops.Load(),
		"drop counter must account for every record that never reached the consumer")
}

func Test_agent_enqueueUrlStatCountsDropsFromConcurrentProducers(t *testing.T) {
	const producers, perProducer = 8, 250

	agent := newTestAgent(defaultConfig())
	agent.urlStatChan = make(chan *urlStat, 4)
	defer captureWarnLog(&bytes.Buffer{})()

	var wg sync.WaitGroup
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perProducer; j++ {
				agent.enqueueUrlStat(&urlStat{})
			}
		}()
	}
	wg.Wait()

	close(agent.urlStatChan)
	queued := 0
	for range agent.urlStatChan {
		queued++
	}

	assert.Equal(t, int64(producers*perProducer-queued), agent.urlStatDrops.Load())
}

func Test_agent_enqueueUrlStatRateLimitsOverflowWarning(t *testing.T) {
	const queueSize, enqueued = 4, 100

	agent := newTestAgent(defaultConfig())
	agent.urlStatChan = make(chan *urlStat, queueSize)

	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	for i := 0; i < enqueued; i++ {
		agent.enqueueUrlStat(&urlStat{})
	}

	assert.Greater(t, agent.urlStatDrops.Load(), int64(1), "test did not saturate the queue")
	assert.Equal(t, 1, strings.Count(buf.String(), "url stat queue overflow"),
		"a saturated queue must warn once per report interval, not once per dropped record")

	// The next drop after the report interval elapses warns again, carrying the
	// running total rather than restarting the count.
	agent.urlStatDropReportAt.Store(0)
	agent.enqueueUrlStat(&urlStat{})

	assert.Equal(t, 2, strings.Count(buf.String(), "url stat queue overflow"))
	assert.Contains(t, buf.String(), fmt.Sprintf("%d dropped in total (max queue size %d)",
		agent.urlStatDrops.Load(), queueSize))
}

// Shutdown must not close the channels its producers send on. The producers
// (request-path goroutines for meta and url stat, ticker workers for stat)
// only check enable before sending, so a close races them into a "send on
// closed channel" panic - a raw send models a producer that passed that check
// just before Shutdown flipped it. Shutdown must still stop the consumers,
// which used to ride on the close, so it has to return well inside its own
// worker deadline rather than timing out on workers stuck in a receive.
func Test_agent_ShutdownDoesNotCloseProducerChannels(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.statChan = make(chan *pb.PStatMessage, 1)
	agent.urlStatChan = make(chan *urlStat, 1)

	agent.workerWg.Add(2)
	go agent.superviseWorker("meta", agent.sendMetaWorker)
	go agent.superviseWorker("collect uri stat", agent.collectUrlStatWorker)

	start := time.Now()
	agent.Shutdown()
	assert.Less(t, time.Since(start), shutdownTimeout, "consumers must stop on the shutdown signal")

	assert.NotPanics(t, func() { agent.metaChan <- stringMeta{} }, "metaChan")
	assert.NotPanics(t, func() { agent.statChan <- &pb.PStatMessage{} }, "statChan")
	assert.NotPanics(t, func() { agent.urlStatChan <- &urlStat{} }, "urlStatChan")
}

// With every permit held by a slow send, the worker parks on the permit
// acquisition; that wait must obey the stop signal, or the worker dispatches
// one more send after shutdown began.
func Test_agent_sendMetaWorkerStopsWhileAllPermitsHeld(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	blocking := &blockingMetaClient{release: make(chan struct{})}
	agent.agentGrpc = &agentGrpc{metaClient: blocking, agent: agent}

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	for i := 0; i < metaMaxConcurrentRequests; i++ {
		agent.metaChan <- stringMeta{id: int32(i), funcName: "f"}
	}
	assert.Eventually(t, func() bool { return blocking.inFlight() == metaMaxConcurrentRequests },
		5*time.Second, time.Millisecond, "all permits held")

	// One more item: the worker pulls it and parks on the permit acquisition.
	agent.metaChan <- stringMeta{id: 99, funcName: "f"}
	assert.Eventually(t, func() bool { return len(agent.metaChan) == 0 },
		5*time.Second, time.Millisecond, "worker pulled the extra item")

	agent.signalShutdown()
	close(blocking.release)

	assert.True(t, waitTimeout(&agent.workerWg, 5*time.Second), "worker exits")
	_, total := blocking.stats()
	assert.Equal(t, metaMaxConcurrentRequests, total, "no send dispatched after the stop signal")
}

// An agent that never finished registration must still release the global, so
// GetAgent stops handing out the dead agent and NewAgent can be retried.
func Test_agent_ShutdownReleasesGlobalWhenNeverRegistered(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, err := NewAgent(c)
	assert.NoError(t, err, "new agent")
	assert.False(t, a.(*agent).enable.Load(), "never registered")

	a.Shutdown()
	assert.Equal(t, NoopAgent(), GetAgent(), "global agent released")

	// A second Shutdown must not panic, nor unseat the agent created after it.
	c2, _ := NewConfig(opts...)
	c2.offGrpc = true
	a2, err := NewAgent(c2)
	assert.NoError(t, err, "agent creation can be retried")
	a.Shutdown()
	assert.Equal(t, a2, GetAgent(), "stale shutdown leaves the new agent alone")
	a2.Shutdown()
}

func Test_agent_GetAgentIsRaceFreeAgainstShutdown(t *testing.T) {
	c, _ := NewConfig(WithAppName("test"), WithAgentId("testagent"))
	c.offGrpc = true
	a, err := NewAgent(c)
	assert.NoError(t, err, "new agent")
	a.(*agent).enable.Store(true)

	// Request-path readers concurrent with the Shutdown swap; run under -race.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			GetAgent().Enable()
		}
	}()
	a.Shutdown()
	wg.Wait()

	assert.Equal(t, NoopAgent(), GetAgent(), "global agent released")
}

// A metadata item dropped by a full queue must not stay cached: its id was
// already handed to spans, so the entry has to be re-registered rather than
// left pointing at an id the collector never received.
func Test_agent_MetaCacheDropsEntryWhenQueueIsFull(t *testing.T) {
	a := newTestAgent(defaultConfig())

	for full := false; !full; {
		select {
		case a.metaChan <- stringMeta{}:
		default:
			full = true
		}
	}

	first := a.cacheError("boom")
	second := a.cacheError("boom")
	assert.NotZero(t, first, "id minted")
	assert.NotEqual(t, first, second, "dropped metadata is re-registered with a new id")
}

// shortWorkerRestartDelay shortens the supervisor's restart pacing for the
// duration of a test.
func shortWorkerRestartDelay(t *testing.T) {
	prev := workerRestartDelay
	workerRestartDelay = 10 * time.Millisecond
	t.Cleanup(func() { workerRestartDelay = prev })
}

// An agent bug must not take the host process down: a worker body that panics
// is recovered and the worker is restarted after the delay, then stops
// normally on the shutdown signal.
func Test_agent_superviseWorkerRecoversAndRestarts(t *testing.T) {
	shortWorkerRestartDelay(t)
	agent := newTestAgent(defaultConfig())
	stop := agent.stopSignal().Done()

	var runs atomic.Int32
	agent.workerWg.Add(1)
	go agent.superviseWorker("test", func() {
		if runs.Add(1) == 1 {
			panic("worker bug")
		}
		<-stop
	})

	assert.Eventually(t, func() bool { return runs.Load() == 2 },
		5*time.Second, time.Millisecond, "worker must be restarted after the panic")

	agent.signalShutdown()
	assert.True(t, waitTimeout(&agent.workerWg, 5*time.Second), "worker exits on the stop signal")
	assert.EqualValues(t, 2, runs.Load(), "a normal return is not restarted")
}

// A panic while the agent is stopping ends the worker like a normal return:
// no restart, whether shutdown is seen through the stop signal or the enable
// flag.
func Test_agent_superviseWorkerDoesNotRestartWhileStopping(t *testing.T) {
	for name, stopping := range map[string]func(*agent){
		"stop signal": func(a *agent) { a.signalShutdown() },
		"disabled":    func(a *agent) { a.enable.Store(false) },
	} {
		t.Run(name, func(t *testing.T) {
			shortWorkerRestartDelay(t)
			agent := newTestAgent(defaultConfig())

			var runs atomic.Int32
			agent.workerWg.Add(1)
			go agent.superviseWorker("test", func() {
				runs.Add(1)
				stopping(agent)
				panic("worker bug during shutdown")
			})

			assert.True(t, waitTimeout(&agent.workerWg, 5*time.Second), "a panic while stopping must end the worker")
			assert.EqualValues(t, 1, runs.Load(), "worker must not be restarted while stopping")
		})
	}
}

// A panic inside a metadata send must not escape the per-item goroutine: the
// worker keeps running and still exits cleanly on shutdown.
func Test_agent_sendMetaWorkerSurvivesPanicInSend(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.agentGrpc = nil // every send dereferences it: nil pointer panic

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	agent.metaChan <- stringMeta{id: 1, funcName: "f"}
	assert.Eventually(t, func() bool { return len(agent.metaChan) == 0 },
		5*time.Second, time.Millisecond, "worker pulled the item")

	agent.signalShutdown()
	assert.True(t, waitTimeout(&agent.workerWg, 5*time.Second), "worker exits after the recovered send panic")
}
