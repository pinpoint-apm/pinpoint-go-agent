package pinpoint

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
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

	// metaRetry holds metadata sends waiting out a retry delay, and rejected
	// items waiting out their cache release, budgeted apart from metaChan.
	metaRetry      metaRetryQueue
	metaRetryDrops dropReporter

	errorCache  *metaCache[string, int32]
	errorIdGen  idGen
	sqlCache    *metaCache[string, int32]
	sqlIdGen    idGen
	sqlUidCache *metaCache[string, []byte]
	rawSqlCache *metaCache[string, normalizedSql]
	apiCache    *metaCache[apiCacheKey, int32]
	apiIdGen    idGen

	// sqlCacheLengthLimit is SQL.CacheLengthLimit read once in NewAgent, like
	// SQL.CacheSize: the key is fixed, so a reload can neither leave over-limit
	// entries in the caches nor turn a cached statement into one re-sent per use.
	sqlCacheLengthLimit int

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

	// enable is the lifecycle phase (see lifecycle.go): registering, running,
	// stopping, stopped or failed, in one atomic value where two bools used to
	// encode the same phases by combination. It moves only through
	// transitionTo, and is read only through the named predicates. The field
	// keeps its old name: tests pin it, and its Load still answers "may the
	// request path record", which is what the enable bool always meant.
	enable lifecycle

	// workerStates holds one running flag per worker startWorkers started,
	// so a Shutdown that overruns its deadline can name the workers still in
	// flight - a WaitGroup cannot say even how many remain. Written once by
	// startWorkers before any worker goroutine exists; the flags are atomic.
	workerStates []*workerState

	// shutdownOnce serializes the teardown. Without it a concurrent second
	// Shutdown returned at the phase check below and ran its deferred
	// connection close while the first call was still draining spans.
	shutdownOnce sync.Once

	// stopCtx is cancelled when shutdown begins. The phase above is only
	// polled, so it cannot wake a goroutine already blocked in a wait;
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

// sqlMeta and sqlUidMeta carry both the text to publish and the key that
// cached the id: sql is abbreviated to maxSqlSize for the collector, key is the
// untruncated statement the cache is keyed on. They differ for any statement
// past the cap, and deleteMetaCache needs the key - dropping the wrong entry
// would leave every later span pointing at an id the collector never received.
type sqlMeta struct {
	id  int32
	sql string
	key string
}

// cached records whether the statement is in the UID cache. It cannot be
// inferred from key: key is left empty for a bypassed statement to keep the
// untruncated text off the queue, but a statement whose normalization is empty
// ("/* hint */" under Sql.RemoveComments) is cached under an empty key too -
// sqlCacheable admits it (agent.go:1080) and the normalizer returns it as it
// stands (sql_util.go:39). Reading the empty key as "bypassed" left that entry
// cached after a failed send, pointing every later span at a UID the collector
// never received.
type sqlUidMeta struct {
	uid    []byte
	sql    string
	key    string
	cached bool
}

type exceptionMeta struct {
	txId        TransactionId
	spanId      int64
	uriTemplate string
	exceptions  []*exception
}

type exception struct {
	exceptionId int64
	depth       int32  // 0 for the recorded error, 1..n down its cause chain
	className   string // SetError name or the error's Go type name
	callstack   *errorWithCallStack
}

const (
	// Capacity of the api and error metadata caches. The SQL caches take
	// theirs from SQL.CacheSize.
	cacheSize        = 1024
	defaultQueueSize = 1024

	defaultSpanBatchSize                  = 50
	defaultSpanBatchFlushInterval         = 1000
	defaultSpanBatchCollectDeadline       = 500
	defaultSpanBatchMaxConcurrentRequests = 10

	// AgentInfo refresh, matching the Java (AgentInfoSender) and C++ agents'
	// Collector.AgentInfo defaults: re-send every 24h so a collector that lost
	// the agent meta recovers it. 0 turns the refresh off.
	defaultAgentInfoRefreshInterval   = 24 * 60 * 60 * 1000
	defaultAgentInfoSendRetryInterval = 3000
	defaultAgentInfoMaxTryPerAttempt  = 3

	maxSqlSize = 64 * 1024
	// maxErrorMessageSize matches the Java agent, which abbreviates exception
	// messages to 256 chars before recording them on a span or span event.
	maxErrorMessageSize = 256
	// maxExceptionMessageSize bounds the message of one exception metadata
	// entry, matching the Java agent's profiler.exceptiontrace.errormessage.max
	// default. A cause chain carries one message per link, and a driver error
	// quoting a whole statement is easily megabytes on its own.
	maxExceptionMessageSize = 2048
	// maxBindValueMarkerSize bounds either "...(n)" marker the bind value
	// writers append past SQL.MaxBindValueSize - the length of the value that
	// was cut, or the number of values the list left out; 20 digits holds any
	// count or length an int can reach.
	maxBindValueMarkerSize = len("...(") + 20 + len(")")
)

