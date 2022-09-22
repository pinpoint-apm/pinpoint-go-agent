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

	startTime  int64
	sequence   int64
	agentGrpc  *agentGrpc
	spanGrpc   *spanGrpc
	statGrpc   *statGrpc
	cmdGrpc    *cmdGrpc
	spanBuffer []*span
	spanChan   chan *span
	metaChan   chan interface{}
	wg         sync.WaitGroup
	sampler    traceSampler

	errorIdCache *lru.Cache
	errorIdGen   int32
	sqlCache     *lru.Cache
	sqlIdGen     int32
	apiCache     *lru.Cache
	apiIdGen     int32

	enable bool
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
	noopAgent := NoopAgent()

	if config == nil {
		return noopAgent, errors.New("configuration is missing")
	}
	if globalAgent != noopAgent {
		return globalAgent, errors.New("agent is already created")
	}

	agent := agent{}
	agent.appName = config.String(cfgAppName)
	agent.appType = int32(config.Int(cfgAppType))
	agent.agentID = config.String(cfgAgentID)

	agent.startTime = time.Now().UnixNano() / int64(time.Millisecond)
	agent.sequence = 0

	var err error
	agent.spanChan = make(chan *span, 5*1024)
	agent.metaChan = make(chan interface{}, 1*1024)

	agent.errorIdGen = 0
	agent.errorIdCache, err = lru.New(cacheSize)
	if err != nil {
		return &agent, err
	}

	agent.sqlIdGen = 0
	agent.sqlCache, err = lru.New(cacheSize)
	if err != nil {
		return &agent, err
	}

	agent.apiIdGen = 0
	agent.apiCache, err = lru.New(cacheSize)
	if err != nil {
		return &agent, err
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

	if !offGrpc {
		go connectGrpc(&agent, config)
	}

	globalAgent = &agent
	return globalAgent, nil
}

