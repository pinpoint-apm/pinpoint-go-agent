package pinpoint

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cast"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/time/rate"
)

// Config option keys
const (
	CfgAppName                             = "ApplicationName"
	CfgAppType                             = "ApplicationType"
	CfgAgentName                           = "AgentName"
	CfgCollectorHost                       = "Collector.Host"
	CfgCollectorAgentPort                  = "Collector.AgentPort"
	CfgCollectorSpanPort                   = "Collector.SpanPort"
	CfgCollectorStatPort                   = "Collector.StatPort"
	CfgCollectorAgentInfoRefreshInterval   = "Collector.AgentInfo.RefreshInterval"
	CfgCollectorAgentInfoSendRetryInterval = "Collector.AgentInfo.SendRetryInterval"
	CfgCollectorAgentInfoMaxTryPerAttempt  = "Collector.AgentInfo.MaxTryPerAttempt"

	// Collector.Grpc.* mirrors the C++ agent's channel option keys; time values
	// are in milliseconds without a "Ms" suffix, following the convention of
	// the other millisecond keys (Stat.CollectInterval, Span.BatchFlushInterval).
	CfgCollectorGrpcKeepAliveTime               = "Collector.Grpc.KeepAliveTime"
	CfgCollectorGrpcKeepAliveTimeout            = "Collector.Grpc.KeepAliveTimeout"
	CfgCollectorGrpcKeepAlivePermitWithoutCalls = "Collector.Grpc.KeepAlivePermitWithoutCalls"
	CfgCollectorGrpcMaxSendMessageSize          = "Collector.Grpc.MaxSendMessageSize"
	CfgCollectorGrpcMaxReceiveMessageSize       = "Collector.Grpc.MaxReceiveMessageSize"
	CfgCollectorGrpcFlowControlWindow           = "Collector.Grpc.FlowControlWindow"
	CfgCollectorGrpcWriteBufferSize             = "Collector.Grpc.WriteBufferSize"
	CfgCollectorGrpcMaxHeaderListSize           = "Collector.Grpc.MaxHeaderListSize"
	CfgCollectorGrpcSslEnable                   = "Collector.Grpc.SslEnable"
	CfgCollectorGrpcTrustCertFilePath           = "Collector.Grpc.TrustCertFilePath"
	// Connection and stream renewal, ported from the Java agent:
	//   ConnectionMaxAge <-> profiler.transport.grpc.loadbalancer.renew.period.millis
	//   StreamMaxAge     <-> profiler.transport.grpc.span.sender.rpc.age.max.millis
	// Both in milliseconds; 0 (the default) disables the renewal.
	CfgCollectorGrpcConnectionMaxAge = "Collector.Grpc.ConnectionMaxAge"
	CfgCollectorGrpcStreamMaxAge     = "Collector.Grpc.StreamMaxAge"
	// Channel idle timeout in milliseconds; 0 (the default) disables idling,
	// as the Java agent's ClientOption.idleTimeoutMillis effectively does. The
	// C++ agent's key is Collector.Grpc.IdleTimeoutMs.
	CfgCollectorGrpcIdleTimeout = "Collector.Grpc.IdleTimeout"
	// Collector.Grpc.SenderQueueSize sizes the metadata queue (metaChan), as
	// the C++ agent's key of the same name does. The retry schedule has its own
	// fixed bound (metaRetryQueueSize in grpc.go), not this one.
	CfgCollectorGrpcSenderQueueSize = "Collector.Grpc.SenderQueueSize"

	CfgLogLevelOld                    = "LogLevel"
	CfgLogLevel                       = "Log.Level"
	CfgLogOutput                      = "Log.Output"
	CfgLogMaxSize                     = "Log.MaxSize"
	CfgSamplingType                   = "Sampling.Type"
	CfgSamplingCounterRate            = "Sampling.CounterRate"
	CfgSamplingPercentRate            = "Sampling.PercentRate"
	CfgSamplingNewThroughput          = "Sampling.NewThroughput"
	CfgSamplingContinueThroughput     = "Sampling.ContinueThroughput"
	CfgSpanQueueSize                  = "Span.QueueSize"
	CfgSpanBatchEnable                = "Span.Batch.Enable"
	CfgSpanBatchSize                  = "Span.BatchSize"
	CfgSpanBatchFlushInterval         = "Span.BatchFlushInterval"
	CfgSpanBatchCollectDeadline       = "Span.BatchCollectDeadline"
	CfgSpanBatchMaxConcurrentRequests = "Span.BatchMaxConcurrentRequests"
	CfgSpanEventChunkSize             = "Span.EventChunkSize"
	CfgSpanMaxCallStackDepth          = "Span.MaxCallStackDepth"
	CfgSpanMaxCallStackSequence       = "Span.MaxCallStackSequence"
	CfgSpanIgnoreErrors               = "Span.IgnoreErrors"
	CfgSpanErrorMark                  = "Span.ErrorMark"
	CfgSpanErrorMarkExclude           = "Span.ErrorMarkExclude"
	CfgStatCollectInterval            = "Stat.CollectInterval"
	CfgStatBatchCount                 = "Stat.BatchCount"
	CfgStatQueueSize                  = "Stat.QueueSize"
	CfgIsContainerEnv                 = "IsContainerEnv"
	CfgConfigFile                     = "ConfigFile"
	CfgActiveProfile                  = "ActiveProfile"
	CfgSQLTraceBindValue              = "SQL.TraceBindValue"
	CfgSQLMaxBindValueSize            = "SQL.MaxBindValueSize"
	CfgSQLTraceCommit                 = "SQL.TraceCommit"
	CfgSQLTraceRollback               = "SQL.TraceRollback"
	CfgSQLTraceQueryStat              = "SQL.TraceQueryStat"
	CfgSQLEnableRawSqlCache           = "SQL.EnableRawSqlCache"
	CfgSQLCacheSize                   = "SQL.CacheSize"
	CfgSQLCacheLengthLimit            = "SQL.CacheLengthLimit"
	CfgSQLCacheExpireHours            = "SQL.CacheExpireHours"
	CfgSQLErrorCount                  = "SQL.ErrorCount"
	CfgSQLRemoveComments              = "SQL.RemoveComments"
	CfgEnable                         = "Enable"
	CfgHttpUrlStatEnable              = "Http.UrlStat.Enable"
	CfgHttpUrlStatLimitSize           = "Http.UrlStat.LimitSize"
	CfgHttpUrlStatQueueSize           = "Http.UrlStat.QueueSize"
	CfgHttpUrlStatWithMethod          = "Http.UrlStat.WithMethod"
	CfgErrorTraceCallStack            = "Error.TraceCallStack"
	CfgErrorCallStackDepth            = "Error.CallStackDepth"
	CfgErrorNewThroughput             = "Error.NewThroughput"
	CfgErrorMaxChainDepth             = "Error.MaxChainDepth"
	CfgUIDVersion                     = "Uid.Version"
	CfgServiceName                    = "ServiceName"
	CfgApiKey                         = "ApiKey"
)

const (
	cfgIdPattern        = "[a-zA-Z0-9\\._\\-]+"
	samplingTypeCounter = "COUNTER"
	samplingTypePercent = "PERCENT"
	// The Java agent's SamplerType names the counter sampler COUNTING, so a
	// config copied from a Java agent must not take the fallback path. Its
	// percent sampler is named PERCENT already and needs no alias.
	samplingTypeCounting = "COUNTING"

	defaultErrorCallStackDepth = 32
	// defaultErrorMaxChainDepth keeps the walk the agent has always done. Java
	// stops at 5 (profiler.exceptiontrace.max.depth); lowering this would drop
	// links applications already see, so it stays a knob, not a new default.
	defaultErrorMaxChainDepth = maxCauserDepth
	// New exception chains a second, like the Java agent's
	// profiler.exceptiontrace.new.throughput default.
	defaultErrorNewThroughput = 1000
	// Bound the per-error runtime.Callers allocation. This option is dynamic
	// and can come from a config file, so an accidental huge value must not turn
	// one instrumented error into an allocation spike or an integer-overflow
	// panic in make([]uintptr, depth+3).
	maxErrorCallStackDepth = 1024

	// Entries per SQL metadata cache (id, uid and raw), as in the Java agent
	// (profiler.jdbc.sqlcachesize, SimpleCacheFactory). Java sizes only its
	// SQL caches by that key; the api and error caches keep cacheSize.
	defaultSqlCacheSize = 1024
	// The upper bound keeps a typo from committing gigabytes at startup: each
	// of the three caches is bounded by this x SQL.CacheLengthLimit.
	maxSqlCacheSize = 65536

	// The traced bind value length, matching the Java agent
	// (profiler.jdbc.maxsqlbindvaluesize, DefaultJdbcOption). It bounds the
	// whole bind value list of one SQL span event, not each value.
	defaultSqlMaxBindValueSize = 1024
	// The ceiling on SQL.MaxBindValueSize, which used to be the default itself:
	// anything above 1024 was silently pulled back down, so raising the option
	// to read longer bind values did nothing. Neither the Java
	// (DefaultJdbcOption) nor the C++ (config.cpp) agent bounds the key at all,
	// and a user who asks for longer values should get them.
	//
	// Not unbounded, though. Bind values ride on the span itself, one
	// annotation per SQL span event, and the span send path - unlike the
	// metadata one, see sendExceptionMetadata - has no size guard: a span whose
	// serialized size passes Collector.Grpc.MaxSendMessageSize is rejected by
	// grpc-go at Send, and the whole span is lost, not just its bind values.
	// A sixteenth of the 4MiB default message size still leaves room for 16
	// span events carrying bind values at the limit, and is 256x the default -
	// past any real "let me see the long bind value" need.
	maxSqlBindValueSize = grpcMaxMessageSize / 16

	// SQL at or above this many bytes bypasses the SQL metadata caches, as in
	// the Java agent (profiler.jdbc.sqlcachelengthlimit, UidCache.bypassLength).
	defaultSqlCacheLengthLimit = 2048
	// defaultSqlCacheExpireHours matches the Java agent's
	// profiler.jdbc.sqlcacheexpirehours (SimpleCacheFactory, 7 days).
	defaultSqlCacheExpireHours = 168
	// 100 years. An expiry this long is already "never" in practice, and the
	// bound keeps hours * time.Hour from overflowing the Duration.
	maxSqlCacheExpireHours = 24 * 365 * 100

	// A span running this many queries is marked failed, as in the Java agent
	// (profiler.sql.error.count, DefaultSqlCountService). Java's separate
	// profiler.sql.error.enable collapses into a limit of 0 here.
	defaultSqlErrorCount = 100

	// Upper bounds for the queue sizes and stat collection settings, matching
	// the C++ agent. See publish.
	maxQueueSize           = 65536
	minStatCollectInterval = 1000
	maxStatCollectInterval = 60000
	maxStatBatchCount      = 100

	// defaultMetaQueueSize is the Java agent's
	// profiler.transport.grpc.metadata.sender.executor.queue.size default
	// (GrpcTransportConfig.DEFAULT_METADATA_SENDER_EXECUTOR_QUEUE_SIZE) and
	// the C++ agent's Collector.Grpc.SenderQueueSize default.
	defaultMetaQueueSize = 1000
)

