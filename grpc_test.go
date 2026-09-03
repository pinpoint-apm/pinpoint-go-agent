package pinpoint

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type blockingAgentInfoClient struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (c *blockingAgentInfoClient) RequestAgentInfo(ctx context.Context, _ *pb.PAgentInfo, _ ...grpc.CallOption) (*pb.PResult, error) {
	close(c.started)
	select {
	case <-ctx.Done():
		close(c.canceled)
		return nil, ctx.Err()
	case <-c.release:
		return nil, context.Canceled
	}
}

func (*blockingAgentInfoClient) PingSession(context.Context, ...grpc.CallOption) (pb.Agent_PingSessionClient, error) {
	return nil, nil
}

func Test_agentGrpc_sendAgentInfo(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpc(agent)
			b := agent.agentGrpc.registerAgentWithRetry()
			assert.Equal(t, true, b, "sendAgentInfo")
		})
	}
}

func Test_agentGrpc_registerAgentWithRetry_cancelsRequestOnShutdown(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.enable.Store(false)
	client := &blockingAgentInfoClient{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	defer close(client.release)
	agent.agentGrpc = &agentGrpc{agentClient: client, agent: agent}

	agent.connectWg.Add(1)
	go func() {
		defer agent.connectWg.Done()
		agent.agentGrpc.registerAgentWithRetry()
	}()

	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("RequestAgentInfo did not start")
	}

	shutdownDone := make(chan struct{})
	go func() {
		agent.Shutdown()
		close(shutdownDone)
	}()

	select {
	case <-client.canceled:
	case <-time.After(connectGraceTimeout + 2*time.Second):
		t.Fatal("shutdown did not cancel RequestAgentInfo")
	}

	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown remained blocked after canceling RequestAgentInfo")
	}
}

func Test_agentGrpc_sendApiMetadata(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpc(agent)
			b := agent.agentGrpc.sendApiMetadataWithRetry(1, "Asynchronous Invocation", -1, apiTypeInvocation)
			assert.Equal(t, true, b, "sendApiMetadata")
		})
	}
}

func Test_agentGrpc_sendSqlMetadata(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpc(agent)
			b := agent.agentGrpc.sendSqlMetadataWithRetry(1, "SELECT 1")
			assert.Equal(t, true, b, "sendSqlMetadata")
		})
	}
}

func Test_agentGrpc_sendStringMetadata(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpc(agent)
			b := agent.agentGrpc.sendStringMetadataWithRetry(1, "string value")
			assert.Equal(t, true, b, "sendStringMetadata")
		})
	}
}

func Test_pingStream_sendPing(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpc(agent)
			stream := agent.agentGrpc.newPingStreamWithRetry()
			err := stream.sendPing()
			assert.NoError(t, err, "sendPing")
		})
	}
}

func Test_spanStream_sendSpan(t *testing.T) {
	type args struct {
		agent *agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(defaultConfig())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.spanGrpc = newMockSpanGrpc(agent)
			stream := agent.spanGrpc.newSpanStreamWithRetry()

			span := defaultSpan(agent)
			span.NewSpanEvent("t1")
			err := stream.sendSpan(span.newEventChunk(true))
			assert.NoError(t, err, "sendSpan")
			stream.close()
		})
	}
}

func Test_spanGrpc_sendSpanBatch(t *testing.T) {
	type args struct {
		agent *agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(defaultConfig())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.spanGrpc = newMockSpanGrpc(agent)

			span := defaultSpan(agent)
			span.NewSpanEvent("t1")
			agent.spanGrpc.sendSpanBatchAsync([]*spanChunk{span.newEventChunk(true)})
			agent.spanGrpc.awaitInFlightSpanBatch()

			client := agent.spanGrpc.spanClient.(*mockSpanGrpcClient)
			assert.Equal(t, 1, client.requestCount(), "sendSpanBatch")
			assert.Len(t, client.lastRequest().GetSpan(), 1, "span batch size")
		})
	}
}

func Test_spanGrpc_sendSpanBatchEmptyReleasesPermit(t *testing.T) {
	spanGrpc := &spanGrpc{
		batchFlushTimeout:       time.Millisecond,
		maxConcurrentRequests:   1,
		concurrentRequestPermit: make(chan struct{}, 1),
	}

	spanGrpc.sendSpanBatchAsync(nil)

	assert.Empty(t, spanGrpc.concurrentRequestPermit)
}

func Test_agent_enqueueSpan_discardsOldestAndEnqueuesNewest(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSpanBatchEnable, true)
	agent := newTestAgent(cfg)
	agent.spanQueue = newSpanQueue(2) // single shard: FIFO is deterministic

	first := newTestSpanChunk(agent)
	second := newTestSpanChunk(agent)
	third := newTestSpanChunk(agent)

	assert.True(t, agent.enqueueSpan(first), "enqueue first")
	assert.True(t, agent.enqueueSpan(second), "enqueue second")
	assert.True(t, agent.enqueueSpan(third), "enqueue third")

	got, _ := agent.spanQueue.tryDequeue()
	assert.Equal(t, second, got, "oldest span should be discarded")
	got, _ = agent.spanQueue.tryDequeue()
	assert.Equal(t, third, got, "newest span should be enqueued")
}

func Test_agent_enqueueSpan_streamModeAlsoDiscardsOldest(t *testing.T) {
	// The queue-full drop policy is independent of the span transport: even in
	// legacy stream mode (Span.Batch.Enable=false) enqueueSpan discards the
	// oldest span and enqueues the newest, so recent traces are favored under
	// backpressure just like in batch mode.
	cfg := defaultConfig()
	cfg.Set(CfgSpanBatchEnable, false)
	agent := newTestAgent(cfg)
	agent.spanQueue = newSpanQueue(2) // single shard: FIFO is deterministic

	first := newTestSpanChunk(agent)
	second := newTestSpanChunk(agent)
	third := newTestSpanChunk(agent)

	assert.True(t, agent.enqueueSpan(first), "enqueue first")
	assert.True(t, agent.enqueueSpan(second), "enqueue second")
	assert.True(t, agent.enqueueSpan(third), "newest span is enqueued after discarding the oldest")

	got, _ := agent.spanQueue.tryDequeue()
	assert.Equal(t, second, got, "oldest span should be discarded")
	got, _ = agent.spanQueue.tryDequeue()
	assert.Equal(t, third, got, "newest span should be enqueued")
}

func Test_agent_enqueueSpan_saturatedConcurrentProducers(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	const queueCap = 256 // 8 shards of 32
	agent.spanQueue = newSpanQueue(queueCap)
	chunk := newTestSpanChunk(agent)

	const producers = 16
	const perProducer = 500

	var consumed atomic.Int64
	var consumerWg sync.WaitGroup
	consumerWg.Add(1)
	go func() {
		defer consumerWg.Done()
		for {
			if _, ok := agent.spanQueue.dequeue(); !ok {
				return
			}
			consumed.Add(1)
			time.Sleep(10 * time.Microsecond) // slow consumer keeps the queue saturated
		}
	}()

	var rejected atomic.Int64
	var producerWg sync.WaitGroup
	for i := 0; i < producers; i++ {
		producerWg.Add(1)
		go func() {
			defer producerWg.Done()
			for j := 0; j < perProducer; j++ {
				if !agent.enqueueSpan(chunk) {
					rejected.Add(1)
				}
				if n := agent.spanQueue.length(); n > queueCap {
					t.Errorf("queue length %d exceeds capacity %d", n, queueCap)
				}
			}
		}()
	}
	producerWg.Wait()
	agent.spanQueue.close() // the consumer drains the rest, then dequeue reports done
	consumerWg.Wait()

	dropped := agent.spanQueue.dropCount()
	assert.Zero(t, rejected.Load(), "a saturated queue must never lose the new span")
	assert.Positive(t, dropped, "test must actually saturate the queue")
	assert.Zero(t, agent.spanQueue.length(), "consumer must drain the closed queue")
	assert.Equal(t, int64(producers*perProducer), consumed.Load()+dropped,
		"produced == consumed + dropped")
}

// Multi-producer enqueue with a draining consumer: the producer-contention
// path a loaded service exercises on every request.
func Benchmark_agent_enqueueSpan_parallel(b *testing.B) {
	agent := newTestAgent(defaultConfig())
	stop := startDrain(agent)
	defer stop()
	chunk := newTestSpanChunk(agent)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			agent.enqueueSpan(chunk)
		}
	})
}

// Every enqueue exercises the queue-full drop-oldest path: the queue is
// pre-filled and there is no consumer.
func Benchmark_agent_enqueueSpan_saturated(b *testing.B) {
	agent := newTestAgent(defaultConfig())
	agent.spanQueue = newSpanQueue(cacheSize)
	chunk := newTestSpanChunk(agent)
	for i := range agent.spanQueue.shards {
		shard := &agent.spanQueue.shards[i]
		for shard.tryEnqueue(chunk, &agent.spanQueue.closed) {
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			agent.enqueueSpan(chunk)
		}
	})
}

