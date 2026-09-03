package pinpoint

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	grpcmock "github.com/pinpoint-apm/pinpoint-go-agent/protobuf/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

// The mocks in protobuf/mock are generated from the same .proto files as
// the clients themselves (protoc-gen-go-grpcmock, testify), so they satisfy the
// real interfaces and a scenario can script the collector per call -- fail
// twice, then recover -- instead of hand-rolling a counter per stub.
var (
	_ pb.AgentClient                  = (*grpcmock.MockAgentClient)(nil)
	_ pb.MetadataClient               = (*grpcmock.MockMetadataClient)(nil)
	_ pb.SpanClient                   = (*grpcmock.MockSpanClient)(nil)
	_ pb.StatClient                   = (*grpcmock.MockStatClient)(nil)
	_ pb.ProfilerCommandServiceClient = (*grpcmock.MockProfilerCommandServiceClient)(nil)
)

func collectorDown() error {
	return status.Error(codes.Unavailable, "collector down")
}

// counter records how often a mocked call ran, without racing the worker
// goroutine the way reading testify's own call log would.
type counter struct{ n atomic.Int32 }

func (c *counter) count(mock.Arguments) { c.n.Add(1) }
func (c *counter) get() int32           { return c.n.Load() }

// The ping worker is the agent's liveness signal: a stream the collector broke
// must be closed and replaced, and the worker must go on using the replacement.
// It does not re-ping immediately -- the next ping rides the normal 60s tick --
// so what recovery looks like here is the swap itself.
func Test_sendPingWorker_replacesStreamTheCollectorBroke(t *testing.T) {
	agent := newTestAgent(defaultConfig())

	broken := grpcmock.NewMockAgent_PingSessionClient()
	broken.OnSend(mock.Anything).Return(collectorDown())
	broken.On("CloseSend").Return(nil)

	healthy := grpcmock.NewMockAgent_PingSessionClient()
	healthy.OnSend(mock.Anything).Return(nil)
	healthy.OnRecv().Return(&pb.PPing{}, nil)
	healthy.On("CloseSend").Return(nil)

	var opened counter
	client := grpcmock.NewMockAgentClient()
	client.OnPingSession(mock.Anything).Run(opened.count).Return(broken, nil).Once()
	client.OnPingSession(mock.Anything).Run(opened.count).Return(healthy, nil)
	agent.agentGrpc = &agentGrpc{agentClient: client, agent: agent}

	agent.workerWg.Add(1)
	go agent.superviseWorker("ping", agent.sendPingWorker)
	waitFor(t, "the broken ping stream to be replaced", func() bool { return opened.get() == 2 })

	agent.signalShutdown()
	agent.workerWg.Wait()

	broken.AssertNumberOfCalls(t, "Send", 1)
	broken.AssertNumberOfCalls(t, "CloseSend", 1)
	// Shutdown closes whichever stream the worker is holding, so this is what
	// proves it adopted the replacement rather than keeping the dead one.
	healthy.AssertNumberOfCalls(t, "CloseSend", 1)
}

// A failed send costs one reconnect and no spans: everything already queued
// still goes out on the replacement stream. The worker used to arm a filter
// that skipped spans whose startTime predated the failure, which is why this
// asserts the whole queue arrives - see the reconnect path in sendSpanWorker
// for why that policy was removed.
func Test_sendSpanWorker_reopensStreamAndResendsNothingLost(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.spanQueue = newSpanQueue(4) // one shard: FIFO is deterministic

	var mu sync.Mutex
	var sent []int64
	record := func(args mock.Arguments) {
		// The transport recycles the message once Send returns, so read it here.
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, args.Get(0).(*pb.PSpanMessage).GetSpan().GetSpanId())
	}

	broken := grpcmock.NewMockSpan_SendSpanClient()
	broken.OnSend(mock.Anything).Return(collectorDown())
	broken.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)

	healthy := grpcmock.NewMockSpan_SendSpanClient()
	healthy.OnSend(mock.Anything).Run(record).Return(nil)
	healthy.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)

	client := grpcmock.NewMockSpanClient()
	client.OnSendSpan(mock.Anything).Return(broken, nil).Once()
	client.OnSendSpan(mock.Anything).Return(healthy, nil)
	agent.spanGrpc = &spanGrpc{spanClient: client, agent: agent}

	// The queue is filled and closed up front, so the worker drains exactly
	// these three and exits: no timing to wait on. Span 2 is a slow request -
	// it started before the failure but its chunk is queued live, and the old
	// policy discarded exactly this span.
	for i, startTime := range []time.Time{
		time.Now(), time.Now().Add(-time.Hour), time.Now(),
	} {
		span := defaultSpan(agent)
		span.spanId = int64(i + 1)
		span.startTime = startTime
		require.True(t, agent.spanQueue.enqueue(span.newEventChunk(true)))
	}
	agent.spanQueue.close()

	agent.workerWg.Add(1)
	go agent.superviseWorker("span", agent.sendSpanWorker)
	agent.workerWg.Wait()

	broken.AssertNumberOfCalls(t, "Send", 1)
	client.AssertNumberOfCalls(t, "SendSpan", 2)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int64{2, 3}, sent,
		"span 1 failed with the stream; 2 and 3 must both survive the reconnect")
}

