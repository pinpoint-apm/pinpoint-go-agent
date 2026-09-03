package pinpoint

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/spaolacci/murmur3"
)

func init() {
	initLogger()
	initConfig()
	initNoopAgent()
	initGoroutine()
	setGlobalAgent(NoopAgent())
}

type agent struct {
	appName     string
	appType     int32
	agentID     string
	agentName   string
	serviceName string
	objName     *objectName

	startTime   int64
	sequence    int64
	agentGrpc   *agentGrpc
	spanGrpc    *spanGrpc
	statGrpc    *statGrpc
	cmdGrpc     *cmdGrpc
	spanQueue   *spanQueue
	metaChan    chan interface{}
	urlStatChan chan *urlStat
	statChan    chan *pb.PStatMessage

	// One reporter per bounded queue: each counts the records that queue lost
	// to overflow and rate-limits its overflow warning. spanDrops counts only
	// the batches the sender skipped for want of a permit; the span queue's
	// head-drops live in its per-shard counters and are added at report time.
	urlStatDrops dropReporter
	metaDrops    dropReporter
	statDrops    dropReporter
	spanDrops    dropReporter

	errorCache  *metaCache[string, int32]
	errorIdGen  int32
	sqlCache    *metaCache[string, int32]
	sqlIdGen    int32
	sqlUidCache *metaCache[string, []byte]
	rawSqlCache *metaCache[string, normalizedSql]
	apiCache    *metaCache[apiCacheKey, int32]
	apiIdGen    int32

	// asyncIdGen numbers this agent's async chunks. Like the ids above it is
	// reported with the agent's own transaction ids, so it restarts per agent.
	asyncIdGen atomic.Int32

	// exceptionIdGen numbers this agent's exception chains. Chain ids are
	// scoped to the span they are reported with, so a new agent starts over
	// rather than continuing a previous agent's count.
	exceptionIdGen atomic.Int64

	// asyncApiId caches this agent's id for the "Goroutine Invocation" API.
	// Per-agent, not package-global: ids come from apiIdGen and are published
	// through this agent's metadata channel, so a new agent after Shutdown()
	// must mint and register its own. Accessed atomically; 0 means not cached
	// yet (cacheSpanApi returns 0 while the agent is disabled, so it retries).
	asyncApiId int32

	// realTimeActiveSpan tracks this agent's in-flight spans by goroutine id
	// for the real-time active thread views, gated by atcStreamCount so the
	// span path only pays for it while a viewer is attached. Per-agent: a
	// package map kept the entries of spans still in flight at shutdown for
	// the life of the process.
	realTimeActiveSpan sync.Map
	atcStreamCount     atomic.Int32

	// stats and urlStats hold this agent's statistics (see agentStats and
	// urlStats). Per-agent, not package-global: a restart used to re-prime or
	// swap the counters and the url snapshot while the previous agent's
	// abandoned workers and still-in-flight spans were reading them.
	stats    *agentStats
	urlStats *urlStats

	config    *Config
	connectWg sync.WaitGroup
	workerWg  sync.WaitGroup
	enable    atomic.Bool
	shutdown  atomic.Bool

	// stopCtx is cancelled when shutdown begins. The shutdown flag above is
	// only polled, so it cannot wake a goroutine already blocked in a wait;
	// the context can. NewAgent creates it before starting goroutines, while
	// stopOnce also supports agents built as struct literals in tests.
	stopOnce   sync.Once
	stopCtx    context.Context
	stopCancel context.CancelFunc

	// grpcMetaCtx caches the outgoing-metadata context (socketId <= 0), whose
	// headers are immutable for the agent's lifetime, so per-send callers reuse
	// it instead of rebuilding the metadata map on every request.
	grpcMetaOnce sync.Once
	grpcMetaCtx  context.Context
}

type apiMeta struct {
	id         int32
	descriptor string
	apiType    int
}

// apiCacheKey identifies a cached API id by its descriptor and type.
type apiCacheKey struct {
	descriptor string
	apiType    int
}

type stringMeta struct {
	id       int32
	funcName string
}

type sqlMeta struct {
	id  int32
	sql string
}

type sqlUidMeta struct {
	uid []byte
	sql string
}

type exceptionMeta struct {
	txId        TransactionId
	spanId      int64
	uriTemplate string
	exceptions  []*exception
}

type exception struct {
	exceptionId int64
	callstack   *errorWithCallStack
}

const (
	cacheSize        = 1024
	defaultQueueSize = 1024

	defaultSpanBatchSize                  = 50
	defaultSpanBatchFlushInterval         = 1000
	defaultSpanBatchCollectDeadline       = 500
	defaultSpanBatchMaxConcurrentRequests = 10

	// AgentInfo refresh, matching the C++ agent's Collector.AgentInfo defaults
	// except the interval: 0 keeps the refresh off, preserving the historical
	// Go behavior of sending AgentInfo only once at startup.
	defaultAgentInfoSendRetryInterval = 3000
	defaultAgentInfoMaxTryPerAttempt  = 3

	// shutdownTimeout bounds how long Shutdown waits for the worker goroutines
	// to drain their queues before abandoning them.
	shutdownTimeout = 3 * time.Second
	// connectGraceTimeout bounds how long Shutdown waits for an in-progress
	// agent registration, in case Shutdown was called too early.
	connectGraceTimeout = 1 * time.Second

	maxSqlSize = 64 * 1024
)