func Test_spanGrpc_collectSpanBatch_stopsAtBatchSize(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	spanGrpc := newMockSpanGrpc(agent)
	spanGrpc.batchSize = 2
	spanGrpc.batchCollectDeadline = time.Second

	first := newTestSpanChunk(agent)
	second := newTestSpanChunk(agent)
	third := newTestSpanChunk(agent)
	queue := newSpanQueue(2)
	queue.enqueue(second)
	queue.enqueue(third)

	batch, closed := spanGrpc.collectSpanBatch(first, queue)

	assert.False(t, closed)
	assert.Equal(t, []*spanChunk{first, second}, batch)
	assert.Equal(t, 1, queue.length(), "third chunk should wait for the next batch")
}

func Test_spanGrpc_collectSpanBatch_flushesClosedQueue(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	spanGrpc := newMockSpanGrpc(agent)
	spanGrpc.batchSize = 50
	spanGrpc.batchCollectDeadline = time.Second

	first := newTestSpanChunk(agent)
	second := newTestSpanChunk(agent)
	queue := newSpanQueue(1)
	queue.enqueue(second)
	queue.close()

	batch, closed := spanGrpc.collectSpanBatch(first, queue)

	assert.True(t, closed)
	assert.Equal(t, []*spanChunk{first, second}, batch)
}

func Test_statStream_sendStat(t *testing.T) {
	type args struct {
		agent *agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(defaultConfig())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.statGrpc = newMockStatGrpc(agent)
			stream := agent.statGrpc.newStatStreamWithRetry()

			stats := make([]*inspectorStats, 1)
			stats[0] = agent.stats.getStats()
			msg := makePAgentStatBatch(stats)
			err := stream.sendStats(msg)
			assert.NoError(t, err, "sendStats")
		})
	}
}

func newTestSpanChunk(agent *agent) *spanChunk {
	return defaultSpan(agent).newEventChunk(true)
}

func Test_statStream_sendStatRetry(t *testing.T) {
	type args struct {
		agent *agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(defaultConfig())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.statGrpc = newRetryMockStatGrpc(agent)
			stream := agent.statGrpc.newStatStreamWithRetry()

			stats := make([]*inspectorStats, 1)
			stats[0] = agent.stats.getStats()
			msg := makePAgentStatBatch(stats)
			err := stream.sendStats(msg)
			assert.NoError(t, err, "sendStats")
		})
	}
}

func Test_backOffUntilReady_abortsOnShutdown(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	// Port 1 has no listener, so this connection never becomes ready and the
	// back-off loop keeps waiting until it is told to stop.
	conn, err := grpc.NewClient("127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
	assert.NoError(t, err)
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		backOffUntilReady(agent, conn, "test")
	}()

	// Let the goroutine reach the blocking wait. The first back-off interval is
	// at least 2.1s, so returning within 1s can only be the stop signal.
	time.Sleep(100 * time.Millisecond)
	agent.signalShutdown()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("backOffUntilReady did not return within 1s of shutdown")
	}
}

func Test_backOffSleep_rampsGentlyToCeiling(t *testing.T) {
	// 3s x 1.2^attempt, clamped at 30s, then randomized by +/-30%.
	wantMillis := []float64{
		3000, 3600, 4320, 5184, 6220.8, 7464.96, 8957.952, 10749.5424,
		12899.45088, 15479.341056, 18575.2092672, 22290.25112064,
		26748.301344768, 30000,
	}
	for attempt, want := range wantMillis {
		got := backOffSleep(attempt)
		lo := time.Duration(want * (1 - backOffJitter) * float64(time.Millisecond))
		hi := time.Duration(math.Ceil(want*(1+backOffJitter)) * float64(time.Millisecond))
		assert.GreaterOrEqual(t, got, lo, "attempt %d", attempt)
		assert.LessOrEqual(t, got, hi, "attempt %d", attempt)
	}

	// The ceiling holds once reached: 13 attempts of gentle ramp, unlike the
	// base-2 ramp this replaced, which pinned itself in 4.
	for attempt := len(wantMillis) - 1; attempt < 100; attempt++ {
		got := backOffSleep(attempt)
		assert.GreaterOrEqual(t, got, time.Duration(float64(backOffMaxInterval)*(1-backOffJitter)), "attempt %d", attempt)
		assert.LessOrEqual(t, got, time.Duration(float64(backOffMaxInterval)*(1+backOffJitter)), "attempt %d", attempt)
	}
}

func Test_waitUntilReady_connectsIdleChannel(t *testing.T) {
	// A NewClient channel starts IDLE, as every collector connection now does,
	// and stays there until told to connect: the state this path exists for.
	conn, err := grpc.NewClient("127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
	assert.NoError(t, err)
	defer conn.Close()

	assert.Equal(t, connectivity.Idle, conn.GetState())
	assert.False(t, waitUntilReady(context.Background(), conn, 200*time.Millisecond, "test"))
	// An IDLE channel used to sit there for the whole interval; it must now
	// have been asked to connect.
	assert.NotEqual(t, connectivity.Idle, conn.GetState())
}

func Test_sendStreamWithTimeout_passesThroughResult(t *testing.T) {
	assert.NoError(t, sendStreamWithTimeout(func() error { return nil }, func() {}, time.Second, "test"))

	sendErr := status.Errorf(codes.Internal, "boom")
	assert.Equal(t, sendErr, sendStreamWithTimeout(func() error { return sendErr }, func() {}, time.Second, "test"))
}

func Test_sendStreamWithTimeout_waitsForStartedCallback(t *testing.T) {
	// Force Stop to lose to the timer while cancelStream is still running.
	// The helper must join that callback instead of letting cancellation escape.
	cancelStarted := make(chan struct{})
	finishCancel := make(chan struct{})
	cancelFinished := make(chan struct{})
	result := make(chan error, 1)
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(finishCancel) }) }
	defer finish()

	go func() {
		result <- sendStreamWithTimeout(
			func() error {
				<-cancelStarted
				return nil
			},
			func() {
				close(cancelStarted)
				<-finishCancel
				close(cancelFinished)
			},
			0, "test stream.Send()",
		)
	}()

	<-cancelStarted
	select {
	case err := <-result:
		t.Fatalf("returned %v while the cancellation callback was still running", err)
	case <-time.After(50 * time.Millisecond):
	}

	finish()
	select {
	case err := <-result:
		assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
		select {
		case <-cancelFinished:
		default:
			t.Fatal("returned before the cancellation callback completed")
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after the cancellation callback completed")
	}
}

// An unresponsive stream: Send blocks until the stream context is cancelled,
// which is how a grpc-go Send stuck on flow control behaves. The wrapper must
// unblock it via cancel and, running the send on the calling goroutine, leave
// no goroutines behind no matter how many sends time out.
func Test_sendStreamWithTimeout_unresponsiveStreamLeaksNothing(t *testing.T) {
	before := runtime.NumGoroutine()

	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		err := sendStreamWithTimeout(
			func() error {
				<-ctx.Done()
				return ctx.Err()
			},
			cancel, time.Millisecond, "test stream.Send()",
		)
		assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
	}

	// Fired AfterFunc callbacks are joined before each call returns; give any
	// unrelated runtime cleanup a moment to settle before comparing.
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), before+2)
}

// failingMetaClient fails every metadata request with a fixed error, counting
// the attempts.
type failingMetaClient struct {
	calls int32
	err   error
	mu    sync.Mutex
	at    []time.Time
	// failFirst > 0 fails only the leading n calls and then succeeds; the zero
	// value fails every call.
	failFirst int32
}

func (c *failingMetaClient) fail() error {
	c.mu.Lock()
	c.at = append(c.at, time.Now())
	c.mu.Unlock()
	n := atomic.AddInt32(&c.calls, 1)
	if c.failFirst > 0 && n > c.failFirst {
		return nil
	}
	return c.err
}

// result mirrors a real client: an accepted request answers with
// PResult.Success=true, which the send path now requires.
func (c *failingMetaClient) result() (*pb.PResult, error) {
	if err := c.fail(); err != nil {
		return nil, err
	}
	return &pb.PResult{Success: true}, nil
}

func (c *failingMetaClient) callCount() int32 {
	return atomic.LoadInt32(&c.calls)
}

func (c *failingMetaClient) callTimes() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.at...)
}

func (c *failingMetaClient) RequestApiMetaData(context.Context, *pb.PApiMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.result()
}

func (c *failingMetaClient) RequestSqlMetaData(context.Context, *pb.PSqlMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.result()
}

func (c *failingMetaClient) RequestSqlUidMetaData(context.Context, *pb.PSqlUidMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.result()
}

func (c *failingMetaClient) RequestStringMetaData(context.Context, *pb.PStringMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.result()
}

func (c *failingMetaClient) RequestExceptionMetaData(context.Context, *pb.PExceptionMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.result()
}

func newFailingMetaAgentGrpc(agent *agent, err error) (*agentGrpc, *failingMetaClient) {
	failing := &failingMetaClient{err: err}
	return &agentGrpc{metaClient: failing, agent: agent}, failing
}

