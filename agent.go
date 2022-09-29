package pinpoint

import (
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/sirupsen/logrus"
	prefixed "github.com/x-cray/logrus-prefixed-formatter"
)

func init() {
	initLogger()
	initConfig()
	globalAgent = NoopAgent()
}

var logger *logrus.Logger

func initLogger() {
	logger = logrus.New()
	formatter := new(prefixed.TextFormatter)
	formatter.TimestampFormat = "2006-01-02 15:04:05.000000"
	formatter.FullTimestamp = true
	formatter.ForceFormatting = true
	formatter.ForceColors = true
	logger.Formatter = formatter
}

func Log(src string) *logrus.Entry {
	return logger.WithFields(logrus.Fields{"module": "pinpoint", "src": src})
}

type agent struct {
	appName string
	appType int32
	agentID string

	startTime int64
	sequence  int64
	agentGrpc *agentGrpc
	spanGrpc  *spanGrpc
	statGrpc  *statGrpc
	cmdGrpc   *cmdGrpc
	spanChan  chan *span
	metaChan  chan interface{}
	sampler   traceSampler

	errorCache *lru.Cache
	errorIdGen int32
	sqlCache   *lru.Cache
	sqlIdGen   int32
	apiCache   *lru.Cache
	apiIdGen   int32

	wg       sync.WaitGroup
	enable   bool
	shutdown bool
	offGrpc  bool //for test
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

const cacheSize = 1024

var globalAgent Agent

func GetAgent() Agent {
	return globalAgent
}

func NewAgent(config *Config) (Agent, error) {
	if config == nil {
		return NoopAgent(), errors.New("configuration is missing")
	}
	if globalAgent != NoopAgent() {
		return globalAgent, errors.New("agent is already created")
	}

	Log("agent").Info("new pinpoint agent")
	config.printConfigString()

	agent := &agent{
		appName:   config.String(cfgAppName),
		appType:   int32(config.Int(cfgAppType)),
		agentID:   config.String(cfgAgentID),
		offGrpc:   config.offGrpc,
		startTime: time.Now().UnixNano() / int64(time.Millisecond),
		spanChan:  make(chan *span, 5*1024),
		metaChan:  make(chan interface{}, 1*1024),
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

	var baseSampler sampler
	if config.String(cfgSamplingType) == SamplingTypeCounter {
		baseSampler = newRateSampler(config.Int(cfgSamplingCounterRate))
	} else {
		baseSampler = newPercentSampler(config.Float(cfgSamplingPercentRate))
	}

	if config.Int(cfgSamplingNewThroughput) > 0 || config.Int(cfgSamplingContinueThroughput) > 0 {
		agent.sampler = newThroughputLimitTraceSampler(baseSampler, config.Int(cfgSamplingNewThroughput),
			config.Int(cfgSamplingContinueThroughput))
	} else {
		agent.sampler = newBasicTraceSampler(baseSampler)
	}

	if !agent.offGrpc {
		go connectGrpc(agent, config)
	}

	globalAgent = agent
	return agent, nil
}

func connectGrpc(agent *agent, config *Config) {
	var err error

	if agent.agentGrpc, err = newAgentGrpc(agent, config); err != nil {
		return
	}
	if !agent.agentGrpc.registerAgentWithRetry() {
		return
	}
	if agent.spanGrpc, err = newSpanGrpc(agent, config); err != nil {
		return
	}
	if agent.statGrpc, err = newStatGrpc(agent, config); err != nil {
		return
	}
	if agent.cmdGrpc, err = newCommandGrpc(agent, config); err != nil {
		return
	}

	agent.enable = true
	go agent.sendPingWorker()
	go agent.sendSpanWorker()
	go agent.sendStatsWorker(config)
	go agent.runCommandService()
	go agent.sendMetaWorker()

	agent.wg.Add(5)
}

func (agent *agent) Shutdown() {
	agent.shutdown = true
	Log("agent").Info("shutdown pinpoint agent")

	if !agent.enable {
		return
	}

	agent.enable = false
	globalAgent = NoopAgent()

	close(agent.spanChan)
	close(agent.metaChan)

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

func (agent *agent) samplingSpan(samplingFunc func() bool, operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	if samplingFunc() {
		tracer := newSampledSpan(agent, operation, rpcName)
		tracer.Extract(reader)
		return tracer
	} else {
		return newUnSampledSpan(rpcName)
	}
}

func (agent *agent) NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	if !agent.enable {
		return NoopTracer()
	}

	sampled := reader.Get(HttpSampled)
	if sampled == "s0" {
		incrUnSampleCont()
		return newUnSampledSpan(rpcName)
	}

	tid := reader.Get(HttpTraceId)
	if tid == "" {
		return agent.samplingSpan(agent.sampler.isNewSampled, operation, rpcName, reader)
	} else {
		return agent.samplingSpan(agent.sampler.isContinueSampled, operation, rpcName, reader)
	}
}

func (agent *agent) generateTransactionId() TransactionId {
	atomic.AddInt64(&agent.sequence, 1)
	return TransactionId{agent.agentID, agent.startTime, agent.sequence}
}

func (agent *agent) Enable() bool {
	return agent.enable
}

func (agent *agent) sendPingWorker() {
	Log("agent").Info("start ping goroutine")
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
			continue
		}

		time.Sleep(60 * time.Second)
	}

	stream.close()
	Log("agent").Info("end ping goroutine")
}

func (agent *agent) sendSpanWorker() {
	Log("agent").Info("start span goroutine")
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
	Log("agent").Info("end span goroutine")
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
	Log("agent").Info("start meta goroutine")
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
		}

		if !success {
			agent.deleteMetaCache(md)
		}
	}

	Log("agent").Info("end meta goroutine")
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