// globalAgent is an atomic.Value rather than a plain interface variable:
// plugins call GetAgent on every request while NewAgent and Shutdown swap the
// value, and an unsynchronized two-word interface write can be torn - a reader
// could pair one implementation's itab with another's data pointer.
// globalAgentLock serializes the writers, so two concurrent NewAgent calls
// cannot both pass the already-created check and leak the loser's agent.
var (
	globalAgent     atomic.Value // holds agentHolder
	globalAgentLock sync.Mutex
)

// agentHolder keeps the concrete type stored in globalAgent constant across
// the *agent and noop implementations; atomic.Value panics when it varies.
type agentHolder struct {
	agent Agent
}

// GetAgent returns a global Agent created by NewAgent.
func GetAgent() Agent {
	return globalAgent.Load().(agentHolder).agent
}

func setGlobalAgent(a Agent) {
	globalAgent.Store(agentHolder{a})
}

// NewAgent creates an Agent and spawns goroutines that manage spans and statistical data.
// The generated Agent is maintained globally and only one instance is retained.
// The provided config is generated by NewConfig and an error is returned if it is nil.
//
// example:
//
//	opts := []pinpoint.ConfigOption{
//	  pinpoint.WithAppName("GoTestApp"),
//	  pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
//	}
//	cfg, err := pinpoint.NewConfig(opts...)
//	agent, err := pinpoint.NewAgent(cfg)
func NewAgent(config *Config) (Agent, error) {
	globalAgentLock.Lock()
	defer globalAgentLock.Unlock()

	if a := GetAgent(); a != NoopAgent() {
		if config != nil && config != a.Config() {
			config.Close()
		}
		return a, errors.New("agent is already created")
	}
	if config == nil {
		return NoopAgent(), errors.New("configuration is missing")
	}
	// A reused Config arrives with its watcher stopped, so restart it and pick
	// up the dynamic options the file changed in the meantime. Only dynamic
	// ones: reloadConfig never applies the rest, so a watcher that had stayed
	// up would not have applied them either.
	if config.startConfigWatcher() {
		config.reloadConfig(config.configFileCfg)
	}

	logger.setup(config)
	if err := config.checkNameAndID(); err != nil {
		config.Close()
		return NoopAgent(), err
	}
	if !config.Bool(CfgEnable) {
		config.Close()
		return NoopAgent(), nil
	}

	Log("agent").Infof("new pinpoint agent")
	config.printConfigString()

	agent := &agent{
		appName:     config.objName.applicationName,
		appType:     int32(config.Int(CfgAppType)),
		agentID:     config.objName.agentID,
		agentName:   config.objName.agentName,
		serviceName: config.objName.serviceName,
		objName:     config.objName,
		startTime:   time.Now().UnixNano() / int64(time.Millisecond),
		spanQueue:   newSpanQueue(config.Int(CfgSpanQueueSize)),
		metaChan:    make(chan interface{}, config.Int(CfgSpanQueueSize)),
		urlStatChan: make(chan *urlStat, config.Int(CfgHttpUrlStatQueueSize)),
		statChan:    make(chan *pb.PStatMessage, config.Int(CfgSpanQueueSize)),
		config:      config,
		stats:       newAgentStats(),
		urlStats:    newUrlStats(config),
	}
	agent.stopSignal()

	agent.errorCache = newMetaCache[string, int32](cacheSize)
	agent.sqlCache = newMetaCache[string, int32](cacheSize)
	agent.sqlUidCache = newMetaCache[string, []byte](cacheSize)
	agent.rawSqlCache = newMetaCache[string, normalizedSql](cacheSize)
	agent.apiCache = newMetaCache[apiCacheKey, int32](cacheSize)

	config.logCallbackOnce.Do(func() {
		config.AddReloadCallback([]string{CfgLogLevel}, func() { logger.reloadLevel(config) })
		config.AddReloadCallback([]string{CfgLogOutput, CfgLogMaxSize}, func() { logger.reloadOutput(config) })
	})

	if !config.offGrpc {
		agent.connectWg.Add(1)
		go agent.connectGrpcServer()
	}
	setGlobalAgent(agent)
	return agent, nil
}

func (agent *agent) connectGrpcServer() {
	var err error
	defer agent.connectWg.Done()
	// A connect that fails for a reason other than Shutdown (bad address, TLS
	// setup) would otherwise leave a never-enabled agent as the global, so
	// GetAgent hands out a dead agent and NewAgent cannot retry. Release it,
	// identity-guarded as in Shutdown.
	defer func() {
		if agent.enable.Load() || agent.shutdown.Load() {
			return
		}
		Log("agent").Errorf("failed to connect to collector, agent disabled: %v", err)
		globalAgentLock.Lock()
		if GetAgent() == Agent(agent) {
			setGlobalAgent(NoopAgent())
		}
		globalAgentLock.Unlock()
	}()

	if agent.agentGrpc, err = newAgentGrpc(agent); err != nil {
		return
	}
	if !agent.agentGrpc.registerAgentWithRetry() {
		return
	}
	if agent.spanGrpc, err = newSpanGrpc(agent); err != nil {
		return
	}
	if agent.statGrpc, err = newStatGrpc(agent); err != nil {
		return
	}
	if agent.cmdGrpc, err = newCommandGrpc(agent); err != nil {
		return
	}

	agent.enable.Store(true)
	agent.workerWg.Add(8)
	go agent.superviseWorker("ping", agent.sendPingWorker)
	if agent.config.Bool(CfgSpanBatchEnable) {
		go agent.superviseWorker("span batch", agent.sendSpanBatchWorker)
	} else {
		go agent.superviseWorker("span", agent.sendSpanWorker)
	}
	go agent.superviseWorker("command", agent.runCommandService)
	go agent.superviseWorker("meta", agent.sendMetaWorker)
	go agent.superviseWorker("collect agent stat", agent.collectAgentStatWorker)
	go agent.superviseWorker("collect uri stat", agent.collectUrlStatWorker)
	go agent.superviseWorker("send uri stat", agent.sendUrlStatWorker)
	go agent.superviseWorker("send stats", agent.sendStatsWorker)

	if interval := time.Duration(agent.config.Int(CfgCollectorAgentInfoRefreshInterval)) * time.Millisecond; interval > 0 {
		agent.workerWg.Add(1)
		go agent.superviseWorker("agent info refresh", func() { agent.refreshAgentInfoWorker(interval) })
	}
}