// Config value type
const (
	CfgInt int = iota
	CfgFloat
	CfgBool
	CfgString
	CfgStringSlice
)

type cfgMapItem struct {
	value        interface{}
	defaultValue interface{}
	valueType    int
	cmdKey       string
	envKey       string
	dynamic      bool
	source       int
}

// Where a config value came from, in ascending precedence. A reload only
// restages keys whose source is at or below cfgSrcProfile.
const (
	cfgSrcDefault = iota // default or ConfigOption
	cfgSrcFile
	cfgSrcProfile
	cfgSrcEnv
	cfgSrcCmd
	cfgSrcAPI
)

var (
	cfgBaseMap map[string]*cfgMapItem
)

func initConfig() {
	cfgBaseMap = make(map[string]*cfgMapItem, 0)

	AddConfig(CfgAppName, CfgString, "", false)
	AddConfig(CfgAppType, CfgInt, ServiceTypeGoApp, false)
	AddConfig(CfgAgentName, CfgString, "", false)
	AddConfig(CfgCollectorHost, CfgString, "localhost", false)
	AddConfig(CfgCollectorAgentPort, CfgInt, 9991, false)
	AddConfig(CfgCollectorSpanPort, CfgInt, 9993, false)
	AddConfig(CfgCollectorStatPort, CfgInt, 9992, false)
	AddConfig(CfgCollectorAgentInfoRefreshInterval, CfgInt, defaultAgentInfoRefreshInterval, false)
	AddConfig(CfgCollectorAgentInfoSendRetryInterval, CfgInt, defaultAgentInfoSendRetryInterval, false)
	AddConfig(CfgCollectorAgentInfoMaxTryPerAttempt, CfgInt, defaultAgentInfoMaxTryPerAttempt, false)
	AddConfig(CfgCollectorGrpcKeepAliveTime, CfgInt, grpcKeepAliveTime, false)
	AddConfig(CfgCollectorGrpcKeepAliveTimeout, CfgInt, grpcKeepAliveTimeout, false)
	AddConfig(CfgCollectorGrpcKeepAlivePermitWithoutCalls, CfgBool, grpcKeepAlivePermitWithoutCalls, false)
	AddConfig(CfgCollectorGrpcMaxSendMessageSize, CfgInt, grpcMaxMessageSize, false)
	AddConfig(CfgCollectorGrpcMaxReceiveMessageSize, CfgInt, grpcMaxMessageSize, false)
	AddConfig(CfgCollectorGrpcFlowControlWindow, CfgInt, grpcFlowControlWindow, false)
	AddConfig(CfgCollectorGrpcWriteBufferSize, CfgInt, grpcWriteBufferSize, false)
	AddConfig(CfgCollectorGrpcMaxHeaderListSize, CfgInt, grpcMaxHeaderListSize, false)
	AddConfig(CfgCollectorGrpcSslEnable, CfgBool, false, false)
	AddConfig(CfgCollectorGrpcTrustCertFilePath, CfgString, "", false)
	AddConfig(CfgCollectorGrpcConnectionMaxAge, CfgInt, grpcConnectionMaxAge, false)
	AddConfig(CfgCollectorGrpcStreamMaxAge, CfgInt, grpcStreamMaxAge, false)
	AddConfig(CfgCollectorGrpcIdleTimeout, CfgInt, grpcIdleTimeout, false)
	AddConfig(CfgCollectorGrpcSenderQueueSize, CfgInt, defaultMetaQueueSize, false)
	AddConfig(CfgLogLevelOld, CfgString, "info", true)
	AddConfig(CfgLogLevel, CfgString, "info", true)
	AddConfig(CfgLogOutput, CfgString, "stderr", true)
	AddConfig(CfgLogMaxSize, CfgInt, 10, true)
	AddConfig(CfgSamplingType, CfgString, samplingTypeCounter, true)
	AddConfig(CfgSamplingCounterRate, CfgInt, 1, true)
	AddConfig(CfgSamplingPercentRate, CfgFloat, 100, true)
	AddConfig(CfgSamplingNewThroughput, CfgInt, 0, true)
	AddConfig(CfgSamplingContinueThroughput, CfgInt, 0, true)
	AddConfig(CfgSpanQueueSize, CfgInt, defaultQueueSize, false)
	AddConfig(CfgSpanBatchEnable, CfgBool, true, false)
	AddConfig(CfgSpanBatchSize, CfgInt, defaultSpanBatchSize, false)
	AddConfig(CfgSpanBatchFlushInterval, CfgInt, defaultSpanBatchFlushInterval, false)
	AddConfig(CfgSpanBatchCollectDeadline, CfgInt, defaultSpanBatchCollectDeadline, false)
	AddConfig(CfgSpanBatchMaxConcurrentRequests, CfgInt, defaultSpanBatchMaxConcurrentRequests, false)
	AddConfig(CfgSpanEventChunkSize, CfgInt, defaultEventChunkSize, true)
	AddConfig(CfgSpanMaxCallStackDepth, CfgInt, defaultEventDepth, true)
	AddConfig(CfgSpanMaxCallStackSequence, CfgInt, defaultEventSequence, true)
	AddConfig(CfgSpanIgnoreErrors, CfgStringSlice, []string{}, true)
	AddConfig(CfgSpanErrorMark, CfgStringSlice, []string{}, true)
	AddConfig(CfgSpanErrorMarkExclude, CfgStringSlice, []string{}, true)
	AddConfig(CfgStatCollectInterval, CfgInt, 5000, false)
	AddConfig(CfgStatBatchCount, CfgInt, 6, false)
	AddConfig(CfgStatQueueSize, CfgInt, defaultQueueSize, false)
	AddConfig(CfgIsContainerEnv, CfgBool, false, false)
	AddConfig(CfgConfigFile, CfgString, "", false)
	AddConfig(CfgActiveProfile, CfgString, "", false)
	AddConfig(CfgSQLTraceBindValue, CfgBool, true, true)
	AddConfig(CfgSQLMaxBindValueSize, CfgInt, defaultSqlMaxBindValueSize, true)
	AddConfig(CfgSQLTraceCommit, CfgBool, true, true)
	AddConfig(CfgSQLTraceRollback, CfgBool, true, true)
	AddConfig(CfgSQLTraceQueryStat, CfgBool, false, true)
	AddConfig(CfgSQLEnableRawSqlCache, CfgBool, true, true)
	AddConfig(CfgSQLCacheSize, CfgInt, defaultSqlCacheSize, false)
	AddConfig(CfgSQLCacheLengthLimit, CfgInt, defaultSqlCacheLengthLimit, true)
	AddConfig(CfgSQLCacheExpireHours, CfgInt, defaultSqlCacheExpireHours, false)
	AddConfig(CfgSQLErrorCount, CfgInt, defaultSqlErrorCount, true)
	AddConfig(CfgSQLRemoveComments, CfgBool, true, false)
	AddConfig(CfgEnable, CfgBool, true, false)
	AddConfig(CfgHttpUrlStatEnable, CfgBool, false, true)
	AddConfig(CfgHttpUrlStatLimitSize, CfgInt, 1024, true)
	AddConfig(CfgHttpUrlStatQueueSize, CfgInt, defaultQueueSize, false)
	AddConfig(CfgHttpUrlStatWithMethod, CfgBool, false, true)
	AddConfig(CfgErrorTraceCallStack, CfgBool, false, true)
	AddConfig(CfgErrorCallStackDepth, CfgInt, defaultErrorCallStackDepth, true)
	AddConfig(CfgErrorNewThroughput, CfgInt, defaultErrorNewThroughput, true)
	AddConfig(CfgErrorMaxChainDepth, CfgInt, defaultErrorMaxChainDepth, true)
	AddConfig(CfgUIDVersion, CfgString, "v3", false)
	AddConfig(CfgServiceName, CfgString, "", false)
	AddConfig(CfgApiKey, CfgString, "", false)
}

// AddConfig adds a configuration item.
//
// Call it only during package initialization (an init function), before any
// NewConfig call: it writes the unsynchronized package-global registry that
// NewConfig reads, so a concurrent call at runtime is a data race.
func AddConfig(cfgName string, valueType int, defaultValue interface{}, dynamic bool) {
	cfgBaseMap[cfgName] = &cfgMapItem{
		defaultValue: defaultValue,
		valueType:    valueType,
		cmdKey:       cmdName(cfgName),
		envKey:       envName(cfgName),
		dynamic:      dynamic,
	}
}

func cmdName(cfgName string) string {
	return "pinpoint-" + strings.ReplaceAll(strings.ToLower(cfgName), ".", "-")
}

func envName(cfgName string) string {
	return strings.ReplaceAll(strings.ToLower(cfgName), ".", "_")
}

// Config holds agent configuration, for passing to NewAgent.
//
// Everything a config file reload can change lives in an immutable
// configSnapshot behind an atomic pointer. The Config value itself is handed
// out by GetConfig and held for the process lifetime, so its identity is
// stable; only the snapshot it points at is replaced.
type Config struct {
	// mu guards the cfgMap staging area and the reload callback list. Readers
	// never take it - they load the published snapshot instead - so it is only
	// ever contended between startup and the config watcher goroutine.
	mu       sync.Mutex
	cfgMap   map[string]*cfgMapItem
	callback []reloadCallback
	snapshot atomic.Pointer[configSnapshot]
	// logCallbackOnce registers the logger's reload callbacks once per Config.
	// A Config can back several agents in turn, and NewAgent used to append the
	// same pair on every one, so a single file change ended up reopening the
	// log file once per agent this Config had ever served.
	logCallbackOnce sync.Once

	// watchMu owns the single restartable fsnotify watcher for this Config.
	watchMu       sync.Mutex
	watcher       *fsnotify.Watcher
	watcherDone   chan struct{}
	watcherClose  *sync.Once
	configFile    string
	configFileCfg *viper.Viper

	containerCheck bool
	useNewLogOpt   bool
	offGrpc        bool //for test
	objName        *objectName
}