// maxBindValueAnnotationSize is the widest bind value list the driver writers
// can produce for a limit of maxSize. The limit is a budget checked between
// values, so a value that finds one byte of it left still writes maxSize of
// itself plus its length marker, and the separator and the count marker can
// follow that. SetSQL bounds args by this and not by the limit itself, or it
// would cut a bind value list the driver composed exactly as intended.
func maxBindValueAnnotationSize(maxSize int) int {
	return 2*maxSize + 2*maxBindValueMarkerSize + len(", ")
}

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
		metaChan:    make(chan interface{}, config.Int(CfgCollectorGrpcSenderQueueSize)),
		urlStatChan: make(chan *urlStat, config.Int(CfgHttpUrlStatQueueSize)),
		statChan:    make(chan *pb.PStatMessage, config.Int(CfgStatQueueSize)),
		config:      config,
		stats:       newAgentStats(),
		urlStats:    newUrlStats(config),
	}
	agent.stopSignal()

	// SQL.CacheSize sizes the three SQL caches only, as the Java agent's
	// profiler.jdbc.sqlcachesize does; the api and error caches keep
	// cacheSize, like Java's SimpleCacheFactory.newSimpleCache(). NewConfig
	// has already confined the value to [1, maxSqlCacheSize].
	sqlCacheSize := config.Int(CfgSQLCacheSize)
	agent.sqlCacheLengthLimit = config.Int(CfgSQLCacheLengthLimit)
	agent.errorCache = newMetaCache[string, int32](cacheSize)
	agent.sqlCache = newMetaCache[string, int32](sqlCacheSize)
	agent.sqlUidCache = newMetaCache[string, []byte](sqlCacheSize)
	agent.sqlUidCache.ttl = time.Duration(config.Int(CfgSQLCacheExpireHours)) * time.Hour
	agent.rawSqlCache = newMetaCache[string, normalizedSql](sqlCacheSize)
	agent.apiCache = newMetaCache[apiCacheKey, int32](cacheSize)

	config.logCallbackOnce.Do(func() {
		config.AddReloadCallback([]string{CfgLogLevel}, func() { logger.reloadLevel(config) })
		config.AddReloadCallback([]string{CfgLogOutput, CfgLogMaxSize, CfgLogMaxBackups}, func() { logger.reloadOutput(config) })
	})

	if !config.offGrpc {
		agent.connectWg.Add(1)
		go agent.connectGrpcServer()
	}
	setGlobalAgent(agent)
	return agent, nil
}

// closeGrpc closes whatever connections connectGrpcServer managed to create.
// Both callers reach it only after connectGrpcServer has stopped touching the
// fields - the release defer runs on that goroutine, and Shutdown gets here
// past connectWg.Wait - so the reads need no synchronisation. Each close is a
// grpc.ClientConn.Close, which is idempotent, so the two paths may overlap on
// the same connection.
func (agent *agent) closeGrpc() {
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
}

