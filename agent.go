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

type Agent struct {
	config     Config
	startTime  int64
	sequence   int64
	agentGrpc  *agentGrpc
	spanGrpc   *spanGrpc
	statGrpc   *statGrpc
	spanBuffer []*span
	spanChan   chan *span
	wg         sync.WaitGroup
	sampler    traceSampler

	exceptionIdCache *lru.Cache
	exceptionIdGen   int32
	sqlCache         *lru.Cache
	sqlIdGen         int32
	apiCache         *lru.Cache
	apiIdGen         int32
	metaMutex        sync.Mutex

	enable bool
}

func NewAgent(config *Config) (*Agent, error) {
	if config == nil {
		return nil, errors.New("configuration is missing")
	}

	agent := Agent{}
	agent.config = *config
	agent.startTime = time.Now().UnixNano() / int64(time.Millisecond)
	agent.sequence = 0

	log("agent").Info("config= ", agent.config.String())
	logger.SetLevel(agent.config.LogLevel)
	if config.LogLevel > logrus.InfoLevel {
		logger.SetReportCaller(true)
	}

	var err error
	agent.agentGrpc, err = newAgentGrpc(&agent)
	if err != nil {
		return nil, err
	}

	agent.spanGrpc, err = newSpanGrpc(&agent)
	if err != nil {
		return nil, err
	}
	agent.spanChan = make(chan *span, 10*1024)

	agent.statGrpc, err = newStatGrpc(&agent)
	if err != nil {
		return nil, err
	}

	agent.exceptionIdGen = 0
	agent.exceptionIdCache, err = lru.New(cacheSize)
	if err != nil {
		return nil, err
	}

	agent.sqlIdGen = 0
	agent.sqlCache, err = lru.New(cacheSize)
	if err != nil {
		return nil, err
	}

	agent.apiIdGen = asyncApiId
	agent.apiCache, err = lru.New(cacheSize)
	if err != nil {
		return nil, err
	}

	baseSampler := newRateSampler(uint64(config.Sampling.Rate))
	if config.Sampling.NewThroughput > 0 || config.Sampling.ContinueThroughput > 0 {
		agent.sampler = newThroughputLimitTraceSampler(baseSampler, config.Sampling.NewThroughput, config.Sampling.ContinueThroughput)
	} else {
		agent.sampler = newBasicTraceSampler(baseSampler)
	}

	agent.enable = true
	agent.agentGrpc.sendAgentInfo()
	agent.agentGrpc.sendApiMetadata(asyncApiId, "Asynchronous Invocation", -1, ApiTypeInvocation)

	go agent.sendPingWorker()
	go agent.sendSpanWorker()
	go collectStats(&agent)
	agent.wg.Add(3)

	return &agent, nil
}

func (agent *Agent) Shutdown() {
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

func (agent *Agent) NewSpanTracer(operation string) Tracer {
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

func (agent *Agent) NewSpanTracerWithReader(operation string, reader DistributedTracingContextReader) Tracer {
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

func (agent *Agent) RegisterSpanApiId(descriptor string, apiType int) int32 {
	if !agent.enable {
		return 0
	}

	id := agent.cacheSpanApiId(descriptor, apiType)
	return id
}

func (agent *Agent) generateTransactionId() TransactionId {
	return TransactionId{agent.config.AgentId, agent.startTime, agent.sequence}
}

func (agent *Agent) sendPingWorker() {
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

		time.Sleep(5 * time.Second)
	}

	stream.close()
	log("agent").Info("ping goroutine finish")
}

func (agent *Agent) sendSpanWorker() {
	log("agent").Info("span goroutine start")
	defer agent.wg.Done()
	stream := agent.spanGrpc.newSpanStreamWithRetry()

	for span := range agent.spanChan {
		if !agent.enable {
			continue
		}

		err := stream.sendSpan(span)
		if err != nil {
			log("agent").Errorf("fail to sendSpan(): %v", err)
			stream.close()
			stream = agent.spanGrpc.newSpanStreamWithRetry()
		}
	}

	stream.close()
	log("agent").Info("span goroutine finish")
}

func (agent *Agent) tryEnqueueSpan(span *span) bool {
	if !agent.enable {
		return false
	}

	select {
	case agent.spanChan <- span:
		return true
	default:
		return false
	}
}

func (agent *Agent) cacheErrorFunc(funcname string) int32 {
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

	agent.metaMutex.Lock()
	defer agent.metaMutex.Unlock()
	agent.agentGrpc.sendStringMetadata(id, funcname)

	log("agent").Info("cache exception id: ", id, funcname)
	return id
}

func (agent *Agent) cacheSql(sql string) int32 {
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

	agent.metaMutex.Lock()
	defer agent.metaMutex.Unlock()
	agent.agentGrpc.sendSqlMetadata(id, sql)

	log("agent").Info("cache sql id: ", id, sql)
	return id
}

func (agent *Agent) cacheSpanApiId(descriptor string, apiType int) int32 {
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

	agent.metaMutex.Lock()
	defer agent.metaMutex.Unlock()
	agent.agentGrpc.sendApiMetadata(id, descriptor, -1, apiType)

	log("agent").Info("cache api id: ", id, key)
	return id
}