func Test_retryMeta_stopsAtRetryBound(t *testing.T) {
	cfg, _ := NewConfig(WithAppName("TestApp"))
	agent := newTestAgent(cfg)
	agentGrpc, failing := newFailingMetaAgentGrpc(agent, status.Errorf(codes.Unavailable, "collector down"))

	ok := agentGrpc.sendApiMetadataWithRetry(1, "test.api", -1, apiTypeInvocation)

	assert.False(t, ok, "retryable errors must stop at the bound")
	assert.Equal(t, int32(metaRetryMaxAttempts), failing.callCount())
}

// A collector that is up but refusing (Unavailable) leaves the channel Ready,
// so the readiness wait returns at once; the fixed pause must still space the
// attempts out instead of firing the whole budget back to back.
func Test_retryMeta_pausesBetweenAttemptsOnReadyChannel(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.config.offGrpc = false
	conn := dialReadyConn(t)
	agentGrpc, failing := newFailingMetaAgentGrpc(agent, status.Errorf(codes.Unavailable, "collector overloaded"))
	agentGrpc.agentConn = conn
	agentGrpc.retryDelay = 100 * time.Millisecond

	assert.False(t, agentGrpc.sendApiMetadataWithRetry(1, "test.api", -1, apiTypeInvocation))

	at := failing.callTimes()
	require.Len(t, at, metaRetryMaxAttempts)
	for i := 1; i < len(at); i++ {
		assert.GreaterOrEqual(t, at[i].Sub(at[i-1]), agentGrpc.retryDelay, "attempt %d fired without the pause", i+1)
	}
	assert.Equal(t, connectivity.Ready, conn.GetState(), "the pause, not a reconnect, spaced the attempts")
}

// Shutdown must not sit out a pending retry pause.
func Test_retryMeta_pauseReturnsOnShutdown(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agentGrpc, failing := newFailingMetaAgentGrpc(agent, status.Errorf(codes.Unavailable, "collector overloaded"))
	agentGrpc.retryDelay = time.Hour

	done := make(chan bool, 1)
	go func() { done <- agentGrpc.sendApiMetadataWithRetry(1, "test.api", -1, apiTypeInvocation) }()
	assert.Eventually(t, func() bool { return failing.callCount() == 1 }, time.Second, time.Millisecond)

	agent.signalShutdown()

	select {
	case ok := <-done:
		assert.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("retryMeta kept pausing after shutdown")
	}
	assert.Equal(t, int32(1), failing.callCount(), "no further attempt after shutdown")
}

func Test_retryMeta_noRetryOnNonRetryableError(t *testing.T) {
	cfg, _ := NewConfig(WithAppName("TestApp"))
	agent := newTestAgent(cfg)
	agentGrpc, failing := newFailingMetaAgentGrpc(agent, status.Errorf(codes.Internal, "bad request"))

	ok := agentGrpc.sendStringMetadataWithRetry(1, "test.error")

	assert.False(t, ok)
	assert.Equal(t, int32(1), failing.callCount(), "non-retryable errors must not retry")
}

// A collector that keeps failing must not wedge the metadata worker: each item
// gives up at the retry bound, the worker moves on to the next item, and the
// failed items' cache entries are released so their next use re-registers them.
func Test_sendMetaWorker_movesOnAndReleasesCache(t *testing.T) {
	cfg, _ := NewConfig(WithAppName("TestApp"))
	agent := newTestAgent(cfg)
	agentGrpc, failing := newFailingMetaAgentGrpc(agent, status.Errorf(codes.Unavailable, "collector down"))
	agent.agentGrpc = agentGrpc

	apiKey := apiCacheKey{"test.api", apiTypeInvocation}
	apiCached := func() bool { _, ok := agent.apiCache.peek(apiKey); return ok }
	errCached := func() bool { _, ok := agent.errorCache.peek("test.error"); return ok }
	assert.NotZero(t, agent.cacheSpanApi(apiKey.descriptor, apiKey.apiType))
	assert.NotZero(t, agent.cacheError("test.error"))
	assert.True(t, apiCached())
	assert.True(t, errCached())

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	// both queued items exhaust their retry budget: the worker was not wedged
	// by the first one
	assert.Eventually(t, func() bool {
		return failing.callCount() == int32(2*metaRetryMaxAttempts)
	}, 5*time.Second, 5*time.Millisecond, "worker must drain both items, got %d calls", failing.callCount())

	agent.signalShutdown()
	agent.workerWg.Wait()

	// the failed items released their cache entries...
	assert.False(t, apiCached())
	assert.False(t, errCached())

	// ...so the next use re-registers and re-enqueues the metadata
	assert.NotZero(t, agent.cacheSpanApi(apiKey.descriptor, apiKey.apiType))
	assert.True(t, apiCached())
	assert.Equal(t, 1, len(agent.metaChan))
}

// blockingMetaClient parks every metadata request until release is closed,
// tracking how many requests are in flight at once.
type blockingMetaClient struct {
	mu      sync.Mutex
	current int
	max     int
	total   int
	release chan struct{}
}

func (c *blockingMetaClient) block() (*pb.PResult, error) {
	c.mu.Lock()
	c.current++
	c.total++
	if c.current > c.max {
		c.max = c.current
	}
	c.mu.Unlock()

	<-c.release

	c.mu.Lock()
	c.current--
	c.mu.Unlock()
	return &pb.PResult{Success: true}, nil
}

func (c *blockingMetaClient) inFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *blockingMetaClient) stats() (max, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.max, c.total
}

func (c *blockingMetaClient) RequestApiMetaData(context.Context, *pb.PApiMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.block()
}

func (c *blockingMetaClient) RequestSqlMetaData(context.Context, *pb.PSqlMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.block()
}

func (c *blockingMetaClient) RequestSqlUidMetaData(context.Context, *pb.PSqlUidMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.block()
}

func (c *blockingMetaClient) RequestStringMetaData(context.Context, *pb.PStringMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.block()
}

func (c *blockingMetaClient) RequestExceptionMetaData(context.Context, *pb.PExceptionMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.block()
}

// While earlier sends are still waiting on the collector, the worker must keep
// pulling items and pipeline up to metaMaxConcurrentRequests sends -- and no
// more.
func Test_sendMetaWorker_pipelinesUpToConcurrencyLimit(t *testing.T) {
	cfg, _ := NewConfig(WithAppName("TestApp"))
	agent := newTestAgent(cfg)
	blocking := &blockingMetaClient{release: make(chan struct{})}
	agent.agentGrpc = &agentGrpc{metaClient: blocking, agent: agent}

	const items = 2 * metaMaxConcurrentRequests
	for i := 0; i < items; i++ {
		agent.metaChan <- apiMeta{id: int32(i), descriptor: "test.api", apiType: apiTypeInvocation}
	}

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	assert.Eventually(t, func() bool {
		return blocking.inFlight() == metaMaxConcurrentRequests
	}, 5*time.Second, time.Millisecond, "sends must pipeline while the collector is slow")

	close(blocking.release)
	assert.Eventually(t, func() bool {
		_, total := blocking.stats()
		return total == items
	}, 5*time.Second, time.Millisecond, "every queued item must be sent")

	agent.signalShutdown()
	agent.workerWg.Wait()

	max, _ := blocking.stats()
	assert.Equal(t, metaMaxConcurrentRequests, max, "in-flight sends must not exceed the limit")
}

// countingAgentClient counts RequestAgentInfo calls and fails them on demand.
type countingAgentClient struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (c *countingAgentClient) RequestAgentInfo(ctx context.Context, agentInfo *pb.PAgentInfo, _ ...grpc.CallOption) (*pb.PResult, error) {
	c.calls.Add(1)
	if c.fail.Load() {
		return nil, status.Errorf(codes.Unavailable, "collector down")
	}
	return &pb.PResult{Success: true}, nil
}

func (c *countingAgentClient) PingSession(ctx context.Context, _ ...grpc.CallOption) (pb.Agent_PingSessionClient, error) {
	return nil, status.Errorf(codes.Unimplemented, "not used")
}

func Test_config_agentInfoRefreshDisabledByDefault(t *testing.T) {
	cfg, _ := NewConfig(WithAppName("TestApp"))
	assert.Equal(t, 0, cfg.Int(CfgCollectorAgentInfoRefreshInterval))
	assert.Equal(t, defaultAgentInfoSendRetryInterval, cfg.Int(CfgCollectorAgentInfoSendRetryInterval))
	assert.Equal(t, defaultAgentInfoMaxTryPerAttempt, cfg.Int(CfgCollectorAgentInfoMaxTryPerAttempt))
}

func Test_agentGrpc_refreshAgentInfo_stopsAtMaxTry(t *testing.T) {
	cfg, _ := NewConfig(WithAppName("TestApp"))
	agent := newTestAgent(cfg)
	client := &countingAgentClient{}
	client.fail.Store(true)
	agentGrpc := &agentGrpc{agentClient: client, agent: agent}

	ok := agentGrpc.refreshAgentInfo(3, time.Millisecond)

	assert.False(t, ok, "refresh must give up after maxTry sends")
	assert.EqualValues(t, 3, client.calls.Load())
}