// dropReportInterval bounds how often a saturated queue may warn, matching
// the C++ QueueDropReporter::kDefaultReportInterval. A variable so tests can
// shorten it.
var dropReportInterval = 60 * time.Second

// workerRestartDelay paces the restart of a worker whose body panicked, so a
// deterministic bug cannot spin the supervisor hot. A variable so tests can
// shorten it.
var workerRestartDelay = 1 * time.Second

// superviseWorker runs body as one of the agent's worker goroutines and owns
// its workerWg slot, releasing it once on the final exit. A panic in body is
// recovered - the agent must never take the host process down - and the
// worker is restarted after workerRestartDelay, unless the agent is stopping,
// in which case the panic ends the worker like a normal return would. Mirrors
// the C++ agent's superviseWorker.
func (agent *agent) superviseWorker(name string, body func()) {
	defer agent.workerWg.Done()

	stop := agent.stopSignal().Done()
	for {
		if recoverPanic(name, body) {
			return
		}
		if !agent.enable.Load() {
			return
		}
		timer := time.NewTimer(workerRestartDelay)
		select {
		case <-stop:
			timer.Stop()
			Log("agent").Infof("%s goroutine stopping, not restarted", name)
			return
		case <-timer.C:
		}
		if !agent.enable.Load() {
			return
		}
		Log("agent").Warnf("restart %s goroutine", name)
	}
}

// recoverPanic calls body and reports whether it returned normally. A panic
// is recovered and logged with its stack instead of unwinding the goroutine.
func recoverPanic(name string, body func()) (completed bool) {
	defer func() {
		if e := recover(); e != nil {
			Log("agent").Errorf("%s goroutine panic: %v\n%s", name, e, debug.Stack())
		}
	}()
	body()
	return true
}

// refreshAgentInfoWorker re-sends AgentInfo every refresh interval, mirroring
// the C++ agent's AgentInfo scheduler. Best-effort: a failed cycle waits for
// the next interval and never affects the agent's enabled state.
func (agent *agent) refreshAgentInfoWorker(interval time.Duration) {
	Log("agent").Infof("start agent info refresh goroutine")

	retryInterval := time.Duration(agent.config.Int(CfgCollectorAgentInfoSendRetryInterval)) * time.Millisecond
	maxTry := agent.config.Int(CfgCollectorAgentInfoMaxTryPerAttempt)

	timer := time.NewTimer(interval)
	defer timer.Stop()
	stop := agent.stopSignal().Done()

	for {
		select {
		case <-stop:
			Log("agent").Infof("end agent info refresh goroutine")
			return
		case <-timer.C:
		}
		agent.agentGrpc.refreshAgentInfo(maxTry, retryInterval)
		timer.Reset(interval)
	}
}

// stopSignal returns a context cancelled when shutdown begins. Reconnect waits
// derive their deadline from it, so a shutdown aborts them instead of holding
// the agent for a whole back-off interval.
func (agent *agent) stopSignal() context.Context {
	agent.stopOnce.Do(func() {
		agent.stopCtx, agent.stopCancel = context.WithCancel(context.Background())
	})
	return agent.stopCtx
}

// signalShutdown marks the agent as shutting down and unblocks the waits that
// are already in progress.
func (agent *agent) signalShutdown() {
	agent.shutdown.Store(true)
	agent.stopSignal() // ensure the context exists before cancelling it
	agent.stopCancel()

	// Hand the config file watcher back. Guarded by the same identity check as
	// the global release below, because a Config outlives the agent it was
	// passed to: once this agent is no longer the current one, a later NewAgent
	// may already have restarted the watcher on that same Config, and a stale
	// second Shutdown must not stop it. An agent that was never published -
	// a struct literal in a test - owns nothing here either.
	if agent.config != nil && GetAgent() == Agent(agent) {
		agent.config.Close()
	}
}