// configSnapshot is the immutable view of the config that request goroutines
// read: the option values plus every component derived from them. A reload
// builds a whole new snapshot and publishes it with a single atomic store, so
// an in-flight request can never observe a half-applied reload.
type configSnapshot struct {
	values  map[string]interface{}
	sampler traceSampler
	// newExceptionLimiter caps how many new exception chains a second are
	// recorded, the counterpart of the Java agent's ExceptionChainSampler.
	// nil means unlimited.
	newExceptionLimiter *rate.Limiter

	collectUrlStat       bool              // CfgHttpUrlStatEnable
	urlStatLimitSize     int               // CfgHttpUrlStatLimitSize
	urlStatWithMethod    bool              // CfgHttpUrlStatWithMethod
	sqlTraceBindValue    bool              // CfgSQLTraceBindValue
	sqlMaxBindValueSize  int               // CfgSQLMaxBindValueSize
	sqlTraceCommit       bool              // CfgSQLTraceCommit
	sqlTraceRollback     bool              // CfgSQLTraceRollback
	sqlTraceQueryStat    bool              // CfgSQLTraceQueryStat
	sqlEnableRawSqlCache bool              // CfgSQLEnableRawSqlCache
	sqlCacheLengthLimit  int               // CfgSQLCacheLengthLimit
	sqlErrorCount        int               // CfgSQLErrorCount
	sqlRemoveComments    bool              // CfgSQLRemoveComments
	spanEventChunkSize   int               // CfgSpanEventChunkSize
	spanMaxEventDepth    int32             // CfgSpanMaxCallStackDepth
	spanMaxEventSequence int32             // CfgSpanMaxCallStackSequence
	errorTraceCallStack  bool              // CfgErrorTraceCallStack
	errorCallStackDepth  int               // CfgErrorCallStackDepth
	errorIgnoreRules     []ignoreErrorRule // CfgSpanIgnoreErrors
	errorMaxChainDepth   int               // CfgErrorMaxChainDepth
	// errorMarkMask is the set of ErrorCategory bits allowed to fail a
	// transaction, resolved from CfgSpanErrorMark and
	// CfgSpanErrorMarkExclude. Zero means "unset" and reads as every
	// category: a resolved mask always carries ErrorCategoryUnknown, so zero
	// can only come from a snapshot built by hand instead of by NewConfig
	// (emptyConfigSnapshot, tests), and every category is what Java's unset
	// profiler.error.mark gives.
	errorMarkMask ErrorCategory // CfgSpanErrorMark, CfgSpanErrorMarkExclude
}

// ignoreErrorRule is one parsed Span.IgnoreErrors entry, "<type>:<message>";
// an empty type or message matches anything. It is the Go counterpart of the
// Java agent's profiler.ignore-error-handler.<name>.class-name /
// .exception-message.contains descriptor pair.
type ignoreErrorRule struct {
	typeName        string
	messageContains string
}

func parseIgnoreErrorRules(entries []string) []ignoreErrorRule {
	rules := make([]ignoreErrorRule, 0, len(entries))
	for _, e := range entries {
		typ, msg, _ := strings.Cut(e, ":")
		typ, msg = strings.TrimSpace(typ), strings.TrimSpace(msg)
		if typ == "" && msg == "" {
			continue
		}
		rules = append(rules, ignoreErrorRule{typeName: typ, messageContains: msg})
	}
	return rules
}

// allErrorCategories is every cause a transaction can fail on, which is what
// an unset Span.ErrorMark enables - Java's EnumSet.allOf(ErrorCategory.class)
// fallback in ConfigurableErrorRecorderFactory.getEnabledTypes.
const allErrorCategories = ErrorCategoryUnknown | ErrorCategoryException |
	ErrorCategoryHttpStatus | ErrorCategorySql

// errorCategoryBit maps one Span.ErrorMark / Span.ErrorMarkExclude entry to
// its bit, 0 for an unrecognised one. The spellings are Java's
// (ConfigurableErrorRecorderFactory.toCategorySet), matched
// case-insensitively. ErrorCategoryUnknown has no spelling on purpose: it is
// never selectable, because it is always on.
func errorCategoryBit(token string) ErrorCategory {
	switch strings.ToLower(token) {
	case "exception":
		return ErrorCategoryException
	case "http-status":
		return ErrorCategoryHttpStatus
	case "sql":
		return ErrorCategorySql
	}
	return 0
}

// toErrorCategoryMask folds a category list into a mask. Each entry is split
// on commas as well, so Java's single comma-separated string works verbatim
// in a config file list too. An empty entry is skipped silently (Java's
// case ""); anything else unrecognised is a typo worth naming, since it
// silently widens or narrows which errors fail a transaction.
func toErrorCategoryMask(entries []string, cfgName string) ErrorCategory {
	var mask ErrorCategory
	for _, entry := range entries {
		for _, token := range strings.Split(entry, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			if bit := errorCategoryBit(token); bit != 0 {
				mask |= bit
			} else {
				Log("config").Warnf("%s = %q is not a valid error category, ignoring it", cfgName, token)
			}
		}
	}
	return mask
}

// parseErrorMarkMask resolves Span.ErrorMark and Span.ErrorMarkExclude into
// the mask of categories allowed to fail a transaction, the Go counterpart of
// Java's ConfigurableErrorRecorderFactory.getEnabledTypes. An empty mark
// enables every category, the way Java's unset profiler.error.mark does, and
// the exclude list is then removed from it.
func parseErrorMarkMask(mark []string, exclude []string) ErrorCategory {
	marked := allErrorCategories
	if len(mark) > 0 {
		marked = toErrorCategoryMask(mark, CfgSpanErrorMark)
	}
	excluded := toErrorCategoryMask(exclude, CfgSpanErrorMarkExclude)
	// ErrorCategoryUnknown survives every exclusion, exactly as Java re-adds
	// it after removing the excluded ones (getEnabledTypes). It is the
	// category of a failure whose cause was not classified, so excluding it
	// would amount to "never fail a transaction" - which is not what either
	// key is for.
	return (marked &^ excluded) | ErrorCategoryUnknown
}

// marksError reports whether category is allowed to fail a transaction, i.e.
// whether Span.ErrorMark and Span.ErrorMarkExclude left it enabled. Java
// applies the same test inside ConfigurableErrorRecorder.recordError, so an
// excluded category records nothing at all - not even ErrorCategoryUnknown.
func (snapshot *configSnapshot) marksError(category ErrorCategory) bool {
	if snapshot.errorMarkMask == 0 {
		return true // unset, see the field comment
	}
	return snapshot.errorMarkMask&category != 0
}

// ignoreError reports whether err, or any error it wraps (the nextCause chain,
// Cause() before Unwrap(), as the Java NestedErrorHandler walks getCause),
// matches a Span.IgnoreErrors rule. The walk stops after maxCauserDepth
// links: the chain comes from a user error whose Unwrap() may return itself or
// an ancestor, and this runs on the request goroutine inside SetError. Such an error is still recorded as exception info
// but does not mark the span as failed. The type part matches the dynamic type
// string (reflect.TypeOf(err).String()) or the errorName given to SetError.
func (snapshot *configSnapshot) ignoreError(err error, errName string) bool {
	if len(snapshot.errorIgnoreRules) == 0 {
		return false
	}
	for e, depth := err, 0; e != nil && depth < maxCauserDepth; e, depth = nextCause(e), depth+1 {
		// Computed on first use, per link: Error() on a wrapped chain allocates
		// and is O(depth) itself, and a rule matching on type alone, or on the
		// SetError name, never needs the message at all.
		var typ, msg string
		var haveTyp, haveMsg bool
		for _, r := range snapshot.errorIgnoreRules {
			if r.typeName != "" && r.typeName != errName {
				if !haveTyp {
					typ, haveTyp = reflect.TypeOf(e).String(), true
				}
				if r.typeName != typ {
					continue
				}
			}
			if r.messageContains != "" {
				if !haveMsg {
					msg, haveMsg = e.Error(), true
				}
				if !strings.Contains(msg, r.messageContains) {
					continue
				}
			}
			return true
		}
	}
	return false
}

// emptyConfigSnapshot stands in for a Config that was built by hand instead of
// by NewConfig, so its accessors keep returning zero values rather than panic.
var emptyConfigSnapshot = &configSnapshot{}

// load returns the currently published snapshot. Callers that must see a
// consistent set of options - a span, a single request - load it once and keep
// the returned pointer instead of calling load per field.
func (config *Config) load() *configSnapshot {
	if snapshot := config.snapshot.Load(); snapshot != nil {
		return snapshot
	}
	return emptyConfigSnapshot
}

// ConfigOption represents an option that can be passed to NewConfig.
type ConfigOption func(*Config)

// GetConfig returns a global Config created by NewConfig.
func GetConfig() *Config {
	return GetAgent().Config()
}

// Set stores the specified configuration item value.
// A value set here survives config file reloads. Setting a non-dynamic option
// updates the stored value only; the agent applies it after a restart.
// A value that does not convert to the option's type is rejected with a
// warning and the option keeps the value it already had.
func (config *Config) Set(cfgName string, value interface{}) {
	config.mu.Lock()
	defer config.mu.Unlock()

	if v, ok := config.cfgMap[cfgName]; ok {
		converted, err := convertCfgValue(v.valueType, value)
		if err != nil {
			Log("config").Warnf("%s = %v is not a valid %s, keeping %v",
				cfgName, value, cfgTypeName(v.valueType), v.value)
			return
		}
		if !v.dynamic {
			Log("config").Warnf("config %s is not dynamic: the new value takes effect after restart", cfgName)
		}
		v.value = converted
		v.source = cfgSrcAPI
		config.publish()
	}
}

// Int returns an integer value for the specified configuration item.
func (config *Config) Int(cfgName string) int {
	return cast.ToInt(config.load().values[cfgName])
}

