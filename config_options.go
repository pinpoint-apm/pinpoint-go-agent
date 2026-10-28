package pinpoint

// withValue is the shape of every plain ConfigOption: it stores v under key.
func withValue(key string, v any) ConfigOption {
	return func(c *Config) { c.cfgMap[key].value = v }
}

// WithAppName sets the application name.
func WithAppName(name string) ConfigOption { return withValue(CfgAppName, name) }

// WithAppType sets the application type.
func WithAppType(typ int32) ConfigOption { return withValue(CfgAppType, typ) }

// WithAgentName sets the agent name.
func WithAgentName(name string) ConfigOption { return withValue(CfgAgentName, name) }

// WithUidVersion sets the agent self-identification version (v1, v3, or v4).
// The default is v4, and unknown values fall back to v4.
func WithUidVersion(version string) ConfigOption { return withValue(CfgUIDVersion, version) }

// WithServiceName sets the service name (required for v4).
func WithServiceName(name string) ConfigOption { return withValue(CfgServiceName, name) }

// WithClusterNamespace names the Pinpoint cluster this agent belongs to, Java's
// profiler.cluster.namespace. When set, it is sent as Pinpoint-pAppNamespace on
// every outbound call, and an inbound call carrying a different value starts a
// new transaction instead of continuing the caller's. Unset (the default), no
// header is sent and every trace is continued.
func WithClusterNamespace(namespace string) ConfigOption {
	return withValue(CfgClusterNamespace, namespace)
}

// WithApiKey sets the api key (optional, used only for v4).
func WithApiKey(apiKey string) ConfigOption { return withValue(CfgApiKey, apiKey) }

// WithConfigFile sets the configuration file.
func WithConfigFile(filePath string) ConfigOption { return withValue(CfgConfigFile, filePath) }

// WithCollectorHost sets the host address of pinpoint collector.
func WithCollectorHost(host string) ConfigOption { return withValue(CfgCollectorHost, host) }

// WithCollectorAgentPort sets the agent port of pinpoint collector.
func WithCollectorAgentPort(port int) ConfigOption { return withValue(CfgCollectorAgentPort, port) }

// WithCollectorSpanPort sets the span port of pinpoint collector.
func WithCollectorSpanPort(port int) ConfigOption { return withValue(CfgCollectorSpanPort, port) }

// WithCollectorStatPort sets the agent stat of pinpoint collector.
func WithCollectorStatPort(port int) ConfigOption { return withValue(CfgCollectorStatPort, port) }

// WithCollectorAgentInfoRefreshInterval sets the cycle for re-sending the agent information
// to the collector, in milliseconds. Defaults to 24 hours; if 0 or less, it is sent only
// once at startup.
func WithCollectorAgentInfoRefreshInterval(interval int) ConfigOption {
	return withValue(CfgCollectorAgentInfoRefreshInterval, interval)
}

// WithCollectorAgentInfoSendRetryInterval sets the wait between agent information send
// retries, in milliseconds. It paces both the registration retry at startup and the
// retries within one refresh cycle.
func WithCollectorAgentInfoSendRetryInterval(interval int) ConfigOption {
	return withValue(CfgCollectorAgentInfoSendRetryInterval, interval)
}

// WithCollectorAgentInfoMaxTryPerAttempt sets the max number of agent information sends
// per refresh cycle.
func WithCollectorAgentInfoMaxTryPerAttempt(count int) ConfigOption {
	return withValue(CfgCollectorAgentInfoMaxTryPerAttempt, count)
}

// WithCollectorAgentInfoSendArgs sets whether the process's command line arguments
// are sent to the collector with the agent information, where the Pinpoint web
// shows them on the server map. On by default; turn it off when the command
// line carries secrets.
func WithCollectorAgentInfoSendArgs(send bool) ConfigOption {
	return withValue(CfgCollectorAgentInfoSendArgs, send)
}

// WithCollectorGrpcKeepAliveTime sets the gRPC keepalive ping interval in milliseconds.
func WithCollectorGrpcKeepAliveTime(ms int) ConfigOption {
	return withValue(CfgCollectorGrpcKeepAliveTime, ms)
}

