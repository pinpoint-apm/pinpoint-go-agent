# Pinpoint Go Agent Configuration

## Overview
Pinpoint Go Agent creates a Config populated with default settings, command line flags, environment variables, config file 
and config functions are prefixed with 'With', such as WithAppName.
Config uses the following precedence order.
Each item takes precedence over the item below it:

1. command line flag
2. environment variable
3. config file
4. config function
5. default

For example, if a configuration item is specified in the environment variable and in the configuration file respectively,
the value set in the environment variable is finally used.

### Dynamic Configuration
Pinpoint Go Agent supports the ability to have your application live read a config file while running.
Configuration options marked with the **dynamic** can be changed at runtime when you change the config file.

## Configuration Option
The titles below are used as configuration keys in config file.
In the description of each config option below, the list is shown in the order command flag, environment variable,
config function, value type and additional information.

### ConfigFile
The config options below can be saved to the config file is set by ConfigFile option.
It is supported JSON, YAML and Properties config files
and configuration keys used in config files are case-insensitive.

* --pinpoint-configfile
* PINPOINT_GO_CONFIGFILE
* WithConfigFile()
* string
* case-sensitive

For `.` delimited path keys, they are accessed in nested field.
The format of the YAML config file is as follows:
``` yaml
applicationName: "MyAppName"
collector:
  host: "collector.myhost.com"
sampling:
  type: "percent"
  percentRate: 10
logLevel: "error"
```

* [YAML File Example](/example/pinpoint-config.yaml)
* [JSON File Example](/example/pinpoint-config.json)
* [Properties File Example](/example/pinpoint-config.prop)

### ActiveProfile
The configuration profile feature is supported.
You can set the profile in the config file and specify the profile to activate with the ActiveProfile option.

* --pinpoint-activeprofile
* PINPOINT_GO_ACTIVEPROFILE
* WithActiveProfile()
* string
* case-insensitive

The example below shows that config file and profile are set by command flag.
```
--pinpoint-configfile=pinpoint-config.json --pinpoint-activeprofile=dev
```
```json
{
  "applicationName": "JsonAppName",
  "agentId": "JsonAgentID",
  "loglevel": "debug",
  "profile": {
    "dev": {
      "collector": {
        "host": "dev.collector.host"
      },
      "sampling": {
        "type": "COUNTER",
        "CounterRate": 1
      }
    },
    "real": {
      "collector": {
        "host": "real.collector.host"
      },
      "sampling": {
        "type": "percent",
        "percentRate": 5.5
      }
    }
  }
}
```

