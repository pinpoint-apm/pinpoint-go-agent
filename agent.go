package pinpoint

import (
	"context"
	"errors"
	"io"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"
)

func init() {
	initPackage()
	initDone = true
}

// initDone is set once init ran. Log discards before that: it writes through
// logrus, whose own package (a dependency of this one, so initialized no
// earlier) has a nil buffer pool until its init ran, and a hook reached from
// another package's init function would crash in it.
var initDone bool

// initPackage is this package's initialization. init runs it, and so does the
// first GetAgent, NoopAgent or Log call that arrives before init did: Go
// initializes packages one at a time, dependencies first and then in import
// path order, and a compile-time instrumentation hook is reached from the init
// function of a package that does not import this one (a sql.Open in a
// package init, for instance) with every variable here still zero. Running the
// steps twice is harmless: no other package can have registered configuration
// in between, since anything that could has this package as a dependency.
// Package variable initializers run again in between too, so this must not
// rely on a value it set surviving until init.
func initPackage() {
	logger = newLogger()
	initConfig()
	if defaultNoopAgent == nil {
		defaultNoopAgent = &noopAgent{}
	}
	initNoopAgent()
	initGoroutine()
	setGlobalAgent(NoopAgent())
}

type agent struct {
	appName string
	appType int32
	// appTypeStr is appType as Inject writes it, rendered once: strconv on a
	// service type outside its small-integer cache allocated per outbound call.
	appTypeStr       string
	agentID          string
	agentName        string
	serviceName      string
	clusterNamespace string
	objName          *objectName

	startTime int64
	// sequence is atomic.Int64 rather than an int64 under atomic.AddInt64: the
	// typed value guarantees the 64-bit alignment the 386 and arm builds need,
	// which a plain field only had by the luck of its position in the struct.
	sequence    atomic.Int64
	agentGrpc   *agentGrpc
	spanGrpc    *spanGrpc
	statGrpc    *statGrpc
	cmdGrpc     *cmdGrpc
	spanQueue   *spanQueue
	metaChan    chan any
	urlStatChan chan *urlStat
	statChan    chan *pb.PStatMessage

	// One reporter per bounded queue: each counts the records that queue lost
	// to overflow and rate-limits its overflow warning. spanDrops counts the
	// spans the batch sender lost - skipped for want of a permit, failed to
	// send, or too large for any request; the span queue's head-drops live in
	// its per-shard counters and are added at report time.
	urlStatDrops dropReporter
	metaDrops    dropReporter
	statDrops    dropReporter
	spanDrops    dropReporter

	// metaRetry holds metadata sends waiting out a retry delay, and rejected
	// and given-up items waiting out their cache release, budgeted apart from
	// metaChan. metaDelivered counts the items the collector accepted, which
	// ends a given-up item's wait, and metaGiveUps the give-ups since the last
	// of them, which decides whether there is a wait (metaGiveUpParks).
	metaRetry      metaRetryQueue
	metaRetryDrops dropReporter
	metaDelivered  atomic.Int64
	metaGiveUps    atomic.Int64

	errorCache  *metaCache[string, int32]
	errorIdGen  idGen
	sqlCache    *metaCache[sqlHash, int32]
	sqlIdGen    idGen
	sqlUidCache *metaCache[sqlHash, []byte]
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

	// realTimeActiveSpan tracks this agent's in-flight spans by goroutine id for
	// the real-time active thread views, gated by atcStreamCount so the span
	// path only pays for it while a viewer is attached, and cleared when the
	// last viewer leaves (atcStreams.remove) so a span that is never ended
	// cannot pin an entry for the life of the agent. Per-agent, so spans
	// still in flight at shutdown are dropped with the agent.
	realTimeActiveSpan sync.Map
	atcStreamCount     atomic.Int32

	// stats and urlStats are per-agent so restarts cannot mix workers or
	// in-flight spans with another agent's counters or URL snapshots.
	stats    *agentStats
	urlStats *urlStats

	config    *Config
	connectWg sync.WaitGroup
	workerWg  sync.WaitGroup

	// enable is the lifecycle phase (see lifecycle.go): registering, running,
	// stopping, stopped or failed. It moves only through cas and beginShutdown, and is
	// read only through the named predicates.
	enable lifecycle

	// workerStates holds one running flag per worker startWorkers started,
	// so a Shutdown that overruns its deadline can name the workers still in
	// flight - a WaitGroup cannot say even how many remain. Written once by
	// startWorkers before any worker goroutine exists; the flags are atomic.
	workerStates []*workerState

	// shutdownOnce serializes the teardown. Without it a concurrent second
	// Shutdown would return at the phase check below and run its deferred
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

const (
	// Capacity of the api and error metadata caches. The SQL caches take
	// theirs from SQL.CacheSize.
	cacheSize        = 1024
	defaultQueueSize = 1024

	defaultSpanBatchSize                  = 50
	defaultSpanBatchFlushInterval         = 1000
	defaultSpanBatchCollectDeadline       = 500
	defaultSpanBatchMaxConcurrentRequests = 10

	// Collector.AgentInfo defaults: re-send every 24h so a collector that lost
	// the agent meta recovers it. 0 turns the refresh off.
	defaultAgentInfoRefreshInterval   = 24 * 60 * 60 * 1000
	defaultAgentInfoSendRetryInterval = 3000
	defaultAgentInfoMaxTryPerAttempt  = 3

	maxSqlSize = 64 * 1024
	// maxSqlParamSize bounds the literal parameters a SQL span event carries
	// (SetSQL). They ride on the span itself and were its one field with no
	// bound short of maxSqlNormalizeLength, so a chunk of events each carrying
	// a bulk insert's worth of literals outgrew the request that carried it.
	// The published statement is cut at maxSqlSize, so parameters well past
	// that fill placeholders the web never shows anyway. A longer list is
	// dropped whole, never cut (see SetSQL).
	maxSqlParamSize = maxSqlSize
	// maxErrorMessageSize abbreviates error messages to 256 chars before
	// recording them on a span or span event.
	maxErrorMessageSize = 256
	// maxExceptionMessageSize bounds the message of one exception metadata
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

// globalAgent is an atomic pointer rather than a plain interface variable:
// plugins call GetAgent on every request while NewAgent and Shutdown swap the
// value, and an unsynchronized two-word interface write can be torn - a reader
// could pair one implementation's itab with another's data pointer.
// globalAgentLock serializes the writers, so two concurrent NewAgent calls
// cannot both pass the already-created check and leak the loser's agent.
var (
	globalAgent     atomic.Pointer[Agent]
	globalAgentLock sync.Mutex
)

// ErrAgentAlreadyCreated is returned by NewAgent, together with the existing
// agent, when an agent exists already. An application whose agent may have
// been created before its own NewAgent call - by the compile-time
// instrumentation tool's bootstrap, or by another package - checks for it
// with errors.Is and keeps the returned agent instead of failing.
var ErrAgentAlreadyCreated = errors.New("agent is already created")

// GetAgent returns a global Agent created by NewAgent, or NoopAgent before
// NewAgent ran. It is safe to call before this package's init function ran
// (see initPackage).
func GetAgent() Agent {
	if a := globalAgent.Load(); a != nil {
		return *a
	}
	initPackage()
	return *globalAgent.Load()
}

func setGlobalAgent(a Agent) {
	globalAgent.Store(&a)
}

// NewAgent creates an Agent and spawns goroutines that manage spans and statistical data.
// The generated Agent is maintained globally and only one instance is retained.
// The provided config is generated by NewConfig and an error is returned if it is nil.
//
// example:
//
//	opts := []pinpoint.ConfigOption{
//	  pinpoint.WithAppName("GoTestApp"),
//	  pinpoint.WithServiceName("GoTestService"),
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
		return a, ErrAgentAlreadyCreated
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

	agent := newAgentStruct(config)
	agent.stopSignal()

	config.logCallbackOnce.Do(func() {
		config.AddReloadCallback([]string{CfgLogLevel, CfgLogOutput, CfgLogMaxSize, CfgLogMaxBackups}, func() { logger.reload(config) })
	})

	if !config.offGrpc {
		agent.connectWg.Add(1)
		go agent.connectGrpcServer()
	}
	setGlobalAgent(agent)
	return agent, nil
}

// newAgentStruct builds an agent on config: its identity, queues and metadata
// caches. NewAgent and NewTestAgent share it and differ only in what they
// connect it to.
func newAgentStruct(config *Config) *agent {
	agent := &agent{
		appName:          config.objName.applicationName,
		appType:          int32(config.Int(CfgAppType)),
		appTypeStr:       strconv.Itoa(config.Int(CfgAppType)),
		agentID:          config.objName.agentID,
		agentName:        config.objName.agentName,
		serviceName:      config.objName.serviceName,
		clusterNamespace: resolveClusterNamespace(config),
		objName:          config.objName,
		startTime:        time.Now().UnixMilli(),
		spanQueue:        newSpanQueue(config.Int(CfgSpanQueueSize)),
		metaChan:         make(chan any, config.Int(CfgCollectorGrpcSenderQueueSize)),
		urlStatChan:      make(chan *urlStat, config.Int(CfgHttpUrlStatQueueSize)),
		statChan:         make(chan *pb.PStatMessage, config.Int(CfgStatQueueSize)),
		config:           config,
		stats:            newAgentStats(),
		urlStats:         newUrlStats(config),
	}
	// The SQL caches are sized by SQL.CacheSize; the api and error caches keep
	// the shared default. Config has already confined the value to
	// [1, maxSqlCacheSize].
	sqlCacheSize := config.Int(CfgSQLCacheSize)
	agent.sqlCacheLengthLimit = config.Int(CfgSQLCacheLengthLimit)
	agent.errorCache = newMetaCache[string, int32](cacheSize)
	agent.sqlCache = newMetaCache[sqlHash, int32](sqlCacheSize)
	agent.sqlUidCache = newMetaCache[sqlHash, []byte](sqlCacheSize)
	agent.sqlUidCache.ttl = time.Duration(config.Int(CfgSQLCacheExpireHours)) * time.Hour
	agent.rawSqlCache = newMetaCache[string, normalizedSql](sqlCacheSize)
	agent.apiCache = newMetaCache[apiCacheKey, int32](cacheSize)
	return agent
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
		if !agent.enable.cas(phaseRegistering, phaseFailed) {
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

	// Recovered like the workers are: a panic here - a dial option, the route
	// lookup, the metadata built from argv - must not take the host down, and
	// the release above needs the goroutine to get there.
	if !recoverPanic("connect", func() { err = agent.connect() }) && err == nil {
		err = errors.New("connect panicked")
	}
}

// connect opens the collector connections, registers the agent and starts the
// workers. A nil error with the phase still registering means the registration
// was cut short by Shutdown.
func (agent *agent) connect() (err error) {
	if agent.agentGrpc, err = newAgentGrpc(agent); err != nil {
		return err
	}
	if !agent.agentGrpc.registerAgentWithRetry() {
		return nil
	}
	if agent.spanGrpc, err = newSpanGrpc(agent); err != nil {
		return err
	}
	if agent.statGrpc, err = newStatGrpc(agent); err != nil {
		return err
	}
	if agent.cmdGrpc, err = newCommandGrpc(agent); err != nil {
		return err
	}

	// A Shutdown that completed while registration was finishing has moved the
	// phase to stopped; workers started now would only find it so and exit,
	// and the closed connections are already released by that Shutdown. The
	// refusal is that race's normal outcome, so it is not warned about.
	if !agent.enable.cas(phaseRegistering, phaseRunning) {
		return nil
	}
	agent.startWorkers(agent.workerTable())
	return nil
}

// worker declares one of the agent's worker goroutines: the name the
// supervisor logs it under, the body it runs, and the condition under which
// this agent starts it. The names are part of the log contract - the
// troubleshooting guide reads the "start <name> goroutine" and "restart <name>
// goroutine" lines by these values - so they must not change.
type worker struct {
	name string
	body func()
	// start is false for a worker this agent's config leaves out.
	start bool
}

// workerTable is the one place the agent's workers are declared, each with its
// start condition: agent info refresh runs only for a positive refresh
// interval. The connections (agentGrpc, spanGrpc, statGrpc, cmdGrpc) are
// deliberately not in the table: they are closed by closeGrpc under a nil guard
// rather than supervised, and nothing counts them.
func (agent *agent) workerTable() []worker {
	refreshInterval := agent.agentInfoRefreshInterval()
	return []worker{
		{name: "ping", body: agent.sendPingWorker, start: true},
		{name: "span batch", body: agent.sendSpanBatchWorker, start: true},
		{name: "command", body: agent.runCommandService, start: true},
		{name: "meta", body: agent.sendMetaWorker, start: true},
		{name: "collect agent stat", body: agent.collectAgentStatWorker, start: true},
		{name: "collect uri stat", body: agent.collectUrlStatWorker, start: true},
		{name: "send uri stat", body: agent.sendUrlStatWorker, start: true},
		{name: "send stats", body: agent.sendStatsWorker, start: true},
		{
			name:  "agent info refresh",
			body:  func() { agent.refreshAgentInfoWorker(refreshInterval) },
			start: refreshInterval > 0,
		},
	}
}

// startWorkers starts every worker whose start flag is set, one supervised
// goroutine each. workerWg is incremented per worker, right before its go
// statement, so the count matches the goroutines by construction: a count that
// drifted would either make every Shutdown wait out its full deadline or panic
// the WaitGroup, and the compiler catches neither.
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
		if w.start {
			states = append(states, &workerState{name: w.name, done: make(chan struct{})})
		}
	}
	agent.workerStates = states
	for _, w := range workers {
		if !w.start {
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

// workerRestartDelay paces the restart of a worker whose body panicked, so a
// deterministic bug cannot spin the supervisor hot. A variable so tests can
// shorten it.
var workerRestartDelay = 1 * time.Second

// superviseWorker runs body as one of the agent's worker goroutines and owns
// its workerWg slot, releasing it once on the final exit. A panic in body is
// recovered - the agent must never take the host process down - and the
// worker is restarted after workerRestartDelay, unless the agent is stopping,
// in which case the panic ends the worker like a normal return would.
func (agent *agent) superviseWorker(name string, body func()) {
	defer agent.workerWg.Done()
	if st := agent.workerStateOf(name); st != nil {
		st.running.Store(true)
		defer close(st.done)
		defer st.running.Store(false)
	}

	for {
		if recoverPanic(name, body) {
			return
		}
		if !agent.workerContinues() {
			return
		}
		if !sleepUnlessStopped(agent, workerRestartDelay) {
			Log("agent").Infof("%s goroutine stopping, not restarted", name)
			return
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

// refreshAgentInfoWorker re-sends AgentInfo every refresh interval. A failed
// cycle waits for the next interval and never affects the agent's enabled state.
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
	// A running agent enters stopping, where the request path stops recording -
	// tracingEnabled is running-only, so NewSpanTracer hands out noop tracers
	// from here on - while workerContinues keeps the workers draining what was
	// queued before the signal, until shutdownAgent moves the agent to stopped.
	// An agent that never ran - still registering, or failed - has nothing to
	// drain and goes straight to stopped, as the shutdown flag alone did.
	agent.enable.beginShutdown()
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
// The goroutine it parks on wg.Wait outlives a timeout, which is why
// shutdownAgent uses waitWorkers instead; the span batch drain and the tests
// use it.
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
	// Flush the url stat tick in progress before anything is signalled. Both url
	// stat workers and sendStatsWorker stop on stopCtx, so a flush issued after
	// the signal could land on a queue nobody reads any more: enqueueing here
	// puts the tick in statChan before sendStatsWorker sees the stop, and that
	// worker drains the queue once on the stop, which is what actually gets the
	// last tick out. The drain first aggregates what the request path queued and
	// collectUrlStatWorker has not consumed, so the flush sees it. Skipped for
	// an agent that never ran - it has no workers and no stat queue.
	if agent.tracingEnabled() {
		agent.drainUrlStatChan()
		agent.flushUrlStat(true)
	}

	// Signal before waiting on connectWg, never after: registration retries for
	// as long as the collector is unreachable, so a wait that runs first pays
	// its whole timeout during exactly the outage it was meant to survive. The
	// signal is what ends that loop - it cancels the in-flight RequestAgentInfo
	// and the back-off pause - which makes the wait below short on every path.
	agent.signalShutdown()
	Log("agent").Infof("shutdown pinpoint agent")

	// Wait for the grpc connection to complete.
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
	if !agent.enable.cas(phaseStopping, phaseStopped) {
		return
	}

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
	// send lands after it. signalShutdown() above already cancelled stopCtx:
	// metadata and stat senders drain their queued records before exiting,
	// within the same worker deadline as the span sender.
	agent.spanQueue.close()

	// To terminate the listening state of the command stream, close the command
	// grpc channel first.
	if agent.cmdGrpc != nil {
		agent.cmdGrpc.close()
	}

	// Bound the drain: a collector outage must not keep the process alive.
	// Abandoned workers are unblocked by the connection close below. On an
	// overrun, name the workers still running so the deadline can be
	// investigated; the in-time path logs nothing extra.
	if !agent.waitWorkers(shutdownTimeout) {
		Log("agent").Warnf("shutdown timeout(%v) exceeded, abandon in-flight workers: %s",
			shutdownTimeout, strings.Join(agent.runningWorkerNames(), ", "))
	}

	// Url stat records that arrived after the final flush above - still in
	// the channel, or aggregated by the worker's own stop drain - have no
	// send left to carry them. Counted rather than lost in silence.
	if agent.urlStatChan != nil {
		late := agent.drainUrlStatChan()
		if snapshot := agent.urlStats.takeSnapshot(true); !snapshot.isEmpty() {
			late += len(snapshot.urlMap)
		}
		if late > 0 {
			agent.urlStatDrops.record(int64(late))
			Log("agent").Warnf("%d url stat record(s) arrived after the shutdown flush and are lost", late)
		}
	}

	// The stat queues report from their producers, so a burst that ended
	// inside the rate limit's interval is still held back, and nothing reports
	// after this: the last report bypasses the limit. The url stat total
	// includes the late records above.
	agent.statDrops.reportAt.Store(0)
	agent.statDrops.report("stat", cap(agent.statChan))
	agent.urlStatDrops.reportAt.Store(0)
	agent.urlStatDrops.report("url stat", cap(agent.urlStatChan))
}

// drainUrlStatChan aggregates every record queued so far without blocking and
// reports how many it took.
func (agent *agent) drainUrlStatChan() int {
	n := 0
	for {
		select {
		case uri := <-agent.urlStatChan:
			agent.urlStats.add(uri)
			n++
		default:
			return n
		}
	}
}

func (agent *agent) NewSpanTracer(operation string, rpcName string) Tracer {
	return agent.NewSpanTracerWithReader(operation, rpcName, &noopDistributedTracingContextReader{})
}

func (agent *agent) NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	// NewSpanTracer delegates here: the SpanStart hook sees every root tracer.
	return hookSpanStart(agent.newSpanTracerWithReader(operation, rpcName, reader))
}

func (agent *agent) newSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	if !agent.tracingEnabled() || reader == nil {
		return NoopTracer()
	}

	sampled, _ := reader.Get(HeaderSampled)
	if sampled == "s0" {
		agent.stats.incr(statUnSampleCont)
		return newUnSampledSpan(agent, rpcName)
	}

	sampler := agent.config.load().sampler
	// Continuation bypasses the base sampling rate, so only pick it for headers
	// extract will actually continue, including the cluster namespace check.
	// extract calls it again, which is cheaper than widening its signature.
	decide := sampler.isNewSampled
	if _, continued := continueHeaders(reader, agent.clusterNamespace); continued {
		decide = sampler.isContinueSampled
	}
	if !decide(agent.stats) {
		return newUnSampledSpan(agent, rpcName)
	}
	tracer := newSampledSpan(agent, operation, rpcName)
	tracer.extract(reader)
	return tracer
}

func (agent *agent) generateTransactionId() TransactionId {
	// Use the value returned by Add: reading agent.sequence separately races
	// with concurrent increments and could hand two transactions the same
	// sequence (or a torn read on 32-bit).
	seq := agent.sequence.Add(1)
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
	// ended up holding, on every exit - including a panicked body that
	// superviseWorker restarts, and the phase reaching stopped between
	// iterations.
	defer func() { stream.close() }()

	for agent.workerContinues() {
		stream = renewIfExpired(stream, agent.agentGrpc.newPingStreamWithRetry, "ping")
		// An empty stream is what newPingStreamWithRetry hands back once
		// shutdown began during its wait: nothing to send on, and nothing to
		// report - the select below ends the worker.
		if stream.open() {
			if err := stream.sendPing(); err != nil {
				if !errors.Is(err, io.EOF) {
					Log("agent").Errorf("send ping - %v", err)
				}

				stream.close()
				stream = agent.agentGrpc.newPingStreamWithRetry()
			}
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

func (agent *agent) sendSpanBatchWorker() {
	Log("agent").Infof("start span batch goroutine")

	// Drain span chunks into unary SendSpanBatch requests.
	// The first chunk starts a batch, collectSpanBatch opportunistically gathers more chunks,
	// and sendSpanBatchAsync hands the batch to a bounded async sender.
	for {
		// Drops are reported once per cycle, before the wait. While spans flow
		// collectSpanBatch's collect deadline bounds a cycle, so the poll stays
		// regular; a total the rate limit holds back bounds the wait instead,
		// so a burst that ends in silence still gets its total logged.
		// ponytail: a send that fails once the worker is idle with nothing held
		// back waits for the next cycle or the exit report; bound the wait while
		// sends are in flight too if that lag matters.
		chunk, ok := agent.spanQueue.dequeue(agent.reportSpanDrops())
		if !ok {
			break
		}
		if chunk == nil {
			continue // the limit has lifted: report what it held back
		}

		batch, closed := agent.spanGrpc.collectSpanBatch(chunk, agent.spanQueue)
		agent.spanGrpc.sendSpanBatchAsync(batch)
		if closed {
			break
		}
	}

	// The span queue is closed during shutdown; wait for already accepted async batches
	// before the worker exits so queued spans get the same best-effort flush.
	agent.spanGrpc.awaitInFlightSpanBatch()
	// No later cycle will log what the rate limit holds back, so the last
	// report bypasses it - after the await, to count the sends that failed in it.
	agent.spanDrops.reportAt.Store(0)
	agent.reportSpanDrops()
	Log("agent").Infof("end span batch goroutine")
}

// reportSpanDrops warns about spans lost to a saturated span queue or by the
// batch sender, and returns how long the rate limit holds back a total it did
// not log. Called by the span batch worker once per cycle: producers only bump
// their shard's counter, so the clock read and the logging land on the
// consumer. The line keeps the two causes apart: an overflow is sized away
// with Span.QueueSize, a sender loss is a collector that is down, slow or
// refusing the request, and one "queue overflow" for both sent an operator to
// the wrong knob.
func (agent *agent) reportSpanDrops() time.Duration {
	overflow, sender := agent.spanQueue.dropCount(), agent.spanDrops.dropped.Load()
	held, ok := agent.spanDrops.acquire(overflow + sender)
	if !ok {
		return held
	}
	Log("agent").Warnf("span drops: %d in total, %d overwritten by span queue overflow (max queue size %d), "+
		"%d dropped by the batch sender (no permit, a failed or an oversize request)",
		overflow+sender, overflow, agent.spanQueue.capacity, sender)
	return 0
}

func (agent *agent) enqueueSpan(span *spanChunk) bool {
	if !agent.tracingEnabled() {
		return false
	}
	return agent.spanQueue.enqueue(span)
}

// validUTF8 replaces invalid UTF-8 sequences in s with the replacement rune.
// Plugins feed network- and user-origin bytes into string fields (percent-decoded
// URL paths, query DSLs, binary row keys, driver error strings), and protobuf
// rejects invalid UTF-8 string fields at marshal time: one bad string would fail
// the whole span, stat, or metadata message carrying it - and a failed span
// stream Send cancels the stream. Applied at the protobuf conversion boundary,
// off the application hot path; returns s unchanged (no copy) when valid.
//
// ValidString first: it checks ASCII eight bytes at a time, where
// ToValidUTF8 decodes rune by rune even when nothing needs replacing, and
// valid input is the case every call but a hostile one takes (2 KB ASCII:
// 45 ns against 1.3 us).
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// abbreviateString truncates str to at most length bytes plus a "...(n)" marker
// carrying the original byte length, which the limit alone does not tell a
// reader. The cut lands on a rune boundary: protobuf rejects invalid UTF-8
// string fields at marshal time, so a mid-rune cut would fail the whole span or
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

func (agent *agent) enqueueUrlStat(stat *urlStat) bool {
	if !agent.tracingEnabled() {
		return false
	}
	queued, dropped := headDropEnqueue(agent.urlStatChan, stat)
	if dropped > 0 {
		agent.urlStatDrops.record(dropped)
		agent.urlStatDrops.report("url stat", cap(agent.urlStatChan))
	}
	return queued
}

func (agent *agent) collectUrlStatWorker() {
	Log("agent").Infof("start collect uri stat goroutine")

	stop := agent.stopSignal().Done()

	for agent.workerContinues() {
		select {
		case <-stop:
			// Final drain, so a record that was queued before the stop is
			// aggregated rather than left in the channel.
			agent.drainUrlStatChan()
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

	// A completed tick is sent as soon as urlStats closes it (completedWake);
	// the ticker is only the ceiling on the trailing tick of an agent whose
	// traffic stopped, which nothing arrives to close. It rides the agent stat
	// interval rather than a second timer of its own. Read once:
	// Stat.CollectInterval is not reloadable.
	interval := time.Duration(agent.config.Int(CfgStatCollectInterval)) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	stop := agent.stopSignal().Done()
	completed := agent.urlStats.completedWake

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
// An empty snapshot is not enqueued: there is nothing for the collector to
// store, and the sender would otherwise wake for nothing.
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
	// stat is a time series, so the newest sample is the one worth keeping.
	queued, dropped := headDropEnqueue(agent.statChan, stat)
	if dropped > 0 {
		agent.statDrops.record(dropped)
		// Reported here rather than in sendStatsWorker: the worker only
		// reaches its report after pulling from the queue, so a collector
		// outage parks it in newStatStreamWithRetry and silences the warning
		// for exactly the stretch where the drops happen. Same policy as
		// enqueueUrlStat.
		agent.statDrops.report("stat", cap(agent.statChan))
	}
	return queued
}

func (agent *agent) sendStatsWorker() {
	Log("agent").Infof("start send stats goroutine")

	stream := agent.statGrpc.newStatStreamWithRetry()
	defer func() { stream.close() }()

	stop := agent.stopSignal().Done()

	for {
		var stats *pb.PStatMessage
		select {
		case <-stop:
			// Drain what is already queued before leaving: shutdownAgent
			// enqueues the last url stat tick and then cancels stopCtx, so
			// both cases are ready together here, and a select picks between
			// ready cases at random. One pass over the queue, no waiting - a
			// record enqueued after this returns is dropped with the channel,
			// which shutdownAgent never closes.
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
}

// sendStatsOrReopen sends stats on stream and returns the stream to keep
// using: the same one, or its replacement when the send broke it.
//
// A failed send is usually how the worker learns the collector already closed
// the stream (io.EOF), so stats is sent once more on the replacement instead
// of being lost with the dead stream. If that fails too it is dropped, and the
// next send replaces the stream again. The empty stream returned while
// stopping is not retried on.
func (agent *agent) sendStatsOrReopen(stream *statStream, stats *pb.PStatMessage) *statStream {
	stream = renewIfExpired(stream, agent.statGrpc.newStatStreamWithRetry, "stat")
	// An empty stream is what newStatStreamWithRetry hands back once shutdown
	// began during its wait. The stat is dropped, as the rest of the queue
	// then is, and not reported as a send failure: during a collector outage
	// every queued stat logged one.
	if !stream.open() {
		return stream
	}
	err := stream.sendStats(stats)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			Log("stats").Errorf("send stats - %v", err)
		}

		stream.close()
		stream = agent.statGrpc.newStatStreamWithRetry()
		if stream.open() {
			if err := stream.sendStats(stats); err != nil && !errors.Is(err, io.EOF) {
				Log("stats").Errorf("resend stats - %v", err)
			}
		}
	}
	return stream
}

// NewTestAgent makes a running agent that talks to no collector, for tests:
// spans are queued and dropped, nothing is sent. It sets the global agent as
// NewAgent does. The *testing.T of v1 was never used; dropping it keeps the
// testing package out of production binaries.
func NewTestAgent(config *Config) (Agent, error) {
	config.offGrpc = true
	logger.setup(config)

	if config.objName == nil {
		if err := config.checkNameAndID(); err != nil {
			// Tests may omit required identity fields, such as the ServiceName
			// the default v4 requires; fall back to a v3 identity so the header
			// builder has a non-nil object name.
			agentID := ""
			if uid, err := uuid.NewV7(); err == nil {
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

	agent := newAgentStruct(config)

	// offGrpc keeps connectGrpcServer - and every worker it starts - from
	// running, so no caller ever reaches the clients. A bare struct is enough
	// to keep the field non-nil and keeps the canned mocks out of the shipped
	// library.
	agent.agentGrpc = &agentGrpc{agent: agent}

	setGlobalAgent(agent)
	agent.enable.cas(phaseRegistering, phaseRunning)

	return agent, nil
}