// waitTimeout waits for wg and reports whether it completed within timeout.
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (agent *agent) Shutdown() {
	// Give an in-progress registration a moment to finish, in case shutdown was
	// called too early. A registered agent has already released connectWg, so
	// the normal shutdown path pays nothing here.
	waitTimeout(&agent.connectWg, connectGraceTimeout)

	agent.signalShutdown()
	Log("agent").Infof("shutdown pinpoint agent")

	// wait for the grpc connection to be completed
	agent.connectWg.Wait()

	// Close whatever connections connectGrpcServer managed to create, on every
	// path: grpc dials lazily, so an agent whose registration never finished
	// (collector down at boot) still holds a live agent connection, and the
	// never-enabled early return below would leak it once per failed
	// NewAgent/Shutdown retry cycle. Deferred so the enabled path keeps its
	// order - workers drain first, connections close last. The closes are
	// nil-checked and idempotent, so a second Shutdown is harmless. Reading
	// the fields is safe: connectGrpcServer wrote them before connectWg.Done.
	defer func() {
		if agent.agentGrpc != nil {
			agent.agentGrpc.close()
		}
		if agent.spanGrpc != nil {
			agent.spanGrpc.close()
		}
		if agent.statGrpc != nil {
			agent.statGrpc.close()
		}
		if agent.cmdGrpc != nil {
			agent.cmdGrpc.close()
		}
	}()

	// Release the global on every path, before the enable guard below. An agent
	// whose registration never finished was never enabled, and leaving
	// globalAgent pointing at it would keep GetAgent returning a dead agent and
	// make every later NewAgent fail with "agent is already created", so a
	// process could never retry after a failed startup. Guarded by identity so
	// a second Shutdown of an old agent cannot unseat a newer one.
	globalAgentLock.Lock()
	if GetAgent() == Agent(agent) {
		setGlobalAgent(NoopAgent())
	}
	globalAgentLock.Unlock()

	// CompareAndSwap, not Load-then-Store: two concurrent Shutdown calls could
	// both pass a plain check and both reach the teardown below, panicking on
	// the second close of spanQueue's already-closed done channel. Only the
	// swap winner proceeds. A never-enabled agent stops here: it has no
	// workers, queues or streams to tear down.
	if !agent.enable.CompareAndSwap(true, false) {
		return
	}

	// spanQueue.close() signals; it does not close the channel producers use.
	// The three chans below get the same treatment - deliberately never closed.
	// Their producers (request-path goroutines for url stat and meta, ticker
	// workers for stat) only check enable before sending, which is check-then-
	// act against a close and panics with "send on closed channel" when the
	// send lands after it. signalShutdown() above already cancelled stopCtx,
	// which is what stops the consumers; whatever is still queued is dropped
	// with the channel itself.
	agent.spanQueue.close()

	//To terminate the listening state of the command stream,
	//close the command grpc channel first
	if agent.cmdGrpc != nil {
		agent.cmdGrpc.close()
	}

	// Bound the drain: a collector outage must not keep the process alive.
	// Abandoned workers are unblocked by the connection close below.
	if !waitTimeout(&agent.workerWg, shutdownTimeout) {
		Log("agent").Warnf("shutdown timeout(%v) exceeded, abandon in-flight workers", shutdownTimeout)
	}
}

func (agent *agent) NewSpanTracer(operation string, rpcName string) Tracer {
	var tracer Tracer

	if agent.enable.Load() {
		reader := &noopDistributedTracingContextReader{}
		tracer = agent.NewSpanTracerWithReader(operation, rpcName, reader)
	} else {
		tracer = NoopTracer()
	}
	return tracer
}

func (agent *agent) NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	if !agent.enable.Load() || reader == nil {
		return NoopTracer()
	}

	sampled := reader.Get(HeaderSampled)
	if sampled == "s0" {
		agent.stats.incrUnSampleCont()
		return newUnSampledSpan(agent, rpcName)
	}

	sampler := agent.config.load().sampler
	tid := reader.Get(HeaderTraceId)
	if tid == "" {
		return agent.samplingSpan(func() bool { return sampler.isNewSampled(agent.stats) }, operation, rpcName, reader)
	} else {
		return agent.samplingSpan(func() bool { return sampler.isContinueSampled(agent.stats) }, operation, rpcName, reader)
	}
}

func (agent *agent) samplingSpan(samplingFunc func() bool, operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	if samplingFunc() {
		tracer := newSampledSpan(agent, operation, rpcName)
		tracer.Extract(reader)
		return tracer
	} else {
		return newUnSampledSpan(agent, rpcName)
	}
}

func (agent *agent) generateTransactionId() TransactionId {
	// Use the value returned by AddInt64: reading agent.sequence separately
	// races with concurrent increments and could hand two transactions the
	// same sequence (or a torn read on 32-bit).
	seq := atomic.AddInt64(&agent.sequence, 1)
	return TransactionId{agent.agentID, agent.startTime, seq}
}

func (agent *agent) Enable() bool {
	return agent.enable.Load()
}

func (agent *agent) Config() *Config {
	return agent.config
}

func (agent *agent) sendPingWorker() {
	Log("agent").Infof("start ping goroutine")

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	stop := agent.stopSignal().Done()
	stream := agent.agentGrpc.newPingStreamWithRetry()

	for agent.enable.Load() {
		stream = renewIfExpired(stream, agent.agentGrpc.newPingStreamWithRetry, "ping")
		err := stream.sendPing()
		if err != nil {
			if err != io.EOF {
				Log("agent").Errorf("send ping - %v", err)
			}

			stream.close()
			stream = agent.agentGrpc.newPingStreamWithRetry()
		}

		select {
		case <-stop:
			Log("agent").Infof("end ping goroutine")
			stream.close()
			return
		case t := <-ticker.C:
			if IsDebugLogLevelEnabled() {
				Log("agent").Debugf("ping at %v", t)
			}
		}
	}
}