func Test_agentGrpc_refreshAgentInfo_stopsOnSuccess(t *testing.T) {
	cfg, _ := NewConfig(WithAppName("TestApp"))
	agent := newTestAgent(cfg)
	client := &countingAgentClient{}
	agentGrpc := &agentGrpc{agentClient: client, agent: agent}

	ok := agentGrpc.refreshAgentInfo(3, time.Millisecond)

	assert.True(t, ok)
	assert.EqualValues(t, 1, client.calls.Load())
}

func Test_agent_refreshAgentInfoWorker_honorsInterval(t *testing.T) {
	cfg, _ := NewConfig(WithAppName("TestApp"),
		WithCollectorAgentInfoRefreshInterval(20),
		WithCollectorAgentInfoSendRetryInterval(10),
		WithCollectorAgentInfoMaxTryPerAttempt(1),
	)
	agent := newTestAgent(cfg)
	client := &countingAgentClient{}
	agent.agentGrpc = &agentGrpc{agentClient: client, agent: agent}

	agent.workerWg.Add(1)
	go agent.superviseWorker("agent info refresh", func() { agent.refreshAgentInfoWorker(20 * time.Millisecond) })

	deadline := time.Now().Add(3 * time.Second)
	for client.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	agent.signalShutdown()
	agent.workerWg.Wait()

	assert.GreaterOrEqual(t, client.calls.Load(), int32(2), "worker must re-send agent info every interval")
}

// With no Collector.Grpc.* keys set, the channel options must equal the
// values that were hard-coded before they became configurable, except for
// PermitWithoutStream, which was deliberately flipped to false to match the
// C++ agent.
func Test_grpcChannelOptions_defaults(t *testing.T) {
	cfg, err := NewConfig(WithAppName("TestApp"))
	assert.NoError(t, err)

	o := newGrpcChannelOptions(cfg)
	assert.Equal(t, 30*time.Second, o.keepAlive.Time, "keepalive time")
	assert.Equal(t, 60*time.Second, o.keepAlive.Timeout, "keepalive timeout")
	assert.Equal(t, false, o.keepAlive.PermitWithoutStream, "permit without calls")
	assert.Equal(t, int32(1*1024*1024), o.flowControlWindow, "flow control window")
	assert.Equal(t, 1*1024*1024, o.writeBufferSize, "write buffer size")
	assert.Equal(t, 4*1024*1024, o.maxSendMsgSize, "max send message size")
	assert.Equal(t, 4*1024*1024, o.maxRecvMsgSize, "max receive message size")
	assert.Equal(t, uint32(8*1024), o.maxHeaderListSize, "max header list size")
	assert.Len(t, o.dialOptions(insecure.NewCredentials()), 7)
}

func Test_grpcChannelOptions_configured(t *testing.T) {
	cfg, err := NewConfig(
		WithAppName("TestApp"),
		WithCollectorGrpcKeepAliveTime(10000),
		WithCollectorGrpcKeepAliveTimeout(20000),
		WithCollectorGrpcKeepAlivePermitWithoutCalls(true),
		WithCollectorGrpcMaxSendMessageSize(8*1024*1024),
		WithCollectorGrpcMaxReceiveMessageSize(16*1024*1024),
		WithCollectorGrpcFlowControlWindow(2*1024*1024),
		WithCollectorGrpcWriteBufferSize(512*1024),
		WithCollectorGrpcMaxHeaderListSize(16*1024),
	)
	assert.NoError(t, err)

	o := newGrpcChannelOptions(cfg)
	assert.Equal(t, 10*time.Second, o.keepAlive.Time, "keepalive time")
	assert.Equal(t, 20*time.Second, o.keepAlive.Timeout, "keepalive timeout")
	assert.Equal(t, true, o.keepAlive.PermitWithoutStream, "permit without calls")
	assert.Equal(t, int32(2*1024*1024), o.flowControlWindow, "flow control window")
	assert.Equal(t, 512*1024, o.writeBufferSize, "write buffer size")
	assert.Equal(t, 8*1024*1024, o.maxSendMsgSize, "max send message size")
	assert.Equal(t, 16*1024*1024, o.maxRecvMsgSize, "max receive message size")
	assert.Equal(t, uint32(16*1024), o.maxHeaderListSize, "max header list size")
}

func Test_makePException_EmptyCallstack(t *testing.T) {
	e := &exception{
		callstack: &errorWithCallStack{
			err:       status.Error(codes.Unknown, "boom"),
			errorTime: time.Now(),
		},
		exceptionId: 1,
	}

	var p *pb.PException
	assert.NotPanics(t, func() { p = makePException(e) })
	assert.Equal(t, "unknown", p.ExceptionClassName)
	assert.Empty(t, p.StackTraceElement)
}

// --- metadata ---------------------------------------------------------------

// newMockMetaAgentGrpc wires an agent to a metadata client that accepts every
// request and keeps it, so a test can assert on the payload put on the wire.
func newMockMetaAgentGrpc(agent *agent) (*agentGrpc, *mockMetaGrpcClient) {
	meta := &mockMetaGrpcClient{}
	return &agentGrpc{metaClient: meta, agent: agent}, meta
}

// Each metadata type must reach the collector with the fields the caller
// supplied. Mirrors the C++ agent's MetaDataApiTest / MetaDataStringTest /
// MetaDataSqlUidTest.
func Test_agentGrpc_sendMetadata_payloads(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agentGrpc, meta := newMockMetaAgentGrpc(agent)

	assert.True(t, agentGrpc.sendApiMetadataWithRetry(7, "test.api", 42, apiTypeInvocation))
	assert.True(t, agentGrpc.sendStringMetadataWithRetry(8, "test.error"))
	assert.True(t, agentGrpc.sendSqlMetadataWithRetry(9, "SELECT 1"))
	assert.True(t, agentGrpc.sendSqlUidMetadataWithRetry([]byte{0xde, 0xad}, "SELECT 2"))

	api, str, sql, sqlUid, _ := meta.sentMeta()

	require.Len(t, api, 1)
	assert.Equal(t, int32(7), api[0].GetApiId())
	assert.Equal(t, "test.api", api[0].GetApiInfo())
	assert.Equal(t, int32(42), api[0].GetLine())
	assert.EqualValues(t, apiTypeInvocation, api[0].GetType())

	require.Len(t, str, 1)
	assert.Equal(t, int32(8), str[0].GetStringId())
	assert.Equal(t, "test.error", str[0].GetStringValue())

	require.Len(t, sql, 1)
	assert.Equal(t, int32(9), sql[0].GetSqlId())
	assert.Equal(t, "SELECT 1", sql[0].GetSql())

	require.Len(t, sqlUid, 1)
	assert.Equal(t, []byte{0xde, 0xad}, sqlUid[0].GetSqlUid())
	assert.Equal(t, "SELECT 2", sqlUid[0].GetSql())
}

// Exception metadata carries the transaction that raised it plus one entry per
// chained error. Mirrors the C++ agent's MetaDataExceptionTest.
func Test_agentGrpc_sendExceptionMetadata(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agentGrpc, meta := newMockMetaAgentGrpc(agent)

	pcs := make([]uintptr, 8)
	pcs = pcs[:runtime.Callers(1, pcs)]
	errorTime := time.Unix(0, 1234*int64(time.Millisecond))

	assert.True(t, agentGrpc.sendExceptionMetadataWithRetry(&exceptionMeta{
		txId:        TransactionId{AgentId: "testAgent", StartTime: 11, Sequence: 22},
		spanId:      33,
		uriTemplate: "/test/uri",
		exceptions: []*exception{{
			exceptionId: 44,
			callstack:   &errorWithCallStack{err: errors.New("boom"), errorTime: errorTime, callstack: pcs},
		}},
	}))

	_, _, _, _, except := meta.sentMeta()
	require.Len(t, except, 1)
	assert.Equal(t, "testAgent", except[0].GetTransactionId().GetAgentId())
	assert.Equal(t, int64(11), except[0].GetTransactionId().GetAgentStartTime())
	assert.Equal(t, int64(22), except[0].GetTransactionId().GetSequence())
	assert.Equal(t, int64(33), except[0].GetSpanId())
	assert.Equal(t, "/test/uri", except[0].GetUriTemplate())

	require.Len(t, except[0].GetExceptions(), 1)
	e := except[0].GetExceptions()[0]
	assert.Equal(t, int64(44), e.GetExceptionId())
	assert.Equal(t, "boom", e.GetExceptionMessage())
	assert.Equal(t, int32(1), e.GetExceptionDepth())
	assert.Equal(t, int64(1234), e.GetStartTime())
	require.NotEmpty(t, e.GetStackTraceElement())
	assert.Equal(t, e.GetStackTraceElement()[0].GetClassName(), e.GetExceptionClassName(),
		"the class name is the innermost frame's module")
}

