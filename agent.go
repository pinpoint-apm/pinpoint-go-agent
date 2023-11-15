package pinpoint

import (
	"errors"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lru "github.com/hashicorp/golang-lru"
)

func init() {
	initLogger()
	initConfig()
	initGoroutine()
	globalAgent = NoopAgent()
}

type agent struct {
	appName   string
	appType   int32
	agentID   string
	agentName string

	startTime   int64
	sequence    int64
	agentGrpc   *agentGrpc
	spanGrpc    *spanGrpc
	statGrpc    *statGrpc
	cmdGrpc     *cmdGrpc
	spanChan    chan *span
	metaChan    chan interface{}
	urlStatChan chan *urlStat
	statChan    chan *pb.PStatMessage
	sampler     traceSampler

	errorCache *lru.Cache
	errorIdGen int32
	sqlCache   *lru.Cache
	sqlIdGen   int32
	apiCache   *lru.Cache
	apiIdGen   int32

	config   *Config
	wg       sync.WaitGroup
	enable   bool
	shutdown bool
}

type apiMeta struct {
	id         int32
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
)

var globalAgent Agent

// GetAgent returns a global Agent created by NewAgent.
func GetAgent() Agent {
	return globalAgent
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
	if globalAgent != NoopAgent() {
		return globalAgent, errors.New("agent is already created")
	}
	if config == nil {
		return NoopAgent(), errors.New("configuration is missing")
	}

	logger.setup(config)
	if err := config.checkNameAndID(); err != nil {
		return NoopAgent(), err
	}
	if !config.Bool(CfgEnable) {
		return NoopAgent(), nil
	}

	Log("agent").Infof("new pinpoint agent")
	config.printConfigString()

	agent := &agent{
		appName:     config.String(CfgAppName),
		appType:     int32(config.Int(CfgAppType)),
		agentID:     config.String(CfgAgentID),
		agentName:   config.String(CfgAgentName),
		startTime:   time.Now().UnixNano() / int64(time.Millisecond),
		spanChan:    make(chan *span, config.Int(CfgSpanQueueSize)),
		metaChan:    make(chan interface{}, config.Int(CfgSpanQueueSize)),
		urlStatChan: make(chan *urlStat, config.Int(CfgSpanQueueSize)),
		statChan:    make(chan *pb.PStatMessage, config.Int(CfgSpanQueueSize)),
		config:      config,
	}

	var err error
	if agent.errorCache, err = lru.New(cacheSize); err != nil {
		return NoopAgent(), err
	}
	if agent.sqlCache, err = lru.New(cacheSize); err != nil {
		return NoopAgent(), err
	}
	if agent.apiCache, err = lru.New(cacheSize); err != nil {
		return NoopAgent(), err
	}

	agent.newSampler()
	samplingOpts := []string{CfgSamplingType, CfgSamplingCounterRate, CfgSamplingPercentRate, CfgSamplingNewThroughput, CfgSamplingContinueThroughput}
	config.AddReloadCallback(samplingOpts, agent.newSampler)
	config.AddReloadCallback([]string{CfgLogLevel}, logger.reloadLevel)
	config.AddReloadCallback([]string{CfgLogOutput, CfgLogMaxSize}, logger.reloadOutput)

	if !config.offGrpc {
		go agent.connectGrpcServer()
	}
	globalAgent = agent
	return agent, nil
}

func (agent *agent) newSampler() {
	config := agent.config
	var baseSampler sampler
	if config.String(CfgSamplingType) == samplingTypeCounter {
		baseSampler = newRateSampler(config.Int(CfgSamplingCounterRate))
	} else {
		baseSampler = newPercentSampler(config.Float(CfgSamplingPercentRate))
	}

	if config.Int(CfgSamplingNewThroughput) > 0 || config.Int(CfgSamplingContinueThroughput) > 0 {
		agent.sampler = newThroughputLimitTraceSampler(baseSampler, config.Int(CfgSamplingNewThroughput),
			config.Int(CfgSamplingContinueThroughput))
	} else {
		agent.sampler = newBasicTraceSampler(baseSampler)
	}
}