// WithCollectorGrpcKeepAliveTimeout sets the gRPC keepalive ping timeout in milliseconds.
func WithCollectorGrpcKeepAliveTimeout(ms int) ConfigOption {
	return withValue(CfgCollectorGrpcKeepAliveTimeout, ms)
}

// WithCollectorGrpcKeepAlivePermitWithoutCalls sets whether keepalive pings are sent without active streams.
func WithCollectorGrpcKeepAlivePermitWithoutCalls(permit bool) ConfigOption {
	return withValue(CfgCollectorGrpcKeepAlivePermitWithoutCalls, permit)
}

// WithCollectorGrpcMaxSendMessageSize sets the max size in bytes of a gRPC message the agent can send.
func WithCollectorGrpcMaxSendMessageSize(size int) ConfigOption {
	return withValue(CfgCollectorGrpcMaxSendMessageSize, size)
}

// WithCollectorGrpcMaxReceiveMessageSize sets the max size in bytes of a gRPC message the agent can receive.
func WithCollectorGrpcMaxReceiveMessageSize(size int) ConfigOption {
	return withValue(CfgCollectorGrpcMaxReceiveMessageSize, size)
}

// WithCollectorGrpcFlowControlWindow sets the initial HTTP/2 flow-control window size in bytes.
func WithCollectorGrpcFlowControlWindow(size int) ConfigOption {
	return withValue(CfgCollectorGrpcFlowControlWindow, size)
}

// WithCollectorGrpcWriteBufferSize sets the gRPC transport write buffer size in bytes.
func WithCollectorGrpcWriteBufferSize(size int) ConfigOption {
	return withValue(CfgCollectorGrpcWriteBufferSize, size)
}

// WithCollectorGrpcMaxHeaderListSize sets the max size in bytes of gRPC response headers.
func WithCollectorGrpcMaxHeaderListSize(size int) ConfigOption {
	return withValue(CfgCollectorGrpcMaxHeaderListSize, size)
}

// WithCollectorGrpcSslEnable enables TLS on the gRPC channels to pinpoint collector.
func WithCollectorGrpcSslEnable(enable bool) ConfigOption {
	return withValue(CfgCollectorGrpcSslEnable, enable)
}

// WithCollectorGrpcTrustCertFilePath sets the PEM certificate used as the trust
// root when verifying the collector's TLS certificate.
// If not set, the system root CAs are used.
func WithCollectorGrpcTrustCertFilePath(path string) ConfigOption {
	return withValue(CfgCollectorGrpcTrustCertFilePath, path)
}

// WithCollectorGrpcConnectionMaxAge sets the max age in milliseconds of a
// collector connection. Once a connection is older than this, the next send
// opens a replacement and switches over as soon as it is ready, so agents
// behind a load balancer spread across collector instances over time.
// 0 (the default) never replaces a working connection.
func WithCollectorGrpcConnectionMaxAge(ms int) ConfigOption {
	return withValue(CfgCollectorGrpcConnectionMaxAge, ms)
}

// WithCollectorGrpcDnsResolverEnable selects the gRPC name resolver used for
// the collector target. The default true uses the dns resolver, which keeps
// every address a collector hostname resolves to and re-resolves it. false
// falls back to the passthrough scheme, which hands the target to the dialer
// as-is: one address at a time, no re-resolution. Keep it true unless the dns
// resolver has to be rolled back without a redeploy.
func WithCollectorGrpcDnsResolverEnable(enable bool) ConfigOption {
	return withValue(CfgCollectorGrpcDnsResolverEnable, enable)
}

// WithCollectorGrpcStreamMaxAge sets the max age in milliseconds of the
// long-lived ping, stat and command streams. A stream older than this is
// closed normally and reopened by its worker. 0 (the default) keeps a stream
// open until it fails.
func WithCollectorGrpcStreamMaxAge(ms int) ConfigOption {
	return withValue(CfgCollectorGrpcStreamMaxAge, ms)
}