// The size guard follows the configured Collector.Grpc.MaxSendMessageSize,
// not the write buffer size: a message under the limit is sent, one over it is
// dropped before encoding, and lowering the limit lowers the guard.
func Test_agentGrpc_sendExceptionMetadata_sizeGuardFollowsConfiguredLimit(t *testing.T) {
	// A message that fits the 4MB send limit but exceeds the 1MB write buffer
	// must be sent; the two limits are unrelated.
	agent := newTestAgent(defaultConfig())
	agentGrpc, meta := newMockMetaAgentGrpc(agent)
	assert.True(t, agentGrpc.sendExceptionMetadataWithRetry(&exceptionMeta{uriTemplate: strings.Repeat("x", 2*grpcWriteBufferSize)}))
	_, _, _, _, except := meta.sentMeta()
	assert.Len(t, except, 1, "a message under MaxSendMessageSize is sent")

	// Lowering the limit in config lowers the guard.
	cfg, err := NewConfig(WithAppName("TestApp"), WithCollectorGrpcMaxSendMessageSize(64*1024))
	require.NoError(t, err)
	agent = newTestAgent(cfg)
	agentGrpc, meta = newMockMetaAgentGrpc(agent)

	assert.True(t, agentGrpc.sendExceptionMetadataWithRetry(&exceptionMeta{uriTemplate: strings.Repeat("x", 32*1024)}))
	assert.False(t, agentGrpc.sendExceptionMetadataWithRetry(&exceptionMeta{uriTemplate: strings.Repeat("x", 64*1024)}))
	_, _, _, _, except = meta.sentMeta()
	assert.Len(t, except, 1, "only the message under the configured limit reaches the collector")
}

// An oversized exception is reported as ResourceExhausted, which retryMeta does
// not retry, so a message that can never fit is dropped after a single attempt.
func Test_agentGrpc_sendExceptionMetadata_oversizedIsSkippedWithoutRetry(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agentGrpc, meta := newMockMetaAgentGrpc(agent)
	oversized := &exceptionMeta{uriTemplate: strings.Repeat("x", grpcMaxMessageSize)}

	assert.False(t, agentGrpc.sendExceptionMetadataWithRetry(oversized))
	_, _, _, _, except := meta.sentMeta()
	assert.Empty(t, except, "an oversized message must not reach the collector")

	err := agentGrpc.sendExceptionMetadata(makePExceptionMetaData(oversized))
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.False(t, isRetryableError(err), "an oversized message never becomes sendable by retrying")
}

// A retryable failure is retried within the attempt budget, and a send that
// then succeeds keeps its cache entry. Mirrors the C++ agent's
// GrpcMetadataRetriesFailedResultWithoutEvictingCache.
func Test_retryMeta_succeedsAfterRetryableFailure(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	failing := &failingMetaClient{
		err:       status.Errorf(codes.Unavailable, "collector down"),
		failFirst: metaRetryMaxAttempts - 1,
	}
	agentGrpc := &agentGrpc{metaClient: failing, agent: agent}

	assert.True(t, agentGrpc.sendApiMetadataWithRetry(1, "test.api", -1, apiTypeInvocation))
	assert.Equal(t, int32(metaRetryMaxAttempts), failing.callCount())
}

func Test_isRetryableError(t *testing.T) {
	// Only a transport-level failure can succeed on a later attempt; anything
	// the collector rejected on its merits would be rejected again.
	assert.True(t, isRetryableError(status.Errorf(codes.Unavailable, "collector down")))
	assert.True(t, isRetryableError(status.Errorf(codes.DeadlineExceeded, "too slow")))
	assert.False(t, isRetryableError(status.Errorf(codes.Internal, "bad request")))
	assert.False(t, isRetryableError(status.Errorf(codes.ResourceExhausted, "too big")))
	assert.False(t, isRetryableError(errors.New("not a status")))
	assert.False(t, isRetryableError(nil))
}

// The sql and sql-uid caches are released the same way the api and string
// caches are when their metadata never reaches the collector, so the next use
// re-registers them. Mirrors the C++ agent's
// GrpcMetadataEvictsSqlCacheAfterRetryExhaustion / ...SqlUidCache...
func Test_sendMetaWorker_releasesSqlCachesOnFailure(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agentGrpc, failing := newFailingMetaAgentGrpc(agent, status.Errorf(codes.Unavailable, "collector down"))
	agent.agentGrpc = agentGrpc

	const sql = "SELECT * FROM t WHERE id = 1"
	sqlCached := func() bool { _, ok := agent.sqlCache.peek(sql); return ok }
	sqlUidCached := func() bool { _, ok := agent.sqlUidCache.peek(sql); return ok }
	assert.NotZero(t, agent.cacheSql(sql))
	assert.NotEmpty(t, agent.cacheSqlUid(sql))
	assert.True(t, sqlCached())
	assert.True(t, sqlUidCached())

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	assert.Eventually(t, func() bool {
		return failing.callCount() == int32(2*metaRetryMaxAttempts)
	}, 5*time.Second, 5*time.Millisecond, "worker must drain both items, got %d calls", failing.callCount())

	agent.signalShutdown()
	agent.workerWg.Wait()

	assert.False(t, sqlCached(), "a failed sql metadata send must release its cache entry")
	assert.False(t, sqlUidCached(), "a failed sql uid metadata send must release its cache entry")
}

// Every metadata type the agent can queue is dispatched by the worker to its
// own RPC. Mirrors the C++ agent's GrpcAgentMetaWorkerAllTypesSuccessTest.
func Test_sendMetaWorker_sendsEveryMetadataType(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agentGrpc, meta := newMockMetaAgentGrpc(agent)
	agent.agentGrpc = agentGrpc

	agent.metaChan <- apiMeta{id: 1, descriptor: "test.api", apiType: apiTypeInvocation}
	agent.metaChan <- stringMeta{id: 2, funcName: "test.error"}
	agent.metaChan <- sqlMeta{id: 3, sql: "SELECT 1"}
	agent.metaChan <- sqlUidMeta{uid: []byte{1, 2}, sql: "SELECT 2"}
	agent.metaChan <- exceptionMeta{
		spanId:     4,
		exceptions: []*exception{{exceptionId: 5, callstack: &errorWithCallStack{err: errors.New("boom")}}},
	}

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	assert.Eventually(t, func() bool {
		api, str, sql, sqlUid, except := meta.sentMeta()
		return len(api) == 1 && len(str) == 1 && len(sql) == 1 && len(sqlUid) == 1 && len(except) == 1
	}, 5*time.Second, 5*time.Millisecond, "every queued metadata type must be sent")

	agent.signalShutdown()
	agent.workerWg.Wait()
}

// --- agent info -------------------------------------------------------------

// The registration payload describes the running process, and its headers ride
// along on the same context. Mirrors the C++ agent's
// GrpcAgentRegisterAgentUsesDefaultServerMetaData.
func Test_agentGrpc_makeAgentInfo(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agentGrpc := newMockAgentGrpc(agent)

	ctx, info := agentGrpc.makeAgentInfo()

	assert.NotEmpty(t, info.GetHostname())
	assert.EqualValues(t, os.Getpid(), info.GetPid())
	assert.Equal(t, Version, info.GetAgentVersion())
	assert.Equal(t, runtime.Version(), info.GetVmVersion())
	assert.Equal(t, agent.appType, info.GetServiceType())
	assert.Equal(t, agent.config.Bool(CfgIsContainerEnv), info.GetContainer())

	meta := info.GetServerMetaData()
	assert.Equal(t, "Go Application", meta.GetServerInfo())
	require.Len(t, meta.GetServiceInfo(), 1)
	assert.Contains(t, meta.GetServiceInfo()[0].GetServiceName(), runtime.GOOS)
	assert.Contains(t, meta.GetServiceInfo()[0].GetServiceName(), runtime.GOARCH)

	assert.Equal(t, pb.PJvmGcType_JVM_GC_TYPE_CMS, info.GetJvmInfo().GetGcType())
	assert.Contains(t, info.GetJvmInfo().GetVmVersion(), runtime.Version())

	md, ok := metadata.FromOutgoingContext(ctx)
	require.True(t, ok, "agent info must carry the agent headers")
	assert.Equal(t, []string{agent.appName}, md.Get(headerAppName))
	assert.Equal(t, []string{agent.agentID}, md.Get(headerAgentID))
}

// --- local IP ---------------------------------------------------------------

// Everything below runs without network access: it only inspects local
// interfaces and the loopback route.

func Test_firstUnicastIP(t *testing.T) {
	ipNet := func(s string) net.Addr { return &net.IPNet{IP: net.ParseIP(s)} }

	assert.Equal(t, "", firstUnicastIP(nil))
	assert.Equal(t, "", firstUnicastIP([]net.Addr{ipNet("127.0.0.1"), ipNet("fe80::1"), ipNet("0.0.0.0")}),
		"loopback, link-local and unspecified addresses never identify a host")
	assert.Equal(t, "10.0.0.5", firstUnicastIP([]net.Addr{ipNet("fe80::1"), ipNet("2001:db8::1"), ipNet("10.0.0.5")}),
		"IPv4 wins over a routable IPv6 address listed before it")
	assert.Equal(t, "2001:db8::1", firstUnicastIP([]net.Addr{ipNet("fe80::1"), ipNet("2001:db8::1")}),
		"a routable IPv6 address is used when there is no IPv4")
	assert.Equal(t, "", firstUnicastIP([]net.Addr{&net.TCPAddr{IP: net.ParseIP("10.0.0.5")}}),
		"only interface (IPNet) addresses are considered")
}

