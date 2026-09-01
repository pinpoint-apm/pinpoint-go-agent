package pinpoint

import (
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
	go agent.sendPingWorker()
	waitFor(t, "the broken ping stream to be replaced", func() bool { return opened.get() == 2 })

	agent.signalShutdown()
	agent.workerWg.Wait()

	broken.AssertNumberOfCalls(t, "Send", 1)
	broken.AssertNumberOfCalls(t, "CloseSend", 1)
	// Shutdown closes whichever stream the worker is holding, so this is what
	// proves it adopted the replacement rather than keeping the dead one.
	healthy.AssertNumberOfCalls(t, "CloseSend", 1)
}

// After a failed send the span worker reopens the stream and skips whatever was
// queued before the outage: those spans are already a second stale, and
// replaying them would delay the live traffic behind them.
func Test_sendSpanWorker_reopensStreamAndDropsSpansStaleFromTheOutage(t *testing.T) {
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
	// these three and exits: no timing to wait on.
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
	go agent.sendSpanWorker()
	agent.workerWg.Wait()

	broken.AssertNumberOfCalls(t, "Send", 1)
	client.AssertNumberOfCalls(t, "SendSpan", 2)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int64{3}, sent,
		"span 1 failed, span 2 predates the outage and is skipped, span 3 resumes on the new stream")
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
	go agent.sendSpanBatchWorker()
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
	go agent.sendStatsWorker()
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
	go agent.runCommandService()

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
