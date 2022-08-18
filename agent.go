package pinpoint

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/sirupsen/logrus"
	prefixed "github.com/x-cray/logrus-prefixed-formatter"
)

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
	cmdGrpc    *cmdGrpc
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

	httpStatusErrors           *httpStatusError
	httpUrlFilter              *httpUrlFilter
	httpRequestHeaderRecorder  httpHeaderRecorder
	httpResponseHeaderRecorder httpHeaderRecorder
	httpCookieRecorder         httpHeaderRecorder

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

	agent.apiIdGen = 0
	agent.apiCache, err = lru.New(cacheSize)
	if err != nil {
		return &agent, err
	}

	var baseSampler sampler
	if config.Sampling.Type == SamplingTypeCounter {
		baseSampler = newRateSampler(config.Sampling.Rate)
	} else {
		baseSampler = newPercentSampler(config.Sampling.PercentRate)
	}

	if config.Sampling.NewThroughput > 0 || config.Sampling.ContinueThroughput > 0 {
		agent.sampler = newThroughputLimitTraceSampler(baseSampler, config.Sampling.NewThroughput,
			config.Sampling.ContinueThroughput)
	} else {
		agent.sampler = newBasicTraceSampler(baseSampler)
	}

	agent.httpStatusErrors = newHttpStatusError(config)
	agent.httpUrlFilter = newHttpUrlFilter(config)

	agent.httpRequestHeaderRecorder = makeHttpHeaderRecoder(config.Http.RecordRequestHeader)
	agent.httpResponseHeaderRecorder = makeHttpHeaderRecoder(config.Http.RecordRespondHeader)
	agent.httpCookieRecorder = makeHttpHeaderRecoder(config.Http.RecordRequestCookie)

	if !config.OffGrpc {
		go connectGrpc(&agent)
	}
	return &agent, nil
}

func makeHttpHeaderRecoder(cfg []string) httpHeaderRecorder {
	if len(cfg) == 0 {
		return newNoopHttpHeaderRecoder()
	} else if strings.EqualFold(cfg[0], "HEADERS-ALL") {
		return newAllHttpHeaderRecoder()
	} else {
		return newDefaultHttpHeaderRecoder(cfg)
	}
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

		agent.cmdGrpc, err = newCommandGrpc(agent)
		if err != nil {
			agent.agentGrpc.close()
			agent.spanGrpc.close()
			agent.statGrpc.close()
			continue
		}

		break
	}

	for true {
		res, err := agent.agentGrpc.sendAgentInfo()
		if err == nil {
			if res.Success {
				break
			} else {
				log("agent").Errorf("agent registration failed: %s", res.Message)
				return
			}
		}
		time.Sleep(1 * time.Second)
	}

	agent.enable = true
	go agent.sendPingWorker()
	go agent.sendSpanWorker()
	go agent.sendStatsWorker()
	go agent.runCommandService()
	go agent.sendMetaWorker()

	agent.wg.Add(5)
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
		incrUnsampleCont()
		return newUnSampledSpan(rpcName)
	}

	tid := reader.Get(HttpTraceId)
	if tid == "" {
		return agent.samplingSpan(agent.sampler.isNewSampled, operation, rpcName, reader)
	} else {
		return agent.samplingSpan(agent.sampler.isContinueSampled, operation, rpcName, reader)
	}
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
	atomic.AddInt64(&agent.sequence, 1)
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
			if err != io.EOF {
				log("agent").Errorf("fail to sendPing(): %v", err)
			}

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
				log("agent").Errorf("fail to sendSpan(): %v", err)
			}

			spanStream.close()
			spanStream = agent.spanGrpc.newSpanStreamWithRetry()

			skipOldSpan = true
			skipBaseTime = time.Now().Add(-time.Second * 1)
		}
	}

	spanStream.close()
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
			agent.deleteMetaCache(md)
		}
	}

	log("agent").Info("meta goroutine finish")
}

func (agent *agent) deleteMetaCache(md interface{}) {
	switch md.(type) {
	case apiMeta:
		api := md.(apiMeta)
		key := api.descriptor + "_" + strconv.Itoa(api.apiType)
		agent.apiCache.Remove(key)
		break
	case stringMeta:
		agent.exceptionIdCache.Remove(md.(stringMeta).funcname)
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

func (agent *agent) CacheErrorFunc(funcName string) int32 {
	if !agent.enable {
		return 0
	}

	if v, ok := agent.exceptionIdCache.Get(funcName); ok {
		return v.(int32)
	}

	id := atomic.AddInt32(&agent.exceptionIdGen, 1)
	agent.exceptionIdCache.Add(funcName, id)

	md := stringMeta{}
	md.id = id
	md.funcname = funcName
	agent.tryEnqueueMeta(md)

	log("agent").Info("cache exception id: ", id, funcName)
	return id
}

func (agent *agent) CacheSql(sql string) int32 {
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

	log("agent").Info("cache sql id: ", id, sql)
	return id
}

func (agent *agent) CacheSpanApiId(descriptor string, apiType int) int32 {
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

	log("agent").Info("cache api id: ", id, key)
	return id
}

func (agent *agent) IsHttpError(code int) bool {
	return agent.httpStatusErrors.isError(code)
}

func (agent *agent) IsExcludedUrl(url string) bool {
	return agent.httpUrlFilter.isFiltered(url)
}

func (agent *agent) IsExcludedMethod(method string) bool {
	for _, em := range agent.config.Http.ExcludeMethod {
		if strings.EqualFold(em, method) {
			return true
		}
	}
	return false
}

func (agent *agent) HttpHeaderRecorder(key int) httpHeaderRecorder {
	if key == AnnotationHttpRequestHeader {
		return agent.httpRequestHeaderRecorder
	} else if key == AnnotationHttpResponseHeader {
		return agent.httpResponseHeaderRecorder
	} else if key == AnnotationHttpCookie {
		return agent.httpCookieRecorder
	} else {
		return newNoopHttpHeaderRecoder()
	}
}