// The same guarantee at the default capacity, where the queue sweeps 32 shards
// in unspecified order: a reconnect must not turn dequeue order into a
// data-loss lottery, which is what the removed startTime filter did.
func Test_sendSpanWorker_reconnectLosesNothingAcrossShards(t *testing.T) {
	const queued = 200

	agent := newTestAgent(defaultConfig())
	agent.spanQueue = newSpanQueue(1024) // default: 32 shards

	var mu sync.Mutex
	var sent int
	healthy := grpcmock.NewMockSpan_SendSpanClient()
	healthy.OnSend(mock.Anything).Run(func(mock.Arguments) {
		mu.Lock()
		sent++
		mu.Unlock()
	}).Return(nil)
	healthy.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)

	broken := grpcmock.NewMockSpan_SendSpanClient()
	broken.OnSend(mock.Anything).Return(collectorDown())
	broken.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)

	client := grpcmock.NewMockSpanClient()
	client.OnSendSpan(mock.Anything).Return(broken, nil).Once()
	client.OnSendSpan(mock.Anything).Return(healthy, nil)
	agent.spanGrpc = &spanGrpc{spanClient: client, agent: agent}

	// Every span predates the failure by an hour: under the old policy these
	// were exactly the spans meant to be skipped, and shard order decided how
	// many actually were.
	for i := 0; i < queued; i++ {
		span := defaultSpan(agent)
		span.startTime = time.Now().Add(-time.Hour)
		require.True(t, agent.spanQueue.enqueue(span.newEventChunk(true)))
	}
	agent.spanQueue.close()

	agent.workerWg.Add(1)
	go agent.superviseWorker("span", agent.sendSpanWorker)
	agent.workerWg.Wait()

	assert.Zero(t, agent.spanQueue.dropCount(), "the queue was never saturated")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, queued-1, sent,
		"only the span that rode the broken stream is lost, regardless of shard order")
}

// A collector outage must not wedge the batch sender: the failed batches give
// their concurrency permits back and the batches behind them still go out.
func Test_sendSpanBatchWorker_resumesAfterCollectorOutage(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSpanBatchEnable, true)
	agent := newTestAgent(cfg)
	agent.spanQueue = newSpanQueue(4)

	var delivered counter
	client := grpcmock.NewMockSpanClient()
	client.OnSendSpanBatch(mock.Anything, mock.Anything).
		Return((*pb.PSpanResultBatch)(nil), collectorDown()).Twice()
	client.OnSendSpanBatch(mock.Anything, mock.Anything).
		Run(delivered.count).Return(&pb.PSpanResultBatch{}, nil)

	agent.spanGrpc = &spanGrpc{
		spanClient:              client,
		agent:                   agent,
		batchSize:               1, // one chunk per batch keeps the count exact
		batchFlushTimeout:       time.Second,
		batchCollectDeadline:    time.Millisecond,
		maxConcurrentRequests:   2,
		concurrentRequestPermit: make(chan struct{}, 2),
	}

	for i := 0; i < 4; i++ {
		require.True(t, agent.spanQueue.enqueue(newTestSpanChunk(agent)))
	}
	agent.spanQueue.close()

	agent.workerWg.Add(1)
	go agent.superviseWorker("span batch", agent.sendSpanBatchWorker)
	agent.workerWg.Wait()

	client.AssertNumberOfCalls(t, "SendSpanBatch", 4)
	assert.EqualValues(t, 2, delivered.get(), "the batches after the outage are delivered")
	assert.Empty(t, agent.spanGrpc.concurrentRequestPermit, "every batch returns its permit, failed ones included")
}