func Test_firstInterfaceIP(t *testing.T) {
	// A host may legitimately have only a loopback interface, so the value is
	// only checked when there is one.
	if ip := firstInterfaceIP(); ip != "" {
		parsed := net.ParseIP(ip)
		require.NotNil(t, parsed, "must be a bare IP, not a CIDR")
		assert.False(t, parsed.IsLoopback())
		assert.False(t, parsed.IsLinkLocalUnicast())
	}
}

func Test_routeSourceIP(t *testing.T) {
	assert.Equal(t, "", routeSourceIP("127.0.0.1:9991"), "a loopback collector must not hide the host address")
	assert.Equal(t, "", routeSourceIP("not an address"))
}

func Test_localIP_neverLoopback(t *testing.T) {
	for _, collector := range []string{"localhost:9991", "127.0.0.1:9991", "[::1]:9991"} {
		if ip := localIP(collector); ip != "" {
			assert.False(t, net.ParseIP(ip).IsLoopback(), "collector %s", collector)
		}
	}
}

// A collector that answers Success=false has rejected this agent outright, so
// registration reports failure instead of retrying forever. Mirrors the C++
// agent's GrpcAgentRegisterAgentFailureTest.
func Test_agentGrpc_registerAgentWithRetry_stopsOnRejection(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	client := &mockAgentGrpcClient{reject: true}
	agentGrpc := &agentGrpc{agentClient: client, agent: agent}

	assert.False(t, agentGrpc.registerAgentWithRetry())
	assert.Len(t, client.sentAgentInfo(), 1, "a rejected registration must not be retried")
}

// dialReadyConn returns a channel to an empty in-process gRPC server that is
// already READY, so the reconnect waits inside the retry loops return at once.
func dialReadyConn(t *testing.T) *grpc.ClientConn {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	require.True(t, waitUntilReady(context.Background(), conn, 5*time.Second, "test"))
	return conn
}

// A transport failure is retried until the collector answers. Mirrors the C++
// agent's GrpcAgentRegisterWithRetryRetriesUntilSuccess.
func Test_agentGrpc_registerAgentWithRetry_retriesUntilSuccess(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	client := &mockAgentGrpcClient{failures: 2}
	agentGrpc := &agentGrpc{
		agentConn:          dialReadyConn(t),
		agentClient:        client,
		agent:              agent,
		registerRetryDelay: 100 * time.Millisecond,
	}

	assert.True(t, agentGrpc.registerAgentWithRetry())
	assert.Len(t, client.sentAgentInfo(), 3, "registration retries until the collector accepts it")
	callAt := client.callTimes()
	for i := 1; i < len(callAt); i++ {
		assert.GreaterOrEqual(t, callAt[i].Sub(callAt[i-1]), agentGrpc.registerRetryDelay,
			"attempt %d fired without the pause", i+1)
	}
	assert.Equal(t, connectivity.Ready, agentGrpc.agentConn.GetState(),
		"the pause, not a reconnect, spaced the attempts")
}

// --- headers ----------------------------------------------------------------

// Only the ping stream identifies a socket, and it must not pollute the header
// set every other RPC shares. Mirrors the C++ agent's
// GrpcMetadataTest.SocketIdNeverInBaseHeaderSet.
func Test_grpcMetadataContext_socketId(t *testing.T) {
	agent := newTestAgent(defaultConfig())

	base := grpcMetadataContext(agent, -1)
	md, ok := metadata.FromOutgoingContext(base)
	require.True(t, ok)
	assert.Empty(t, md.Get(headerSocketID), "the shared header set carries no socket id")
	assert.Equal(t, []string{agent.appName}, md.Get(headerAppName))

	// Non-positive socket ids reuse the one context instead of rebuilding it.
	assert.Equal(t, base, grpcMetadataContext(agent, 0))

	pingMd, ok := metadata.FromOutgoingContext(grpcMetadataContext(agent, 7))
	require.True(t, ok)
	assert.Equal(t, []string{"7"}, pingMd.Get(headerSocketID))
	assert.Equal(t, []string{agent.appName}, pingMd.Get(headerAppName))

	md, _ = metadata.FromOutgoingContext(grpcMetadataContext(agent, -1))
	assert.Empty(t, md.Get(headerSocketID), "a ping's socket id must not leak into the shared set")
}

// --- span batch -------------------------------------------------------------

// newBoundedSpanGrpc returns a batch sender with a single permit and a short
// flush timeout, so permit contention resolves within a test's patience.
func newBoundedSpanGrpc(agent *agent, client *mockSpanGrpcClient) *spanGrpc {
	return &spanGrpc{
		spanClient:              client,
		agent:                   agent,
		batchSize:               1,
		batchFlushTimeout:       10 * time.Millisecond,
		batchCollectDeadline:    10 * time.Millisecond,
		maxConcurrentRequests:   1,
		concurrentRequestPermit: make(chan struct{}, 1),
	}
}

// A chunk's wire shape depends on what it is: only a finished synchronous span
// is a PSpan. Mirrors the C++ agent's GrpcSpanSendBatchSpanVsSpanChunkTest.
func Test_spanGrpc_sendSpanBatch_spanShapePerChunk(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.spanGrpc = newMockSpanGrpc(agent)

	final := defaultSpan(agent)
	final.NewSpanEvent("final")
	partial := defaultSpan(agent)
	partial.NewSpanEvent("partial")
	async := defaultSpan(agent)
	async.asyncId = 11
	async.asyncSequence = 3
	async.NewSpanEvent("async")

	agent.spanGrpc.sendSpanBatchAsync([]*spanChunk{
		final.newEventChunk(true), partial.newEventChunk(false), async.newEventChunk(true),
	})
	agent.spanGrpc.awaitInFlightSpanBatch()

	client := agent.spanGrpc.spanClient.(*mockSpanGrpcClient)
	batch := client.lastRequest().GetSpan()
	require.Len(t, batch, 3)
	assert.NotNil(t, batch[0].GetSpan(), "a final synchronous chunk is a PSpan")
	assert.NotNil(t, batch[1].GetSpanChunk(), "a non-final chunk is a PSpanChunk")
	assert.NotNil(t, batch[2].GetSpanChunk(), "an async span is a PSpanChunk even when final")
	assert.Equal(t, int32(11), batch[2].GetSpanChunk().GetLocalAsyncId().GetAsyncId())
	assert.Equal(t, int32(3), batch[2].GetSpanChunk().GetLocalAsyncId().GetSequence())
}

// The caller that produced this trace is described on the accept event, service
// name included. Mirrors the C++ agent's
// GrpcSpanBatchCarriesParentServiceNameTest.
func Test_spanGrpc_sendSpanBatch_carriesParentInfo(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.spanGrpc = newMockSpanGrpc(agent)

	span := defaultSpan(agent)
	span.parentAppName = "ParentApp"
	span.parentAppType = 1500
	span.parentServiceName = "parent-service"
	span.acceptorHost = "acceptor:8080"
	span.rpcName = "/hello"
	span.endPoint = "endpoint:8080"
	span.remoteAddr = "10.0.0.1"
	span.NewSpanEvent("op")

	agent.spanGrpc.sendSpanBatchAsync([]*spanChunk{span.newEventChunk(true)})
	agent.spanGrpc.awaitInFlightSpanBatch()

	client := agent.spanGrpc.spanClient.(*mockSpanGrpcClient)
	batch := client.lastRequest().GetSpan()
	require.Len(t, batch, 1)
	accept := batch[0].GetSpan().GetAcceptEvent()
	assert.Equal(t, "/hello", accept.GetRpc())
	assert.Equal(t, "endpoint:8080", accept.GetEndPoint())
	assert.Equal(t, "10.0.0.1", accept.GetRemoteAddr())

	parent := accept.GetParentInfo()
	assert.Equal(t, "ParentApp", parent.GetParentApplicationName())
	assert.Equal(t, int32(1500), parent.GetParentApplicationType())
	assert.Equal(t, "parent-service", parent.GetParentServiceName())
	assert.Equal(t, "acceptor:8080", parent.GetAcceptorHost())
}

// A chunk without a span carries nothing to report and must not reach the
// encoder. Mirrors the C++ agent's GrpcSpanEnqueueDropsNullAndExitingChunksTest.
func Test_makePSpanMessageBatch_skipsEmptyChunks(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	builder := acquireSpanMessageBuilder()
	defer releaseSpanMessageBuilder(builder)

	batch := builder.makePSpanMessageBatch([]*spanChunk{nil, {span: nil}, newTestSpanChunk(agent)})

	assert.Len(t, batch.GetSpan(), 1)
}

