package pinpoint

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/sirupsen/logrus"
	prefixed "github.com/x-cray/logrus-prefixed-formatter"
)

const asyncApiId = 1
const cacheSize = 1024

var logger *logrus.Logger

func init() {
	logger = logrus.New()
	formatter := new(prefixed.TextFormatter)
	formatter.TimestampFormat = "2006-01-02 15:04:05.000000"
	formatter.FullTimestamp = true
	formatter.ForceFormatting = true
	formatter.ForceColors = true
	logger.Formatter = formatter
}

func log(srcFile string) *logrus.Entry {
	return logger.WithFields(logrus.Fields{"module": "pinpoint", "src": srcFile})
}

type agent struct {
	config     Config
	startTime  int64
	sequence   int64
	agentGrpc  *agentGrpc
	spanGrpc   *spanGrpc
	statGrpc   *statGrpc
	spanBuffer []*span
	spanChan   chan *span
	metaChan   chan interface{}
	wg         sync.WaitGroup
	sampler    traceSampler

	exceptionIdCache *lru.Cache
	exceptionIdGen   int32
	sqlCache         *lru.Cache
	sqlIdGen         int32
	apiCache         *lru.Cache
	apiIdGen         int32

	spanStream         *spanStream
	spanStreamReq      bool
	spanStreamReqCount uint64

	statStream         *statStream
	statStreamReq      bool
	statStreamReqCount uint64

	enable bool
}

type apiMeta struct {
	id         int32
	descriptor string
	apiType    int
}

type stringMeta struct {
	id       int32
	funcname string
}

type sqlMeta struct {
	id  int32
	sql string
}

func NewAgent(config *Config) (Agent, error) {
	agent := agent{}

	if config == nil {
		return &agent, errors.New("configuration is missing")
	}

	agent.config = *config
	agent.startTime = time.Now().UnixNano() / int64(time.Millisecond)
	agent.sequence = 0

	log("agent").Info("config= ", agent.config.String())
	logger.SetLevel(agent.config.LogLevel)
	if config.LogLevel > logrus.InfoLevel {
		logger.SetReportCaller(true)
	}

	var err error
	agent.spanChan = make(chan *span, 5*1024)
	agent.metaChan = make(chan interface{}, 1*1024)

	agent.exceptionIdGen = 0
	agent.exceptionIdCache, err = lru.New(cacheSize)
	if err != nil {
		return &agent, err
	}

	agent.sqlIdGen = 0
	agent.sqlCache, err = lru.New(cacheSize)
	if err != nil {
		return &agent, err
	}

	agent.apiIdGen = asyncApiId
	agent.apiCache, err = lru.New(cacheSize)
	if err != nil {
		return &agent, err
	}

	baseSampler := newRateSampler(uint64(config.Sampling.Rate))
	if config.Sampling.NewThroughput > 0 || config.Sampling.ContinueThroughput > 0 {
		agent.sampler = newThroughputLimitTraceSampler(baseSampler, config.Sampling.NewThroughput, config.Sampling.ContinueThroughput)
	} else {
		agent.sampler = newBasicTraceSampler(baseSampler)
	}

	if !config.OffGrpc {
		go connectGrpc(&agent)
	}
	return &agent, nil
}

func connectGrpc(agent *agent) {
	var err error

	for true {
		agent.agentGrpc, err = newAgentGrpc(agent)
		if err != nil {
			continue
		}

		agent.spanGrpc, err = newSpanGrpc(agent)
		if err != nil {
			agent.agentGrpc.close()
			continue
		}

		agent.statGrpc, err = newStatGrpc(agent)
		if err != nil {
			agent.agentGrpc.close()
			agent.spanGrpc.close()
			continue
		}

		break
	}

	for true {
		err = agent.agentGrpc.sendAgentInfo()
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}

	for true {
		err = agent.agentGrpc.sendApiMetadata(asyncApiId, "Asynchronous Invocation", -1, ApiTypeInvocation)
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}

	agent.enable = true
	go agent.sendPingWorker()
	go agent.sendSpanWorker()
	go agent.sendStatsWorker()
	go agent.sendMetaWorker()

	agent.spanStreamReq = false
	agent.spanStreamReqCount = 0
	go agent.spanStreamMonitor()

	agent.statStreamReq = false
	agent.statStreamReqCount = 0
	go agent.statStreamMonitor()

	agent.wg.Add(6)
}

func (agent *agent) Shutdown() {
	if !agent.enable {
		return
	}

	agent.enable = false
	time.Sleep(1 * time.Second)

	close(agent.spanChan)
	agent.wg.Wait()

	agent.agentGrpc.close()
	agent.spanGrpc.close()
	agent.statGrpc.close()
}

func (agent *agent) NewSpanTracer(operation string) Tracer {
	var tracer Tracer

	if agent.enable {
		reader := &noopDistributedTracingContextReader{}
		tracer = agent.NewSpanTracerWithReader(operation, reader)
		tracer.Extract(reader)
	} else {
		tracer = newNoopSpan(agent)
	}
	return tracer
}