### ApplicationName
ApplicationName option sets the application name.
If this option is not provided, the agent can't be started.
The maximum length depends on Uid.Version: 24 bytes for v1, and 254 bytes for v3 and v4.
See [Identity Versions](#identity-versions).

* --pinpoint-applicationname
* PINPOINT_GO_APPLICATIONNAME
* WithAppName()
* string
* case-sensitive

### ApplicationType
ApplicationType option sets the application type.

* --pinpoint-applicationtype
* PINPOINT_GO_APPLICATIONTYPE
* WithAppType()
* int
* default: 1800 (ServiceTypeGoApp)

### AgentId
AgentId option set id to distinguish agent.
We recommend that you enable hostname to be included.
For Uid.Version v1 and v3, the maximum length of AgentId is 24 bytes.
If agent id is not set, has invalid characters, or the maximum length is exceeded, an id is automatically generated.
For Uid.Version v4 this option is ignored: the agent id is always generated at startup.
See [Identity Versions](#identity-versions).

* --pinpoint-agentid
* PINPOINT_GO_AGENTID
* WithAgentId()
* string
* case-sensitive

### AgentName
AgentName option sets the agent name.
If this option is not set, the resolved AgentId is used as AgentName.
The maximum length is 255 bytes for Uid.Version v1 and v3, and 254 bytes for v4.
See [Identity Versions](#identity-versions).

* --pinpoint-agentname
* PINPOINT_GO_AGENTNAME
* WithAgentName()
* string
* case-sensitive

### Uid.Version
Uid.Version option selects the agent identity format used to identify the agent to Pinpoint collector.
It mirrors the Java agent's `pinpoint.modules.uid.version` property.
Supported values are v1, v3 and v4.
The default is v3, and unknown or empty values fall back to v3.

**v4 is not usable at this time.**
The v4 identity protocol is implemented in the agent, but it has not been released on the Pinpoint server side yet,
so no collector accepts it.
Use v1 or v3; the v4 details below are documented for when server-side support ships.

* --pinpoint-uid-version
* PINPOINT_GO_UID_VERSION
* WithUidVersion()
* string
* default: "v3"
* case-insensitive

#### Identity Versions

| | v1 | v3 (default) | v4 |
|---|---|---|---|
| ApplicationName | **required**, max 24 bytes | **required**, max 254 bytes | **required**, max 254 bytes |
| AgentId | optional, max 24 bytes; auto-generated when unset or invalid | same as v1 | not configurable, always auto-generated |
| AgentName | optional, max 255 bytes; falls back to AgentId | same as v1 | optional, max 254 bytes; falls back to AgentId |
| ServiceName | not used | not used | **required**, max 254 bytes |
| ApiKey | not used | not used | **required**, non-empty (no length or character check) |
| gRPC `protocol.version` header | 100 | 100 | 400 |
| gRPC headers sent | `applicationname`, `agentid`, `agentname`, `starttime`, `servicetype`, `protocol.version` | same as v1 | v1 headers plus `servicename` and `apikey` |

ApplicationName, AgentId, AgentName and ServiceName must match `[a-zA-Z0-9\._\-]+`,
and the maximum lengths above are UTF-8 byte lengths.
ApiKey is checked for non-emptiness only.

An auto-generated agent id is a 22 character URL-safe Base64 UUIDv7.
Because v4 always generates it, the agent id changes on every restart; use AgentName for a stable label.

v1 and v3 are identical on the wire, both sending `protocol.version=100`;
they differ only in the ApplicationName length limit.
A missing or invalid required value aborts agent startup:
NewAgent returns a no-op agent and an error.

The `socketid` header is not listed above because it is not part of the identity headers;
it is added by the ping stream for every version.

### ServiceName
ServiceName option sets the service name reported to Pinpoint collector.
It is used only when Uid.Version is v4, where it is required and its maximum length is 254 bytes.
It is ignored for v1 and v3.
If it is not set, has invalid characters, or the maximum length is exceeded, agent startup fails.
Note that v4 is not usable at this time, so this option currently has no effect. See [Uid.Version](#uidversion).

* --pinpoint-servicename
* PINPOINT_GO_SERVICENAME
* WithServiceName()
* string
* default: ""
* case-sensitive

### ApiKey
ApiKey option sets the api key sent to Pinpoint collector on the `apikey` gRPC header.
It is used only when Uid.Version is v4, where it is required.
It is ignored for v1 and v3.
Only non-emptiness is checked; there is no length or character restriction.
If it is not set, agent startup fails.
The value is masked in agent logs and is never logged in plaintext.
Note that v4 is not usable at this time, so this option currently has no effect. See [Uid.Version](#uidversion).

* --pinpoint-apikey
* PINPOINT_GO_APIKEY
* WithApiKey()
* string
* default: ""
* case-sensitive

### Collector.Host
Collector.Host option sets the host address of Pinpoint collector.

* --pinpoint-collector-host
* PINPOINT_GO_COLLECTOR_HOST
* WithCollectorHost()
* string
* default: "localhost"
* case-sensitive

### Collector.AgentPort
Collector.AgentPort option sets the agent port of Pinpoint collector.

* --pinpoint-collector-agentport
* PINPOINT_GO_COLLECTOR_AGENTPORT
* WithCollectorAgentPort()
* int
* default: 9991

### Collector.SpanPort
Collector.SpanPort option sets the span port of Pinpoint collector.

* --pinpoint-collector-spanport
* PINPOINT_GO_COLLECTOR_SPANPORT
* WithCollectorSpanPort()
* int
* default: 9993

### Collector.StatPort
Collector.StatPort option sets the stat port of Pinpoint collector.

* --pinpoint-collector-statport
* PINPOINT_GO_COLLECTOR_STATPORT
* WithCollectorStatPort()
* int
* default: 9992

### Sampling.Type
Sampling.Type option sets the type of agent sampler.
Either "COUNTER" or "PERCENT" must be specified.

* --pinpoint-sampling-type
* PINPOINT_GO_SAMPLING_TYPE
* WithSamplingType()
* string
* default: "COUNTER"
* case-insensitive
* dynamic

### Sampling.CounterRate
Sampling.CounterRate option sets the counter sampling rate.
Sample 1/rate. In other words, if the rate is 1, then it will be 100% and if it is 100, it will be 1% sampling.

* --pinpoint-sampling-counterrate
* PINPOINT_GO_SAMPLING_COUNTERRATE
* WithSamplingCounterRate()
* int
* default: 1
* valid range: 0 ~ 100
* dynamic

### Sampling.PercentRate
Sampling.PercentRate option sets the sampling rate for a 'percent sampler'.

* --pinpoint-sampling-percentrate
* PINPOINT_GO_SAMPLING_PERCENTRATE
* WithSamplingPercentRate()
* float
* default: 100
* valid range: 0.01 ~ 100
* dynamic

### Sampling.NewThroughput
Sampling.NewThroughput option sets the new TPS for a 'throughput sampler'.

* --pinpoint-sampling-newthroughput
* PINPOINT_GO_SAMPLING_NEWTHROUGHPUT
* WithSamplingNewThroughput()
* type: int
* default: 0
* dynamic

### Sampling.ContinueThroughput
Sampling.ContinueThroughput option sets the cont TPS for a 'throughput sampler'.

* --pinpoint-sampling-continuethroughput
* PINPOINT_GO_SAMPLING_CONTINUETHROUGHPUT
* WithSamplingContinueThroughput()
* type: int
* default: 0
* dynamic

### Span.QueueSize
Span.QueueSize option sets the size of agent's span queue for gRPC.

* --pinpoint-span-queuesize
* PINPOINT_GO_SPAN_QUEUESIZE
* WithSpanQueueSize()
* type: int
* default: 1024

### Span.Batch.Enable
Span.Batch.Enable option enables SendSpanBatch unary requests instead of the long-lived SendSpan stream.

* --pinpoint-span-batch-enable
* PINPOINT_GO_SPAN_BATCH_ENABLE
* WithSpanBatchEnable()
* type: bool
* default: false

### Span.BatchSize
Span.BatchSize option sets the max number of spans per SendSpanBatch request.

* --pinpoint-span-batchsize
* PINPOINT_GO_SPAN_BATCHSIZE
* WithSpanBatchSize()
* type: int
* default: 50

### Span.BatchFlushInterval
Span.BatchFlushInterval option sets how long span batch sender waits for an available request permit.

* --pinpoint-span-batchflushinterval
* PINPOINT_GO_SPAN_BATCHFLUSHINTERVAL
* WithSpanBatchFlushInterval()
* type: int
* default: 1000
* unit: milliseconds

### Span.BatchCollectDeadline
Span.BatchCollectDeadline option sets how long span batch sender collects additional spans after the first span arrives.

* --pinpoint-span-batchcollectdeadline
* PINPOINT_GO_SPAN_BATCHCOLLECTDEADLINE
* WithSpanBatchCollectDeadline()
* type: int
* default: 500
* unit: milliseconds

### Span.BatchMaxConcurrentRequests
Span.BatchMaxConcurrentRequests option sets the max number of concurrent SendSpanBatch requests.

* --pinpoint-span-batchmaxconcurrentrequests
* PINPOINT_GO_SPAN_BATCHMAXCONCURRENTREQUESTS
* WithSpanBatchMaxConcurrentRequests()
* type: int
* default: 10

### Span.EventChunkSize
Span.EventChunkSize option sets the size of span event chunk for gRPC.

* --pinpoint-span-eventchunksize
* PINPOINT_GO_SPAN_EVENTCHUNKSIZE
* WithSpanEventChunkSize()
* type: int
* default: 20

### Span.MaxCallStackDepth
Span.MaxCallStackDepth option sets the max callstack depth of a span, if -1 is unlimited and min is 2.

* --pinpoint-span-maxcallstackdepth
* PINPOINT_GO_SPAN_MAXCALLSTACKDEPTH
* WithSpanMaxCallStackDepth()
* type: int
* default: 64
* dynamic

### Span.MaxCallStackSequence
Span.MaxCallStackDepth option sets the max callstack sequence of a span, if -1 is unlimited and min is 4.

* --pinpoint-span-maxcallstacksequence
* PINPOINT_GO_SPAN_MAXCALLSTACKSEQUENCE
* WithSpanMaxCallStackSequence()
* type: int
* default: 5000
* dynamic

### Stat.CollectInterval
Stat.CollectInterval option sets the statistics collection cycle for the agent.

* --pinpoint-stat-collectinterval
* PINPOINT_GO_STAT_COLLECTINTERVAL
* WithStatCollectInterval()
* type: int
* default: 5000
* unit: milliseconds

### Stat.BatchCount
Stat.BatchCount option sets batch delivery units for collected statistics.

* --pinpoint-stat-batchcount
* PINPOINT_GO_STAT_BATCHCOUNT
* WithStatBatchCount()
* type: int
* default: 6

### SQL.TraceBindValue
SQL.TraceBindValue option enables bind value tracing for SQL Driver.

* --pinpoint-sql-tracebindvalue
* PINPOINT_GO_SQL_TRACEBINDVALUE
* WithSQLTraceBindValue()
* type: bool 
* default: true
* dynamic

### SQL.MaxBindValueSize
SQL.MaxBindValueSize option sets the max length of traced bind value for SQL Driver.

* --pinpoint-sql-maxbindvaluesize
* PINPOINT_GO_SQL_MAXBINDVALUESIZE
* WithSQLMaxBindValueSize()
* type: int
* default: 1024
* unit: bytes
* dynamic

### SQL.TraceCommit
SQL.TraceCommit option enables commit tracing for SQL Driver.

* --pinpoint-sql-tracecommit
* PINPOINT_GO_SQL_TRACECOMMIT
* WithSQLTraceCommit()
* type: bool
* default: true
* dynamic

### SQL.TraceRollback
SQL.TraceRollback option enables rollback tracing for SQL Driver.

* --pinpoint-sql-tracerollback
* PINPOINT_GO_SQL_TRACEROLLBACK
* WithSQLTraceRollback()
* type: bool
* default: true
* dynamic

### SQL.TraceQueryStat
SQL.TraceQueryStat option enables trace SQL query statistics.

* --pinpoint-sql-tracequerystat
* PINPOINT_GO_SQL_TRACEQUERYSTAT
* WithSQLTraceQueryStat()
* type: bool
* default: false
* dynamic


### Log.Level
Log.Level option sets the level of log generated by the agent. 
Either trace, debug, info, warn, or error must be set.

* --pinpoint-log-level
* PINPOINT_GO_LOG_LEVEL
* WithLogLevel()
* type: string
* default: "info"
* case-insensitive
* dynamic

### Log.Output
Log.Output option sets the output file of log generated by the agent.
You can set stderr, stdout or file path.

* --pinpoint-log-output
* PINPOINT_GO_LOG_OUTPUT
* WithLogOutput()
* type: string
* default: "stderr"
* case-insensitive
* dynamic

### Log.MaxSize
Log.MaxSize option sets the max size of log file. The unit of value is MB.

* --pinpoint-log-maxsize
* PINPOINT_GO_LOG_MAXSIZE
* WithLogMaxSize()
* type: int
* default: 10
* dynamic

### Error.TraceCallStack
Error.TraceCallStack option enables trace callstack dump when a error occurs.

* --pinpoint-error-tracecallstack
* PINPOINT_GO_ERROR_TRACECALLSTACK
* WithErrorTraceCallStack()
* type: bool
* default: false
* dynamic

### Error.CallStackDepth
Error.CallStackDepth option sets the max depth of callstack to be dumped.

* --pinpoint-error-callstackdepth
* PINPOINT_GO_ERROR_CALLSTACKDEPTH
* WithErrorCallStackDepth()
* type: int
* default: 32
* dynamic

### IsContainerEnv
IsContainerEnv option sets whether the application is running in a container environment or not.
If this is not set, the agent automatically checks it.

* --pinpoint-iscontainerenv
* PINPOINT_GO_ISCONTAINERENV
* WithIsContainerEnv()
* type: bool
* default: false

### Enable
Enable option enables the agent is operational state.
If this is set as false, the agent doesn't start working.

* --pinpoint-enable
* PINPOINT_GO_ENABLE
* WithEnable()
* type: bool
* default: true

### Http.Server.StatusCodeErrors
Http.Server.StatusCodeErrors option sets HTTP status code with request failure.
Refer https://pinpoint-apm.gitbook.io/pinpoint/documents/http-status-code-failure.

* --pinpoint-http-server-statuscodeerrors
* PINPOINT_GO_HTTP_SERVER_STATUSCODEERRORS
* WithHttpServerStatusCodeError()
* type: string slice
* default: {"5xx"}
* case-insensitive
* dynamic

The string slice value is set as follows.
```
--pinpoint-http-server-statuscodeerrors=5xx,301,400
```
```
export PINPOINT_GO_HTTP_SERVER_STATUSCODEERRORS=5xx,301,400
```
``` yaml
http:
  server: 
    statusCodeErrors:
      - 5xx
      - 301
      - 400
```

### Http.Server.ExcludeUrl
Http.Server.ExcludeUrl option sets URLs to exclude from tracking.
It supports ant style pattern. (e.g. /aa/*.html, /??/exclude.html)
A pattern matches the whole URL path, where `?` matches exactly one character other than `/`,
`*` matches zero or more characters within a single path segment, and `**` matches zero or more
characters across path segments. Every other character, including regular expression
metacharacters, is matched literally. URI template variables (e.g. `/aa/{name}.html`) are not
supported.
Refer https://docs.spring.io/spring-framework/docs/current/javadoc-api/org/springframework/util/AntPathMatcher.html.

* --pinpoint-http-server-excludeurl
* PINPOINT_GO_HTTP_SERVER_EXCLUDEURL
* WithHttpServerExcludeUrl()
* type: string slice
* case-sensitive
* dynamic

### Http.Server.ExcludeMethod
Http.Server.ExcludeMethod option sets HTTP Request methods to exclude from tracking.

* --pinpoint-http-server-excludemethod
* PINPOINT_GO_HTTP_SERVER_EXCLUDEMETHOD
* WithHttpServerExcludeMethod()
* type: string slice
* case-insensitive
* dynamic

### Http.Server.RecordRequestHeader
Http.Server.RecordRequestHeader option sets HTTP request headers to be logged on the server side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-server-recordrequestheader
* PINPOINT_GO_HTTP_SERVER_RECORDREQUESTHEADER
* WithHttpServerRecordRequestHeader()
* type: string slice
* case-insensitive
* dynamic

### Http.Server.RecordResponseHeader
Http.Server.RecordResponseHeader option sets HTTP response headers to be logged on the server side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-server-recordresponseheader
* PINPOINT_GO_HTTP_SERVER_RECORDRESPONSEHEADER
* WithHttpServerRecordRespondHeader()
* type: string slice
* case-insensitive
* dynamic

### Http.Server.RecordRequestCookie
Http.Server.RecordRequestCookie option sets HTTP request cookies to be logged on the server side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-server-recordrequestcookie
* PINPOINT_GO_HTTP_SERVER_RECORDREQUESTCOOKIE
* WithHttpServerRecordRequestCookie()
* type: string slice
* case-insensitive
* dynamic

### Http.Server.RecordHandlerError
Http.Server.RecordHandlerError sets whether to record the error returned by http handler.

* --pinpoint-http-server-recordhandlererror
* PINPOINT_GO_HTTP_SERVER_RECORDHANDLERERROR
* WithHttpServerRecordHandlerError()
* type: bool
* default: true
* dynamic

### Http.Client.RecordRequestHeader
Http.Client.RecordRequestHeader option sets HTTP request headers to be logged on the client side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-client-recordrequestheader
* PINPOINT_GO_HTTP_CLIENT_RECORDREQUESTHEADER
* WithHttpClientRecordRequestHeader()
* type: string slice
* case-insensitive
* dynamic

### Http.Client.RecordResponseHeader
Http.Client.RecordResponseHeader option sets HTTP response headers to be logged on the client side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-client-recordresponseheader
* PINPOINT_GO_HTTP_CLIENT_RECORDRESPONSEHEADER
* WithHttpClientRecordRespondHeader()
* type: string slice
* case-insensitive
* dynamic

### Http.Client.RecordRequestCookie
Http.Client.RecordRequestCookie option sets HTTP request cookies to be logged on the client side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-client-recordrequestcookie
* PINPOINT_GO_HTTP_CLIENT_RECORDREQUESTCOOKIE
* WithHttpClientRecordRequestCookie()
* type: string slice
* case-insensitive
* dynamic

### Http.UrlStat.Enable
Http.UrlStat.Enable option enables the agent's HTTP URL statistics feature.
If this is set as false, the agent doesn't collect HTTP URL statistics.
Pinpoint Go Agent collects response times, successes and failures for all http requests regardless of sampling.
The HTTP URL statistics feature is supported from Pinpoint version 2.5.0.

* --pinpoint-http-urlstat-enable
* PINPOINT_GO_HTTP_URLSTAT_ENABLE
* WithHttpUrlStatEnable()
* type: bool
* default: false
* dynamic

### Http.UrlStat.LimitSize
Http.UrlStat.LimitSize option sets the limit size of the URLs to be collected.

* --pinpoint-http-urlstat-limitsize
* PINPOINT_GO_HTTP_URLSTAT_LIMITSIZE
* WithHttpUrlStatLimitSize()
* type: int
* default: 1024
* dynamic

### Http.UrlStat.QueueSize
Http.UrlStat.QueueSize option sets the size of the agent's URL statistics queue.
This queue buffers the per-request URL records waiting to be aggregated into a snapshot,
unlike Http.UrlStat.LimitSize which caps the number of distinct URLs kept in one snapshot.
When the queue is full the records are dropped, and the agent logs a rate-limited warning
carrying the cumulative number of dropped records.

* --pinpoint-http-urlstat-queuesize
* PINPOINT_GO_HTTP_URLSTAT_QUEUESIZE
* WithHttpUrlStatQueueSize()
* type: int
* default: 1024

### Http.UrlStat.WithMethod
Http.UrlStat.WithMethod option adds http method as prefix to url string key.

* --pinpoint-http-urlstat-withmethod
* PINPOINT_GO_HTTP_URLSTAT_WITHMETHOD
* WithHttpUrlStatWithMethod()
* type: bool
* default: false
* dynamic
