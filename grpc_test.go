package pinpoint

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

			span := defaultSpan()
			span.agent = agent
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

			span := defaultSpan()
			span.agent = agent
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
	agent.spanChan = make(chan *spanChunk, 2)

	first := newTestSpanChunk(agent)
	second := newTestSpanChunk(agent)
	third := newTestSpanChunk(agent)

	assert.True(t, agent.enqueueSpan(first), "enqueue first")
	assert.True(t, agent.enqueueSpan(second), "enqueue second")
	assert.True(t, agent.enqueueSpan(third), "enqueue third")

	assert.Equal(t, second, <-agent.spanChan, "oldest span should be discarded")
	assert.Equal(t, third, <-agent.spanChan, "newest span should be enqueued")
}

func Test_agent_enqueueSpan_streamModeAlsoDiscardsOldest(t *testing.T) {
	// The queue-full drop policy is independent of the span transport: even in
	// legacy stream mode (Span.Batch.Enable=false) enqueueSpan discards the
	// oldest span and enqueues the newest, so recent traces are favored under
	// backpressure just like in batch mode.
	cfg := defaultConfig()
	cfg.Set(CfgSpanBatchEnable, false)
	agent := newTestAgent(cfg)
	agent.spanChan = make(chan *spanChunk, 2)

	first := newTestSpanChunk(agent)
	second := newTestSpanChunk(agent)
	third := newTestSpanChunk(agent)

	assert.True(t, agent.enqueueSpan(first), "enqueue first")
	assert.True(t, agent.enqueueSpan(second), "enqueue second")
	assert.True(t, agent.enqueueSpan(third), "newest span is enqueued after discarding the oldest")

	assert.Equal(t, second, <-agent.spanChan, "oldest span should be discarded")
	assert.Equal(t, third, <-agent.spanChan, "newest span should be enqueued")
}

func Test_spanGrpc_collectSpanBatch_stopsAtBatchSize(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	spanGrpc := newMockSpanGrpc(agent, t)
	spanGrpc.batchSize = 2
	spanGrpc.batchCollectDeadline = time.Second

	first := newTestSpanChunk(agent)
	second := newTestSpanChunk(agent)
	third := newTestSpanChunk(agent)
	spanChan := make(chan *spanChunk, 2)
	spanChan <- second
	spanChan <- third

	batch, closed := spanGrpc.collectSpanBatch(first, spanChan)

	assert.False(t, closed)
	assert.Equal(t, []*spanChunk{first, second}, batch)
	assert.Equal(t, 1, len(spanChan), "third chunk should wait for the next batch")
}

func Test_spanGrpc_collectSpanBatch_flushesClosedChannel(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	spanGrpc := newMockSpanGrpc(agent, t)
	spanGrpc.batchSize = 50
	spanGrpc.batchCollectDeadline = time.Second

	first := newTestSpanChunk(agent)
	second := newTestSpanChunk(agent)
	spanChan := make(chan *spanChunk, 1)
	spanChan <- second
	close(spanChan)

	batch, closed := spanGrpc.collectSpanBatch(first, spanChan)

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
	span := defaultSpan()
	span.agent = agent
	return span.newEventChunk(true)
}

func Test_streamState_isFailure(t *testing.T) {
	// Mirrors the Java SimpleStreamState contract: a failure is reported only
	// once the window is both dense (> limitCount) and long (> limitTime).
	s := &streamState{limitCount: 3, limitTime: 50 * time.Millisecond}

	assert.False(t, s.isFailure(), "no fails yet")

	for i := 0; i < 10; i++ {
		s.fail()
	}
	assert.False(t, s.isFailure(), "count exceeded but time window not yet elapsed")

	time.Sleep(60 * time.Millisecond)
	assert.True(t, s.isFailure(), "count and time both exceeded")

	// A success resets the window entirely.
	s.success()
	assert.False(t, s.isFailure(), "success clears the failure window")
	s.fail()
	assert.False(t, s.isFailure(), "single fail after reset is tolerated")
}

func Test_streamSender_toleratesTransientBlockThenSucceeds(t *testing.T) {
	// A send that is briefly slow (longer than one not-ready probe) but well
	// within the failure window must succeed without tearing down the stream.
	sender := newStreamSender()
	defer sender.close()
	state := newStreamState()

	err := sender.send(func() error {
		time.Sleep(sendStreamReadyTimeout * 3)
		return nil
	}, state, "test")

	assert.NoError(t, err, "transient slowness should be tolerated")
	assert.False(t, state.isFailure(), "successful send resets the failure window")
}

func Test_streamSender_givesUpAfterFailureWindow(t *testing.T) {
	// A send that stays blocked past the failure window returns DeadlineExceeded
	// so the worker can recreate the stream. A small window keeps the test fast.
	sender := newStreamSender()
	defer sender.close()
	state := &streamState{limitCount: 2, limitTime: 30 * time.Millisecond}

	release := make(chan struct{})
	err := sender.send(func() error {
		<-release // stay blocked until the sender goroutine is told to unblock
		return nil
	}, state, "test")
	close(release) // unblock the in-flight send so the sender goroutine can exit

	assert.Error(t, err, "sustained backpressure should surface an error")
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err), "give-up uses DeadlineExceeded")
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