func (agent *agent) NewSpanTracerWithReader(operation string, reader DistributedTracingContextReader) Tracer {
	if !agent.enable {
		return newNoopSpan(agent)
	}

	atomic.AddInt64(&agent.sequence, 1)

	sampled := reader.Get(HttpSampled)
	if sampled == "s0" {
		incrUnsampleCont()
		return newNoopSpan(agent)
	}

	var tracer Tracer
	isSampled := false

	tid := reader.Get(HttpTraceId)
	if tid == "" {
		if agent.sampler.isNewSampled() {
			tracer = newSampledSpan(agent, operation)
			isSampled = true
		} else {
			tracer = newNoopSpan(agent)
		}
	} else {
		if agent.sampler.isContinueSampled() {
			tracer = newSampledSpan(agent, operation)
			isSampled = true
		} else {
			tracer = newNoopSpan(agent)
		}
	}

	if isSampled {
		tracer.Extract(reader)
	}
	return tracer
}

func (agent *agent) RegisterSpanApiId(descriptor string, apiType int) int32 {
	if !agent.enable {
		return 0
	}

	id := agent.CacheSpanApiId(descriptor, apiType)
	return id
}

func (agent *agent) Config() Config {
	return agent.config
}

func (agent *agent) GenerateTransactionId() TransactionId {
	return TransactionId{agent.config.AgentId, agent.startTime, agent.sequence}
}

func (agent *agent) Enable() bool {
	return agent.enable
}

func (agent *agent) StartTime() int64 {
	return agent.startTime
}

func (agent *agent) sendPingWorker() {
	log("agent").Info("ping goroutine start")
	defer agent.wg.Done()
	stream := agent.agentGrpc.newPingStreamWithRetry()

	for true {
		if !agent.enable {
			break
		}

		err := stream.sendPing()
		if err != nil {
			log("agent").Errorf("fail to sendPing(): %v", err)
			stream.close()
			stream = agent.agentGrpc.newPingStreamWithRetry()
		}

		time.Sleep(60 * time.Second)
	}

	stream.close()
	log("agent").Info("ping goroutine finish")
}

func (agent *agent) sendSpanWorker() {
	log("agent").Info("span goroutine start")
	defer agent.wg.Done()
	agent.spanStream = agent.spanGrpc.newSpanStreamWithRetry()

	for span := range agent.spanChan {
		if !agent.enable {
			break
		}

		agent.spanStreamReq = true
		err := agent.spanStream.sendSpan(span)
		agent.spanStreamReq = false
		agent.spanStreamReqCount++

		if err != nil {
			log("agent").Errorf("fail to sendSpan(): %v", err)
			agent.spanStream.close()
			agent.spanStream = agent.spanGrpc.newSpanStreamWithRetry()
		}
	}

	agent.spanStream.close()
	log("agent").Info("span goroutine finish")
}

func (agent *agent) TryEnqueueSpan(span *span) bool {
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

func (agent *agent) spanStreamMonitor() {
	for true {
		if !agent.enable {
			break
		}

		c := agent.spanStreamReqCount
		time.Sleep(5 * time.Second)

		if agent.spanStreamReq == true && c == agent.spanStreamReqCount {
			agent.spanStream.close()
		}
	}
}

func (agent *agent) statStreamMonitor() {
	for true {
		if !agent.enable {
			break
		}

		c := agent.statStreamReqCount
		time.Sleep(5 * time.Second)

		if agent.statStreamReq == true && c == agent.statStreamReqCount {
			agent.statStream.close()
		}
	}
}

func (agent *agent) sendMetaWorker() {
	log("agent").Info("meta goroutine start")
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
			err = agent.agentGrpc.sendStringMetadata(str.id, str.funcname)
			break
		case sqlMeta:
			sql := md.(sqlMeta)
			err = agent.agentGrpc.sendSqlMetadata(sql.id, sql.sql)
			break
		}

		if err != nil {
			log("agent").Errorf("fail to sendMetadata(): %v", err)
		}
	}

	log("agent").Info("meta goroutine finish")
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

func (agent *agent) CacheErrorFunc(funcname string) int32 {
	var id int32

	if !agent.enable {
		return -1
	}

	if agent.exceptionIdCache.Contains(funcname) {
		v, _ := agent.exceptionIdCache.Get(funcname)
		id = v.(int32)
		return id
	}

	id = atomic.AddInt32(&agent.exceptionIdGen, 1)
	agent.exceptionIdCache.Add(funcname, id)

	md := stringMeta{}
	md.id = id
	md.funcname = funcname
	agent.tryEnqueueMeta(md)

	log("agent").Info("cache exception id: ", id, funcname)
	return id
}

func (agent *agent) CacheSql(sql string) int32 {
	var id int32

	if !agent.enable {
		return -1
	}

	if agent.sqlCache.Contains(sql) {
		v, _ := agent.sqlCache.Get(sql)
		id = v.(int32)
		return id
	}

	id = atomic.AddInt32(&agent.sqlIdGen, 1)
	agent.sqlCache.Add(sql, id)

	md := sqlMeta{}
	md.id = id
	md.sql = sql
	agent.tryEnqueueMeta(md)

	log("agent").Info("cache sql id: ", id, sql)
	return id
}

func (agent *agent) CacheSpanApiId(descriptor string, apiType int) int32 {
	var id int32

	if !agent.enable {
		return -1
	}

	key := descriptor + "_" + strconv.Itoa(apiType)

	if agent.apiCache.Contains(key) {
		v, _ := agent.apiCache.Get(key)
		id = v.(int32)
		return id
	}

	id = atomic.AddInt32(&agent.apiIdGen, 1)
	agent.apiCache.Add(key, id)

	md := apiMeta{}
	md.id = id
	md.descriptor = descriptor
	md.apiType = apiType
	agent.tryEnqueueMeta(md)

	log("agent").Info("cache api id: ", id, key)
	return id
}