func (agent *agent) connectGrpcServer() {
	var err error

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

	agent.enable = true
	go agent.sendPingWorker()
	go agent.sendSpanWorker()
	go agent.runCommandService()
	go agent.sendMetaWorker()
	go agent.collectAgentStatWorker()
	go agent.collectUrlStatWorker()
	go agent.sendUrlStatWorker()
	go agent.sendStatsWorker()
	agent.wg.Add(8)
}

func (agent *agent) Shutdown() {
	agent.shutdown = true
	Log("agent").Infof("shutdown pinpoint agent")

	if !agent.enable {
		return
	}

	agent.enable = false
	globalAgent = NoopAgent()

	close(agent.spanChan)
	close(agent.metaChan)
	close(agent.urlStatChan)
	close(agent.statChan)

	//To terminate the listening state of the command stream,
	//close the command grpc channel first
	if agent.cmdGrpc != nil {
		agent.cmdGrpc.close()
	}

	agent.wg.Wait()

	if agent.agentGrpc != nil {
		agent.agentGrpc.close()
	}
	if agent.spanGrpc != nil {
		agent.spanGrpc.close()
	}
	if agent.statGrpc != nil {
		agent.statGrpc.close()
	}
}

func (agent *agent) NewSpanTracer(operation string, rpcName string) Tracer {
	var tracer Tracer

	if agent.enable {
		reader := &noopDistributedTracingContextReader{}
		tracer = agent.NewSpanTracerWithReader(operation, rpcName, reader)
	} else {
		tracer = NoopTracer()
	}
	return tracer
}

func (agent *agent) NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	if !agent.enable || reader == nil {
		return NoopTracer()
	}

	sampled := reader.Get(HeaderSampled)
	if sampled == "s0" {
		incrUnSampleCont()
		return newUnSampledSpan(agent, rpcName)
	}

	tid := reader.Get(HeaderTraceId)
	if tid == "" {
		return agent.samplingSpan(func() bool { return agent.sampler.isNewSampled() }, operation, rpcName, reader)
	} else {
		return agent.samplingSpan(func() bool { return agent.sampler.isContinueSampled() }, operation, rpcName, reader)
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
	atomic.AddInt64(&agent.sequence, 1)
	return TransactionId{agent.agentID, agent.startTime, agent.sequence}
}

func (agent *agent) Enable() bool {
	return agent.enable
}

func (agent *agent) Config() *Config {
	return agent.config
}

func (agent *agent) sendPingWorker() {
	Log("agent").Infof("start ping goroutine")
	defer agent.wg.Done()
	stream := agent.agentGrpc.newPingStreamWithRetry()

	for agent.enable {
		err := stream.sendPing()
		if err != nil {
			if err != io.EOF {
				Log("agent").Errorf("send ping - %v", err)
			}

			stream.close()
			stream = agent.agentGrpc.newPingStreamWithRetry()
		}

		time.Sleep(60 * time.Second)
	}

	stream.close()
	Log("agent").Infof("end ping goroutine")
}

func (agent *agent) sendSpanWorker() {
	Log("agent").Infof("start span goroutine")
	defer agent.wg.Done()

	var (
		skipOldSpan  = bool(false)
		skipBaseTime time.Time
	)

	stream := agent.spanGrpc.newSpanStreamWithRetry()
	for span := range agent.spanChan {
		if !agent.enable {
			break
		}

		if skipOldSpan {
			if span.startTime.Before(skipBaseTime) {
				continue //skip old span
			} else {
				skipOldSpan = false
			}
		}

		err := stream.sendSpan(span)
		if err != nil {
			if err != io.EOF {
				Log("agent").Errorf("send span - %v", err)
			}

			stream.close()
			stream = agent.spanGrpc.newSpanStreamWithRetry()

			skipOldSpan = true
			skipBaseTime = time.Now().Add(-time.Second * 1)
		}
	}

	stream.close()
	Log("agent").Infof("end span goroutine")
}