// Float returns a float value for the specified configuration item.
func (config *Config) Float(cfgName string) float64 {
	return cast.ToFloat64(config.load().values[cfgName])
}

// String returns a string value for the specified configuration item.
func (config *Config) String(cfgName string) string {
	return cast.ToString(config.load().values[cfgName])
}

// StringSlice returns a string slice value for the specified configuration item.
// The returned slice belongs to the published snapshot - copy it before writing.
func (config *Config) StringSlice(cfgName string) []string {
	return cast.ToStringSlice(config.load().values[cfgName])
}

// Bool returns a boolean value for the specified configuration item.
func (config *Config) Bool(cfgName string) bool {
	return cast.ToBool(config.load().values[cfgName])
}

// staged reads a value straight out of the cfgMap staging area. Only the
// snapshot build path may use it: it bypasses the published snapshot, and the
// caller must hold config.mu.
func (config *Config) staged(cfgName string) interface{} {
	if v, ok := config.cfgMap[cfgName]; ok {
		return v.value
	}
	return nil
}

func (config *Config) stagedInt(cfgName string) int {
	return cast.ToInt(config.staged(cfgName))
}

func (config *Config) stagedString(cfgName string) string {
	return cast.ToString(config.staged(cfgName))
}

// NewConfig creates a Config populated with default settings, command line arguments,
// environment variables and the given config options.
// Config uses the following precedence order. Each item takes precedence over the item below it:
//  1. command line flag
//  2. environment variable
//  3. configuration file
//  4. ConfigOption
//  5. default
//
// configuration keys used in config files are case-insensitive.
// The generated Config is maintained globally.
// When a Config with a config file is not passed to NewAgent, the caller should
// call Close to stop its file watcher.
//
// example:
//
//	opts := []pinpoint.ConfigOption{
//	  pinpoint.WithAppName("GoTestApp"),
//	  pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
//	}
//	cfg, err := pinpoint.NewConfig(opts...)
func NewConfig(opts ...ConfigOption) (*Config, error) {
	config := defaultConfig()
	if opts != nil {
		for _, fn := range opts {
			fn(config)
		}
		config.mu.Lock()
		config.normalizeCfgValues()
		config.mu.Unlock()
	}

	cmdEnvViper := viper.New()
	flagSet := config.newFlagSet()
	if err := flagSet.Parse(filterCmdArgs()); err != nil {
		Log("config").Errorf("commad line config loading error: %v", err)
	}
	cmdEnvViper.BindPFlags(flagSet)
	cmdEnvViper.SetEnvPrefix("pinpoint_go")
	cmdEnvViper.AutomaticEnv()

	cfgFileViper := config.loadConfigFile(cmdEnvViper)

	config.mu.Lock()

	profileViper := config.loadProfile(cmdEnvViper, cfgFileViper)
	config.loadConfig(cmdEnvViper, cfgFileViper, profileViper)

	if config.containerCheck {
		config.cfgMap[CfgIsContainerEnv].value = isContainerEnv()
	}
	config.publish()
	config.mu.Unlock()

	config.startConfigWatcher()
	return config, nil
}

func defaultConfig() *Config {
	config := new(Config)
	config.cfgMap = make(map[string]*cfgMapItem, 0)
	for k, v := range cfgBaseMap {
		config.cfgMap[k] = &cfgMapItem{
			defaultValue: v.defaultValue,
			valueType:    v.valueType,
			cmdKey:       v.cmdKey,
			envKey:       v.envKey,
			dynamic:      v.dynamic,
		}
	}
	for _, v := range config.cfgMap {
		v.value = v.defaultValue
	}

	config.containerCheck = true
	config.callback = make([]reloadCallback, 0)

	config.mu.Lock()
	defer config.mu.Unlock()
	config.normalizeCfgValues()
	config.publish()

	return config
}

func (config *Config) newFlagSet() *pflag.FlagSet {
	flagSet := pflag.NewFlagSet("pinpoint_go_agent", pflag.ContinueOnError)

	for _, v := range config.cfgMap {
		switch v.valueType {
		case CfgInt:
			flagSet.Int(v.cmdKey, 0, "")
		case CfgFloat:
			flagSet.Float64(v.cmdKey, 0, "")
		case CfgBool:
			flagSet.Bool(v.cmdKey, false, "")
		case CfgString:
			flagSet.String(v.cmdKey, "", "")
		case CfgStringSlice:
			flagSet.StringSlice(v.cmdKey, nil, "")
		}
	}

	return flagSet
}

func filterCmdArgs() []string {
	cmdArgs := make([]string, 0)

	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--pinpoint-") {
			cmdArgs = append(cmdArgs, arg)
		}
	}
	return cmdArgs
}

func (config *Config) loadConfigFile(cmdEnvViper *viper.Viper) *viper.Viper {
	var cfgFile string

	item := config.cfgMap[CfgConfigFile]
	if cmdEnvViper.IsSet(item.cmdKey) {
		cfgFile = cmdEnvViper.GetString(item.cmdKey)
	} else if cmdEnvViper.IsSet(item.envKey) {
		cfgFile = cmdEnvViper.GetString(item.envKey)
	} else {
		cfgFile = item.value.(string)
	}

	cfgFileViper := viper.New()
	if cfgFile != "" {
		cfgFileViper.SetConfigFile(cfgFile)
		if err := cfgFileViper.ReadInConfig(); err != nil {
			Log("config").Errorf("config file loading error: %v", err)
		}
		config.configFile = cfgFile
		config.configFileCfg = cfgFileViper
	}

	return cfgFileViper
}

// Close stops the config file watcher and waits for its goroutine to exit. It
// is safe to call more than once; a later NewAgent with the same Config starts
// the watcher again.
//
// Close must not be called from a reload callback: callbacks run on the very
// goroutine Close waits for, so a callback that closes its own Config
// deadlocks and leaves the watcher lock held.
func (config *Config) Close() {
	config.watchMu.Lock()
	defer config.watchMu.Unlock()

	if config.watcher == nil {
		return
	}

	// Closing the watcher releases its descriptor right here; the wait below is
	// only for the goroutine, which may still be running a reload callback.
	// Callbacks are caller-supplied (see AddReloadCallback) and Close is on the
	// Shutdown path, so bound the wait the same way the worker drain is bounded
	// - a slow callback must not keep the process alive. The abandoned
	// goroutine reads a watcher that is already closed, so it only has to
	// finish the callback in flight before returning; it publishes nothing
	// after the timeout, having published before the callbacks ran.
	config.watcherClose.Do(func() { _ = config.watcher.Close() })
	timer := time.NewTimer(shutdownTimeout)
	select {
	case <-config.watcherDone:
	case <-timer.C:
		Log("config").Warnf("config file watcher shutdown timeout(%v) exceeded, abandon reload in progress", shutdownTimeout)
	}
	timer.Stop()

	config.watcher = nil
	config.watcherDone = nil
	config.watcherClose = nil
}

func (config *Config) startConfigWatcher() bool {
	config.watchMu.Lock()
	defer config.watchMu.Unlock()

	if config.configFileCfg == nil {
		return false
	}
	if config.watcher != nil {
		select {
		case <-config.watcherDone:
			config.watcher = nil
			config.watcherDone = nil
			config.watcherClose = nil
		default:
			return false
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		Log("config").Errorf("config file watcher creation error: %v", err)
		return false
	}

	configFile := filepath.Clean(config.configFile)
	configDir, _ := filepath.Split(configFile)
	if err := watcher.Add(configDir); err != nil {
		_ = watcher.Close()
		Log("config").Errorf("config file watcher start error: %v", err)
		return false
	}

	done := make(chan struct{})
	closeOnce := new(sync.Once)
	config.watcher = watcher
	config.watcherDone = done
	config.watcherClose = closeOnce
	go config.watchConfigFile(watcher, done, closeOnce, configFile, config.configFileCfg)
	return true
}

func (config *Config) watchConfigFile(watcher *fsnotify.Watcher, done chan struct{}, closeOnce *sync.Once, configFile string, cfgFileViper *viper.Viper) {
	defer close(done)
	defer closeOnce.Do(func() { _ = watcher.Close() })

	realConfigFile, _ := filepath.EvalSymlinks(configFile)
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			// A Remove of the config file is deliberately not an exit: the watch
			// is on the directory, so it stays valid, and unlink+rewrite savers
			// (editors, deploy tools) emit Remove then Create - returning on the
			// Remove silently ended dynamic reload for the rest of the process.
			currentConfigFile, _ := filepath.EvalSymlinks(configFile)
			const writeOrCreateMask = fsnotify.Write | fsnotify.Create
			if (filepath.Clean(event.Name) == configFile && event.Op&writeOrCreateMask != 0) ||
				(currentConfigFile != "" && currentConfigFile != realConfigFile) {
				realConfigFile = currentConfigFile
				config.reloadConfig(cfgFileViper)
			}
		case err, ok := <-watcher.Errors:
			if ok {
				Log("config").Errorf("config file watcher error: %v", err)
			}
			return
		}
	}
}

func (config *Config) loadProfile(cmdEnvViper *viper.Viper, cfgFileViper *viper.Viper) *viper.Viper {
	var profile string

	item := config.cfgMap[CfgActiveProfile]
	if cmdEnvViper.IsSet(item.cmdKey) {
		profile = cmdEnvViper.GetString(item.cmdKey)
	} else if cmdEnvViper.IsSet(item.envKey) {
		profile = cmdEnvViper.GetString(item.envKey)
	} else if cfgFileViper.IsSet(CfgActiveProfile) {
		profile = cfgFileViper.GetString(CfgActiveProfile)
	} else {
		profile = item.value.(string)
	}

	if profile != "" {
		profileViper := cfgFileViper.Sub("profile." + profile)
		if profileViper != nil {
			return profileViper
		} else {
			Log("config").Warnf("config file doesn't have the profile: %s", profile)
		}
	}

	return viper.New()
}