// The stat stream is long-lived, so a single failed send has to cost one
// reconnect and not the statistics that follow it.
func Test_sendStatsWorker_reopensStreamAfterSendErrorAndResumes(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.statChan = make(chan *pb.PStatMessage, 4)

	broken := grpcmock.NewMockStat_SendAgentStatClient()
	broken.OnSend(mock.Anything).Return(collectorDown())
	broken.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)

	var stats counter
	healthy := grpcmock.NewMockStat_SendAgentStatClient()
	healthy.OnSend(mock.Anything).Run(stats.count).Return(nil)
	healthy.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)

	client := grpcmock.NewMockStatClient()
	client.OnSendAgentStat(mock.Anything).Return(broken, nil).Once()
	client.OnSendAgentStat(mock.Anything).Return(healthy, nil)
	agent.statGrpc = &statGrpc{statClient: client, agent: agent}

	agent.statChan <- makePAgentStatBatch([]*inspectorStats{agent.stats.getStats()})
	agent.statChan <- makePAgentStatBatch([]*inspectorStats{agent.stats.getStats()})

	agent.workerWg.Add(1)
	go agent.superviseWorker("send stats", agent.sendStatsWorker)
	waitFor(t, "the replacement stat stream to carry a batch", func() bool { return stats.get() > 0 })

	agent.signalShutdown()
	agent.workerWg.Wait()

	broken.AssertNumberOfCalls(t, "Send", 1)
	client.AssertNumberOfCalls(t, "SendAgentStat", 2)
}

// A collector that accepts the command stream and then immediately rejects the
// handshake leaves the channel READY, so the reconnect back-off is the only
// thing keeping this loop from opening streams continuously inside the host
// application -- and a shutdown must not have to wait it out.
func Test_runCommandService_pacesReconnectsAndStopsPromptly(t *testing.T) {
	agent := newTestAgent(defaultConfig())

	stream := grpcmock.NewMockProfilerCommandService_HandleCommandClient()
	stream.OnSend(mock.Anything).Return(collectorDown())
	stream.On("CloseSend").Return(nil)

	var opened counter
	client := grpcmock.NewMockProfilerCommandServiceClient()
	client.OnHandleCommand(mock.Anything).Run(opened.count).Return(stream, nil)
	agent.cmdGrpc = &cmdGrpc{cmdClient: client, agent: agent, atcStreams: atcStreams{agent: agent}}

	agent.workerWg.Add(1)
	go agent.superviseWorker("command", agent.runCommandService)

	// The first attempt runs at once; the second waits out backOffSleep(0),
	// which is at least 2.1s. A hot loop would show up here as a large count.
	time.Sleep(300 * time.Millisecond)
	assert.EqualValues(t, 1, opened.get(), "a rejected handshake must not be retried hot")

	stopped := make(chan struct{})
	go func() { agent.workerWg.Wait(); close(stopped) }()
	agent.signalShutdown()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown must interrupt the reconnect back-off instead of waiting it out")
	}
}

// Stream renewal is the normal path, not the outage path: the span worker
// swaps an aged stream for a new one between two sends and delivers both spans,
// whereas a failed send would have skipped the span that hit the failure.
func Test_sendSpanWorker_renewsAgedStreamWithoutDroppingSpans(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgCollectorGrpcStreamMaxAge, 1)
	agent := newTestAgent(cfg)

	var sent counter
	stream := grpcmock.NewMockSpan_SendSpanClient()
	stream.OnSend(mock.Anything).Run(sent.count).Return(nil)
	stream.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)

	client := grpcmock.NewMockSpanClient()
	client.OnSendSpan(mock.Anything).Return(stream, nil)
	agent.spanGrpc = &spanGrpc{spanClient: client, agent: agent}

	agent.workerWg.Add(1)
	go agent.superviseWorker("span", agent.sendSpanWorker)

	require.True(t, agent.spanQueue.enqueue(newTestSpanChunk(agent)))
	waitFor(t, "the first span to be sent", func() bool { return sent.get() == 1 })
	time.Sleep(5 * time.Millisecond) // the stream passes its jittered max age
	require.True(t, agent.spanQueue.enqueue(newTestSpanChunk(agent)))
	waitFor(t, "the second span to be sent", func() bool { return sent.get() == 2 })

	agent.spanQueue.close()
	agent.workerWg.Wait()

	client.AssertNumberOfCalls(t, "SendSpan", 2)
	// One CloseAndRecv for the renewal, one for the worker's final close.
	stream.AssertNumberOfCalls(t, "CloseAndRecv", 2)
	assert.EqualValues(t, 2, sent.get(), "no span is dropped over a renewal")
}