func (agent *agent) enqueueSpan(span *span) bool {
	if !agent.enable {
		return false
	}

	select {
	case agent.spanChan <- span:
		return true
	default:
		break
	}

	<-agent.spanChan
	return false
}

func (agent *agent) sendMetaWorker() {
	Log("agent").Infof("start meta goroutine")
	defer agent.wg.Done()

	for md := range agent.metaChan {
		if !agent.enable {
			break
		}

		var success bool
		switch md.(type) {
		case apiMeta:
			api := md.(apiMeta)
			success = agent.agentGrpc.sendApiMetadataWithRetry(api.id, api.descriptor, -1, api.apiType)
			break
		case stringMeta:
			str := md.(stringMeta)
			success = agent.agentGrpc.sendStringMetadataWithRetry(str.id, str.funcName)
			break
		case sqlMeta:
			sql := md.(sqlMeta)
			success = agent.agentGrpc.sendSqlMetadataWithRetry(sql.id, sql.sql)
			break
		case exceptionMeta:
			em := md.(exceptionMeta)
			success = agent.agentGrpc.sendExceptionMetadataWithRetry(&em)
		}

		if !success {
			agent.deleteMetaCache(md)
		}
	}

	Log("agent").Infof("end meta goroutine")
}

func (agent *agent) deleteMetaCache(md interface{}) {
	switch md.(type) {
	case apiMeta:
		api := md.(apiMeta)
		key := api.descriptor + "_" + strconv.Itoa(api.apiType)
		agent.apiCache.Remove(key)
		break
	case stringMeta:
		agent.errorCache.Remove(md.(stringMeta).funcName)
		break
	case sqlMeta:
		agent.sqlCache.Remove(md.(sqlMeta).sql)
		break
	case exceptionMeta:
		break
	}
}

func (agent *agent) tryEnqueueMeta(md interface{}) bool {
	if !agent.enable {
		return false
	}

	select {
	case agent.metaChan <- md:
		return true
	default:
		break
	}

	<-agent.metaChan
	return false
}

func (agent *agent) cacheError(errorName string) int32 {
	if !agent.enable {
		return 0
	}

	if v, ok := agent.errorCache.Peek(errorName); ok {
		return v.(int32)
	}

	id := atomic.AddInt32(&agent.errorIdGen, 1)
	agent.errorCache.Add(errorName, id)

	md := stringMeta{}
	md.id = id
	md.funcName = errorName
	agent.tryEnqueueMeta(md)

	Log("agent").Infof("cache error id: %d, %s", id, errorName)
	return id
}

func (agent *agent) cacheSql(sql string) int32 {
	if !agent.enable {
		return 0
	}

	if v, ok := agent.sqlCache.Peek(sql); ok {
		return v.(int32)
	}

	id := atomic.AddInt32(&agent.sqlIdGen, 1)
	agent.sqlCache.Add(sql, id)

	md := sqlMeta{}
	md.id = id
	md.sql = sql
	agent.tryEnqueueMeta(md)

	Log("agent").Infof("cache sql id: %d, %s", id, sql)
	return id
}

func (agent *agent) cacheSpanApi(descriptor string, apiType int) int32 {
	if !agent.enable {
		return 0
	}

	key := descriptor + "_" + strconv.Itoa(apiType)

	if v, ok := agent.apiCache.Peek(key); ok {
		return v.(int32)
	}

	id := atomic.AddInt32(&agent.apiIdGen, 1)
	agent.apiCache.Add(key, id)

	md := apiMeta{}
	md.id = id
	md.descriptor = descriptor
	md.apiType = apiType
	agent.tryEnqueueMeta(md)

	Log("agent").Infof("cache api id: %d, %s", id, key)
	return id
}