func (config *Config) loadConfig(cmdEnvViper *viper.Viper, cfgFileViper *viper.Viper, profileViper *viper.Viper) {
	sortKeys := make([]string, 0)
	for k := range config.cfgMap {
		sortKeys = append(sortKeys, k)
	}
	sort.Strings(sortKeys)
	for _, k := range sortKeys {
		v := config.cfgMap[k]
		if cmdEnvViper.IsSet(v.cmdKey) {
			config.setFinalValue(k, v, cmdEnvViper.Get(v.cmdKey), cfgSrcCmd)
		} else if cmdEnvViper.IsSet(v.envKey) {
			config.setFinalValue(k, v, cmdEnvViper.Get(v.envKey), cfgSrcEnv)
		} else if profileViper.IsSet(k) {
			config.setFinalValue(k, v, profileViper.Get(k), cfgSrcProfile)
		} else if cfgFileViper.IsSet(k) {
			config.setFinalValue(k, v, cfgFileViper.Get(k), cfgSrcFile)
		}
	}
}

// cfgTypeName names a value type for the log lines below.
func cfgTypeName(valueType int) string {
	switch valueType {
	case CfgInt:
		return "int"
	case CfgFloat:
		return "float"
	case CfgBool:
		return "bool"
	case CfgStringSlice:
		return "string slice"
	default:
		return "string"
	}
}

// convertCfgValue coerces a raw config value to the type its option was
// registered with, using the strict cast.To*E variants. The lenient cast.To*
// the accessors call turns anything it cannot parse into the zero value
// instead, so before this a typo like Sampling.CounterRate: "abc" became a rate
// of 0 - rateSampler.isSampled then returns false for every transaction, which
// is tracing switched off with nothing in the log to say so.
func convertCfgValue(valueType int, value interface{}) (interface{}, error) {
	switch valueType {
	case CfgInt:
		return cast.ToIntE(value)
	case CfgFloat:
		return cast.ToFloat64E(value)
	case CfgBool:
		return cast.ToBoolE(value)
	case CfgStringSlice:
		// A plain string is how a config file or an environment variable
		// spells a list, comma separated.
		if str, ok := value.(string); ok {
			return strings.Split(str, ","), nil
		}
		// cast.ToStringSliceE turns any other single value it can stringify
		// into a one-element slice, so a bare scalar where a list belongs -
		// a list written without its dashes, say - would pass as a one-entry
		// list instead of being reported. Only an actual sequence converts.
		if k := reflect.ValueOf(value).Kind(); k != reflect.Slice && k != reflect.Array {
			return nil, fmt.Errorf("unable to cast %#v of type %T to []string", value, value)
		}
		return cast.ToStringSliceE(value)
	default:
		return cast.ToStringE(value)
	}
}

// setFinalValue stages a value coming from a config file, a profile, an
// environment variable or a command line flag.
//
// A value that does not convert to the option's registered type is dropped
// with a warning and the option keeps what it already had, which is what the
// C++ agent does (get_yaml<T> in src/config.cpp logs "Failed to read ... Using
// default value" and leaves the current value alone). Java is not the
// reference here because it is not consistent with itself: readInt/readLong in
// DefaultProfilerConfig fall back to the default silently through
// NumberUtils.parseInteger, while an @Value injection wraps the parse failure
// in a RuntimeException and fails startup.
//
// The converted value is what gets stored, never the raw one, so an option
// holds its declared type whatever source it came from. That is what keeps the
// accessors off the silent zero-conversion path and what makes the DeepEqual
// in loadDynamicConfig compare like with like across a reload.
func (config *Config) setFinalValue(cfgName string, item *cfgMapItem, value interface{}, source int) {
	converted, err := convertCfgValue(item.valueType, value)
	if err != nil {
		Log("config").Warnf("%s = %v is not a valid %s, keeping %v",
			cfgName, value, cfgTypeName(item.valueType), item.value)
		return
	}
	value = converted

	item.value = value
	item.source = source
	if cfgName == CfgIsContainerEnv {
		config.containerCheck = false
	} else if cfgName == CfgLogLevel {
		config.useNewLogOpt = true
	} else if cfgName == CfgLogLevelOld && !config.useNewLogOpt {
		config.cfgMap[CfgLogLevel].value = value
	}
}

// normalizeCfgValues converts the staged values that were written into the
// cfgMap directly - the registered defaults and whatever the ConfigOption
// functions stored - to their registered type, so that every source reaches
// publish with the same representation of an option. Without it a default of
// int 100 on a float option, or the float32 WithSamplingPercentRate stores,
// would differ from the float64 a config file yields and report a reload as a
// change of a value that did not change.
//
// A value that does not convert falls back to the registered default: these
// come from the agent's own code, not from a user's config, so a failure here
// is a caller bug rather than a typo, but it must not leave a wrong-typed
// value staged either. The caller must hold config.mu.
func (config *Config) normalizeCfgValues() {
	sortKeys := make([]string, 0, len(config.cfgMap))
	for k := range config.cfgMap {
		sortKeys = append(sortKeys, k)
	}
	sort.Strings(sortKeys)

	for _, k := range sortKeys {
		v := config.cfgMap[k]
		converted, err := convertCfgValue(v.valueType, v.value)
		if err != nil {
			Log("config").Warnf("%s = %v is not a valid %s, keeping %v",
				k, v.value, cfgTypeName(v.valueType), v.defaultValue)
			converted, _ = convertCfgValue(v.valueType, v.defaultValue)
		}
		v.value = converted
	}
}

// checkNameAndID resolves the agent self-identification (ObjectName) according
// to the configured version (Uid.Version: v1/v3/v4) and writes the resolved
// values back into config so accessors stay consistent. Missing required fields
// (applicationName for all versions; serviceName/apiKey for v4) return an error
// that aborts agent startup.
func (config *Config) checkNameAndID() error {
	objName, err := resolveObjectName(config)
	if err != nil {
		return err
	}
	config.mu.Lock()
	defer config.mu.Unlock()

	config.objName = objName
	config.cfgMap[CfgAgentName].value = objName.agentName
	config.cfgMap[CfgServiceName].value = objName.serviceName
	config.publish()
	return nil
}

var samplingOpts = []string{
	CfgSamplingType, CfgSamplingCounterRate, CfgSamplingPercentRate,
	CfgSamplingNewThroughput, CfgSamplingContinueThroughput,
}

// defaultIfOutOfRange restores the key's registered default when the staged
// value falls outside [min, max], logging a warning. Recovering with the
// default is what the C++ agent does (in_range in src/config.cpp); the Java
// agent is not a reference here, as it does no range checking in its config
// classes - what validation it has sits with the code consuming the value
// instead, e.g. DefaultAgentStatMonitor.java:106-114 restoring the default
// collection interval. Clamping to the nearest bound would turn
// a typo like Span.QueueSize: 0 into a queue of 1 and drop virtually every
// span.
func (config *Config) defaultIfOutOfRange(name string, min, max int) {
	v := config.stagedInt(name)
	if v < min || v > max {
		def := config.cfgMap[name].defaultValue
		Log("config").Warnf("%s = %d is out of range [%d, %d], using default %v", name, v, min, max, def)
		config.cfgMap[name].value = def
	}
}

// zeroIfNegative normalizes a negative value to 0 on options where 0 already
// carries the meaning: the feature off, or a throughput unlimited. A negative
// value has always behaved like 0 here, so storing 0 keeps the published value
// equal to the effective one and the warning makes the coercion visible - the
// C++ agent reads a negative value as "use the default" instead, which turns
// the feature on.
func (config *Config) zeroIfNegative(name string) {
	if v := config.stagedInt(name); v < 0 {
		Log("config").Warnf("%s = %d is negative, using 0", name, v)
		config.cfgMap[name].value = 0
	}
}

