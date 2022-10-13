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

## Config Option
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
The maximum length of ApplicationName is 24.

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
The maximum length of AgentId is 24.
If agent id is not set or the maximum length is exceeded, automatically generated id is given.

* --pinpoint-agentid
* PINPOINT_GO_AGENTID
* WithAgentId()
* string
* case-sensitive

### AgentName
AgentName option sets the agent name.
The maximum length of AgentName is 255.

* --pinpoint-agentname
* PINPOINT_GO_AGENTNAME
* WithAgentName()
* string
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

### Sampling.CounterRate
Sampling.CounterRate option sets the counter sampling rate.
Sample 1/rate. In other words, if the rate is 1, then it will be 100% and if it is 100, it will be 1% sampling.

* --pinpoint-sampling-counterrate
* PINPOINT_GO_SAMPLING_COUNTERRATE
* WithSamplingCounterRate()
* int
* default: 1
* valid range: 0 ~ 100

### Sampling.PercentRate
Sampling.PercentRate option sets the sampling rate for a 'percent sampler'.

* --pinpoint-sampling-percentrate
* PINPOINT_GO_SAMPLING_PERCENTRATE
* WithSamplingPercentRate()
* float
* default: 100
* valid range: 0.01 ~ 100

### Sampling.NewThroughput
Sampling.NewThroughput option sets the new TPS for a 'throughput sampler'.

* --pinpoint-sampling-newthroughput
* PINPOINT_GO_SAMPLING_NEWTHROUGHPUT
* WithSamplingNewThroughput()
* type: int
* default: 0

### Sampling.ContinueThroughput
Sampling.ContinueThroughput option sets the cont TPS for a 'throughput sampler'.

* --pinpoint-sampling-continuethroughput
* PINPOINT_GO_SAMPLING_CONTINUETHROUGHPUT
* WithSamplingContinueThroughput()
* type: int
* default: 0

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

### SQL.MaxBindValueSize
SQL.MaxBindValueSize option sets the max length of traced bind value for SQL Driver.

* --pinpoint-sql-maxbindvaluesize
* PINPOINT_GO_SQL_MAXBINDVALUESIZE
* WithSQLMaxBindValueSize()
* type: int
* default: 1024
* unit: bytes

### SQL.TraceCommit
SQL.TraceCommit option enables commit tracing for SQL Driver.

* --pinpoint-sql-tracecommit
* PINPOINT_GO_SQL_TRACECOMMIT
* WithSQLTraceCommit()
* type: bool
* default: true

### SQL.TraceRollback
SQL.TraceRollback option enables rollback tracing for SQL Driver.

* --pinpoint-sql-tracerollback
* PINPOINT_GO_SQL_TRACEROLLBACK
* WithSQLTraceRollback()
* type: bool
* default: true

### LogLevel
LogLevel option sets the level of log generated by the pinpoint agent. 
Either debug, info, warn, or error must be set.

* --pinpoint-loglevel
* PINPOINT_GO_LOGLEVEL
* WithLogLevel()
* type: string
* default: "info"
* case-insensitive

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
Refer https://docs.spring.io/spring-framework/docs/current/javadoc-api/org/springframework/util/AntPathMatcher.html.

* --pinpoint-http-server-excludeurl
* PINPOINT_GO_HTTP_SERVER_EXCLUDEURL
* WithHttpServerExcludeUrl()
* type: string slice
* case-sensitive

### Http.Server.ExcludeMethod
Http.Server.ExcludeMethod option sets HTTP Request methods to exclude from tracking.

* --pinpoint-http-server-excludemethod
* PINPOINT_GO_HTTP_SERVER_EXCLUDEMETHOD
* WithHttpServerExcludeMethod()
* type: string slice
* case-insensitive

### Http.Server.RecordRequestHeader
Http.Server.RecordRequestHeader option sets HTTP request headers to be logged on the server side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-server-recordrequestheader
* PINPOINT_GO_HTTP_SERVER_RECORDREQUESTHEADER
* WithHttpServerRecordRequestHeader()
* type: string slice
* case-insensitive

### Http.Server.RecordResponseHeader
Http.Server.RecordResponseHeader option sets HTTP response headers to be logged on the server side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-server-recordresponseheader
* PINPOINT_GO_HTTP_SERVER_RECORDRESPONSEHEADER
* WithHttpServerRecordRespondHeader()
* type: string slice
* case-insensitive

### Http.Server.RecordRequestCookie
Http.Server.RecordRequestCookie option sets HTTP request cookies to be logged on the server side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-server-recordrequestcookie
* PINPOINT_GO_HTTP_SERVER_RECORDREQUESTCOOKIE
* WithHttpServerRecordRequestCookie()
* type: string slice
* case-insensitive

### Http.Client.RecordRequestHeader
Http.Client.RecordRequestHeader option sets HTTP request headers to be logged on the client side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-client-recordrequestheader
* PINPOINT_GO_HTTP_CLIENT_RECORDREQUESTHEADER
* WithHttpClientRecordRequestHeader()
* type: string slice
* case-insensitive

### Http.Client.RecordResponseHeader
Http.Client.RecordResponseHeader option sets HTTP response headers to be logged on the client side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-client-recordresponseheader
* PINPOINT_GO_HTTP_CLIENT_RECORDRESPONSEHEADER
* WithHttpClientRecordRespondHeader()
* type: string slice
* case-insensitive


### Http.Client.RecordRequestCookie
Http.Client.RecordRequestCookie option sets HTTP request cookies to be logged on the client side.
If sets to "HEADERS-ALL", it records all request headers.

* --pinpoint-http-client-recordrequestcookie
* PINPOINT_GO_HTTP_CLIENT_RECORDREQUESTCOOKIE
* WithHttpClientRecordRequestCookie()
* type: string slice
* case-insensitive