// With every permit held by a slow collector, a new batch is dropped rather
// than parking the sender behind it; completing the in-flight request lets the
// next one through. Mirrors the C++ agent's
// GrpcSpanPermitExhaustionDropsBatchTest.
func Test_spanGrpc_sendSpanBatchAsync_permitExhaustionDropsBatch(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	client := &mockSpanGrpcClient{hold: make(chan struct{})}
	spanGrpc := newBoundedSpanGrpc(agent, client)

	spanGrpc.sendSpanBatchAsync([]*spanChunk{newTestSpanChunk(agent)})
	assert.Eventually(t, func() bool { return client.requestCount() == 1 },
		time.Second, time.Millisecond, "the first batch takes the only permit")

	spanGrpc.sendSpanBatchAsync([]*spanChunk{newTestSpanChunk(agent)})
	assert.Equal(t, 1, client.requestCount(), "a batch that cannot get a permit is dropped, not queued")

	close(client.hold)
	spanGrpc.awaitInFlightSpanBatch()
	spanGrpc.sendSpanBatchAsync([]*spanChunk{newTestSpanChunk(agent)})
	spanGrpc.awaitInFlightSpanBatch()
	assert.Equal(t, 2, client.requestCount(), "the released permit lets the next batch through")
	assert.Empty(t, spanGrpc.concurrentRequestPermit)
}

// A failed send returns its permit, so a collector outage cannot leak the
// sender's capacity away. Mirrors the C++ agent's
// GrpcSpanErrorStatusReleasesPermitTest.
func Test_spanGrpc_sendSpanBatchAsync_errorReleasesPermit(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	client := &mockSpanGrpcClient{err: status.Errorf(codes.Unavailable, "collector down")}
	spanGrpc := newBoundedSpanGrpc(agent, client)

	for i := 0; i < 3; i++ {
		spanGrpc.sendSpanBatchAsync([]*spanChunk{newTestSpanChunk(agent)})
		spanGrpc.awaitInFlightSpanBatch()
	}

	assert.Equal(t, 3, client.requestCount(), "a failed send must return its permit")
	assert.Empty(t, spanGrpc.concurrentRequestPermit)
}

// A partially rejected batch is a warning, not a sender failure: the permit
// comes back and later batches still go out. Mirrors the C++ agent's
// GrpcSpanPartialSuccessHandledTest.
func Test_spanGrpc_sendSpanBatchAsync_partialSuccessKeepsSending(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	client := &mockSpanGrpcClient{response: &pb.PSpanResultBatch{
		PartialSuccess: &pb.PPartialSuccess{RejectedSpans: 2, ErrorId: 7, ErrorMessage: "rejected"},
	}}
	spanGrpc := newBoundedSpanGrpc(agent, client)

	for i := 0; i < 2; i++ {
		spanGrpc.sendSpanBatchAsync([]*spanChunk{newTestSpanChunk(agent)})
		spanGrpc.awaitInFlightSpanBatch()
	}

	assert.Equal(t, 2, client.requestCount())
	assert.Empty(t, spanGrpc.concurrentRequestPermit)
}

func Test_handleSpanBatchResponse_toleratesEveryShape(t *testing.T) {
	assert.NotPanics(t, func() {
		handleSpanBatchResponse(nil)
		handleSpanBatchResponse(&pb.PSpanResultBatch{})
		handleSpanBatchResponse(&pb.PSpanResultBatch{PartialSuccess: &pb.PPartialSuccess{}})
		handleSpanBatchResponse(&pb.PSpanResultBatch{PartialSuccess: &pb.PPartialSuccess{ErrorMessage: "warning only"}})
		handleSpanBatchResponse(&pb.PSpanResultBatch{PartialSuccess: &pb.PPartialSuccess{RejectedSpans: 1}})
	})
}

// --- streams the collector never gave us ------------------------------------

// newXxxStreamWithRetry hands back an empty stream once the agent gives up, and
// every worker keeps calling into it until it notices. Sending must report
// Unavailable rather than dereferencing a nil stream.
func Test_streams_nilStreamReportsUnavailable(t *testing.T) {
	agent := newTestAgent(defaultConfig())

	assert.Equal(t, codes.Unavailable, status.Code((&pingStream{}).sendPing()))
	assert.Equal(t, codes.Unavailable, status.Code((&spanStream{}).sendSpan(newTestSpanChunk(agent))))
	assert.Equal(t, codes.Unavailable, status.Code((&statStream{}).sendStats(&pb.PStatMessage{})))
	assert.Equal(t, codes.Unavailable, status.Code((&cmdStream{}).sendCommandMessage()))
	assert.Equal(t, codes.Unavailable, status.Code((&cmdStream{}).sendFailMessage(1, "rejected")))
	assert.Equal(t, codes.Unavailable, status.Code((&activeThreadCountStream{}).sendActiveThreadCount()))

	_, err := (&cmdStream{}).recvCommandRequest()
	assert.Equal(t, codes.Unavailable, status.Code(err))

	assert.NotPanics(t, func() {
		(&pingStream{}).close()
		(&spanStream{}).close()
		(&statStream{}).close()
		(&cmdStream{}).close()
		(&activeThreadCountStream{}).close()
	}, "closing a stream that was never opened is a no-op")
}

// --- commands ---------------------------------------------------------------

// The handshake tells the collector which commands this agent serves; anything
// missing here is a command the web UI will never offer.
func Test_cmdStream_sendCommandMessage_advertisesSupportedCommands(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	cmd, _ := newMockCmdGrpc(agent)

	require.NoError(t, cmd.sendCommandMessage())

	sent := cmd.stream.(*mockCmdStream).sentMessages()
	require.Len(t, sent, 1)
	assert.Equal(t, []int32{
		int32(pb.PCommandType_ECHO),
		int32(pb.PCommandType_ACTIVE_THREAD_COUNT),
		int32(pb.PCommandType_ACTIVE_THREAD_DUMP),
		int32(pb.PCommandType_ACTIVE_THREAD_LIGHT_DUMP),
	}, sent[0].GetHandshakeMessage().GetSupportCommandServiceKey())
}

// Mirrors the C++ agent's GrpcCommandWorkerEchoTest.
func Test_cmdGrpc_sendEcho(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	_, client := newMockCmdGrpc(agent)

	agent.cmdGrpc.sendEcho(9, "hello")

	echoes := client.sentEchoes()
	require.Len(t, echoes, 1)
	assert.Equal(t, "hello", echoes[0].GetMessage())
	assert.Equal(t, int32(9), echoes[0].GetCommonResponse().GetResponseId())
	assert.Equal(t, int32(0), echoes[0].GetCommonResponse().GetStatus())
}

func Test_cmdGrpc_sendActiveThreadDump(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	_, client := newMockCmdGrpc(agent)

	dump := newGoroutineDump()
	for id := int64(1); id <= 2; id++ {
		dump.add(testGoroutine(id))
	}

	agent.cmdGrpc.sendActiveThreadDump(1, 0, []string{"goroutine 2"}, nil, dump)
	agent.cmdGrpc.sendActiveThreadLightDump(2, 0, dump)

	dumps, lightDumps := client.sentDumps()

	require.Len(t, dumps, 1)
	assert.Equal(t, int32(1), dumps[0].GetCommonResponse().GetResponseId())
	assert.Equal(t, int32(0), dumps[0].GetCommonResponse().GetStatus())
	assert.Equal(t, "Go", dumps[0].GetType())
	assert.Equal(t, runtime.Version(), dumps[0].GetVersion())
	require.Len(t, dumps[0].GetThreadDump(), 1, "only the requested goroutine is dumped")
	assert.Equal(t, "goroutine 2", dumps[0].GetThreadDump()[0].GetThreadDump().GetThreadName())

	require.Len(t, lightDumps, 1)
	assert.Equal(t, int32(2), lightDumps[0].GetCommonResponse().GetResponseId())
	assert.Equal(t, int32(0), lightDumps[0].GetCommonResponse().GetStatus())
	assert.Len(t, lightDumps[0].GetThreadDump(), 2, "a light dump reports every goroutine")
}

// A dump the runtime could not produce is reported as a failed response, not as
// an empty successful one.
func Test_cmdGrpc_sendActiveThreadDump_reportsDumpFailure(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	_, client := newMockCmdGrpc(agent)

	agent.cmdGrpc.sendActiveThreadDump(1, 0, nil, nil, nil)
	agent.cmdGrpc.sendActiveThreadLightDump(2, 0, nil)

	dumps, lightDumps := client.sentDumps()
	const failMsg = "An error occurred while dumping Goroutine"

	require.Len(t, dumps, 1)
	assert.Equal(t, int32(-1), dumps[0].GetCommonResponse().GetStatus())
	assert.Equal(t, failMsg, dumps[0].GetCommonResponse().GetMessage().GetValue())
	assert.Empty(t, dumps[0].GetThreadDump())

	require.Len(t, lightDumps, 1)
	assert.Equal(t, int32(-1), lightDumps[0].GetCommonResponse().GetStatus())
	assert.Equal(t, failMsg, lightDumps[0].GetCommonResponse().GetMessage().GetValue())
	assert.Empty(t, lightDumps[0].GetThreadDump())
}