func (agent *agent) connectGrpcServer() {
	var err error
	defer agent.connectWg.Done()
	// A connect that fails for a reason other than Shutdown (bad address, TLS
	// setup) would otherwise leave a never-enabled agent as the global, so
	// GetAgent hands out a dead agent and NewAgent cannot retry. Release it,
	// identity-guarded as in Shutdown.
	defer func() {
		// Registration that succeeded, or a Shutdown that got here first, has
		// moved the phase on; only a connect that failed is still registering.
		// The transition is a CAS, so a Shutdown racing this defer wins or
		// loses cleanly and the loser leaves the release to the winner.
		if agent.enable.current() != phaseRegistering || !agent.enable.transitionTo(phaseFailed) {
			return
		}
		Log("agent").Errorf("failed to connect to collector, agent disabled: %v", err)
		globalAgentLock.Lock()
		if GetAgent() == Agent(agent) {
			// Hand the config file watcher back here, before dropping the
			// global. Shutdown's Close is guarded by the same identity check,
			// so once this agent is no longer the global that guard fails and
			// Config.Close - the only path that stops the watcher goroutine -
			// never runs, leaking the goroutine and its inotify watch for the
			// life of the process. Closing while still holding the global (and
			// the lock) is what makes it safe: no NewAgent can have restarted
			// the watcher on this Config yet, which is exactly the case the
			// guard in signalShutdown exists to protect. Close is idempotent,
			// so the user's later Shutdown is a no-op here.
			if agent.config != nil {
				agent.config.Close()
			}
			// Same reasoning for the collector connections: grpc dials lazily,
			// so a registration that never finished still holds a live agent
			// connection, and releasing the global lets the caller drop the
			// handle without ever calling Shutdown. Free them here rather than
			// leaking one set per failed NewAgent retry.
			agent.closeGrpc()
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

	// A Shutdown that completed while registration was finishing has moved the
	// phase to stopped; workers started now would only find it so and exit,
	// and the closed connections are already released by that Shutdown.
	if !agent.enable.transitionTo(phaseRunning) {
		return
	}
	agent.startWorkers(agent.workerTable())
}

// worker declares one of the agent's worker goroutines: the name the
// supervisor logs it under, the body it runs, and the condition under which
// this agent starts it. The names are part of the log contract - the
// troubleshooting guide reads the "start <name> goroutine" and "restart <name>
// goroutine" lines by these values - so they must not change.
type worker struct {
	name string
	body func()
	when func() bool
}

// always is the when predicate of a worker every enabled agent runs.
func always() bool { return true }

// workerTable is the one place the agent's workers are declared. Every worker
// that connectGrpcServer used to start with its own go statement is an entry
// here, with the conditions that used to be if/else branches around those
// statements expressed as the entry's when predicate: span and span batch are
// mutually exclusive on CfgSpanBatchEnable, and agent info refresh runs only
// for a positive refresh interval. The connection fields (agentGrpc, spanGrpc,
// statGrpc, cmdGrpc) are deliberately not a table: they are a different kind
// of thing, closed by closeGrpc under a nil guard rather than supervised, and
// nothing counts them. The hand-counted number this table replaces was the
// workerWg.Add that the spawn loop in startWorkers now derives from the table.
func (agent *agent) workerTable() []worker {
	spanBatch := agent.config.Bool(CfgSpanBatchEnable)
	refreshInterval := agent.agentInfoRefreshInterval()
	return []worker{
		{name: "ping", body: agent.sendPingWorker, when: always},
		{name: "span batch", body: agent.sendSpanBatchWorker, when: func() bool { return spanBatch }},
		{name: "span", body: agent.sendSpanWorker, when: func() bool { return !spanBatch }},
		{name: "command", body: agent.runCommandService, when: always},
		{name: "meta", body: agent.sendMetaWorker, when: always},
		{name: "collect agent stat", body: agent.collectAgentStatWorker, when: always},
		{name: "collect uri stat", body: agent.collectUrlStatWorker, when: always},
		{name: "send uri stat", body: agent.sendUrlStatWorker, when: always},
		{name: "send stats", body: agent.sendStatsWorker, when: always},
		{
			name: "agent info refresh",
			body: func() { agent.refreshAgentInfoWorker(refreshInterval) },
			when: func() bool { return refreshInterval > 0 },
		},
	}
}

// startWorkers starts every worker whose when predicate holds, one supervised
// goroutine each. workerWg is incremented per worker, right before its go
// statement, so the count matches the goroutines by construction - a hand
// counted Add that drifted from the go statements either made every Shutdown
// wait out its full deadline (too large) or panicked the WaitGroup (too small),
// and neither was caught by the compiler.
//
// The Add, and the state slice below, must stay here, on the connectGrpcServer
// goroutine, and not move into superviseWorker: shutdownAgent reaches its
// worker wait only after connectWg.Wait, which is what guarantees every slot
// exists before the wait. A slot made inside the spawned goroutine would race
// that wait. superviseWorker owns the slot from then on and releases it with
// its deferred Done and close.
func (agent *agent) startWorkers(workers []worker) {
	// Allocate the state slice to its final size before the first go
	// statement: superviseWorker reads the slice header from its goroutine,
	// and shutdownAgent reads it after connectWg.Wait, so it must not change
	// once a worker exists.
	var states []*workerState
	for _, w := range workers {
		if w.when() {
			states = append(states, &workerState{name: w.name, done: make(chan struct{})})
		}
	}
	agent.workerStates = states
	for _, w := range workers {
		if !w.when() {
			continue
		}
		agent.workerWg.Add(1)
		go agent.superviseWorker(w.name, w.body)
	}
}

// workerState is the observable liveness of one started worker: running is
// set on the supervisor's entry and cleared on its final exit, and done is
// closed on that exit so shutdownAgent can wait for it without a goroutine.
type workerState struct {
	name    string
	running atomic.Bool
	done    chan struct{}
}

// workerStateOf finds the state slot of a started worker by name, or nil for a
// worker the table did not start - tests drive superviseWorker directly with
// names that have no slot. A linear scan: the table is under ten entries and
// each worker looks itself up twice in its lifetime.
func (agent *agent) workerStateOf(name string) *workerState {
	for _, st := range agent.workerStates {
		if st.name == name {
			return st
		}
	}
	return nil
}

// runningWorkerNames lists the started workers whose supervisor has not exited.
func (agent *agent) runningWorkerNames() []string {
	var names []string
	for _, st := range agent.workerStates {
		if st.running.Load() {
			names = append(names, st.name)
		}
	}
	return names
}

// shutdownTimeout bounds how long Shutdown waits for the worker goroutines to
// drain their queues before abandoning them. A variable so tests can shorten
// it.
var shutdownTimeout = 3 * time.Second

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
	if st := agent.workerStateOf(name); st != nil {
		st.running.Store(true)
		defer close(st.done)
		defer st.running.Store(false)
	}

	stop := agent.stopSignal().Done()
	for {
		if recoverPanic(name, body) {
			return
		}
		if !agent.workerContinues() {
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
		if !agent.workerContinues() {
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

// agentInfoRefreshInterval returns the configured AgentInfo refresh cycle;
// 0 or less means the refresh worker is not started.
func (agent *agent) agentInfoRefreshInterval() time.Duration {
	return time.Duration(agent.config.Int(CfgCollectorAgentInfoRefreshInterval)) * time.Millisecond
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
	// A running agent enters stopping, where the request path still records
	// and the workers still drain until shutdownAgent moves it to stopped. An
	// agent that never ran - still registering, or failed - has nothing to
	// drain and goes straight to stopped, as the shutdown flag alone did.
	if !agent.enable.transitionTo(phaseStopping) {
		agent.enable.transitionTo(phaseStopped)
	}
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

// waitWorkers waits for every worker startWorkers started to exit and reports
// whether they all did within timeout. It waits on the per-worker done
// channels under one timer rather than on workerWg: wg.Wait cannot be
// cancelled, so a goroutine parked on it for a deadline that then passes is
// leaked for as long as the abandoned worker lives, once per NewAgent/Shutdown
// cycle that overruns. This leaves nothing behind.
func (agent *agent) waitWorkers(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for _, st := range agent.workerStates {
		select {
		case <-st.done:
		case <-timer.C:
			return false
		}
	}
	return true
}

// waitTimeout waits for wg and reports whether it completed within timeout.
// A test helper: the goroutine it parks on wg.Wait outlives a timeout, which
// is why shutdownAgent uses waitWorkers instead.
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

// Shutdown stops the agent. Repeated and concurrent calls are safe: the
// teardown runs once, and a later caller waits for it rather than closing the
// collector connections underneath the first call's span drain.
func (agent *agent) Shutdown() {
	agent.shutdownOnce.Do(agent.shutdownAgent)
}

func (agent *agent) shutdownAgent() {
	// Flush the url stat tick in progress before anything is signalled. Both
	// url stat workers and sendStatsWorker stop on stopCtx, so a flush issued
	// after the signal could land on a queue nobody reads any more: enqueueing
	// here puts the tick in statChan before sendStatsWorker can see the stop,
	// and that worker drains the queue once when the stop arrives, which is
	// what actually gets the last tick out. Skipped for an agent that never
	// ran - it has no workers and no stat queue.
	if agent.tracingEnabled() {
		agent.flushUrlStat(true)
	}

	// Signal before waiting on connectWg, never after: registration retries for
	// as long as the collector is unreachable, so a wait that runs first pays
	// its whole timeout during exactly the outage it was meant to survive. The
	// signal is what ends that loop - it cancels the in-flight RequestAgentInfo
	// and the back-off pause - which makes the wait below short on every path.
	agent.signalShutdown()
	Log("agent").Infof("shutdown pinpoint agent")

	// wait for the grpc connection to be completed
	agent.connectWg.Wait()

	// Close the collector connections on every path, including the
	// never-enabled early return below. Deferred so the enabled path keeps its
	// order - workers drain first, connections close last - and shutdownOnce
	// keeps a concurrent Shutdown from reaching it while the drain is still
	// running. A connect failure already closed them from its own release
	// defer; this is the second, idempotent close.
	defer agent.closeGrpc()

	// Release the global on every path, before the phase guard below. An agent
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

	// A never-enabled agent stops here: it has no workers, queues or streams
	// to tear down. signalShutdown above put it straight into stopped, so only
	// an agent that ran is still stopping; shutdownOnce already rules out a
	// second caller reaching this.
	if agent.enable.current() != phaseStopping {
		return
	}
	agent.enable.transitionTo(phaseStopped)

	// The teardown below is ordered against the workers that workerTable
	// declares: the url stat flush above fed sendStatsWorker, spanQueue.close
	// wakes the span or span batch worker, and the cmdGrpc close ends the
	// command worker's listening stream. Those orderings are explained at
	// each step; the list of workers they apply to lives in workerTable.
	//
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
	// Abandoned workers are unblocked by the connection close below. The
	// workers being waited for are the ones workerTable declared and
	// startWorkers gave a state slot - one per go statement, all in place
	// before connectWg.Wait above returned - so this wait cannot be left short
	// or over-counted by a worker added elsewhere.
	// On the overrun, name the workers still running so the deadline can be
	// investigated; the in-time path logs nothing extra.
	if !agent.waitWorkers(shutdownTimeout) {
		Log("agent").Warnf("shutdown timeout(%v) exceeded, abandon in-flight workers: %s",
			shutdownTimeout, strings.Join(agent.runningWorkerNames(), ", "))
	}
}

func (agent *agent) NewSpanTracer(operation string, rpcName string) Tracer {
	var tracer Tracer

	if agent.tracingEnabled() {
		reader := &noopDistributedTracingContextReader{}
		tracer = agent.NewSpanTracerWithReader(operation, rpcName, reader)
	} else {
		tracer = NoopTracer()
	}
	return tracer
}

func (agent *agent) NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	if !agent.tracingEnabled() || reader == nil {
		return NoopTracer()
	}

	sampled := reader.Get(HeaderSampled)
	if sampled == "s0" {
		agent.stats.incrUnSampleCont()
		return newUnSampledSpan(agent, rpcName)
	}

	sampler := agent.config.load().sampler
	// isContinueSampled is unconditionally true, so it must only be picked for
	// headers Extract will actually continue. continueHeaders is the single
	// definition of that; splitting it in two let a peer bypass the sampling
	// rate with headers Extract then started a new transaction for. Extract
	// calls it again; that is cheaper than widening its signature.
	if _, continued := continueHeaders(reader); !continued {
		return agent.samplingSpan(func() bool { return sampler.isNewSampled(agent.stats) }, operation, rpcName, reader)
	}
	return agent.samplingSpan(func() bool { return sampler.isContinueSampled(agent.stats) }, operation, rpcName, reader)
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
	return agent.tracingEnabled()
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
	// Deferred through a closure so that it closes whichever stream the loop
	// ended up holding, on every exit: a panicked body that superviseWorker
	// restarts, and the phase reaching stopped between iterations, both used
	// to leave the stream open on the collector.
	defer func() { stream.close() }()

	for agent.workerContinues() {
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
	// Deferred for the same reason as the ping worker's: a panic recovered by
	// superviseWorker would otherwise leak this stream and let the restarted
	// body open another on top of it.
	defer func() { stream.close() }()

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
	if !agent.tracingEnabled() {
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
	retry := &agent.metaRetry
	retry.init(metaRetryQueueSize)

	for agent.workerContinues() {
		// New metadata first, as the C++ worker takes its queue before its
		// retry schedule: a retry is a second try at an id whose spans went
		// out a delay ago, a new item is an id whose spans are going out now.
		var item pendingMeta
		select {
		case <-stop:
			return
		case md := <-agent.metaChan:
			item = pendingMeta{md: md}
		default:
			// Nothing to send until a new item, a due retry or a retry
			// scheduled while the queue was empty (wake) arrives. The timer
			// is armed per wait: a retry is only ever appended behind the
			// head, so the head's due time is fixed until it is popped.
			var due <-chan time.Time
			var timer *time.Timer
			if wait, ok := retry.headWait(time.Now()); ok {
				timer = time.NewTimer(wait)
				due = timer.C
			}
			var popped, stopped bool
			select {
			case <-stop:
				stopped = true
			case md := <-agent.metaChan:
				item, popped = pendingMeta{md: md}, true
			case <-retry.wake:
			case <-due:
				item, popped = retry.popDue(time.Now())
			}
			if timer != nil {
				timer.Stop()
			}
			if stopped {
				return
			}
			if !popped {
				continue
			}
		}

		// Reported here rather than from the producers: enqueueMeta and the
		// send goroutines only bump a counter.
		agent.metaDrops.report("meta", cap(agent.metaChan))
		agent.metaRetryDrops.report("meta retry", retry.capacity)

		// A parked release needs no permit: it is the drop the rejection
		// earned, delayed by one retry interval (metaRejected).
		if item.releaseOnly {
			agent.deleteMetaCache(item.md)
			continue
		}

		// The permit acquisition obeys stop too: with every permit held by a
		// slow send, a plain send here parks the worker where the stop signal
		// cannot reach it, and it would dispatch one more send after shutdown
		// began. The item just pulled is dropped, like the rest of the queue.
		select {
		case <-stop:
			return
		case permit <- struct{}{}:
		}
		inFlight.Add(1)
		go func(item pendingMeta) {
			defer inFlight.Done()
			defer func() { <-permit }()

			recoverPanic("meta send", func() {
				agent.sendMetadataOnce(item)
			})
		}(item)
	}
}

// sendMetadataOnce makes one send of item and hands it on according to the
// verdict. The permit is held for the send alone: a retry waits in
// agent.metaRetry, not here, so a collector outage cannot pin the in-flight
// slots and stall sendMetaWorker on the permit while metaChan overflows.
func (agent *agent) sendMetadataOnce(item pendingMeta) {
	attempts := item.attempts + 1
	err := agent.sendMetadata(item.md)
	switch metaVerdictOf(err, attempts) {
	case metaDelivered:
	case metaRetryLater:
		agent.scheduleMetaRetry(pendingMeta{md: item.md, attempts: attempts})
	case metaGiveUp:
		agent.deleteMetaCache(item.md)
	case metaRejected:
		// Exception metadata is never cached, so there is nothing to park.
		if _, ok := item.md.(exceptionMeta); ok {
			return
		}
		agent.scheduleMetaRetry(pendingMeta{md: item.md, releaseOnly: true})
	}
}

// scheduleMetaRetry parks item for one retry delay. The evicted item, if the
// schedule was full, has its cache entry released here: the same policy the
// metaChan overflow applies, so a full schedule costs exactly one cache entry
// and the id is registered again on its next use.
func (agent *agent) scheduleMetaRetry(item pendingMeta) {
	if agent.stopping() {
		return
	}
	item.dueAt = time.Now().Add(agent.agentGrpc.retryDelay)
	if evicted, ok := agent.metaRetry.push(item); ok {
		agent.deleteMetaCache(evicted.md)
		agent.metaRetryDrops.record(1)
	}
}

// sendMetadata makes one send of md and returns its error.
func (agent *agent) sendMetadata(md interface{}) error {
	switch md := md.(type) {
	case apiMeta:
		return agent.agentGrpc.sendApiMetadataOnce(md.id, md.descriptor, -1, md.apiType)
	case stringMeta:
		return agent.agentGrpc.sendStringMetadataOnce(md.id, md.funcName)
	case sqlMeta:
		return agent.agentGrpc.sendSqlMetadataOnce(md.id, md.sql)
	case sqlUidMeta:
		return agent.agentGrpc.sendSqlUidMetadataOnce(md.uid, md.sql)
	case exceptionMeta:
		return agent.agentGrpc.sendExceptionMetadataOnce(&md)
	}
	return fmt.Errorf("unknown metadata type %T", md)
}

// pendingMeta is a metadata item on the retry schedule.
type pendingMeta struct {
	md interface{}
	// attempts counts the sends made so far.
	attempts int
	dueAt    time.Time
	// releaseOnly parks a rejected item: when it comes due only its cache
	// entry is released, nothing is sent (metaRejected).
	releaseOnly bool
}

// metaRetryQueue is the time-ordered retry schedule, the Go counterpart of the
// C++ agent's retry_queue. Every entry waits the same fixed delay, so a push
// is always due last and a slice kept in push order is kept in due order.
// The schedule has its own bound, separate from metaChan's (see
// metaRetryQueueSize), and a full one head-drops: the incoming item is the
// last one due, so dropping it would freeze the schedule on whatever entered
// first and deny every later failure a retry; dropping the oldest keeps the
// freshest failures, whose spans the collector is still receiving, and keeps
// the schedule moving under a sustained outage.
type metaRetryQueue struct {
	mu       sync.Mutex
	items    []pendingMeta
	capacity int
	// wake tells sendMetaWorker a retry was scheduled while it waited with
	// an empty schedule, so it re-arms its timer. Buffered so a push never
	// blocks on a worker that is busy elsewhere.
	wake chan struct{}
}

// init sets the bound and creates the wake channel once; a capacity set
// before it (by a test) is kept.
func (q *metaRetryQueue) init(capacity int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.capacity <= 0 {
		q.capacity = capacity
	}
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
}

// push appends item, evicting and returning the oldest entry when the
// schedule is full. The caller releases the evicted item's cache entry
// outside the lock: the caches have locks of their own, and nesting them
// under this one would put the request path behind the retry schedule.
func (q *metaRetryQueue) push(item pendingMeta) (evicted pendingMeta, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.capacity > 0 && len(q.items) >= q.capacity {
		evicted, ok = q.items[0], true
		q.items[0] = pendingMeta{}
		q.items = q.items[1:]
	}
	q.items = append(q.items, item)
	if q.wake != nil {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
	return evicted, ok
}

// headWait returns how long until the oldest entry is due, or false when the
// schedule is empty.
func (q *metaRetryQueue) headWait(now time.Time) (time.Duration, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return 0, false
	}
	return q.items[0].dueAt.Sub(now), true
}

// popDue removes and returns the oldest entry if it is due.
func (q *metaRetryQueue) popDue(now time.Time) (pendingMeta, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 || q.items[0].dueAt.After(now) {
		return pendingMeta{}, false
	}
	item := q.items[0]
	q.items[0] = pendingMeta{}
	q.items = q.items[1:]
	return item, true
}

// length reports how many entries are scheduled.
func (q *metaRetryQueue) length() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// deleteMetaCache drops the cache entry whose metadata md failed to reach the
// collector. The entry is matched on its value as well as its key: between the
// failed send and this call the key may have been evicted and re-registered
// under a new id whose metadata did go out, and removing that entry would
// re-issue the id a third time for nothing.
func (agent *agent) deleteMetaCache(md interface{}) {
	switch md := md.(type) {
	case apiMeta:
		agent.apiCache.remove(apiCacheKey{md.descriptor, md.apiType}, func(id int32) bool { return id == md.id })
	case stringMeta:
		agent.errorCache.remove(md.funcName, func(id int32) bool { return id == md.id })
	case sqlMeta:
		agent.sqlCache.remove(md.key, func(id int32) bool { return id == md.id })
	case sqlUidMeta:
		// A statement that bypassed the cache has nothing to drop.
		if !md.cached {
			return
		}
		// A re-registered UID is the same hash, so this only guards a key
		// that changed hands to a different statement's entry.
		agent.sqlUidCache.remove(md.key, func(uid []byte) bool { return bytes.Equal(uid, md.uid) })
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
	if !agent.tracingEnabled() {
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

// idGen is an agent-local metadata id sequence. The collector keys its API,
// string and SQL metadata by these int32 ids, so once the sequence wraps past
// math.MaxInt32 its next lap recycles ids that already name other entries, and
// the spans carrying them would point at another entry's text. next refuses to
// issue anything past the wrap instead.
type idGen struct {
	id int32
	// wrapped latches the wrap: without it the sequence would climb back
	// through the negatives and start handing out colliding ids again. Its CAS
	// also keeps the warning to one line per process.
	wrapped atomic.Bool
}

// next returns the next id in the sequence, or 0 once it has wrapped - the same
// 0 the caches return while the agent is disabled, which every consumer already
// reads as "no metadata". kind names the sequence in the overflow warning.
func (g *idGen) next(kind string) int32 {
	if g.wrapped.Load() {
		return 0
	}
	id := atomic.AddInt32(&g.id, 1)
	if id > 0 {
		return id
	}
	if g.wrapped.CompareAndSwap(false, true) {
		Log("agent").Warnf("%s id generator overflowed; no further %s metadata is recorded", kind, kind)
	}
	return 0
}

func (agent *agent) cacheError(errorName string) int32 {
	if !agent.tracingEnabled() {
		return 0
	}

	if v, ok := agent.errorCache.peek(errorName); ok {
		return v
	}

	id := agent.errorIdGen.next("error")
	if id == 0 {
		return 0
	}
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

// abbreviateString truncates str to at most length bytes plus a "...(original
// length)" marker, byte-for-byte what the Java agent's StringUtils.abbreviate
// writes - the limit is already known to every reader, the original size is
// not. The cut lands on a rune boundary: protobuf rejects invalid UTF-8 string
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
	return str[:cut] + "...(" + strconv.Itoa(len(str)) + ")"
}

// sqlCacheable reports whether a SQL key is short enough to keep in the SQL
// metadata caches keyed by a hash of the statement. Anything longer bypasses
// them and re-sends its metadata on every use, so a handful of huge generated
// statements cannot pin megabytes of cache for the life of the process. This
// mirrors the Java agent's UidCache bypassLength
// (profiler.jdbc.sqlcachelengthlimit); the limit is in bytes here, not UTF-16
// chars.
//
// Deliberately not applied to sqlCache: its ids come from a sequence, so a
// bypassed statement would burn a fresh id - and a fresh sqlMeta - on every
// single use, and the same query would show up in the UI as a new entry per
// execution. Java bypasses only the UID cache for the same reason:
// SimpleCacheFactory.newSqlCache() builds the id cache with no length check.
// That exemption is what caps the id cache at cacheSize statements of whatever
// length the application generates, since the key is the untruncated text; the
// UID cache is bounded by the limit instead.
func (agent *agent) sqlCacheable(sql string) bool {
	return len(sql) < agent.sqlCacheLengthLimit
}

func (agent *agent) cacheSql(sql string) int32 {
	if !agent.tracingEnabled() {
		return 0
	}

	// Keyed on the untruncated statement, as Java's DefaultCachingSqlNormalizer
	// is: an abbreviated key keeps no more than a 64KB prefix and the total
	// length, so two statements agreeing on both would share one id and the
	// second would never publish its own metadata.
	//
	// Bounded by maxSqlNormalizeLength: SetSQL drops a raw statement past it,
	// but literal-heavy SQL normalizes larger than it came in, so the key is
	// checked here as well - a key past the cap is refused (no id, so SetSQL
	// records no annotation) rather than admitted to the cache and metaChan.
	if !sqlNormalizable(sql) {
		return 0
	}
	if v, ok := agent.sqlCache.peek(sql); ok {
		return v
	}

	// A wrapped sequence records no SQL: SetSQL skips a zero id.
	id := agent.sqlIdGen.next("sql")
	if id == 0 {
		return 0
	}
	if v, ok := agent.sqlCache.peekOrAdd(sql, id); ok {
		return v
	}

	aSql := abbreviateString(sql, maxSqlSize)
	md := sqlMeta{
		id:  id,
		sql: aSql,
		key: sql,
	}
	agent.enqueueMeta(md)

	if IsDebugLogLevelEnabled() {
		Log("agent").Debugf("cache sql id: %d, %s", id, aSql)
	}
	return id
}

func (agent *agent) cacheSqlUid(sql string) []byte {
	if !agent.tracingEnabled() {
		return nil
	}

	// Java hashes the whole normalized SQL and keys its cache on the same
	// untruncated text (DefaultCachingSqlNormalizer), abbreviating only what it
	// publishes (SqlCacheService) - so both the UID and the key come from sql
	// here. An abbreviated key keeps no more than a 64KB prefix and the total
	// length, and two statements agreeing on both would share one entry: the
	// second would answer with the first's UID and never publish its own
	// metadata. Nothing longer than the cache length limit reaches the LRU
	// either way, since sqlCacheable now measures that same untruncated text,
	// as Java's UidCache bypassLength does. Nothing longer than
	// maxSqlNormalizeLength gets a UID at all (see cacheSql).
	if !sqlNormalizable(sql) {
		return nil
	}
	cacheable := agent.sqlCacheable(sql)
	if cacheable {
		if v, ok := agent.sqlUidCache.peek(sql); ok {
			return v
		}
	}

	uid := sqlUid(sql)
	if cacheable {
		if v, ok := agent.sqlUidCache.peekOrAdd(sql, uid); ok {
			return v
		}
	}

	// A bypassed statement was never cached, so a failed send has no entry to
	// evict and the key is dead weight: every execution queues one item, and
	// the untruncated text is unbounded (normalization has no input cap, and
	// literal-heavy SQL normalizes larger than it came in). Java bounds the
	// same item at 64KB by abbreviating before it enqueues (SqlCacheService).
	aSql := abbreviateString(sql, maxSqlSize)
	md := sqlUidMeta{uid: uid, sql: aSql, cached: cacheable}
	if cacheable {
		md.key = sql
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
	// SQL.CacheLengthLimit applies here as it does to the metadata caches: the
	// raw text is both key and value, so one statement past the limit pinned
	// twice its size per entry, which is the memory the limit exists to cap.
	// SQL.RemoveComments is startup-only, so entries already in the cache stay
	// consistent with the value read here.
	removeComments := agent.config.load().sqlRemoveComments
	if len(sql) > maxSqlSize || !agent.sqlCacheable(sql) {
		return newSqlNormalizer(sql, removeComments).run()
	}
	if n, ok := agent.rawSqlCache.peek(sql); ok {
		return n.sql, n.param
	}
	nsql, param := newSqlNormalizer(sql, removeComments).run()
	agent.rawSqlCache.peekOrAdd(sql, normalizedSql{sql: nsql, param: param})
	return nsql, param
}

func (agent *agent) cacheSpanApi(descriptor string, apiType int) int32 {
	if !agent.tracingEnabled() {
		return 0
	}

	key := apiCacheKey{descriptor, apiType}

	if v, ok := agent.apiCache.peek(key); ok {
		return v
	}

	id := agent.apiIdGen.next("api")
	if id == 0 {
		return 0
	}
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
	if !agent.tracingEnabled() || !span.cfg.errorTraceCallStack {
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
	if !agent.tracingEnabled() {
		return false
	}

	select {
	case agent.urlStatChan <- stat:
		return true
	default:
		break
	}

	// The queue is full: head-drop the oldest record and hand its slot to stat,
	// the same policy as enqueueStat and tryEnqueueMeta. An overflow costs
	// exactly one record - the evicted one, or stat itself when another
	// producer takes the freed slot first.
	dropped := int64(0)
	select {
	case <-agent.urlStatChan:
		dropped++
	default:
		// The consumer drained one meanwhile, so nothing had to be evicted.
	}
	queued := false
	select {
	case agent.urlStatChan <- stat:
		queued = true
	default:
		// Another producer took the freed slot: stat is the record lost.
		dropped++
	}
	if dropped == 0 {
		return true
	}
	agent.urlStatDrops.record(dropped)
	agent.urlStatDrops.report("url stat", cap(agent.urlStatChan))
	return queued
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

// logThrottle rate-limits one warning site to a message per dropReportInterval,
// counting what it held back in between. For warnings a peer or the
// application can trigger once per request - a malformed header, an
// unbalanced span - where an unthrottled WARN is a log-flooding lever that
// anyone able to send a request can pull; the C++ agent's LOG_WARN_THROTTLED
// covers the same sites.
type logThrottle struct {
	// src names the log source. The empty value logs under "span", where every
	// site but the url stat limit one lives.
	src        string
	next       atomic.Int64 // unix nano before which the site stays silent
	suppressed atomic.Int64
}

var (
	malformedTraceIdLog, malformedSpanIdLog, malformedParentSpanIdLog logThrottle
	endSpanTwiceLog, unclosedEventLog, noEventLog, sharedGoroutineLog logThrottle
	afterEndSpanLog, misnestedEventLog                                logThrottle
)

// acquire reports whether the site may log now and, if so, how many calls it
// held back since it last did. A refused call is counted as held back.
func (t *logThrottle) acquire() (held int64, ok bool) {
	now := time.Now().UnixNano()
	next := t.next.Load()
	if now < next || !t.next.CompareAndSwap(next, now+int64(dropReportInterval)) {
		t.suppressed.Add(1)
		return 0, false
	}
	return t.suppressed.Swap(0), true
}

func (t *logThrottle) warnf(format string, args ...interface{}) {
	n, ok := t.acquire()
	if !ok {
		return
	}
	if n > 0 {
		format += " (%d similar warning(s) suppressed)"
		args = append(args, n)
	}
	src := t.src
	if src == "" {
		src = "span"
	}
	Log(src).Warnf(format, args...)
}

func (agent *agent) collectUrlStatWorker() {
	Log("agent").Infof("start collect uri stat goroutine")

	stop := agent.stopSignal().Done()

	for agent.workerContinues() {
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

	// A completed tick is sent as soon as urlStats closes it (completedTick);
	// the ticker is only the ceiling on the trailing tick of an agent whose
	// traffic stopped, which nothing arrives to close and takeSnapshot closes
	// on the clock instead. It follows Stat.CollectInterval the way Java's
	// UriStatCollectingJob rides the agent stat scheduler
	// (profiler.jvm.stat.collect.interval), rather than a second 30s timer of
	// its own - the same policy as the C++ agent's UrlStats send worker.
	// Read once: Stat.CollectInterval is not reloadable.
	interval := time.Duration(agent.config.Int(CfgStatCollectInterval)) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	stop := agent.stopSignal().Done()
	completed := agent.urlStats.completedTick()

	for agent.workerContinues() {
		select {
		case <-stop:
			Log("agent").Infof("end send uri stat goroutine")
			return
		case <-completed:
			agent.flushUrlStat(false)
		case <-ticker.C:
			agent.flushUrlStat(false)
		}
	}
}

// flushUrlStat sends the url stat ticks that are over, if any: the ones closed
// by later traffic, plus the tick in progress once its window has elapsed.
// includeInProgress takes the tick in progress whatever its window and is set
// only on the shutdown path, where no later send will ever come for it.
//
// Nothing is sent when there is nothing to send. Java's UriStatCollectingJob
// breaks out of its poll loop on an empty queue rather than sending an empty
// message (UriStatCollectingJob.java:49-61); an idle agent costs the collector
// nothing.
func (agent *agent) flushUrlStat(includeInProgress bool) {
	if !agent.config.load().collectUrlStat {
		return
	}

	snapshot := agent.urlStats.takeSnapshot(includeInProgress)
	if snapshot.isEmpty() {
		return
	}
	agent.enqueueStat(makePAgentUriStat(snapshot))
}

func (agent *agent) enqueueStat(stat *pb.PStatMessage) bool {
	select {
	case agent.statChan <- stat:
		return true
	default:
		break
	}

	// The queue is full: head-drop the oldest record and hand its slot to stat,
	// the same policy as tryEnqueueMeta. An overflow costs exactly one record
	// either way - the evicted one, or stat itself when another producer takes
	// the freed slot first - and stat is a time series, so the newest sample
	// is the one worth keeping.
	dropped := int64(0)
	select {
	case <-agent.statChan:
		dropped++
	default:
		// The consumer drained one meanwhile, so nothing had to be evicted.
	}
	queued := false
	select {
	case agent.statChan <- stat:
		queued = true
	default:
		// Another producer took the freed slot: stat is the record lost.
		dropped++
	}
	if dropped == 0 {
		return true
	}
	agent.statDrops.record(dropped)
	// Reported here rather than in sendStatsWorker: the worker only reaches its
	// report after pulling from the queue, so a collector outage parks it in
	// newStatStreamWithRetry and silences the warning for exactly the stretch
	// where the drops happen. Same policy as enqueueUrlStat.
	agent.statDrops.report("stat", cap(agent.statChan))
	return queued
}

func (agent *agent) sendStatsWorker() {
	Log("agent").Infof("start send stats goroutine")

	stream := agent.statGrpc.newStatStreamWithRetry()
	defer func() { stream.close() }()

	stop := agent.stopSignal().Done()

	for agent.workerContinues() {
		var stats *pb.PStatMessage
		select {
		case <-stop:
			// Drain what is already queued before leaving: shutdownAgent
			// enqueues the last url stat tick and then cancels stopCtx, so
			// both cases are ready together here, and a select picks between
			// ready cases at random. One pass over the queue, no waiting - a
			// record enqueued after this returns is dropped with the channel,
			// as the teardown comment in shutdownAgent says.
			for {
				select {
				case stats = <-agent.statChan:
					stream = agent.sendStatsOrReopen(stream, stats)
				default:
					Log("agent").Infof("end send stats goroutine")
					return
				}
			}
		case stats = <-agent.statChan:
		}

		stream = agent.sendStatsOrReopen(stream, stats)
	}

	Log("agent").Infof("end send stats goroutine")
}

// sendStatsOrReopen sends stats on stream and returns the stream to keep
// using: the same one, or its replacement when the send broke it.
func (agent *agent) sendStatsOrReopen(stream *statStream, stats *pb.PStatMessage) *statStream {
	stream = renewIfExpired(stream, agent.statGrpc.newStatStreamWithRetry, "stat")
	err := stream.sendStats(stats)
	if err != nil {
		if err != io.EOF {
			Log("stats").Errorf("send stats - %v", err)
		}

		stream.close()
		stream = agent.statGrpc.newStatStreamWithRetry()
	}
	return stream
}
func NewTestAgent(config *Config, t *testing.T) (Agent, error) {
	config.offGrpc = true
	logger.setup(config)

	if config.objName == nil {
		if err := config.checkNameAndID(); err != nil {
			// Tests may omit required identity fields; fall back to a default
			// v3 identity so the header builder has a non-nil object name.
			agentID := ""
			if uid, err := newAgentUID(); err == nil {
				agentID = encodeUID(uid)
			}
			config.objName = &objectName{
				version:         nameV3,
				agentID:         agentID,
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
		metaChan:    make(chan interface{}, config.Int(CfgCollectorGrpcSenderQueueSize)),
		urlStatChan: make(chan *urlStat, config.Int(CfgHttpUrlStatQueueSize)),
		statChan:    make(chan *pb.PStatMessage, config.Int(CfgStatQueueSize)),
		config:      config,
		stats:       newAgentStats(),
		urlStats:    newUrlStats(config),
	}
	// SQL.CacheSize sizes the three SQL caches only, as the Java agent's
	// profiler.jdbc.sqlcachesize does; the api and error caches keep
	// cacheSize, like Java's SimpleCacheFactory.newSimpleCache(). NewConfig
	// has already confined the value to [1, maxSqlCacheSize].
	sqlCacheSize := config.Int(CfgSQLCacheSize)
	agent.sqlCacheLengthLimit = config.Int(CfgSQLCacheLengthLimit)
	agent.errorCache = newMetaCache[string, int32](cacheSize)
	agent.sqlCache = newMetaCache[string, int32](sqlCacheSize)
	agent.sqlUidCache = newMetaCache[string, []byte](sqlCacheSize)
	agent.sqlUidCache.ttl = time.Duration(config.Int(CfgSQLCacheExpireHours)) * time.Hour
	agent.rawSqlCache = newMetaCache[string, normalizedSql](sqlCacheSize)
	agent.apiCache = newMetaCache[apiCacheKey, int32](cacheSize)

	// offGrpc keeps connectGrpcServer - and every worker it starts - from
	// running, so no caller ever reaches the clients. A bare struct is enough
	// to keep the field non-nil and keeps the canned mocks out of the shipped
	// library.
	agent.agentGrpc = &agentGrpc{agent: agent}

	setGlobalAgent(agent)
	agent.enable.transitionTo(phaseRunning)

	return agent, nil
}