// publish normalizes the staged cfgMap values and installs the result as a new
// snapshot with a single atomic store. Everything derived from the config is
// built here so that one store makes the whole generation visible at once.
// The caller must hold config.mu.
func (config *Config) publish() {
	// The normalized type is stored, not just used here: newTraceSampler and
	// the rest compare the published value against samplingTypeCounter, so
	// uppercasing a local copy alone would route "counter" - and the COUNTING
	// alias - to the percent sampler.
	sampleType := strings.ToUpper(strings.TrimSpace(config.stagedString(CfgSamplingType)))
	if sampleType == samplingTypeCounting {
		sampleType = samplingTypeCounter
	}
	if sampleType != samplingTypeCounter && sampleType != samplingTypePercent {
		// Only the type falls back, never the rate. Overwriting the rate with 0
		// made rateSampler drop every trace, so a typo in this dynamic key -
		// including on a reload - switched tracing off with nothing in the log
		// to say so. Java's SamplerType keeps the configured rate too, whose
		// default of 1 samples everything.
		Log("config").Warnf("%s = %q is not supported, using %s with %s = %d",
			CfgSamplingType, config.stagedString(CfgSamplingType), samplingTypeCounter,
			CfgSamplingCounterRate, config.stagedInt(CfgSamplingCounterRate))
		sampleType = samplingTypeCounter
	}
	config.cfgMap[CfgSamplingType].value = sampleType

	// Only the documented levels pass. logrus.ParseLevel also accepts fatal
	// and panic, which would silence warn and error while looking like a
	// valid setting. An unknown level keeps the level already published, or
	// the default on the first publish - never info regardless of what was
	// set, which is what the logger used to do. Logged at Error, not Warn,
	// because the level being kept may well be error: at Warn the one line
	// explaining the rejected value would be the one line the operator cannot
	// see.
	level := config.stagedString(CfgLogLevel)
	if lvl, err := logrus.ParseLevel(level); err != nil || lvl < logrus.ErrorLevel {
		current, ok := config.load().values[CfgLogLevel]
		if !ok {
			current = config.cfgMap[CfgLogLevel].defaultValue
		}
		Log("config").Errorf("%s = %q is not one of trace, debug, info, warn or error, keeping %v",
			CfgLogLevel, level, current)
		config.cfgMap[CfgLogLevel].value = current
	}

	// Both branches warn: the clamp used to be silent, so a config file asking
	// for 4096 ran at 1024 with nothing to say why, and a negative value turned
	// SQL.TraceBindValue off as a side effect - bind value tracing gone
	// entirely, not just shortened.
	maxBind := config.stagedInt(CfgSQLMaxBindValueSize)
	if maxBind > maxSqlBindValueSize {
		Log("config").Warnf("%s = %d is out of range, using %d",
			CfgSQLMaxBindValueSize, maxBind, maxSqlBindValueSize)
		config.cfgMap[CfgSQLMaxBindValueSize].value = maxSqlBindValueSize
	} else if maxBind < 0 {
		Log("config").Warnf("%s = %d is negative, using 0 and turning %s off",
			CfgSQLMaxBindValueSize, maxBind, CfgSQLTraceBindValue)
		config.cfgMap[CfgSQLTraceBindValue].value = false
		config.cfgMap[CfgSQLMaxBindValueSize].value = 0
	}

	// Dynamic key. Only -1 turns the bypass off and caches every SQL, the same
	// escape hatch as the Java agent's bypassLength of -1 and the only unlimited
	// value the C++ agent accepts. Any other negative value is a typo and
	// recovers the default, the rule Span.MaxCallStackDepth already follows.
	if limit := config.stagedInt(CfgSQLCacheLengthLimit); limit == -1 {
		config.cfgMap[CfgSQLCacheLengthLimit].value = math.MaxInt32
	} else if limit < 0 {
		Log("config").Warnf("%s = %d is out of range, using default %d (only -1 means unlimited)",
			CfgSQLCacheLengthLimit, limit, defaultSqlCacheLengthLimit)
		config.cfgMap[CfgSQLCacheLengthLimit].value = defaultSqlCacheLengthLimit
	}
	config.zeroIfNegative(CfgSQLErrorCount)
	// 0 stays 0 and never expires an entry, so the range starts there. A
	// negative value used to reach the ttl > 0 gate in metaCache and disable
	// expiry just like 0, which silently costs a restart to recover from once
	// the collector's SqlUidMetaData row lapses under a still-cached UID.
	// Recovering the default is what the C++ agent does for this key, and it
	// keeps 0 as the only way to ask for no expiry - a negative value is a typo,
	// not a request.
	config.defaultIfOutOfRange(CfgSQLCacheExpireHours, 0, maxSqlCacheExpireHours)

	if config.stagedInt(CfgSpanEventChunkSize) < 1 {
		config.cfgMap[CfgSpanEventChunkSize].value = defaultEventChunkSize
	}

	// Dynamic key, and unlike the queues below a bad value here is silent data
	// loss rather than a panic: with a limit of 0 or less, snapshot.count is
	// already at it before the first url, so every url stat entry is dropped.
	// Java rejects the same value outright (AgentUriStatData asserts capacity
	// > 0). The upper bound is maxQueueSize, the typo guard the queues use -
	// the map grows lazily so a large limit costs nothing up front, but a map
	// of that many distinct urls is a runaway pattern set, not a capacity
	// anyone configures.
	config.defaultIfOutOfRange(CfgHttpUrlStatLimitSize, 1, maxQueueSize)

	// These non-dynamic keys are range checked here, not in NewConfig, because
	// the exported Set() also republishes: a non-positive Stat.CollectInterval
	// panics time.NewTicker and a non-positive Stat.BatchCount panics the stat
	// worker's batch indexing, killing the host process. The upper bounds stop
	// a typo (queue 1e9) from allocating a huge channel buffer or stalling the
	// stat collector. An out-of-range value falls back to the default, the same
	// recovery the C++ agent performs (the Java agent does no range checking at
	// the config layer, so it is not a reference here).
	config.defaultIfOutOfRange(CfgSpanQueueSize, 1, maxQueueSize)
	config.defaultIfOutOfRange(CfgHttpUrlStatQueueSize, 1, maxQueueSize)
	// Non-dynamic: the SQL caches are built once in NewAgent, and resizing
	// them mid-run would orphan ids that in-flight spans already reference.
	config.defaultIfOutOfRange(CfgSQLCacheSize, 1, maxSqlCacheSize)
	config.defaultIfOutOfRange(CfgStatCollectInterval, minStatCollectInterval, maxStatCollectInterval)
	config.defaultIfOutOfRange(CfgStatBatchCount, 1, maxStatBatchCount)
	config.defaultIfOutOfRange(CfgStatQueueSize, 1, maxQueueSize)
	config.defaultIfOutOfRange(CfgCollectorGrpcSenderQueueSize, 1, maxQueueSize)
	if config.stagedInt(CfgSpanBatchSize) < 1 {
		config.cfgMap[CfgSpanBatchSize].value = defaultSpanBatchSize
	}
	if config.stagedInt(CfgSpanBatchFlushInterval) < 1 {
		config.cfgMap[CfgSpanBatchFlushInterval].value = defaultSpanBatchFlushInterval
	}
	if config.stagedInt(CfgSpanBatchCollectDeadline) < 1 {
		config.cfgMap[CfgSpanBatchCollectDeadline].value = defaultSpanBatchCollectDeadline
	}
	if config.stagedInt(CfgSpanBatchMaxConcurrentRequests) < 1 {
		config.cfgMap[CfgSpanBatchMaxConcurrentRequests].value = defaultSpanBatchMaxConcurrentRequests
	}
	if config.stagedInt(CfgCollectorAgentInfoSendRetryInterval) < 1 {
		config.cfgMap[CfgCollectorAgentInfoSendRetryInterval].value = defaultAgentInfoSendRetryInterval
	}
	if config.stagedInt(CfgCollectorAgentInfoMaxTryPerAttempt) < 1 {
		config.cfgMap[CfgCollectorAgentInfoMaxTryPerAttempt].value = defaultAgentInfoMaxTryPerAttempt
	}
	// A negative max age means the same as the default: renewal off.
	if config.stagedInt(CfgCollectorGrpcConnectionMaxAge) < 0 {
		config.cfgMap[CfgCollectorGrpcConnectionMaxAge].value = grpcConnectionMaxAge
	}
	if config.stagedInt(CfgCollectorGrpcStreamMaxAge) < 0 {
		config.cfgMap[CfgCollectorGrpcStreamMaxAge].value = grpcStreamMaxAge
	}
	// A negative idle timeout means the same as the default: idling off.
	if config.stagedInt(CfgCollectorGrpcIdleTimeout) < 0 {
		config.cfgMap[CfgCollectorGrpcIdleTimeout].value = grpcIdleTimeout
	}
	maxDepth := config.stagedInt(CfgSpanMaxCallStackDepth)
	if maxDepth == -1 {
		maxDepth = math.MaxInt32
	} else if maxDepth < minEventDepth {
		maxDepth = minEventDepth
	}
	config.cfgMap[CfgSpanMaxCallStackDepth].value = maxDepth

	maxSeq := config.stagedInt(CfgSpanMaxCallStackSequence)
	if maxSeq == -1 {
		maxSeq = math.MaxInt32
	} else if maxSeq < minEventSequence {
		maxSeq = minEventSequence
	}
	config.cfgMap[CfgSpanMaxCallStackSequence].value = maxSeq

	if config.stagedInt(CfgLogMaxSize) < 1 {
		config.cfgMap[CfgLogMaxSize].value = 10
	}

	// Dynamic key, so both bounds must be enforced on every publish: a reload
	// can otherwise inject a value that makes traceCallStack's allocation panic
	// or exhaust the process memory on an application request goroutine.
	errorDepth := config.stagedInt(CfgErrorCallStackDepth)
	if errorDepth < 1 {
		errorDepth = defaultErrorCallStackDepth
	} else if errorDepth > maxErrorCallStackDepth {
		errorDepth = maxErrorCallStackDepth
	}
	config.cfgMap[CfgErrorCallStackDepth].value = errorDepth

	// 0 or less is Java's "unlimited", which here is the cycle ceiling: the
	// cause walk runs on a request goroutine over an arbitrary user Cause()
	// implementation and must stay bounded whatever the config asks for.
	chainDepth := config.stagedInt(CfgErrorMaxChainDepth)
	if chainDepth < 1 || chainDepth > maxCauserDepth {
		chainDepth = defaultErrorMaxChainDepth
	}
	config.cfgMap[CfgErrorMaxChainDepth].value = chainDepth
	config.zeroIfNegative(CfgErrorNewThroughput)

	values := make(map[string]interface{}, len(config.cfgMap))
	for k, v := range config.cfgMap {
		values[k] = v.value
	}

	snapshot := &configSnapshot{
		values:               values,
		collectUrlStat:       cast.ToBool(values[CfgHttpUrlStatEnable]),
		urlStatLimitSize:     cast.ToInt(values[CfgHttpUrlStatLimitSize]),
		urlStatWithMethod:    cast.ToBool(values[CfgHttpUrlStatWithMethod]),
		sqlTraceBindValue:    cast.ToBool(values[CfgSQLTraceBindValue]),
		sqlMaxBindValueSize:  cast.ToInt(values[CfgSQLMaxBindValueSize]),
		sqlTraceCommit:       cast.ToBool(values[CfgSQLTraceCommit]),
		sqlTraceRollback:     cast.ToBool(values[CfgSQLTraceRollback]),
		sqlTraceQueryStat:    cast.ToBool(values[CfgSQLTraceQueryStat]),
		sqlEnableRawSqlCache: cast.ToBool(values[CfgSQLEnableRawSqlCache]),
		sqlCacheLengthLimit:  cast.ToInt(values[CfgSQLCacheLengthLimit]),
		sqlErrorCount:        cast.ToInt(values[CfgSQLErrorCount]),
		sqlRemoveComments:    cast.ToBool(values[CfgSQLRemoveComments]),
		spanEventChunkSize:   cast.ToInt(values[CfgSpanEventChunkSize]),
		spanMaxEventDepth:    cast.ToInt32(values[CfgSpanMaxCallStackDepth]),
		spanMaxEventSequence: cast.ToInt32(values[CfgSpanMaxCallStackSequence]),
		errorTraceCallStack:  cast.ToBool(values[CfgErrorTraceCallStack]),
		errorCallStackDepth:  cast.ToInt(values[CfgErrorCallStackDepth]),
		errorIgnoreRules:     parseIgnoreErrorRules(cast.ToStringSlice(values[CfgSpanIgnoreErrors])),
		errorMaxChainDepth:   cast.ToInt(values[CfgErrorMaxChainDepth]),
		errorMarkMask: parseErrorMarkMask(cast.ToStringSlice(values[CfgSpanErrorMark]),
			cast.ToStringSlice(values[CfgSpanErrorMarkExclude])),
	}
	snapshot.sampler = newTraceSampler(config.load(), values)
	snapshot.newExceptionLimiter = newExceptionLimiter(config.load(), values)

	config.snapshot.Store(snapshot)
}