func (agent *agent) sendSpanWorker() {
	Log("agent").Infof("start span goroutine")

	stream := agent.spanGrpc.newSpanStreamWithRetry()
	for {
		// Break on a drained queue only, not on the disabled flag: shutdown
		// clears enable before it closes the queue, so also breaking here
		// dropped everything still queued - the very spans the shutdown drain
		// window exists to flush. Matches sendSpanBatchWorker's best-effort
		// flush; if the stream is already gone, each send fails fast and the
		// drain stays bounded by the queue length.
		chunk, ok := agent.spanQueue.dequeue()
		if !ok {
			break
		}
		agent.reportSpanDrops()

		stream = renewIfExpired(stream, agent.spanGrpc.newSpanStreamWithRetry, "span")
		err := stream.sendSpan(chunk)
		if err != nil {
			if err != io.EOF {
				Log("agent").Errorf("send span - %v", err)
			}

			stream.close()
			stream = agent.spanGrpc.newSpanStreamWithRetry()
			if stream.stream == nil {
				// The reconnect gave up, which newStreamWithRetry only does
				// once the agent is disabled - the drain this loop is running.
				// Every later send would fail with "span stream is nil", so
				// carrying on only logs one error per chunk still queued
				// without delivering any of them.
				break
			}

			// Nothing queued is discarded here. A reconnect used to arm a
			// filter that skipped every span whose startTime predated the
			// failure by more than a second, so the outage backlog could not
			// occupy the fresh stream ahead of live traffic. That backlog was
			// the real problem when the queue rejected the incoming span and
			// evicted a queued one on overflow: live spans were lost while
			// stale ones waited to be sent. spanQueue's head-drop inverted
			// that - the incoming span is always accepted and the oldest
			// queued chunk is what goes - so the queue now holds the newest
			// `capacity` chunks and keeps discarding the backlog by itself
			// while the drain proceeds. Skipping on top of that dropped spans
			// twice: startTime is the span's start, not its enqueue time, and
			// non-final chunks are cut while the span is still live, so any
			// request slower than the one-second window lost its chunks even
			// though they were enqueued after the failure - the slow traces
			// an outage most needs. It also read the queue as FIFO, latching
			// off at the first recent chunk; spanQueue sweeps 32 shards in
			// unspecified order at the default capacity, so it released early
			// and passed an arbitrary share of the backlog anyway. Java's
			// SpanGrpcDataSender and the C++ GrpcSpan have no such policy,
			// and neither does sendSpanBatchWorker; a failed send now costs
			// one reconnect and no spans on every span path.
		}
	}

	stream.close()
	Log("agent").Infof("end span goroutine")
}

func (agent *agent) sendSpanBatchWorker() {
	Log("agent").Infof("start span batch goroutine")

	// Drain span chunks into unary SendSpanBatch requests.
	// The first chunk starts a batch, collectSpanBatch opportunistically gathers more chunks,
	// and sendSpanBatchAsync hands the batch to a bounded async sender.
	for {
		chunk, ok := agent.spanQueue.dequeue()
		if !ok {
			break
		}
		// No timer of its own: collectSpanBatch's collect deadline already
		// bounds how long a cycle can take, so the poll stays regular.
		agent.reportSpanDrops()

		batch, closed := agent.spanGrpc.collectSpanBatch(chunk, agent.spanQueue)
		agent.spanGrpc.sendSpanBatchAsync(batch)
		if closed {
			break
		}
	}

	// The span queue is closed during shutdown; wait for already accepted async batches
	// before the worker exits so queued spans get the same best-effort flush.
	agent.spanGrpc.awaitInFlightSpanBatch()
	Log("agent").Infof("end span batch goroutine")
}

// reportSpanDrops warns about spans lost to a saturated span queue or skipped
// by the batch sender. Called by whichever span worker is running, once per
// cycle: producers only bump their shard's counter, so the clock read and the
// logging land on the consumer.
func (agent *agent) reportSpanDrops() {
	total := agent.spanQueue.dropCount() + agent.spanDrops.dropped.Load()
	agent.spanDrops.reportTotal(total, "span", agent.spanQueue.capacity)
}

func (agent *agent) enqueueSpan(span *spanChunk) bool {
	if !agent.enable.Load() {
		return false
	}
	return agent.spanQueue.enqueue(span)
}

func (agent *agent) sendMetaWorker() {
	Log("agent").Infof("start meta goroutine")

	// Metadata sends are pipelined: registration has no ordering requirement
	// (the collector accepts duplicates and metadata arriving after its
	// spans), while serial sends cap throughput at one item per round trip,
	// which falls behind when high error rates produce exception metadata
	// per span.
	permit := make(chan struct{}, metaMaxConcurrentRequests)
	var inFlight sync.WaitGroup
	// Deferred so both exits -- the stop signal and a disabled agent -- wait
	// for the sends already accepted, giving them the same best-effort flush.
	defer func() {
		inFlight.Wait()
		Log("agent").Infof("end meta goroutine")
	}()

	stop := agent.stopSignal().Done()

	for agent.enable.Load() {
		var md interface{}
		select {
		case <-stop:
			return
		case md = <-agent.metaChan:
		}

		// Reported here rather than from the producers: enqueueMeta runs on
		// the request path and only bumps a counter.
		agent.metaDrops.report("meta", cap(agent.metaChan))

		// The permit acquisition obeys stop too: with every permit held by a
		// slow send, a plain send here parks the worker where the stop signal
		// cannot reach it, and it would dispatch one more send after shutdown
		// began. The md just pulled is dropped, like the rest of the queue.
		select {
		case <-stop:
			return
		case permit <- struct{}{}:
		}
		inFlight.Add(1)
		go func(md interface{}) {
			defer inFlight.Done()
			defer func() { <-permit }()

			recoverPanic("meta send", func() {
				if !agent.sendMetadata(md) {
					agent.deleteMetaCache(md)
				}
			})
		}(md)
	}
}