func (agent *agent) enqueueExceptionMeta(span *span) {
	if !agent.enable || !agent.config.errorTraceCallStack {
		return
	}

	exps := make([]*exception, 0)
	for _, se := range span.spanEvents {
		if se.callStack != nil {
			exps = append(exps, &exception{
				exceptionId: span.exceptionId,
				callstack:   se.callStack,
			})
		}
	}
	if len(exps) < 1 {
		return
	}

	md := exceptionMeta{
		txId:       span.txId,
		spanId:     span.spanId,
		exceptions: exps,
	}
	if span.urlStat != nil {
		md.uriTemplate = span.urlStat.Url
	} else {
		md.uriTemplate = "NULL"
	}

	agent.tryEnqueueMeta(md)
	Log("agent").Debugf("enqueue exception meta: %v", md)
}

func (agent *agent) enqueueUrlStat(stat *urlStat) bool {
	if !agent.enable {
		return false
	}

	select {
	case agent.urlStatChan <- stat:
		return true
	default:
		break
	}

	<-agent.urlStatChan
	Log("agent").Tracef("url stat channel - max capacity reached or closed")
	return false
}

func (agent *agent) collectUrlStatWorker() {
	Log("agent").Infof("start collect uri stat goroutine")
	defer agent.wg.Done()

	agent.initUrlStat()

	for uri := range agent.urlStatChan {
		if !agent.enable {
			break
		}
		snapshot := agent.currentUrlStatSnapshot()
		snapshot.add(uri)
	}

	Log("agent").Infof("end collect uri stat goroutine")
}

func (agent *agent) sendUrlStatWorker() {
	Log("agent").Infof("start send uri stat goroutine")
	defer agent.wg.Done()

	interval := 30 * time.Second
	time.Sleep(interval)

	for agent.enable {
		if agent.config.collectUrlStat {
			snapshot := agent.takeUrlStatSnapshot()
			agent.enqueueStat(makePAgentUriStat(snapshot))
		}
		time.Sleep(interval)
	}

	Log("agent").Infof("end send uri stat goroutine")
}

func (agent *agent) enqueueStat(stat *pb.PStatMessage) bool {
	select {
	case agent.statChan <- stat:
		return true
	default:
		break
	}

	<-agent.statChan
	return false
}

func (agent *agent) sendStatsWorker() {
	Log("agent").Infof("start send stats goroutine")
	defer agent.wg.Done()

	stream := agent.statGrpc.newStatStreamWithRetry()
	for stats := range agent.statChan {
		if !agent.enable {
			break
		}

		err := stream.sendStats(stats)
		if err != nil {
			if err != io.EOF {
				Log("stats").Errorf("send stats - %v", err)
			}

			stream.close()
			stream = agent.statGrpc.newStatStreamWithRetry()
		}
	}
	stream.close()

	Log("agent").Infof("end send stats goroutine")
}

func NewTestAgent(config *Config, t *testing.T) (Agent, error) {
	config.offGrpc = true
	logger.setup(config)

	agent := &agent{
		appName:     config.String(CfgAppName),
		appType:     int32(config.Int(CfgAppType)),
		agentID:     config.String(CfgAgentID),
		agentName:   config.String(CfgAgentName),
		startTime:   time.Now().UnixNano() / int64(time.Millisecond),
		spanChan:    make(chan *span, config.Int(CfgSpanQueueSize)),
		metaChan:    make(chan interface{}, config.Int(CfgSpanQueueSize)),
		urlStatChan: make(chan *urlStat, config.Int(CfgSpanQueueSize)),
		statChan:    make(chan *pb.PStatMessage, config.Int(CfgSpanQueueSize)),
		config:      config,
	}
	agent.errorCache, _ = lru.New(cacheSize)
	agent.sqlCache, _ = lru.New(cacheSize)
	agent.apiCache, _ = lru.New(cacheSize)
	agent.newSampler()

	agent.agentGrpc = newMockAgentGrpc(agent, t)
	//agent.spanGrpc = newMockSpanGrpc(agent, t)
	//agent.statGrpc = newMockStatGrpc(agent, t)

	globalAgent = agent
	agent.enable = true

	return agent, nil
}