// newTraceSampler carries the previous sampler over when no sampling option
// changed, so an unrelated reload does not reset the throughput limiter's
// counters.
func newTraceSampler(prev *configSnapshot, values map[string]interface{}) traceSampler {
	if prev != nil && prev.sampler != nil && sameValues(prev.values, values, samplingOpts) {
		return prev.sampler
	}

	var baseSampler sampler
	if cast.ToString(values[CfgSamplingType]) == samplingTypeCounter {
		baseSampler = newRateSampler(cast.ToInt(values[CfgSamplingCounterRate]))
	} else {
		baseSampler = newPercentSampler(cast.ToFloat64(values[CfgSamplingPercentRate]))
	}

	newTps := cast.ToInt(values[CfgSamplingNewThroughput])
	continueTps := cast.ToInt(values[CfgSamplingContinueThroughput])
	if newTps > 0 || continueTps > 0 {
		return newThroughputLimitTraceSampler(baseSampler, newTps, continueTps)
	}
	return newBasicTraceSampler(baseSampler)
}

// newExceptionLimiter builds the rate limiter on new exception chain ids, the
// counterpart of the Java agent's ExceptionChainSampler; a throughput of 0 or
// less means unlimited. Like newTraceSampler it carries the previous limiter
// over when the option did not change, so an unrelated reload does not refill
// the token bucket.
func newExceptionLimiter(prev *configSnapshot, values map[string]interface{}) *rate.Limiter {
	if prev != nil && sameValues(prev.values, values, []string{CfgErrorNewThroughput}) {
		return prev.newExceptionLimiter
	}

	tps := cast.ToInt(values[CfgErrorNewThroughput])
	if tps <= 0 {
		return nil
	}
	// The burst is the tps itself, for the reason newThroughputLimitTraceSampler
	// documents: Java builds this limiter from a Guava RateLimiter, which holds
	// up to one second of permits.
	return rate.NewLimiter(per(tps, time.Second), tps)
}

// sameValues compares config values with DeepEqual: a value can be a slice
// (CfgStringSlice options), and == panics on those.
func sameValues(a, b map[string]interface{}, keys []string) bool {
	for _, k := range keys {
		if !reflect.DeepEqual(a[k], b[k]) {
			return false
		}
	}
	return true
}

type reloadCallback struct {
	cfgNames []string
	callback func()
}

// AddReloadCallback adds a callback function will be called after reloading config file.
func (config *Config) AddReloadCallback(optNames []string, callback func()) {
	config.mu.Lock()
	defer config.mu.Unlock()

	config.callback = append(config.callback, reloadCallback{optNames, callback})
}

func (config *Config) reloadConfig(cfgFileViper *viper.Viper) {
	config.mu.Lock()
	if err := cfgFileViper.ReadInConfig(); err != nil {
		config.mu.Unlock()
		Log("config").Errorf("config file reloading error: %v", err)
		return
	}

	profileViper := config.loadProfile(viper.New(), cfgFileViper)
	changed := config.loadDynamicConfig(cfgFileViper, profileViper)
	config.publish()
	// Callbacks read the config they were just given a new generation of, and
	// may register further callbacks, so run them off a copy with the lock
	// released.
	callback := make([]reloadCallback, len(config.callback))
	copy(callback, config.callback)
	config.mu.Unlock()

	for _, cb := range callback {
		cb.do(changed)
	}
}

// loadDynamicConfig restages the dynamic options from the config file and
// returns the set of option names whose value actually changed.
func (config *Config) loadDynamicConfig(cfgFileViper *viper.Viper, profileViper *viper.Viper) map[string]bool {
	changed := make(map[string]bool)

	sortKeys := make([]string, 0)
	for k := range config.cfgMap {
		sortKeys = append(sortKeys, k)
	}
	sort.Strings(sortKeys)
	for _, k := range sortKeys {
		v := config.cfgMap[k]
		if !v.dynamic {
			continue
		}
		if v.source > cfgSrcProfile {
			Log("config").Debugf("config %s keeps its command line, environment or Set() value across reload", k)
			continue
		}

		oldValue := v.value
		if profileViper.IsSet(k) {
			config.setFinalValue(k, v, profileViper.Get(k), cfgSrcProfile)
		} else if cfgFileViper.IsSet(k) {
			config.setFinalValue(k, v, cfgFileViper.Get(k), cfgSrcFile)
		} else {
			continue
		}
		if !reflect.DeepEqual(oldValue, v.value) {
			changed[k] = true
			// setFinalValue mirrors the deprecated LogLevel key onto Log.Level,
			// and the logger's reload callback is registered on the new name.
			// A file that still uses the old one has to report both, or the
			// snapshot picks the new level up while the logger keeps the one
			// it started with.
			if k == CfgLogLevelOld && !config.useNewLogOpt {
				changed[CfgLogLevel] = true
			}
		}
	}
	return changed
}

func (cb reloadCallback) do(changed map[string]bool) {
	for _, k := range cb.cfgNames {
		if changed[k] {
			cb.invoke()
			break
		}
	}
}

// invoke isolates a caller-supplied callback from the config watcher. A panic
// in any goroutine normally terminates the host process; recovering here also
// lets reloadConfig continue with the remaining callbacks.
func (cb reloadCallback) invoke() {
	defer func() {
		if e := recover(); e != nil {
			Log("config").Errorf("config reload callback panic (%v): %v\n%s", cb.cfgNames, e, debug.Stack())
		}
	}()
	cb.callback()
}

func isContainerEnv() bool {
	_, err := os.Stat("/.dockerenv")
	if err == nil || !os.IsNotExist(err) {
		return true
	}

	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}

	return false
}

// WithAppName sets the application name.
func WithAppName(name string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgAppName].value = name
	}
}

// WithAppType sets the application type.
func WithAppType(typ int32) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgAppType].value = typ
	}
}

// WithAgentName sets the agent name.
func WithAgentName(name string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgAgentName].value = name
	}
}

// WithUidVersion sets the agent self-identification version (v1, v3, or v4).
// Unknown values fall back to v3.
func WithUidVersion(version string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgUIDVersion].value = version
	}
}

// WithServiceName sets the service name (required for v4).
func WithServiceName(name string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgServiceName].value = name
	}
}

// WithApiKey sets the api key (required for v4).
func WithApiKey(apiKey string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgApiKey].value = apiKey
	}
}

// WithConfigFile sets the configuration file.
func WithConfigFile(filePath string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgConfigFile].value = filePath
	}
}

// WithCollectorHost sets the host address of pinpoint collector.
func WithCollectorHost(host string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorHost].value = host
	}
}

// WithCollectorAgentPort sets the agent port of pinpoint collector.
func WithCollectorAgentPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorAgentPort].value = port
	}
}

// WithCollectorSpanPort sets the span port of pinpoint collector.
func WithCollectorSpanPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorSpanPort].value = port
	}
}

// WithCollectorStatPort sets the agent stat of pinpoint collector.
func WithCollectorStatPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorStatPort].value = port
	}
}

// WithCollectorAgentInfoRefreshInterval sets the cycle for re-sending the agent information
// to the collector, in milliseconds. Defaults to 24 hours; if 0 or less, it is sent only
// once at startup.
func WithCollectorAgentInfoRefreshInterval(interval int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorAgentInfoRefreshInterval].value = interval
	}
}

// WithCollectorAgentInfoSendRetryInterval sets the wait between agent information send
// retries, in milliseconds. It paces both the registration retry at startup and the
// retries within one refresh cycle.
func WithCollectorAgentInfoSendRetryInterval(interval int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorAgentInfoSendRetryInterval].value = interval
	}
}

// WithCollectorAgentInfoMaxTryPerAttempt sets the max number of agent information sends
// per refresh cycle.
func WithCollectorAgentInfoMaxTryPerAttempt(count int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorAgentInfoMaxTryPerAttempt].value = count
	}
}

// WithCollectorGrpcKeepAliveTime sets the gRPC keepalive ping interval in milliseconds.
func WithCollectorGrpcKeepAliveTime(ms int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcKeepAliveTime].value = ms
	}
}

// WithCollectorGrpcKeepAliveTimeout sets the gRPC keepalive ping timeout in milliseconds.
func WithCollectorGrpcKeepAliveTimeout(ms int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcKeepAliveTimeout].value = ms
	}
}

// WithCollectorGrpcKeepAlivePermitWithoutCalls sets whether keepalive pings are sent without active streams.
func WithCollectorGrpcKeepAlivePermitWithoutCalls(permit bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcKeepAlivePermitWithoutCalls].value = permit
	}
}

// WithCollectorGrpcMaxSendMessageSize sets the max size in bytes of a gRPC message the agent can send.
func WithCollectorGrpcMaxSendMessageSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcMaxSendMessageSize].value = size
	}
}

// WithCollectorGrpcMaxReceiveMessageSize sets the max size in bytes of a gRPC message the agent can receive.
func WithCollectorGrpcMaxReceiveMessageSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcMaxReceiveMessageSize].value = size
	}
}

// WithCollectorGrpcFlowControlWindow sets the initial HTTP/2 flow-control window size in bytes.
func WithCollectorGrpcFlowControlWindow(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcFlowControlWindow].value = size
	}
}

// WithCollectorGrpcWriteBufferSize sets the gRPC transport write buffer size in bytes.
func WithCollectorGrpcWriteBufferSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcWriteBufferSize].value = size
	}
}

// WithCollectorGrpcMaxHeaderListSize sets the max size in bytes of gRPC response headers.
func WithCollectorGrpcMaxHeaderListSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcMaxHeaderListSize].value = size
	}
}

// WithCollectorGrpcSslEnable enables TLS on the gRPC channels to pinpoint collector.
func WithCollectorGrpcSslEnable(enable bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcSslEnable].value = enable
	}
}

// WithCollectorGrpcTrustCertFilePath sets the PEM certificate used as the trust
// root when verifying the collector's TLS certificate.
// If not set, the system root CAs are used.
func WithCollectorGrpcTrustCertFilePath(path string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcTrustCertFilePath].value = path
	}
}

// WithCollectorGrpcConnectionMaxAge sets the max age in milliseconds of a
// collector connection. Once a connection is older than this, the next send
// opens a replacement and switches over as soon as it is ready, so agents
// behind a load balancer spread across collector instances over time.
// 0 (the default) never replaces a working connection.
func WithCollectorGrpcConnectionMaxAge(ms int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcConnectionMaxAge].value = ms
	}
}

// WithCollectorGrpcStreamMaxAge sets the max age in milliseconds of the
// long-lived ping, span, stat and command streams. A stream older than this is
// closed normally and reopened by its worker. 0 (the default) keeps a stream
// open until it fails.
func WithCollectorGrpcStreamMaxAge(ms int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcStreamMaxAge].value = ms
	}
}