func (agent *agent) sendMetadata(md interface{}) bool {
	switch md.(type) {
	case apiMeta:
		api := md.(apiMeta)
		return agent.agentGrpc.sendApiMetadataWithRetry(api.id, api.descriptor, -1, api.apiType)
	case stringMeta:
		str := md.(stringMeta)
		return agent.agentGrpc.sendStringMetadataWithRetry(str.id, str.funcName)
	case sqlMeta:
		sql := md.(sqlMeta)
		return agent.agentGrpc.sendSqlMetadataWithRetry(sql.id, sql.sql)
	case sqlUidMeta:
		sql := md.(sqlUidMeta)
		return agent.agentGrpc.sendSqlUidMetadataWithRetry(sql.uid, sql.sql)
	case exceptionMeta:
		em := md.(exceptionMeta)
		return agent.agentGrpc.sendExceptionMetadataWithRetry(&em)
	}
	return false
}

func (agent *agent) deleteMetaCache(md interface{}) {
	switch md.(type) {
	case apiMeta:
		api := md.(apiMeta)
		agent.apiCache.remove(apiCacheKey{api.descriptor, api.apiType})
		break
	case stringMeta:
		agent.errorCache.remove(md.(stringMeta).funcName)
		break
	case sqlMeta:
		agent.sqlCache.remove(md.(sqlMeta).sql)
		break
	case sqlUidMeta:
		agent.sqlUidCache.remove(md.(sqlUidMeta).sql)
		break
	case exceptionMeta:
		break
	}
}

// enqueueMeta queues md for the metadata sender, dropping the cache entry that
// published its id when the queue cannot take it. The id was already handed to
// the spans referencing it, so leaving the entry cached would keep every later
// span pointing at an id the collector never received; dropping it makes the
// next span register the metadata again. Same policy the send-failure path
// uses.
func (agent *agent) enqueueMeta(md interface{}) {
	if !agent.tryEnqueueMeta(md) {
		agent.deleteMetaCache(md)
	}
}

// tryEnqueueMeta queues md, head-dropping the oldest item when the queue is
// full: the slot the eviction frees is handed to md rather than left for the
// next producer, so an overflow costs exactly one item - and one cache entry -
// instead of two. Head-drop rather than the C++ agent's drop-the-newest
// because it is what this agent's span queue already does (a full shard
// overwrites its oldest cell), and metadata the collector has not seen yet is
// worth more than metadata whose spans may already have gone out.
func (agent *agent) tryEnqueueMeta(md interface{}) bool {
	if !agent.enable.Load() {
		return false
	}

	select {
	case agent.metaChan <- md:
		return true
	default:
		break
	}

	select {
	case dropped := <-agent.metaChan:
		agent.deleteMetaCache(dropped)
		agent.metaDrops.record(1)
	default:
		// The consumer drained one meanwhile, so nothing had to be evicted.
	}

	select {
	case agent.metaChan <- md:
		return true
	default:
	}

	// Another producer took the freed slot: md is the item lost this time and
	// the caller drops its cache entry.
	agent.metaDrops.record(1)
	return false
}

func (agent *agent) cacheError(errorName string) int32 {
	if !agent.enable.Load() {
		return 0
	}

	if v, ok := agent.errorCache.peek(errorName); ok {
		return v
	}

	id := atomic.AddInt32(&agent.errorIdGen, 1)
	if v, ok := agent.errorCache.peekOrAdd(errorName, id); ok {
		return v
	}

	md := stringMeta{
		id:       id,
		funcName: errorName,
	}
	agent.enqueueMeta(md)

	// Debug, not info: a miss runs on the request goroutine, and a workload
	// whose cardinality exceeds the cache would otherwise log per request.
	if IsDebugLogLevelEnabled() {
		Log("agent").Debugf("cache error id: %d, %s", id, errorName)
	}
	return id
}

