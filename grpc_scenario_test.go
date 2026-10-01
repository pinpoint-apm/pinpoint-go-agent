package pinpoint

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"
	grpcmock "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

// The mocks in internal/protobuf/mock are generated from the same .proto files
// as the clients themselves (protoc-gen-go-grpcmock, testify), so they satisfy
// the real interfaces and a scenario can script the collector per call -- fail
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

// A collector outage must not wedge the batch sender: the failed batches give
// their concurrency permits back and the batches behind them still go out.
func Test_sendSpanBatchWorker_resumesAfterCollectorOutage(t *testing.T) {
	agent := newTestAgent(defaultConfig())
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
// reconnect and no statistics: the batch that hit the failure is re-sent on
// the replacement, and the one behind it follows.
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
	waitFor(t, "the replacement stat stream to carry both batches", func() bool { return stats.get() == 2 })

	agent.signalShutdown()
	agent.workerWg.Wait()

	broken.AssertNumberOfCalls(t, "Send", 1)
	healthy.AssertNumberOfCalls(t, "Send", 2)
	client.AssertNumberOfCalls(t, "SendAgentStat", 2)
}

func Test_sendStatsWorker_drainsLastUrlStatDuringShutdown(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgHttpUrlStatEnable, true)
	agent := newTestAgent(cfg)
	agent.statChan = make(chan *pb.PStatMessage, 4)

	entered := make(chan struct{})
	release := make(chan struct{})
	var sent counter
	stream := grpcmock.NewMockStat_SendAgentStatClient()
	stream.OnSend(mock.Anything).Run(func(mock.Arguments) {
		if sent.n.Add(1) == 1 {
			close(entered)
			<-release
		}
	}).Return(nil)
	stream.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)
	client := grpcmock.NewMockStatClient()
	client.OnSendAgentStat(mock.Anything).Return(stream, nil)
	agent.statGrpc = &statGrpc{statClient: client, agent: agent}

	agent.statChan <- &pb.PStatMessage{}
	agent.startWorkers([]worker{{name: "send stats", body: agent.sendStatsWorker, start: true}})
	<-entered
	agent.urlStats.add(&urlStat{
		entry:   &UrlStatEntry{Url: "/shutdown", Status: 200},
		endTime: time.Now(),
		elapsed: 1,
	})

	done := make(chan struct{})
	go func() {
		agent.Shutdown()
		close(done)
	}()
	<-agent.spanQueue.done
	close(release)
	<-done

	assert.EqualValues(t, 2, sent.get(), "the final URL stat is drained before shutdown returns")
	assert.Empty(t, agent.statChan)
}

// superviseWorker recovers a panicked worker body and runs it again, so the
// stream the panicked body was holding has to be closed on that path too:
// nothing else closes it, the collector kept it open, and the restarted body
// was free to open another on top of it. Each worker owns its stream through a
// defer for exactly this case.
func Test_workers_closeTheirStreamOnThePanicPath(t *testing.T) {
	t.Run("ping", func(t *testing.T) {
		agent := newTestAgent(defaultConfig())

		stream := grpcmock.NewMockAgent_PingSessionClient()
		stream.OnSend(mock.Anything).Run(func(mock.Arguments) { panic("ping send exploded") }).Return(nil)
		stream.On("CloseSend").Return(nil)

		client := grpcmock.NewMockAgentClient()
		client.OnPingSession(mock.Anything).Return(stream, nil)
		agent.agentGrpc = &agentGrpc{agentClient: client, agent: agent}

		assert.Panics(t, agent.sendPingWorker)
		stream.AssertNumberOfCalls(t, "CloseSend", 1)
	})

	t.Run("command", func(t *testing.T) {
		agent := newTestAgent(defaultConfig())

		// The command worker's stream outlives a whole loop of handler calls,
		// so its close sits in serveCommandStream - the scope the panic unwinds.
		stream := grpcmock.NewMockProfilerCommandService_HandleCommandV2Client()
		stream.OnRecv().Run(func(mock.Arguments) { panic("command handler exploded") }).Return(nil, nil)
		stream.On("CloseSend").Return(nil)

		client := grpcmock.NewMockProfilerCommandServiceClient()
		client.OnHandleCommandV2(mock.Anything).Return(stream, nil)
		agent.cmdGrpc = &cmdGrpc{cmdClient: client, agent: agent, atcStreams: atcStreams{agent: agent}}

		assert.Panics(t, func() { agent.serveCommandStream(0) })
		stream.AssertNumberOfCalls(t, "CloseSend", 1)
	})
}

// A collector that accepts the command stream and then immediately closes it
// leaves the channel READY, so the reconnect back-off is the only thing keeping
// this loop from opening streams continuously inside the host application --
// and a shutdown must not have to wait it out.
func Test_runCommandService_pacesReconnectsAndStopsPromptly(t *testing.T) {
	agent := newTestAgent(defaultConfig())

	stream := grpcmock.NewMockProfilerCommandService_HandleCommandV2Client()
	stream.OnRecv().Return((*pb.PCmdRequest)(nil), collectorDown())
	stream.On("CloseSend").Return(nil)

	var opened counter
	client := grpcmock.NewMockProfilerCommandServiceClient()
	client.OnHandleCommandV2(mock.Anything).Run(opened.count).Return(stream, nil)
	agent.cmdGrpc = &cmdGrpc{cmdClient: client, agent: agent, atcStreams: atcStreams{agent: agent}}

	agent.workerWg.Add(1)
	go agent.superviseWorker("command", agent.runCommandService)

	// The first attempt runs at once; the second waits out backOffSleep(0),
	// which is at least 2.1s. A hot loop would show up here as a large count.
	time.Sleep(300 * time.Millisecond)
	assert.EqualValues(t, 1, opened.get(), "a rejected stream must not be retried hot")

	stopped := make(chan struct{})
	go func() { agent.workerWg.Wait(); close(stopped) }()
	agent.signalShutdown()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown must interrupt the reconnect back-off instead of waiting it out")
	}
}