// WithCollectorGrpcIdleTimeout sets how long in milliseconds a collector
// connection may go without an RPC before gRPC closes it and puts the channel
// into IDLE, which also stops its keepalive pings; the next send reconnects.
// 0 (the default) disables idling so a quiet channel keeps its connection.
func WithCollectorGrpcIdleTimeout(ms int) ConfigOption {
	return withValue(CfgCollectorGrpcIdleTimeout, ms)
}

// WithCollectorGrpcSenderQueueSize sets the size of the queue buffering
// metadata (API, string, SQL, exception) waiting to be sent to the collector.
func WithCollectorGrpcSenderQueueSize(size int) ConfigOption {
	return withValue(CfgCollectorGrpcSenderQueueSize, size)
}

// WithLogLevel sets the logging level for agent logger.
func WithLogLevel(level string) ConfigOption { return withValue(CfgLogLevel, level) }

// WithLogOutput sets the output for agent logger.
func WithLogOutput(output string) ConfigOption { return withValue(CfgLogOutput, output) }

// WithLogMaxSize sets the max size of output file for agent logger.
func WithLogMaxSize(size int) ConfigOption { return withValue(CfgLogMaxSize, size) }

// WithLogMaxBackups sets the number of rotated log files kept beside the
// current one. Together with WithLogMaxSize it bounds the disk the agent log
// takes to MaxSize x (MaxBackups+1) MB.
func WithLogMaxBackups(backups int) ConfigOption { return withValue(CfgLogMaxBackups, backups) }

// WithSamplingType sets the type of agent sampler.
// Either "COUNTER" or "PERCENT" must be specified.
func WithSamplingType(samplingType string) ConfigOption {
	return withValue(CfgSamplingType, samplingType)
}

// WithSamplingCounterRate sets the sampling rate for a 'counter sampler'.
func WithSamplingCounterRate(rate int) ConfigOption { return withValue(CfgSamplingCounterRate, rate) }

// WithSamplingPercentRate sets the sampling rate for a 'percent sampler'.
func WithSamplingPercentRate(rate float32) ConfigOption {
	return withValue(CfgSamplingPercentRate, rate)
}

// WithSamplingNewThroughput sets the new tps for a 'throughput sampler'.
func WithSamplingNewThroughput(tps int) ConfigOption { return withValue(CfgSamplingNewThroughput, tps) }

// WithSamplingContinueThroughput sets the cont tps for a 'throughput sampler'.
func WithSamplingContinueThroughput(tps int) ConfigOption {
	return withValue(CfgSamplingContinueThroughput, tps)
}

// WithStatCollectInterval sets the statistics collection cycle for the agent.
func WithStatCollectInterval(interval int) ConfigOption {
	return withValue(CfgStatCollectInterval, interval)
}

// WithStatBatchCount sets batch delivery units for collected statistics.
func WithStatBatchCount(count int) ConfigOption { return withValue(CfgStatBatchCount, count) }

// WithServerInfo sets PServerMetaData.serverInfo, the server description shown
// by the Pinpoint UI. Empty keeps the default "Go Application".
func WithServerInfo(info string) ConfigOption { return withValue(CfgServerInfo, info) }

// serviceInfo is one host-supplied PServerMetaData.serviceInfo entry.
type serviceInfo struct {
	name string
	libs []string
}