// WithCollectorGrpcIdleTimeout sets how long in milliseconds a collector
// connection may go without an RPC before gRPC closes it and puts the channel
// into IDLE, which also stops its keepalive pings; the next send reconnects.
// 0 (the default) disables idling so a quiet channel keeps its connection.
func WithCollectorGrpcIdleTimeout(ms int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcIdleTimeout].value = ms
	}
}

// WithCollectorGrpcSenderQueueSize sets the size of the queue buffering
// metadata (API, string, SQL, exception) waiting to be sent to the collector.
func WithCollectorGrpcSenderQueueSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorGrpcSenderQueueSize].value = size
	}
}

// WithLogLevel sets the logging level for agent logger.
func WithLogLevel(level string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgLogLevel].value = level
	}
}

// WithLogOutput sets the output for agent logger.
func WithLogOutput(output string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgLogOutput].value = output
	}
}

// WithLogMaxSize sets the max size of output file for agent logger.
func WithLogMaxSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgLogMaxSize].value = size
	}
}

// WithSamplingType sets the type of agent sampler.
// Either "COUNTER" or "PERCENT" must be specified.
func WithSamplingType(samplingType string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingType].value = samplingType
	}
}

// WithSamplingRate DEPRECATED: Use WithSamplingCounterRate()
func WithSamplingRate(rate int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingCounterRate].value = rate
	}
}

// WithSamplingCounterRate sets the sampling rate for a 'counter sampler'.
func WithSamplingCounterRate(rate int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingCounterRate].value = rate
	}
}

// WithSamplingPercentRate sets the sampling rate for a 'percent sampler'.
func WithSamplingPercentRate(rate float32) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingPercentRate].value = rate
	}
}

// WithSamplingNewThroughput sets the new tps for a 'throughput sampler'.
func WithSamplingNewThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingNewThroughput].value = tps
	}
}

// WithSamplingContinueThroughput sets the cont tps for a 'throughput sampler'.
func WithSamplingContinueThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingContinueThroughput].value = tps
	}
}

// WithStatCollectInterval sets the statistics collection cycle for the agent.
func WithStatCollectInterval(interval int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgStatCollectInterval].value = interval
	}
}

// WithStatBatchCount sets batch delivery units for collected statistics.
func WithStatBatchCount(count int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgStatBatchCount].value = count
	}
}

// WithIsContainerEnv sets whether the application is running in a container environment or not.
// If this is not set, the agent automatically checks it.
func WithIsContainerEnv(isContainer bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgIsContainerEnv].value = isContainer
		c.containerCheck = false
	}
}

// WithActiveProfile sets the configuration profile.
func WithActiveProfile(profile string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgActiveProfile].value = profile
	}
}

// WithSQLTraceBindValue enables bind value tracing for SQL Driver.
func WithSQLTraceBindValue(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLTraceBindValue].value = trace
	}
}

// WithSQLMaxBindValueSize sets the max length of traced bind value for SQL Driver.
// It also caps the literal parameters extracted by SQL normalization.
func WithSQLMaxBindValueSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLMaxBindValueSize].value = size
	}
}

// WithSQLTraceCommit enables commit tracing for SQL Driver.
func WithSQLTraceCommit(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLTraceCommit].value = trace
	}
}

// WithSQLTraceRollback enables rollback tracing for SQL Driver.
func WithSQLTraceRollback(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLTraceRollback].value = trace
	}
}

// WithSQLEnableRawSqlCache enables caching of SQL normalization results keyed by raw SQL text.
func WithSQLEnableRawSqlCache(enable bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLEnableRawSqlCache].value = enable
	}
}

// WithSQLCacheSize sets how many statements each SQL metadata cache (SQL-ID,
// SQL-UID and raw SQL) holds. Raise it for applications running more distinct
// statements than the default, which otherwise evict and re-register SQL
// metadata continuously. Read once at agent startup.
func WithSQLCacheSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLCacheSize].value = size
	}
}

// WithSQLCacheLengthLimit sets the max length in bytes of a SQL kept in the SQL
// UID cache and the raw SQL cache. The SQL-ID cache is exempt - its ids come
// from a sequence, so bypassing it would issue a new id per execution. A
// negative value caches every SQL.
func WithSQLCacheLengthLimit(limit int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLCacheLengthLimit].value = limit
	}
}

// WithSQLCacheExpireHours sets how many hours a SQL UID stays cached before
// its metadata is registered with the collector again. Zero never expires an
// entry; a negative value is out of range and recovers the default.
func WithSQLCacheExpireHours(hours int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLCacheExpireHours].value = hours
	}
}

// WithSQLRemoveComments drops comments from normalized SQL instead of copying
// them, matching the Java agent's profiler.jdbc.removecomments default. It is
// startup-only: the normalized text is the SQL id cache key and the UID hash
// input, so changing it at runtime would split one statement across two ids.
func WithSQLRemoveComments(remove bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLRemoveComments].value = remove
	}
}

// WithSQLErrorCount sets how many SQL executions mark a span failed. A value of
// 0 or less turns the count off.
func WithSQLErrorCount(count int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLErrorCount].value = count
	}
}

// WithSQLTraceQueryStat enables to trace SQL query statistics for SQL Driver.
func WithSQLTraceQueryStat(collect bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLTraceQueryStat].value = collect
	}
}

// WithEnable enables the agent is operational state.
func WithEnable(enable bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgEnable].value = enable
	}
}

// WithSpanQueueSize sets the size of the span queue for gRPC.
func WithSpanQueueSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanQueueSize].value = size
	}
}

// WithSpanBatchEnable enables SendSpanBatch instead of the long-lived SendSpan stream.
func WithSpanBatchEnable(enable bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanBatchEnable].value = enable
	}
}

// WithSpanBatchSize sets the max number of spans per SendSpanBatch request.
func WithSpanBatchSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanBatchSize].value = size
	}
}

// WithSpanBatchFlushInterval sets the permit wait timeout for span batch requests, in milliseconds.
func WithSpanBatchFlushInterval(interval int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanBatchFlushInterval].value = interval
	}
}

// WithSpanBatchCollectDeadline sets the collection window for a span batch, in milliseconds.
func WithSpanBatchCollectDeadline(deadline int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanBatchCollectDeadline].value = deadline
	}
}

// WithSpanBatchMaxConcurrentRequests sets the max number of concurrent SendSpanBatch requests.
func WithSpanBatchMaxConcurrentRequests(max int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanBatchMaxConcurrentRequests].value = max
	}
}

// WithSpanEventChunkSize sets the event chunk of a span.
func WithSpanEventChunkSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanEventChunkSize].value = size
	}
}

// WithSpanMaxCallStackDepth sets the max callstack depth of a span.
func WithSpanMaxCallStackDepth(depth int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanMaxCallStackDepth].value = depth
	}
}

// WithSpanMaxCallStackSequence sets the max callstack sequence of a span.
func WithSpanMaxCallStackSequence(seq int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanMaxCallStackSequence].value = seq
	}
}

// WithSpanIgnoreErrors sets the errors that are recorded as exception info but
// do not mark the span as failed. Each entry is "<type>:<message substring>";
// either part may be empty.
func WithSpanIgnoreErrors(rules ...string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanIgnoreErrors].value = rules
	}
}

// WithSpanErrorMark sets which error causes are allowed to fail a transaction.
// Each entry is one of "exception", "http-status" and "sql", or several of
// them comma separated; no entries at all means every cause. The unknown
// cause - a failure recorded with no category, as SetFailure does - is always
// marked, whatever this option and WithSpanErrorMarkExclude say.
func WithSpanErrorMark(categories ...string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanErrorMark].value = categories
	}
}

// WithSpanErrorMarkExclude sets the error causes that must not fail a
// transaction, removed from whatever WithSpanErrorMark allows. The entries are
// spelled as in WithSpanErrorMark.
func WithSpanErrorMarkExclude(categories ...string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanErrorMarkExclude].value = categories
	}
}

// WithHttpUrlStatEnable enables the agent collects the HTTP URL statistics.
func WithHttpUrlStatEnable(enable bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgHttpUrlStatEnable].value = enable
	}
}

// WithStatQueueSize sets the size of the queue buffering agent stat messages
// waiting to be sent to the collector.
func WithStatQueueSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgStatQueueSize].value = size
	}
}

// WithHttpUrlStatLimitSize sets the maximum number of URLs that can be stored in one snapshot.
func WithHttpUrlStatLimitSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgHttpUrlStatLimitSize].value = size
	}
}

// WithHttpUrlStatQueueSize sets the size of the queue buffering per-request URL
// statistics records until they are aggregated into a snapshot.
func WithHttpUrlStatQueueSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgHttpUrlStatQueueSize].value = size
	}
}

// WithHttpUrlStatWithMethod adds http method as prefix to uri string key.
func WithHttpUrlStatWithMethod(withMethod bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgHttpUrlStatWithMethod].value = withMethod
	}
}

// WithErrorTraceCallStack enables the agent collects a call stack when error occurs.
func WithErrorTraceCallStack(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgErrorTraceCallStack].value = trace
	}
}

// WithErrorCallStackDepth sets the maximum depth of call stack that can be dumped.
func WithErrorCallStackDepth(depth int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgErrorCallStackDepth].value = depth
	}
}

// WithErrorMaxChainDepth sets how many links of an error's cause chain are
// recorded, the error itself included. 0 or less, and anything above 64, mean
// the 64-link ceiling.
func WithErrorMaxChainDepth(depth int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgErrorMaxChainDepth].value = depth
	}
}

// WithErrorNewThroughput sets the maximum number of new exception chains
// recorded per second. 0 or less means unlimited.
func WithErrorNewThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgErrorNewThroughput].value = tps
	}
}

func (config *Config) printConfigString() {
	values := config.load().values

	sortKeys := make([]string, 0)
	for k := range values {
		sortKeys = append(sortKeys, k)
	}
	sort.Strings(sortKeys)

	for _, k := range sortKeys {
		if k == CfgApiKey {
			if values[k] == "" {
				Log("config").Infof("%s = ", k)
			} else {
				Log("config").Infof("%s = ****", k)
			}
			continue
		}
		Log("config").Infof("%s = %v", k, values[k])
	}
}