// dialOptions returns opaque grpc.DialOptions, so the flow-control settings
// can only be checked on the wire. A correctly configured client advertises the
// window as SETTINGS_INITIAL_WINDOW_SIZE (the per-stream window) and then
// raises the stream-0 window from the 64KB default to the same value with a
// WINDOW_UPDATE (the connection window). Without WithInitialConnWindowSize no
// WINDOW_UPDATE is sent and the connection stays capped at 64KB.
func Test_grpcChannelOptions_dialOptions_flowControlWindow(t *testing.T) {
	const window = 1 * 1024 * 1024
	cfg, err := NewConfig(WithAppName("TestApp"), WithCollectorGrpcFlowControlWindow(window))
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	opts := newGrpcChannelOptions(cfg).dialOptions(insecure.NewCredentials())
	cc, err := grpc.NewClient("passthrough:///"+ln.Addr().String(), opts...)
	require.NoError(t, err)
	defer cc.Close()
	cc.Connect()

	conn, err := ln.Accept()
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	preface := make([]byte, len(http2.ClientPreface))
	_, err = io.ReadFull(conn, preface)
	require.NoError(t, err)
	require.Equal(t, http2.ClientPreface, string(preface))

	fr := http2.NewFramer(io.Discard, conn)
	frame, err := fr.ReadFrame()
	require.NoError(t, err)
	settings, ok := frame.(*http2.SettingsFrame)
	require.True(t, ok, "first frame must be SETTINGS, got %T", frame)
	streamWindow, ok := settings.Value(http2.SettingInitialWindowSize)
	require.True(t, ok, "SETTINGS must carry INITIAL_WINDOW_SIZE")
	assert.Equal(t, uint32(window), streamWindow, "stream window")

	frame, err = fr.ReadFrame()
	require.NoError(t, err)
	update, ok := frame.(*http2.WindowUpdateFrame)
	require.True(t, ok, "connection WINDOW_UPDATE must follow SETTINGS, got %T", frame)
	assert.Equal(t, uint32(0), update.StreamID, "window update must target the connection")
	assert.Equal(t, uint32(window-65535), update.Increment, "connection window")
}

// deadlineMetaClient records the deadline carried by a metadata request.
type deadlineMetaClient struct {
	pb.MetadataClient
	deadline time.Time
}

func (c *deadlineMetaClient) RequestApiMetaData(ctx context.Context, _ *pb.PApiMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	c.deadline, _ = ctx.Deadline()
	return &pb.PResult{Success: true}, nil
}

// Metadata sends must carry the short metaGrpcTimeOut, not agentGrpcTimeOut:
// a hung collector otherwise pins sendMetaWorker's permits for a minute per
// attempt while metaChan overflows and evicts cache entries.
func Test_sendApiMetadata_usesMetaDeadline(t *testing.T) {
	cfg, _ := NewConfig(WithAppName("TestApp"))
	agent := newTestAgent(cfg)
	client := &deadlineMetaClient{}
	agentGrpc := &agentGrpc{metaClient: client, agent: agent}

	before := time.Now()
	assert.NoError(t, agentGrpc.sendApiMetadata(&pb.PApiMetaData{ApiId: 1}))
	assert.WithinDuration(t, before.Add(metaGrpcTimeOut), client.deadline, time.Second)
}

// Renewal is off unless configured: the defaults are zero, negative values
// normalize to zero, and no service config (the eighth dial option) is added,
// so the channel keeps grpc-go's default pick_first policy.
func Test_config_grpcRenewalDisabledByDefault(t *testing.T) {
	cfg, err := NewConfig(WithAppName("TestApp"))
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.Int(CfgCollectorGrpcConnectionMaxAge))
	assert.Equal(t, 0, cfg.Int(CfgCollectorGrpcStreamMaxAge))
	assert.Zero(t, newGrpcChannelOptions(cfg).connectionMaxAge)
	assert.Len(t, newGrpcChannelOptions(cfg).dialOptions(insecure.NewCredentials()), 7)

	cfg, err = NewConfig(WithAppName("TestApp"),
		WithCollectorGrpcConnectionMaxAge(-1), WithCollectorGrpcStreamMaxAge(-1))
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.Int(CfgCollectorGrpcConnectionMaxAge))
	assert.Equal(t, 0, cfg.Int(CfgCollectorGrpcStreamMaxAge))

	agent := newTestAgent(cfg)
	assert.False(t, newStreamAge(agent).expired())
	assert.True(t, newStreamAge(agent).expiresAt.IsZero(), "a disabled max age never expires a stream")
}

func Test_grpcChannelOptions_connectionMaxAgeSelectsExpiringPolicy(t *testing.T) {
	cfg, err := NewConfig(WithAppName("TestApp"), WithCollectorGrpcConnectionMaxAge(600000))
	require.NoError(t, err)

	o := newGrpcChannelOptions(cfg)
	assert.Equal(t, 10*time.Minute, o.connectionMaxAge)
	assert.Len(t, o.dialOptions(insecure.NewCredentials()), 8, "the default service config selecting the policy")
}

func Test_streamAge_expiresWithinJitter(t *testing.T) {
	cfg, err := NewConfig(WithAppName("TestApp"), WithCollectorGrpcStreamMaxAge(1000))
	require.NoError(t, err)
	agent := newTestAgent(cfg)

	before := time.Now()
	age := newStreamAge(agent)
	assert.False(t, age.expired())
	assert.WithinRange(t, age.expiresAt, before.Add(900*time.Millisecond), time.Now().Add(1100*time.Millisecond))

	assert.True(t, streamAge{expiresAt: before}.expired())
}

func Test_randomize_staysWithinJitter(t *testing.T) {
	for i := 0; i < 1000; i++ {
		got := randomize(time.Second, streamAgeJitter)
		assert.GreaterOrEqual(t, got, 900*time.Millisecond)
		assert.LessOrEqual(t, got, 1100*time.Millisecond)
	}
}

// --- collector rejection ----------------------------------------------------

// rejectingMetaClient answers every metadata request with PResult.Success=false,
// the way a collector refuses a payload it will not store.
type rejectingMetaClient struct {
	calls int32
}

func (c *rejectingMetaClient) reject() (*pb.PResult, error) {
	atomic.AddInt32(&c.calls, 1)
	return &pb.PResult{Success: false, Message: "unsupported metadata"}, nil
}

func (c *rejectingMetaClient) callCount() int32 {
	return atomic.LoadInt32(&c.calls)
}

func (c *rejectingMetaClient) RequestApiMetaData(context.Context, *pb.PApiMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.reject()
}

func (c *rejectingMetaClient) RequestSqlMetaData(context.Context, *pb.PSqlMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.reject()
}

func (c *rejectingMetaClient) RequestSqlUidMetaData(context.Context, *pb.PSqlUidMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.reject()
}

func (c *rejectingMetaClient) RequestStringMetaData(context.Context, *pb.PStringMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.reject()
}

func (c *rejectingMetaClient) RequestExceptionMetaData(context.Context, *pb.PExceptionMetaData, ...grpc.CallOption) (*pb.PResult, error) {
	return c.reject()
}

// A rejection is a verdict on the payload, not a transport hiccup: the send
// fails, and it fails without burning the retry budget on the same bytes.
func Test_retryMeta_noRetryOnCollectorRejection(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	rejecting := &rejectingMetaClient{}
	agentGrpc := &agentGrpc{metaClient: rejecting, agent: agent}

	err := agentGrpc.sendApiMetadata(&pb.PApiMetaData{ApiId: 1, ApiInfo: "test.api"})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "unsupported metadata", "the collector's reason must reach the log")

	assert.False(t, agentGrpc.sendStringMetadataWithRetry(1, "test.error"))
	assert.Equal(t, int32(2), rejecting.callCount(), "a rejection must not be retried")
}

// A rejected id was already handed to the spans referencing it, so its cache
// entry must go, exactly as it does when the send never lands.
func Test_sendMetaWorker_releasesCacheOnCollectorRejection(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	rejecting := &rejectingMetaClient{}
	agent.agentGrpc = &agentGrpc{metaClient: rejecting, agent: agent}

	apiKey := apiCacheKey{"test.api", apiTypeInvocation}
	apiCached := func() bool { _, ok := agent.apiCache.peek(apiKey); return ok }
	assert.NotZero(t, agent.cacheSpanApi(apiKey.descriptor, apiKey.apiType))
	assert.True(t, apiCached())

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	assert.Eventually(t, func() bool {
		return rejecting.callCount() == 1
	}, 5*time.Second, 5*time.Millisecond, "the rejected item must be sent exactly once")

	agent.signalShutdown()
	agent.workerWg.Wait()

	assert.False(t, apiCached(), "a rejected metadata send must release its cache entry")

	// ...so the next use re-registers the metadata and queues it again
	assert.NotZero(t, agent.cacheSpanApi(apiKey.descriptor, apiKey.apiType))
	assert.True(t, apiCached())
	assert.Equal(t, 1, len(agent.metaChan))
}