// WithServiceInfo appends a PServerMetaData.serviceInfo entry: a named group of
// libraries, connectors or listening ports the host wants shown beside the
// Go build info the agent reports on its own. Call it once per group; the
// entries are sent in call order after the agent's own entry.
func WithServiceInfo(name string, libs ...string) ConfigOption {
	return func(c *Config) {
		c.serviceInfo = append(c.serviceInfo, serviceInfo{name: name, libs: append([]string(nil), libs...)})
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
func WithActiveProfile(profile string) ConfigOption { return withValue(CfgActiveProfile, profile) }

// WithSQLTraceBindValue enables bind value tracing for SQL Driver.
func WithSQLTraceBindValue(trace bool) ConfigOption { return withValue(CfgSQLTraceBindValue, trace) }

// WithSQLMaxBindValueSize sets the max length of traced bind value for SQL Driver.
func WithSQLMaxBindValueSize(size int) ConfigOption { return withValue(CfgSQLMaxBindValueSize, size) }

// WithSQLTraceCommit enables commit tracing for SQL Driver.
func WithSQLTraceCommit(trace bool) ConfigOption { return withValue(CfgSQLTraceCommit, trace) }

// WithSQLTraceRollback enables rollback tracing for SQL Driver.
func WithSQLTraceRollback(trace bool) ConfigOption { return withValue(CfgSQLTraceRollback, trace) }

// WithSQLEnableRawSqlCache enables caching of SQL normalization results keyed by raw SQL text.
func WithSQLEnableRawSqlCache(enable bool) ConfigOption {
	return withValue(CfgSQLEnableRawSqlCache, enable)
}

// WithSQLCacheSize sets how many statements each SQL metadata cache (SQL-ID,
// SQL-UID and raw SQL) holds. Raise it for applications running more distinct
// statements than the default, which otherwise evict and re-register SQL
// metadata continuously. Read once at agent startup.
func WithSQLCacheSize(size int) ConfigOption { return withValue(CfgSQLCacheSize, size) }

// WithSQLCacheLengthLimit sets the max length in bytes of a raw SQL text whose
// normalization is memoized (the raw SQL cache). The SQL-ID and SQL-UID caches
// are exempt: both key on a hash of the statement, so an entry costs the same
// whatever its length. -1 memoizes every SQL.
func WithSQLCacheLengthLimit(limit int) ConfigOption { return withValue(CfgSQLCacheLengthLimit, limit) }

// WithSQLCacheExpireHours sets how many hours a SQL UID stays cached before
// its metadata is registered with the collector again. Zero never expires an
// entry; a negative value is out of range and recovers the default.
func WithSQLCacheExpireHours(hours int) ConfigOption { return withValue(CfgSQLCacheExpireHours, hours) }

// WithSQLRemoveComments drops comments from normalized SQL instead of copying
// startup-only: the normalized text is the SQL id cache key and the UID hash
// input, so changing it at runtime would split one statement across two ids.
func WithSQLRemoveComments(remove bool) ConfigOption { return withValue(CfgSQLRemoveComments, remove) }

// WithSQLErrorCount sets how many SQL executions mark a span failed. A value of
// 0 or less turns the count off.
func WithSQLErrorCount(count int) ConfigOption { return withValue(CfgSQLErrorCount, count) }

// WithSQLTraceQueryStat enables to trace SQL query statistics for SQL Driver.
func WithSQLTraceQueryStat(collect bool) ConfigOption {
	return withValue(CfgSQLTraceQueryStat, collect)
}

// WithEnable enables the agent is operational state.
func WithEnable(enable bool) ConfigOption { return withValue(CfgEnable, enable) }

// WithSpanQueueSize sets the size of the span queue for gRPC.
func WithSpanQueueSize(size int) ConfigOption { return withValue(CfgSpanQueueSize, size) }

// WithCollectorGrpcSpanBatchSize sets the max number of spans per SendSpanBatch request.
func WithCollectorGrpcSpanBatchSize(size int) ConfigOption {
	return withValue(CfgCollectorGrpcSpanBatchSize, size)
}

// WithCollectorGrpcSpanBatchFlushInterval sets the permit wait timeout for span batch requests, in milliseconds.
func WithCollectorGrpcSpanBatchFlushInterval(interval int) ConfigOption {
	return withValue(CfgCollectorGrpcSpanBatchFlushInterval, interval)
}

// WithCollectorGrpcSpanBatchCollectDeadline sets the collection window for a span batch, in milliseconds.
func WithCollectorGrpcSpanBatchCollectDeadline(deadline int) ConfigOption {
	return withValue(CfgCollectorGrpcSpanBatchCollectDeadline, deadline)
}

// WithCollectorGrpcSpanBatchMaxConcurrentRequests sets the max number of concurrent
// SendSpanBatch requests. The same number bounds the metadata sends (API,
// string, SQL, exception) in flight at once.
func WithCollectorGrpcSpanBatchMaxConcurrentRequests(max int) ConfigOption {
	return withValue(CfgCollectorGrpcSpanBatchMaxConcurrentRequests, max)
}

// WithCollectorGrpcCompression sets the compression of the span batches and
// the stat stream: "gzip" (the default) or "none".
func WithCollectorGrpcCompression(compression string) ConfigOption {
	return withValue(CfgCollectorGrpcCompression, compression)
}

// WithSpanEventChunkSize sets the event chunk of a span.
func WithSpanEventChunkSize(size int) ConfigOption { return withValue(CfgSpanEventChunkSize, size) }

// WithSpanMaxCallStackDepth sets the max callstack depth of a span.
func WithSpanMaxCallStackDepth(depth int) ConfigOption {
	return withValue(CfgSpanMaxCallStackDepth, depth)
}

// WithSpanMaxCallStackSequence sets the max callstack sequence of a span.
func WithSpanMaxCallStackSequence(seq int) ConfigOption {
	return withValue(CfgSpanMaxCallStackSequence, seq)
}

// WithSpanIgnoreErrors sets the errors that are recorded as exception info but
// do not mark the span as failed. Each entry is "<type>:<message substring>";
// either part may be empty.
func WithSpanIgnoreErrors(rules ...string) ConfigOption { return withValue(CfgSpanIgnoreErrors, rules) }

// WithSpanErrorMark sets which error causes are allowed to fail a transaction.
// Each entry is one of "exception", "http-status" and "sql", or several of
// them comma separated; no entries at all means every cause. The unknown
// cause - a failure recorded with no category, as SetFailure does - is always
// marked, whatever this option and WithSpanErrorMarkExclude say.
func WithSpanErrorMark(categories ...string) ConfigOption {
	return withValue(CfgSpanErrorMark, categories)
}

// WithSpanErrorMarkExclude sets the error causes that must not fail a
// transaction, removed from whatever WithSpanErrorMark allows. The entries are
// spelled as in WithSpanErrorMark.
func WithSpanErrorMarkExclude(categories ...string) ConfigOption {
	return withValue(CfgSpanErrorMarkExclude, categories)
}

// WithHttpUrlStatEnable enables the agent collects the HTTP URL statistics.
func WithHttpUrlStatEnable(enable bool) ConfigOption { return withValue(CfgHttpUrlStatEnable, enable) }

// WithStatQueueSize sets the size of the queue buffering agent stat messages
// waiting to be sent to the collector.
func WithStatQueueSize(size int) ConfigOption { return withValue(CfgStatQueueSize, size) }

// WithHttpUrlStatLimitSize sets the maximum number of URLs that can be stored in one snapshot.
func WithHttpUrlStatLimitSize(size int) ConfigOption { return withValue(CfgHttpUrlStatLimitSize, size) }

// WithHttpUrlStatQueueSize sets the size of the queue buffering per-request URL
// statistics records until they are aggregated into a snapshot.
func WithHttpUrlStatQueueSize(size int) ConfigOption { return withValue(CfgHttpUrlStatQueueSize, size) }

// WithHttpUrlStatWithMethod adds http method as prefix to uri string key.
func WithHttpUrlStatWithMethod(withMethod bool) ConfigOption {
	return withValue(CfgHttpUrlStatWithMethod, withMethod)
}

// WithErrorTraceCallStack enables the agent collects a call stack when error occurs.
func WithErrorTraceCallStack(trace bool) ConfigOption {
	return withValue(CfgErrorTraceCallStack, trace)
}

// WithErrorCallStackDepth sets the maximum depth of call stack that can be dumped.
func WithErrorCallStackDepth(depth int) ConfigOption { return withValue(CfgErrorCallStackDepth, depth) }

// WithErrorMaxChainDepth sets how many links of an error's cause chain are
// recorded, the error itself included. 0 or less, and anything above 64, mean
// the 64-link ceiling.
func WithErrorMaxChainDepth(depth int) ConfigOption { return withValue(CfgErrorMaxChainDepth, depth) }

// WithErrorNewThroughput sets the maximum number of new exception chains
// recorded per second. 0 or less means unlimited.
func WithErrorNewThroughput(tps int) ConfigOption { return withValue(CfgErrorNewThroughput, tps) }