// validUTF8 replaces invalid UTF-8 sequences in s with the replacement rune.
// Plugins feed network- and user-origin bytes into string fields (percent-decoded
// URL paths, query DSLs, binary row keys, driver error strings), and protobuf
// rejects invalid UTF-8 string fields at marshal time: one bad string would fail
// the whole span, stat, or metadata message carrying it - and a failed span
// stream Send cancels the stream. Applied at the protobuf conversion boundary,
// off the application hot path; returns s unchanged (no copy) when valid.
func validUTF8(s string) string {
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// abbreviateString truncates str to at most length bytes plus a "...(length)"
// marker, cutting at a rune boundary: protobuf rejects invalid UTF-8 string
// fields at marshal time, so a mid-rune cut would fail the whole span or
// metadata send carrying it.
func abbreviateString(str string, length int) string {
	if len(str) <= length {
		return str
	}
	cut := length
	for cut > 0 && !utf8.RuneStart(str[cut]) {
		cut--
	}
	return str[:cut] + "...(" + fmt.Sprint(length) + ")"
}

func (agent *agent) cacheSql(sql string) int32 {
	if !agent.enable.Load() {
		return 0
	}

	aSql := abbreviateString(sql, maxSqlSize)
	if v, ok := agent.sqlCache.peek(aSql); ok {
		return v
	}

	id := atomic.AddInt32(&agent.sqlIdGen, 1)
	if v, ok := agent.sqlCache.peekOrAdd(aSql, id); ok {
		return v
	}

	md := sqlMeta{
		id:  id,
		sql: aSql,
	}
	agent.enqueueMeta(md)

	if IsDebugLogLevelEnabled() {
		Log("agent").Debugf("cache sql id: %d, %s", id, aSql)
	}
	return id
}

func (agent *agent) cacheSqlUid(sql string) []byte {
	if !agent.enable.Load() {
		return nil
	}

	aSql := abbreviateString(sql, maxSqlSize)
	if v, ok := agent.sqlUidCache.peek(aSql); ok {
		return v
	}

	uid := sqlUid(aSql)
	if v, ok := agent.sqlUidCache.peekOrAdd(aSql, uid); ok {
		return v
	}

	md := sqlUidMeta{
		uid: uid,
		sql: aSql,
	}
	agent.enqueueMeta(md)

	if IsDebugLogLevelEnabled() {
		Log("agent").Debugf("cache sql uid: %#v, %s", uid, aSql)
	}
	return uid
}

// sqlUid hashes a normalized SQL with murmur3 x64 128 (seed 0) and lays out
// h1 then h2 little-endian, byte-for-byte what the Java agent's Guava
// Hashing.murmur3_128().hashBytes(sql.getBytes(UTF_8)).asBytes() and the C++
// agent's MurmurHash3_x64_128 produce. spaolacci/murmur3's Sum() writes the
// two words big-endian, which yielded a different UID for the same SQL.
func sqlUid(sql string) []byte {
	h1, h2 := murmur3.Sum128([]byte(sql))
	uid := make([]byte, 16)
	binary.LittleEndian.PutUint64(uid[0:8], h1)
	binary.LittleEndian.PutUint64(uid[8:16], h2)
	return uid
}

// normalizedSql is the immutable result of normalizing one raw SQL text.
// Both fields are strings, so a cached value can be handed to any number of
// callers without copying.
type normalizedSql struct {
	sql   string
	param string
}

// normalizeSql returns the normalized SQL and extracted parameters for sql,
// memoized by the raw SQL text so repeated statements skip re-parsing. It uses
// the same sharded metaCache as the id caches above, so a hot statement is a
// lock-free lookup and stays resident under aged promotion.
func (agent *agent) normalizeSql(sql string) (string, string) {
	if len(sql) > maxSqlSize {
		return newSqlNormalizer(sql).run()
	}
	if n, ok := agent.rawSqlCache.peek(sql); ok {
		return n.sql, n.param
	}
	nsql, param := newSqlNormalizer(sql).run()
	agent.rawSqlCache.peekOrAdd(sql, normalizedSql{sql: nsql, param: param})
	return nsql, param
}

func (agent *agent) cacheSpanApi(descriptor string, apiType int) int32 {
	if !agent.enable.Load() {
		return 0
	}

	key := apiCacheKey{descriptor, apiType}

	if v, ok := agent.apiCache.peek(key); ok {
		return v
	}

	id := atomic.AddInt32(&agent.apiIdGen, 1)
	if v, ok := agent.apiCache.peekOrAdd(key, id); ok {
		return v
	}

	md := apiMeta{
		id:         id,
		descriptor: descriptor,
		apiType:    apiType,
	}
	agent.enqueueMeta(md)

	if IsDebugLogLevelEnabled() {
		Log("agent").Debugf("cache api id: %d, %s_%d", id, descriptor, apiType)
	}
	return id
}

func (agent *agent) enqueueExceptionMeta(span *span) {
	if !agent.enable.Load() || !span.cfg.errorTraceCallStack {
		return
	}

	md := exceptionMeta{
		txId:       span.txId,
		spanId:     span.spanId,
		exceptions: span.errorChains,
	}
	if span.urlStat != nil {
		md.uriTemplate = span.urlStat.Url
	} else {
		md.uriTemplate = "NULL"
	}

	agent.enqueueMeta(md)
	if IsDebugLogLevelEnabled() {
		Log("agent").Debugf("enqueue exception meta: %v", md)
	}
}

func (agent *agent) enqueueUrlStat(stat *urlStat) bool {
	if !agent.enable.Load() {
		return false
	}

	select {
	case agent.urlStatChan <- stat:
		return true
	default:
		break
	}

	// The queue is full: stat is rejected, and the oldest queued record is
	// evicted on top of it to leave room for the next enqueue (unless the
	// consumer already drained one meanwhile). Both are records the snapshot
	// will never see, so both are counted.
	dropped := int64(1)
	select {
	case <-agent.urlStatChan:
		dropped++
	default:
	}
	agent.urlStatDrops.record(dropped)
	agent.urlStatDrops.report("url stat", cap(agent.urlStatChan))
	return false
}

// dropReporter counts records lost to a full queue and rate-limits the
// overflow warning, mirroring the C++ agent's QueueDropReporter.
type dropReporter struct {
	// dropped is the running total of lost records, reported the total the
	// last warning carried, and reportAt the unix nano before which the next
	// warning stays silent; it starts at zero so the first drop reports.
	dropped  atomic.Int64
	reported atomic.Int64
	reportAt atomic.Int64
}

// record counts n drops and nothing else - no clock read, no logging - so it
// is cheap enough for a producer hot path. The warning is left to report,
// which a consumer calls.
func (r *dropReporter) record(n int64) {
	r.dropped.Add(n)
}

// report logs the running total at most once per dropReportInterval, and only
// when drops have accumulated since the last warning. WARN so the data loss is
// visible at the default log level, rate-limited so a saturated queue cannot
// log once per dropped record.
func (r *dropReporter) report(queue string, queueSize int) {
	r.reportTotal(r.dropped.Load(), queue, queueSize)
}

// reportTotal is report for a queue that keeps its own drop counter: total is
// that queue's running total, so the reporter contributes only the rate limit.
func (r *dropReporter) reportTotal(total int64, queue string, queueSize int) {
	if total == r.reported.Load() {
		return
	}

	now := time.Now().UnixNano()
	next := r.reportAt.Load()
	if now < next || !r.reportAt.CompareAndSwap(next, now+int64(dropReportInterval)) {
		return
	}
	r.reported.Store(total)
	Log("agent").Warnf(
		"%s queue overflow: %d dropped in total (oldest overwritten, max queue size %d)",
		queue, total, queueSize)
}

func (agent *agent) collectUrlStatWorker() {
	Log("agent").Infof("start collect uri stat goroutine")

	stop := agent.stopSignal().Done()

	for agent.enable.Load() {
		select {
		case <-stop:
			Log("agent").Infof("end collect uri stat goroutine")
			return
		case uri := <-agent.urlStatChan:
			agent.urlStats.add(uri)
		}
	}

	Log("agent").Infof("end collect uri stat goroutine")
}

func (agent *agent) sendUrlStatWorker() {
	Log("agent").Infof("start send uri stat goroutine")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	stop := agent.stopSignal().Done()

	for agent.enable.Load() {
		select {
		case <-stop:
			Log("agent").Infof("end send uri stat goroutine")
			return
		case <-ticker.C:
			if agent.config.load().collectUrlStat {
				snapshot := agent.urlStats.takeSnapshot()
				agent.enqueueStat(makePAgentUriStat(snapshot))
			}
		}
	}
}

func (agent *agent) enqueueStat(stat *pb.PStatMessage) bool {
	select {
	case agent.statChan <- stat:
		return true
	default:
		break
	}

	// The queue is full: stat is rejected, and the oldest queued record is
	// evicted on top of it to leave room for the next enqueue. Both are
	// records the collector will never see, so both are counted.
	dropped := int64(1)
	select {
	case <-agent.statChan:
		dropped++
	default:
	}
	agent.statDrops.record(dropped)
	return false
}

func (agent *agent) sendStatsWorker() {
	Log("agent").Infof("start send stats goroutine")

	stream := agent.statGrpc.newStatStreamWithRetry()
	defer func() { stream.close() }()

	stop := agent.stopSignal().Done()

	for agent.enable.Load() {
		var stats *pb.PStatMessage
		select {
		case <-stop:
			Log("agent").Infof("end send stats goroutine")
			return
		case stats = <-agent.statChan:
		}
		agent.statDrops.report("stat", cap(agent.statChan))

		stream = renewIfExpired(stream, agent.statGrpc.newStatStreamWithRetry, "stat")
		err := stream.sendStats(stats)
		if err != nil {
			if err != io.EOF {
				Log("stats").Errorf("send stats - %v", err)
			}

			stream.close()
			stream = agent.statGrpc.newStatStreamWithRetry()
		}
	}

	Log("agent").Infof("end send stats goroutine")
}

func NewTestAgent(config *Config, t *testing.T) (Agent, error) {
	config.offGrpc = true
	logger.setup(config)

	if config.objName == nil {
		if err := config.checkNameAndID(); err != nil {
			// Tests may omit required identity fields; fall back to a default
			// v3 identity so the header builder has a non-nil object name.
			config.objName = &objectName{
				version:         nameV3,
				agentID:         config.String(CfgAgentID),
				agentName:       config.String(CfgAgentName),
				applicationName: config.String(CfgAppName),
			}
		}
	}

	agent := &agent{
		appName:     config.objName.applicationName,
		appType:     int32(config.Int(CfgAppType)),
		agentID:     config.objName.agentID,
		agentName:   config.objName.agentName,
		serviceName: config.objName.serviceName,
		objName:     config.objName,
		startTime:   time.Now().UnixNano() / int64(time.Millisecond),
		spanQueue:   newSpanQueue(config.Int(CfgSpanQueueSize)),
		metaChan:    make(chan interface{}, config.Int(CfgSpanQueueSize)),
		urlStatChan: make(chan *urlStat, config.Int(CfgHttpUrlStatQueueSize)),
		statChan:    make(chan *pb.PStatMessage, config.Int(CfgSpanQueueSize)),
		config:      config,
		stats:       newAgentStats(),
		urlStats:    newUrlStats(config),
	}
	agent.errorCache = newMetaCache[string, int32](cacheSize)
	agent.sqlCache = newMetaCache[string, int32](cacheSize)
	agent.sqlUidCache = newMetaCache[string, []byte](cacheSize)
	agent.rawSqlCache = newMetaCache[string, normalizedSql](cacheSize)
	agent.apiCache = newMetaCache[apiCacheKey, int32](cacheSize)

	// offGrpc keeps connectGrpcServer - and every worker it starts - from
	// running, so no caller ever reaches the clients. A bare struct is enough
	// to keep the field non-nil and keeps the canned mocks out of the shipped
	// library.
	agent.agentGrpc = &agentGrpc{agent: agent}

	setGlobalAgent(agent)
	agent.enable.Store(true)

	return agent, nil
}