func Test_sendStatsWorker_renewsAgedStream(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgCollectorGrpcStreamMaxAge, 1)
	agent := newTestAgent(cfg)
	agent.statChan = make(chan *pb.PStatMessage, 4)

	var sent counter
	stream := grpcmock.NewMockStat_SendAgentStatClient()
	stream.OnSend(mock.Anything).Run(sent.count).Return(nil)
	stream.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)

	client := grpcmock.NewMockStatClient()
	client.OnSendAgentStat(mock.Anything).Return(stream, nil)
	agent.statGrpc = &statGrpc{statClient: client, agent: agent}

	agent.workerWg.Add(1)
	go agent.superviseWorker("send stats", agent.sendStatsWorker)

	agent.statChan <- makePAgentStatBatch([]*inspectorStats{agent.stats.getStats()})
	waitFor(t, "the first batch to be sent", func() bool { return sent.get() == 1 })
	time.Sleep(5 * time.Millisecond)
	agent.statChan <- makePAgentStatBatch([]*inspectorStats{agent.stats.getStats()})
	waitFor(t, "the second batch to be sent", func() bool { return sent.get() == 2 })

	agent.signalShutdown()
	agent.workerWg.Wait()

	client.AssertNumberOfCalls(t, "SendAgentStat", 2)
	stream.AssertNumberOfCalls(t, "CloseAndRecv", 2)
}

// The command worker waits in Recv, so its max age is the stream deadline. When
// it runs out the worker must reopen at once -- a renewal is not a failure, so
// the reconnect back-off that paces failed streams does not apply.
func Test_runCommandService_renewsAgedStreamWithoutBackOff(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgCollectorGrpcStreamMaxAge, 20)
	agent := newTestAgent(cfg)

	// Each HandleCommand hands its context over so that Recv, like the real
	// stream, returns once the deadline set on that context passes.
	contexts := make(chan context.Context, 16)
	stream := grpcmock.NewMockProfilerCommandService_HandleCommandClient()
	stream.OnSend(mock.Anything).Return(nil)
	stream.OnRecv().Run(func(mock.Arguments) {
		ctx := <-contexts
		<-ctx.Done()
	}).Return((*pb.PCmdRequest)(nil), context.DeadlineExceeded)
	stream.On("CloseSend").Return(nil)

	var opened counter
	client := grpcmock.NewMockProfilerCommandServiceClient()
	client.OnHandleCommand(mock.Anything).Run(func(args mock.Arguments) {
		opened.count(args)
		contexts <- args.Get(0).(context.Context)
	}).Return(stream, nil)
	agent.cmdGrpc = &cmdGrpc{cmdClient: client, agent: agent, atcStreams: atcStreams{agent: agent}}

	agent.workerWg.Add(1)
	go agent.superviseWorker("command", agent.runCommandService)

	// backOffSleep(0) is at least 2.1s, so three streams inside the 2s waitFor
	// window can only mean the renewals skipped the back-off.
	waitFor(t, "the command stream to be renewed twice", func() bool { return opened.get() >= 3 })

	agent.enable.Store(false)
	agent.signalShutdown()
	agent.workerWg.Wait()
}

