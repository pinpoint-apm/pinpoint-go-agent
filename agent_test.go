package pinpoint

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
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
			assert.Equal(t, globalAgent, a, "global agent")

			agent.startTime = 12345
			agent.enable.Store(true)
			assert.Equal(t, "testagent^12345^1", agent.generateTransactionId().String(), "generateTransactionId")

			a.Shutdown()
			assert.Equal(t, NoopAgent(), globalAgent, "global agent")
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
			assert.Equal(t, globalAgent, a, "global agent")
			assert.NotEqual(t, globalAgent, NoopAgent(), "global agent")

			a, err := NewAgent(c)
			assert.Error(t, err, "NewAgent")
			assert.Equal(t, globalAgent, a, "global agent")
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
	go agent.sendPingWorker()
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
	go agent.sendMetaWorker()
	go agent.collectUrlStatWorker()

	start := time.Now()
	agent.Shutdown()
	assert.Less(t, time.Since(start), shutdownTimeout, "consumers must stop on the shutdown signal")

	assert.NotPanics(t, func() { agent.metaChan <- stringMeta{} }, "metaChan")
	assert.NotPanics(t, func() { agent.statChan <- &pb.PStatMessage{} }, "statChan")
	assert.NotPanics(t, func() { agent.urlStatChan <- &urlStat{} }, "urlStatChan")
}
