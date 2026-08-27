package pinpoint

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

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
			b := agent.agentGrpc.sendApiMetadataWithRetry(asyncApiId, "Asynchronous Invocation", -1, apiTypeInvocation)
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
			stats[0] = getStats()
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
			stats[0] = getStats()
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
	conn, err := grpc.Dial("127.0.0.1:1", grpc.WithInsecure())
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
	conn, err := grpc.NewClient("127.0.0.1:1", grpc.WithInsecure())
	assert.NoError(t, err)
	defer conn.Close()

	assert.Equal(t, connectivity.Idle, conn.GetState())
	assert.False(t, waitUntilReady(context.Background(), conn, 200*time.Millisecond, "test"))
	// An IDLE channel used to sit there for the whole interval; it must now
	// have been asked to connect.
	assert.NotEqual(t, connectivity.Idle, conn.GetState())
}