// A collector outage saturates the span queue, and the loss has to be visible
// in the log rather than only in dropCount(): the producers just bump their
// shard counter, so it is the worker that has to warn. Both span workers are
// covered because each polls from its own cycle.
func Test_spanWorkers_warnAboutSaturatedQueue(t *testing.T) {
	const queueCap, enqueued = 4, 6

	// The queue is filled and closed up front, so each worker drains exactly
	// queueCap chunks and exits - several report calls, one warning.
	fill := func(agent *agent) {
		for i := 0; i < enqueued; i++ {
			require.True(t, agent.spanQueue.enqueue(newTestSpanChunk(agent)))
		}
		require.EqualValues(t, enqueued-queueCap, agent.spanQueue.dropCount(),
			"test must overflow the queue")
		agent.spanQueue.close()
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (*agent, func())
	}{
		{
			name: "stream",
			setup: func(t *testing.T) (*agent, func()) {
				agent := newTestAgent(defaultConfig())
				agent.spanQueue = newSpanQueue(queueCap)

				stream := grpcmock.NewMockSpan_SendSpanClient()
				stream.OnSend(mock.Anything).Return(nil)
				stream.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)
				client := grpcmock.NewMockSpanClient()
				client.OnSendSpan(mock.Anything).Return(stream, nil)
				agent.spanGrpc = &spanGrpc{spanClient: client, agent: agent}

				return agent, agent.sendSpanWorker
			},
		},
		{
			name: "batch",
			setup: func(t *testing.T) (*agent, func()) {
				cfg := defaultConfig()
				cfg.Set(CfgSpanBatchEnable, true)
				agent := newTestAgent(cfg)
				agent.spanQueue = newSpanQueue(queueCap)

				client := grpcmock.NewMockSpanClient()
				client.OnSendSpanBatch(mock.Anything, mock.Anything).
					Return(&pb.PSpanResultBatch{}, nil)
				agent.spanGrpc = &spanGrpc{
					spanClient:              client,
					agent:                   agent,
					batchSize:               1, // one cycle per chunk, so the poll repeats
					batchFlushTimeout:       time.Second,
					batchCollectDeadline:    time.Millisecond,
					maxConcurrentRequests:   2,
					concurrentRequestPermit: make(chan struct{}, 2),
				}

				return agent, agent.sendSpanBatchWorker
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, worker := tc.setup(t)

			var buf bytes.Buffer
			defer captureWarnLog(&buf)()

			fill(agent)
			assert.Empty(t, buf.String(), "the producer path must not log")

			agent.workerWg.Add(1)
			go agent.superviseWorker(tc.name, worker)
			agent.workerWg.Wait()

			assert.Equal(t, 1, strings.Count(buf.String(), "span queue overflow"),
				"the worker must warn once per report interval, not once per cycle")
			assert.Contains(t, buf.String(),
				fmt.Sprintf("%d dropped in total (oldest overwritten, max queue size %d)",
					enqueued-queueCap, queueCap))
		})
	}
}

// Shutdown clears enable before it closes the span queue, so the drain runs on
// a disabled agent, where a reconnect can never succeed. The worker must stop
// there instead of walking the rest of the queue: every later send fails with
// "span stream is nil", delivering nothing and logging one error per chunk.
func Test_sendSpanWorker_stopsWhenReconnectGivesUp(t *testing.T) {
	const queued = 5

	agent := newTestAgent(defaultConfig())
	agent.spanQueue = newSpanQueue(8) // one shard: FIFO is deterministic

	broken := grpcmock.NewMockSpan_SendSpanClient()
	broken.OnSend(mock.Anything).Return(collectorDown())
	broken.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)

	client := grpcmock.NewMockSpanClient()
	client.OnSendSpan(mock.Anything).Return(broken, nil)
	agent.spanGrpc = &spanGrpc{spanClient: client, agent: agent}

	for i := 0; i < queued; i++ {
		span := defaultSpan(agent)
		span.spanId = int64(i + 1)
		require.True(t, agent.spanQueue.enqueue(span.newEventChunk(true)))
	}

	// The shutdown order: the agent is disabled first, then the queue closed.
	agent.enable.Store(false)
	agent.spanQueue.close()

	agent.workerWg.Add(1)
	go agent.superviseWorker("span", agent.sendSpanWorker)
	agent.workerWg.Wait()

	assert.Equal(t, queued-1, agent.spanQueue.length(),
		"the drain must end at the first chunk a dead stream refuses, not walk the queue")
}
