package pinpoint

import (
	"context"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type blockingAgentInfoClient struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (c *blockingAgentInfoClient) RequestAgentInfo(ctx context.Context, _ *pb.PAgentInfo) (*pb.PResult, error) {
	close(c.started)
	select {
	case <-ctx.Done():
		close(c.canceled)
		return nil, ctx.Err()
	case <-c.release:
		return nil, context.Canceled
	}
}

func (*blockingAgentInfoClient) PingSession(context.Context) (pb.Agent_PingSessionClient, error) {
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
			agent.agentGrpc = newMockAgentGrpc(agent, t)
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
			agent.agentGrpc = newMockAgentGrpc(agent, t)
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
			agent.agentGrpc = newMockAgentGrpc(agent, t)
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
			agent.agentGrpc = newMockAgentGrpc(agent, t)
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
			agent.agentGrpc = newMockAgentGrpcPing(agent, t)
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
			agent.spanGrpc = newMockSpanGrpc(agent, t)
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
			agent.spanGrpc = newMockSpanGrpc(agent, t)

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
		for shard.tryEnqueue(chunk) {
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
	spanGrpc := newMockSpanGrpc(agent, t)
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
	spanGrpc := newMockSpanGrpc(agent, t)
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
			agent.statGrpc = newMockStatGrpc(agent, t)
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
			agent.statGrpc = newRetryMockStatGrpc(agent, t)
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
	conn, err := grpc.Dial("127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
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
	// NewClient, not Dial: it leaves the channel IDLE instead of connecting
	// eagerly, which is the state this path exists for.
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
}

func (c *failingMetaClient) fail() error {
	atomic.AddInt32(&c.calls, 1)
	return c.err
}

func (c *failingMetaClient) callCount() int32 {
	return atomic.LoadInt32(&c.calls)
}

func (c *failingMetaClient) RequestApiMetaData(context.Context, *pb.PApiMetaData) (*pb.PResult, error) {
	return nil, c.fail()
}

func (c *failingMetaClient) RequestSqlMetaData(context.Context, *pb.PSqlMetaData) (*pb.PResult, error) {
	return nil, c.fail()
}

func (c *failingMetaClient) RequestSqlUidMetaData(context.Context, *pb.PSqlUidMetaData) (*pb.PResult, error) {
	return nil, c.fail()
}

func (c *failingMetaClient) RequestStringMetaData(context.Context, *pb.PStringMetaData) (*pb.PResult, error) {
	return nil, c.fail()
}

func (c *failingMetaClient) RequestExceptionMetaData(context.Context, *pb.PExceptionMetaData) (*pb.PResult, error) {
	return nil, c.fail()
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
	go agent.sendMetaWorker()

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

func (c *blockingMetaClient) block() error {
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
	return nil
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

func (c *blockingMetaClient) RequestApiMetaData(context.Context, *pb.PApiMetaData) (*pb.PResult, error) {
	return nil, c.block()
}

func (c *blockingMetaClient) RequestSqlMetaData(context.Context, *pb.PSqlMetaData) (*pb.PResult, error) {
	return nil, c.block()
}

func (c *blockingMetaClient) RequestSqlUidMetaData(context.Context, *pb.PSqlUidMetaData) (*pb.PResult, error) {
	return nil, c.block()
}

func (c *blockingMetaClient) RequestStringMetaData(context.Context, *pb.PStringMetaData) (*pb.PResult, error) {
	return nil, c.block()
}

func (c *blockingMetaClient) RequestExceptionMetaData(context.Context, *pb.PExceptionMetaData) (*pb.PResult, error) {
	return nil, c.block()
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
	go agent.sendMetaWorker()

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

func (c *countingAgentClient) RequestAgentInfo(ctx context.Context, agentInfo *pb.PAgentInfo) (*pb.PResult, error) {
	c.calls.Add(1)
	if c.fail.Load() {
		return nil, status.Errorf(codes.Unavailable, "collector down")
	}
	return &pb.PResult{Success: true}, nil
}

func (c *countingAgentClient) PingSession(ctx context.Context) (pb.Agent_PingSessionClient, error) {
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
	go agent.refreshAgentInfoWorker(20 * time.Millisecond)

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
	assert.Len(t, o.dialOptions(insecure.NewCredentials()), 6)
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