// Stream renewal is the normal path, not the outage path: a worker swaps an
// aged stream for a new one between two sends, so no send fails and both are
// delivered.
// streamMaxAgeForTest is the Collector.Grpc.StreamMaxAge the renewal
// scenarios below run with. It has to be long enough that the first send
// cannot outlive it: the worker opens the stream and then re-checks the age
// before every send, so a 1 ms age turned any scheduling hiccup before the
// first send into an extra renewal, and a count of three where these tests
// assert two. Jitter is +/-10% and the sleeps are twice this, so both sides
// keep a wide margin.
const streamMaxAgeForTest = 50

func Test_sendStatsWorker_renewsAgedStream(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgCollectorGrpcStreamMaxAge, streamMaxAgeForTest)
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
	time.Sleep(2 * streamMaxAgeForTest * time.Millisecond)
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

	// Each HandleCommandV2 hands its context over so that Recv, like the real
	// stream, returns once the deadline set on that context passes.
	contexts := make(chan context.Context, 16)
	stream := grpcmock.NewMockProfilerCommandService_HandleCommandV2Client()
	stream.OnRecv().Run(func(mock.Arguments) {
		ctx := <-contexts
		<-ctx.Done()
	}).Return((*pb.PCmdRequest)(nil), context.DeadlineExceeded)
	stream.On("CloseSend").Return(nil)

	var opened counter
	client := grpcmock.NewMockProfilerCommandServiceClient()
	client.OnHandleCommandV2(mock.Anything).Run(func(args mock.Arguments) {
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
// shard counter, so it is the worker that has to warn.
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
			name: "batch",
			setup: func(t *testing.T) (*agent, func()) {
				agent := newTestAgent(defaultConfig())
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

// A collector outage must not turn metadata drops into metadata inflow. The
// old retry waited for the channel inside the goroutine holding the permit, so
// four failed sends pinned every permit, the worker parked on the permit
// acquisition, metaChan overflowed, the head-drop released the dropped item's
// cache entry, and the next span registered the same item again. Now a failed
// send parks in the retry schedule and hands its permit back at once, so with
// the collector down for the whole test:
//
//	(a) the worker never stalls -- every item queued reaches the collector once,
//	(b) new metadata is not starved by the retries piling up, since the
//	    schedule has its own budget and metaChan drops nothing,
//	(c) only the items the full schedule evicted lose their cache entry; the
//	    parked ones stay registered.
func Test_sendMetaWorker_outageDoesNotAmplifyThroughCacheRelease(t *testing.T) {
	const retryCap = 8
	const items = 3 * retryCap

	agent := newTestAgent(defaultConfig())
	agent.metaRetry.capacity = retryCap

	var sent counter
	client := grpcmock.NewMockMetadataClient()
	client.OnRequestApiMetaData(mock.Anything, mock.Anything).Run(sent.count).Return((*pb.PResult)(nil), collectorDown())
	// The delay is never reached, so nothing is re-sent and nothing exhausts
	// its budget: the only cache releases left are the schedule's evictions.
	agent.agentGrpc = &agentGrpc{metaClient: client, agent: agent, retryDelay: time.Hour}

	agent.workerWg.Add(1)
	go agent.superviseWorker("meta", agent.sendMetaWorker)

	descriptor := func(i int) string { return fmt.Sprintf("test.api.%d", i) }
	late := descriptor(items)
	cached := func(i int) bool {
		_, ok := agent.apiCache.peek(apiCacheKey{descriptor(i), apiTypeInvocation})
		return ok
	}
	for i := 0; i < items; i++ {
		require.NotZero(t, agent.cacheSpanApi(descriptor(i), apiTypeInvocation))
	}

	// (a) and (b): three schedules' worth of failures, each sent exactly once.
	assert.Eventually(t, func() bool { return sent.get() == items }, 5*time.Second, time.Millisecond,
		"every queued item must be attempted while the collector is down, got %d", sent.get())
	assert.Eventually(t, func() bool { return len(agent.metaChan) == 0 }, time.Second, time.Millisecond)
	assert.Zero(t, agent.metaDrops.dropped.Load(), "the retries must not overflow metaChan")

	// A registration arriving mid-outage is still sent at once.
	require.NotZero(t, agent.cacheSpanApi(late, apiTypeInvocation))
	assert.Eventually(t, func() bool { return sent.get() == items+1 }, 5*time.Second, time.Millisecond,
		"new metadata must not wait behind the retries")

	// (c): the schedule holds retryCap items; every other one was evicted and
	// released, and nothing that is still parked has lost its entry.
	assert.Equal(t, retryCap, agent.metaRetry.length())
	assert.EqualValues(t, items+1-retryCap, agent.metaRetryDrops.dropped.Load())
	released := 0
	for i := 0; i <= items; i++ { // the late registration is items
		if !cached(i) {
			released++
		}
	}
	assert.Equal(t, items+1-retryCap, released, "only the evicted items release their cache entry")
	agent.metaRetry.mu.Lock()
	for _, parked := range agent.metaRetry.items {
		api := parked.md.(apiMeta)
		_, ok := agent.apiCache.peek(apiCacheKey{api.descriptor, api.apiType})
		assert.True(t, ok, "%s is parked for retry and must stay registered", api.descriptor)
	}
	agent.metaRetry.mu.Unlock()

	agent.signalShutdown()
	assert.True(t, waitTimeout(&agent.workerWg, time.Second), "the worker must not wait out the retry delay")
	assert.EqualValues(t, items+1, sent.get(), "no send after the stop signal")
}