func connectGrpc(agent *agent, config *Config) {
	var err error

	for {
		agent.agentGrpc, err = newAgentGrpc(agent, config)
		if err != nil {
			continue
		}

		agent.spanGrpc, err = newSpanGrpc(agent, config)
		if err != nil {
			agent.agentGrpc.close()
			continue
		}

		agent.statGrpc, err = newStatGrpc(agent, config)
		if err != nil {
			agent.agentGrpc.close()
			agent.spanGrpc.close()
			continue
		}

		agent.cmdGrpc, err = newCommandGrpc(agent, config)
		if err != nil {
			agent.agentGrpc.close()
			agent.spanGrpc.close()
			agent.statGrpc.close()
			continue
		}

		break
	}

	if !registerAgent(agent) {
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

func registerAgent(agent *agent) bool {
	retry := 1
	for {
		res, err := agent.agentGrpc.sendAgentInfo()
		if err == nil {
			if res.Success {
				break
			} else {
				Log("agent").Errorf("fail to register agent - %s", res.Message)
				return false
			}
		}

		time.Sleep(backOffSleep(retry))
		retry++
	}

	return true
}

func (agent *agent) Shutdown() {
	if !agent.enable {
		return
	}

	agent.enable = false
	globalAgent = NoopAgent()
	time.Sleep(1 * time.Second)

	close(agent.spanChan)
	close(agent.metaChan)

	if !offGrpc {
		agent.wg.Wait()
		agent.agentGrpc.close()
		agent.spanGrpc.close()
		agent.statGrpc.close()
		agent.cmdGrpc.close()
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

func (agent *agent) samplingSpan(samplingFunc func() bool, operation string, rpcName string,
	reader DistributedTracingContextReader) Tracer {
	if samplingFunc() {
		tracer := newSampledSpan(agent, operation, rpcName)
		tracer.Extract(reader)
		return tracer
	} else {
		return newUnSampledSpan(rpcName)
	}
}

func (agent *agent) NewSpanTracerWithReader(operation string, rpcName string,
	reader DistributedTracingContextReader) Tracer {
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

func (agent *agent) StartTime() int64 {
	return agent.startTime
}

func (agent *agent) ApplicationName() string {
	return agent.appName
}

func (agent *agent) ApplicationType() int32 {
	return agent.appType
}

func (agent *agent) AgentID() string {
	return agent.agentID
}

func (agent *agent) sendPingWorker() {
	Log("agent").Info("ping goroutine start")
	defer agent.wg.Done()
	stream := agent.agentGrpc.newPingStreamWithRetry()

	for {
		if !agent.enable {
			break
		}

		err := stream.sendPing()
		if err != nil {
			if err != io.EOF {
				Log("agent").Errorf("fail to send ping - %v", err)
			}

			stream.close()
			stream = agent.agentGrpc.newPingStreamWithRetry()
			continue
		}

		time.Sleep(60 * time.Second)
	}

	stream.close()
	Log("agent").Info("ping goroutine finish")
}

func (agent *agent) sendSpanWorker() {
	Log("agent").Info("span goroutine start")
	defer agent.wg.Done()

	var (
		skipOldSpan  = bool(false)
		skipBaseTime time.Time
	)

	spanStream := agent.spanGrpc.newSpanStreamWithRetry()
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

		err := spanStream.sendSpan(span)
		if err != nil {
			if err != io.EOF {
				Log("agent").Errorf("fail to send span - %v", err)
			}

			spanStream.close()
			spanStream = agent.spanGrpc.newSpanStreamWithRetry()

			skipOldSpan = true
			skipBaseTime = time.Now().Add(-time.Second * 1)
		}
	}

	spanStream.close()
	Log("agent").Info("span goroutine finish")
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
	Log("agent").Info("meta goroutine start")
	defer agent.wg.Done()

	for md := range agent.metaChan {
		if !agent.enable {
			break
		}

		var err error
		switch md.(type) {
		case apiMeta:
			api := md.(apiMeta)
			err = agent.agentGrpc.sendApiMetadata(api.id, api.descriptor, -1, api.apiType)
			break
		case stringMeta:
			str := md.(stringMeta)
			err = agent.agentGrpc.sendStringMetadata(str.id, str.funcName)
			break
		case sqlMeta:
			sql := md.(sqlMeta)
			err = agent.agentGrpc.sendSqlMetadata(sql.id, sql.sql)
			break
		}

		if err != nil {
			agent.deleteMetaCache(md)
		}
	}

	Log("agent").Info("meta goroutine finish")
}

func (agent *agent) deleteMetaCache(md interface{}) {
	switch md.(type) {
	case apiMeta:
		api := md.(apiMeta)
		key := api.descriptor + "_" + strconv.Itoa(api.apiType)
		agent.apiCache.Remove(key)
		break
	case stringMeta:
		agent.errorIdCache.Remove(md.(stringMeta).funcName)
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

func (agent *agent) cacheErrorFunc(funcName string) int32 {
	if !agent.enable {
		return 0
	}

	if v, ok := agent.errorIdCache.Get(funcName); ok {
		return v.(int32)
	}

	id := atomic.AddInt32(&agent.errorIdGen, 1)
	agent.errorIdCache.Add(funcName, id)

	md := stringMeta{}
	md.id = id
	md.funcName = funcName
	agent.tryEnqueueMeta(md)

	Log("agent").Infof("cache exception id: %d, %s", id, funcName)
	return id
}

func (agent *agent) cacheSql(sql string) int32 {
	if !agent.enable {
		return 0
	}

	if v, ok := agent.sqlCache.Get(sql); ok {
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

func (agent *agent) cacheSpanApiId(descriptor string, apiType int) int32 {
	if !agent.enable {
		return 0
	}

	key := descriptor + "_" + strconv.Itoa(apiType)

	if v, ok := agent.apiCache.Get(key); ok {
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
