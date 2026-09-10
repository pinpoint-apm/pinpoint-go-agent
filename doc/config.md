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

A reload keeps the initial precedence: an option given by command line flag or environment variable is not
overwritten by the config file, and neither is a value set through `Config.Set()`.
Only options whose current value came from the config file, a profile, a config function or the default are updated.
`Config.Set()` on a non-dynamic option stores the value and logs a warning; the agent applies it after a restart.

### Malformed Values
A value that cannot be converted to the type its option is declared with is rejected: the agent logs

```
<option> = <value> is not a valid <type>, keeping <current value>
```

and the option keeps the value it already had - at startup the config function value or the default,
on a reload the value currently in effect. A lower precedence source is not consulted as a fallback.
This holds for every source: the config file, a profile, an environment variable, a command line flag,
a config function and `Config.Set()`. So `Sampling.CounterRate: abc` leaves the rate as it was instead
of turning it into 0, which would have stopped sampling every transaction with nothing in the log.

A comma separated string is still accepted where a list is expected (`a,b,c`), because that is how an
environment variable spells one, but a bare scalar is not: a list has to be written as a list.

A value of the right type but outside the option's range is a separate check, described with the option
below; those recover the option's default rather than keeping the previous value.

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
The agent id is not configurable. Every process generates its own id at startup
(base64url of a UUIDv7, 22 bytes) for all Uid.Version values, so each instance is
always distinct in the collector. Use [AgentName](#agentname) for a stable,
human-readable label.
See [Identity Versions](#identity-versions).

### AgentName
AgentName option sets the agent name.
If this option is not set, the generated AgentId is used as AgentName.
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
| AgentId | not configurable, always auto-generated | same as v1 | same as v1 |
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

### Collector.AgentInfo.RefreshInterval
Collector.AgentInfo.RefreshInterval option sets the cycle for re-sending the agent information to the collector.
If it is 0 or less, the agent information is sent only once at agent startup.
The default matches the Java and C++ agents (24 hours).

* --pinpoint-collector-agentinfo-refreshinterval
* PINPOINT_GO_COLLECTOR_AGENTINFO_REFRESHINTERVAL
* WithCollectorAgentInfoRefreshInterval()
* type: int
* default: 86400000 (24 hours)
* unit: milliseconds

### Collector.AgentInfo.SendRetryInterval
Collector.AgentInfo.SendRetryInterval option sets the wait between agent information send retries.
It paces two loops: the registration retry at agent startup, which repeats until the collector accepts the AgentInfo, and the retries within one periodic refresh cycle.
The wait is randomized by +/-30% so agents restarted together do not retry in lockstep, and it does not escalate - a collector that keeps rejecting the registration is polled at this interval for as long as the process runs.
Only the refresh use has no effect if Collector.AgentInfo.RefreshInterval is 0.

* --pinpoint-collector-agentinfo-sendretryinterval
* PINPOINT_GO_COLLECTOR_AGENTINFO_SENDRETRYINTERVAL
* WithCollectorAgentInfoSendRetryInterval()
* type: int
* default: 3000
* unit: milliseconds

### Collector.AgentInfo.MaxTryPerAttempt
Collector.AgentInfo.MaxTryPerAttempt option sets the max number of agent information sends per refresh cycle.
It has no effect if Collector.AgentInfo.RefreshInterval is 0.

* --pinpoint-collector-agentinfo-maxtryperattempt
* PINPOINT_GO_COLLECTOR_AGENTINFO_MAXTRYPERATTEMPT
* WithCollectorAgentInfoMaxTryPerAttempt()
* type: int
* default: 3

### Collector.Grpc.KeepAliveTime
Collector.Grpc.KeepAliveTime option sets the interval in milliseconds after which the agent sends an HTTP/2
keepalive ping on an idle collector connection.
The options below apply equally to every collector connection (agent, metadata, span, stat and command channels).

* --pinpoint-collector-grpc-keepalivetime
* PINPOINT_GO_COLLECTOR_GRPC_KEEPALIVETIME
* WithCollectorGrpcKeepAliveTime()
* int
* default: 30000

### Collector.Grpc.KeepAliveTimeout
Collector.Grpc.KeepAliveTimeout option sets the time in milliseconds the agent waits for a keepalive ping ack
before closing the connection.

* --pinpoint-collector-grpc-keepalivetimeout
* PINPOINT_GO_COLLECTOR_GRPC_KEEPALIVETIMEOUT
* WithCollectorGrpcKeepAliveTimeout()
* int
* default: 60000

### Collector.Grpc.KeepAlivePermitWithoutCalls
Collector.Grpc.KeepAlivePermitWithoutCalls option sets whether keepalive pings are sent even when there is no
active stream.
The default is false, matching the C++ agent. Agents older than this release always behaved as if it were true.

* --pinpoint-collector-grpc-keepalivepermitwithoutcalls
* PINPOINT_GO_COLLECTOR_GRPC_KEEPALIVEPERMITWITHOUTCALLS
* WithCollectorGrpcKeepAlivePermitWithoutCalls()
* type: bool
* default: false

### Collector.Grpc.MaxSendMessageSize
Collector.Grpc.MaxSendMessageSize option sets the max size in bytes of a gRPC message the agent can send.

* --pinpoint-collector-grpc-maxsendmessagesize
* PINPOINT_GO_COLLECTOR_GRPC_MAXSENDMESSAGESIZE
* WithCollectorGrpcMaxSendMessageSize()
* int
* default: 4194304

### Collector.Grpc.MaxReceiveMessageSize
Collector.Grpc.MaxReceiveMessageSize option sets the max size in bytes of a gRPC message the agent can receive.

* --pinpoint-collector-grpc-maxreceivemessagesize
* PINPOINT_GO_COLLECTOR_GRPC_MAXRECEIVEMESSAGESIZE
* WithCollectorGrpcMaxReceiveMessageSize()
* int
* default: 4194304

### Collector.Grpc.FlowControlWindow
Collector.Grpc.FlowControlWindow option sets the initial HTTP/2 flow-control window size in bytes.

* --pinpoint-collector-grpc-flowcontrolwindow
* PINPOINT_GO_COLLECTOR_GRPC_FLOWCONTROLWINDOW
* WithCollectorGrpcFlowControlWindow()
* int
* default: 1048576

### Collector.Grpc.WriteBufferSize
Collector.Grpc.WriteBufferSize option sets the gRPC transport write buffer size in bytes.

* --pinpoint-collector-grpc-writebuffersize
* PINPOINT_GO_COLLECTOR_GRPC_WRITEBUFFERSIZE
* WithCollectorGrpcWriteBufferSize()
* int
* default: 1048576

### Collector.Grpc.MaxHeaderListSize
Collector.Grpc.MaxHeaderListSize option sets the max size in bytes of gRPC response headers the agent accepts.

* --pinpoint-collector-grpc-maxheaderlistsize
* PINPOINT_GO_COLLECTOR_GRPC_MAXHEADERLISTSIZE
* WithCollectorGrpcMaxHeaderListSize()
* int
* default: 8192

### Collector.Grpc.SslEnable
Collector.Grpc.SslEnable option enables TLS on all gRPC channels
(agent, metadata, span, stat) to Pinpoint collector.
When disabled (default), the agent connects in plaintext as before.

* --pinpoint-collector-grpc-sslenable
* PINPOINT_GO_COLLECTOR_GRPC_SSLENABLE
* WithCollectorGrpcSslEnable()
* bool
* default: false

### Collector.Grpc.TrustCertFilePath
Collector.Grpc.TrustCertFilePath option sets the path of a PEM certificate
used as the trust root when verifying the collector's TLS certificate.
If it is not set, the system root CAs are used.
If the file cannot be read or is not a valid certificate, the agent logs an
error and fails the collector connection instead of falling back to plaintext,
so the agent stays disabled.
It is ignored unless [Collector.Grpc.SslEnable](#collectorgrpcsslenable) is enabled.

* --pinpoint-collector-grpc-trustcertfilepath
* PINPOINT_GO_COLLECTOR_GRPC_TRUSTCERTFILEPATH
* WithCollectorGrpcTrustCertFilePath()
* string
* default: ""
* case-sensitive

### Collector.Grpc.ConnectionMaxAge
Collector.Grpc.ConnectionMaxAge option sets the max age in milliseconds of a collector connection.
Once a connection is older than this, the next send opens a replacement connection and switches over
as soon as the replacement is ready; the old connection is closed only then, so no send fails over the switch.
If the replacement never becomes ready, the old connection keeps serving.
Use it when the collector sits behind an L4 load balancer or is scaled out, so that agents already
connected spread across the collector instances over time instead of staying pinned to the one they first reached.
The connection is only replaced while traffic flows, and the age is randomized by +/-10% so that agents
deployed together do not reconnect in lockstep.
This corresponds to the Java agent's `profiler.transport.grpc.loadbalancer.renew.period.millis`.
The default 0 keeps a working connection for as long as the agent runs.

* --pinpoint-collector-grpc-connectionmaxage
* PINPOINT_GO_COLLECTOR_GRPC_CONNECTIONMAXAGE
* WithCollectorGrpcConnectionMaxAge()
* int
* default: 0
* unit: milliseconds

### Collector.Grpc.StreamMaxAge
Collector.Grpc.StreamMaxAge option sets the max age in milliseconds of the long-lived ping, span (when
[Span.Batch.Enable](#spanbatchenable) is off), stat and command streams.
A stream older than this is closed normally and reopened before the next send, so no span or stat is dropped;
the command stream, which waits on the collector, is reopened when its age runs out.
This corresponds to the Java agent's `profiler.transport.grpc.span.sender.rpc.age.max.millis`, and like it the age
is randomized by +/-10%.
The default 0 keeps a stream open until it fails.

* --pinpoint-collector-grpc-streammaxage
* PINPOINT_GO_COLLECTOR_GRPC_STREAMMAXAGE
* WithCollectorGrpcStreamMaxAge()
* int
* default: 0
* unit: milliseconds

### Collector.Grpc.IdleTimeout
Collector.Grpc.IdleTimeout option sets how long in milliseconds a collector connection may go without an RPC
before gRPC closes it and puts the channel into IDLE. An idle channel also stops its keepalive pings, so a
firewall or L4 load balancer on the path can drop the connection unnoticed, and the next send pays a reconnect
and possibly a backoff wait.
The default 0 disables idling: a quiet channel keeps its connection for as long as the agent runs.
Without this option grpc-go (v1.82.1) would apply its own default of 30 minutes, which an application with no
traffic reaches on the span channel in [Span.Batch.Enable](#spanbatchenable) mode.
Note that with [Collector.Grpc.KeepAlivePermitWithoutCalls](#collectorgrpckeepalivepermitwithoutcalls) at its
default false, a connection with no open stream sends no keepalive pings even when idling is disabled.
A negative value is treated as 0.
This corresponds to the Java agent's `ClientOption.idleTimeoutMillis`, which is set to 30 days (in effect
disabled), and to the C++ agent's `Collector.Grpc.IdleTimeoutMs`.

* --pinpoint-collector-grpc-idletimeout
* PINPOINT_GO_COLLECTOR_GRPC_IDLETIMEOUT
* WithCollectorGrpcIdleTimeout()
* int
* default: 0
* unit: milliseconds

### Collector.Grpc.SenderQueueSize
Collector.Grpc.SenderQueueSize option sets the size of the agent's metadata queue: the API, string, SQL and
exception metadata registered by spans and waiting to be sent to the collector.
It used to share [Span.QueueSize](#spanqueuesize). When the queue is full the oldest item is overwritten and
its cache entry released so a later span registers it again, and the agent logs a rate-limited warning
carrying the cumulative number of dropped items.
The retry schedule for failed metadata sends has its own fixed bound of 1000 and is not affected.
The default matches the Java agent's `profiler.transport.grpc.metadata.sender.executor.queue.size` and the
C++ agent's key of the same name.

* --pinpoint-collector-grpc-senderqueuesize
* PINPOINT_GO_COLLECTOR_GRPC_SENDERQUEUESIZE
* WithCollectorGrpcSenderQueueSize()
* type: int
* default: 1000
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)

### Sampling.Type
Sampling.Type option sets the type of agent sampler.
Either "COUNTER" or "PERCENT" must be specified. "COUNTING", the Java agent's
name for the counter sampler, is accepted as an alias of "COUNTER".

An unrecognized type falls back to "COUNTER" and keeps
[Sampling.CounterRate](#samplingcounterrate) as configured, so a typo does not
turn sampling off; the warning it logs names the type and the rate that were
applied.

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
The rate is truncated to hundredths of a percent, and a truncated rate of 0
samples no new transaction at all - so `0`, a negative rate, and any positive
rate below `0.01` (e.g. `0.005`) all turn percent sampling off. This is what the
Java agent does (`PercentSamplerFactory.java:40-48,56-58`: `<= 0` becomes
`FalseSampler`); a rate below `0.01` also logs a warning, because a positive
rate that samples nothing is more often a typo than an intent. A rate of `100`
or above always samples, Java's `TrueSampler`; a rate above `100` is clamped to
it with a warning, because a rate over the documented maximum reads as a misuse
of the option rather than an intent.

* --pinpoint-sampling-percentrate
* PINPOINT_GO_SAMPLING_PERCENTRATE
* WithSamplingPercentRate()
* float
* default: 100
* valid range: 0 ~ 100 (0 = no sampling)
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
It sizes the span queue only; the metadata queue is sized by
[Collector.Grpc.SenderQueueSize](#collectorgrpcsenderqueuesize) and the stat queue by [Stat.QueueSize](#statqueuesize).

* --pinpoint-span-queuesize
* PINPOINT_GO_SPAN_QUEUESIZE
* WithSpanQueueSize()
* type: int
* default: 1024
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)

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
* dynamic

### Span.MaxCallStackDepth
Span.MaxCallStackDepth option sets the max callstack depth of a span, if -1 is unlimited and min is 2.
Events nested one level deeper than this value are still recorded and the next level overflows, matching Java's `DefaultCallStack` (with the default 64, up to 65 levels are recorded).

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

### Span.IgnoreErrors
Span.IgnoreErrors option lists errors that are recorded as exception info (error function id and message)
but do not mark the span as failed (`err` stays 0, and the URL statistics count the request as a success).
Each entry is `<type>:<message substring>`; either part may be empty, and both must match the same error.
`<type>` is the Go type string of the error (`reflect.TypeOf(err).String()`, e.g. `*errors.errorString`,
`*fs.PathError`) or the error name passed to `SpanEventRecorder.SetError` (e.g. `panic`).
The error and every error it wraps are checked, following `Cause()` first and falling back to `Unwrap()`
(the same chain the exception recorder walks), up to 64 links deep.

This corresponds to the Java agent's `profiler.ignore-error-handler.<name>.class-name`,
`profiler.ignore-error-handler.<name>.exception-message.contains` and `profiler.ignore-error-handler.<name>.nested=true`.

* --pinpoint-span-ignoreerrors
* PINPOINT_GO_SPAN_IGNOREERRORS
* WithSpanIgnoreErrors()
* type: string slice
* default: none
* dynamic

Example (yaml):
```
Span:
  IgnoreErrors:
    - "*errors.errorString:not found"
    - "*context.deadlineExceededError"
```

### Span.ErrorMark
Span.ErrorMark option lists the error causes that are allowed to fail a transaction. Every
failure the agent records carries a cause, and the `err` field of a span is the OR of the
causes recorded on that transaction: `1` unknown, `2` exception, `4` http-status, `8` sql.
The server reads those bits to tell *why* a transaction failed, so a request that both
threw and returned 5xx reports `6`.

Each entry is one of `exception`, `http-status` and `sql`, matched case-insensitively; a
single entry may hold several of them comma separated. No entries at all - the default -
means every cause, which is what Java's unset `profiler.error.mark` means. An unrecognised
name is warned about and ignored, so a typo narrows nothing.

The unknown cause (`1`) is not nameable and is always marked: it is what
`SpanRecorder.SetFailure()` records when its caller names no cause, and excluding it would
amount to "never fail a transaction".

| Cause | Recorded by |
|---|---|
| `exception` | `SpanRecorder.SetError` and `SpanEventRecorder.SetError`, wherever they are called - a plugin, a SQL driver error, a recovered panic |
| `http-status` | a response status listed in `Http.Server.StatusCodeErrors` |
| `sql` | the `SQL.ErrorCount` limit on one transaction |
| `unknown` | `SpanRecorder.SetFailure()` called with no cause |

This corresponds to the Java agent's `profiler.error.mark`.

* --pinpoint-span-errormark
* PINPOINT_GO_SPAN_ERRORMARK
* WithSpanErrorMark()
* type: string slice
* default: none, which means every cause
* case-insensitive
* dynamic

Example (yaml):
```
Span:
  ErrorMark:
    - exception
    - sql
```

### Span.ErrorMarkExclude
Span.ErrorMarkExclude option lists the error causes that must **not** fail a transaction,
removed from whatever `Span.ErrorMark` allows (Java's `mark.removeAll(exclude)`, so an
exclusion wins over an inclusion). Entries are spelled as in `Span.ErrorMark`, and the unknown
cause cannot be excluded. `Span.ErrorMarkExclude: http-status` is how to say "a 5xx is not a
transaction failure" while keeping exceptions and the SQL count.

An excluded cause is dropped from the verdict and from nothing else: the HTTP status
annotation is still recorded, the exception info and its chain are still recorded, and the
SQL statements are still counted. What changes is `err`, the failure point in the scatter
chart and the failed histogram of the URL statistics - the three move together, so they
never disagree about the same request.

`Span.IgnoreErrors` excludes individual errors by type and message; this option excludes a
whole cause, however it was recorded.

This corresponds to the Java agent's `profiler.error.mark.exclude`.

* --pinpoint-span-errormarkexclude
* PINPOINT_GO_SPAN_ERRORMARKEXCLUDE
* WithSpanErrorMarkExclude()
* type: string slice
* default: none
* case-insensitive
* dynamic

Example (yaml):
```
Span:
  ErrorMarkExclude:
    - http-status
```

### Stat.CollectInterval
Stat.CollectInterval option sets the statistics collection cycle for the agent.
It is also the timer of the URL statistics send worker (see
[Http.UrlStat.Enable](#httpurlstatenable)): a completed URL stat tick is sent
immediately, and this interval bounds how late the last tick is closed once
traffic stops.

* --pinpoint-stat-collectinterval
* PINPOINT_GO_STAT_COLLECTINTERVAL
* WithStatCollectInterval()
* type: int
* default: 5000
* unit: milliseconds
* range: 1000 ~ 60000 (an out-of-range value falls back to the default with a warning log)

### Stat.BatchCount
Stat.BatchCount option sets batch delivery units for collected statistics.

* --pinpoint-stat-batchcount
* PINPOINT_GO_STAT_BATCHCOUNT
* WithStatBatchCount()
* type: int
* default: 6
* range: 1 ~ 100 (an out-of-range value falls back to the default with a warning log)

### Stat.QueueSize
Stat.QueueSize option sets the size of the agent's stat queue for gRPC.
This queue buffers the collected agent stat and URL stat messages waiting to be
sent to the collector; it is independent of Span.QueueSize, which the stat queue
used to share.
When the queue is full the oldest message is overwritten, and the agent logs a
rate-limited warning carrying the cumulative number of dropped messages.

* --pinpoint-stat-queuesize
* PINPOINT_GO_STAT_QUEUESIZE
* WithStatQueueSize()
* type: int
* default: 1024
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)

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
It applies to bind values only. A truncated list ends with a
`...(number of bind values)` marker, appended past the limit as in the Java
agent. The parameters extracted by SQL normalization are never truncated,
because the server splits them on `,` to restore the original statement. Only
the SQL text published as metadata is truncated, at 64KB, and it carries a
`...(original length)` marker as in the Java agent.

A negative value turns bind value tracing off entirely - the size becomes 0 and
`SQL.TraceBindValue` is set to false - and logs a warning.

* --pinpoint-sql-maxbindvaluesize
* PINPOINT_GO_SQL_MAXBINDVALUESIZE
* WithSQLMaxBindValueSize()
* type: int
* default: 1024
* range: 0 ~ 262144 (a larger value is clamped with a warning log; the ceiling
  is a sixteenth of the 4MB gRPC message limit, so one span event's bind values
  cannot fill a whole span message)
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

### SQL.EnableRawSqlCache
SQL.EnableRawSqlCache option enables caching of SQL normalization results keyed by the raw SQL text,
so repeatedly executed statements skip re-parsing.
Consider disabling it if your application inlines literal values into every query
instead of using bind variables, since such queries never repeat and every call
pays a small cache-miss overhead.

* --pinpoint-sql-enablerawsqlcache
* PINPOINT_GO_SQL_ENABLERAWSQLCACHE
* WithSQLEnableRawSqlCache()
* type: bool
* default: true
* dynamic

### SQL.CacheSize
SQL.CacheSize option sets how many statements each of the three SQL metadata
caches holds: the SQL-ID cache, the SQL-UID cache and the raw SQL cache
([SQL.EnableRawSqlCache](#sqlenablerawsqlcache)). Once a cache is full the least
recently used statement is evicted; its next execution registers it again under
a fresh id (or re-sends its UID metadata) and re-normalizes the raw text, so an
application running more distinct statements than this churns metadata traffic
and can leave spans referencing ids the collector never resolved. Raise it for
high-cardinality SQL; the worst-case memory is roughly entries x
[SQL.CacheLengthLimit](#sqlcachelengthlimit) per cache. The valid range is 1 to
65536; a value outside it is logged and the default is used.

The option applies to the SQL caches only. The API and error caches keep their
fixed 1024 entries, as in the Java agent, whose `profiler.jdbc.sqlcachesize`
sizes `SimpleCacheFactory.newSqlCache()` / `newSqlUidCache()` while
`newSimpleCache()` keeps its own default. It is read once when the agent builds
its caches (`NewAgent`): resizing them while spans are in flight would orphan
the ids those spans already carry.

This corresponds to the Java agent's `profiler.jdbc.sqlcachesize`. The C++
agent exposes the same setting as `Sql.CacheSize`.

* --pinpoint-sql-cachesize
* PINPOINT_GO_SQL_CACHESIZE
* WithSQLCacheSize()
* type: int
* default: 1024

### SQL.CacheLengthLimit
SQL.CacheLengthLimit option sets the max length of a SQL statement kept in the SQL
metadata caches. A statement at or above this length bypasses the cache: it is
registered again and its metadata is sent to the collector on every execution,
so a few huge generated statements cannot hold the cache - and their bytes - for
the life of the process. A limit of 0 caches nothing. A limit of exactly -1
caches every statement regardless of length; any other negative value is treated
as a typo and recovers the default, with a warning.

The limit applies to the SQL-UID cache and to the raw SQL cache
([SQL.EnableRawSqlCache](#sqlenablerawsqlcache)), whose keys are hashes and
whose values do not depend on being cached. It does **not** apply to the SQL-ID
cache, which is used when the collector does not support SQL UIDs. Those ids
come from an agent-local sequence, so bypassing the cache would issue a fresh id
- and send a fresh metadata message - on every execution of the statement, and
the same query would appear in the UI as a separate entry per execution. The
Java agent draws the same line: its bypass lives only in `UidCache`, while the
id cache built by `SimpleCacheFactory.newSqlCache()` has no length check.

This corresponds to the Java agent's `profiler.jdbc.sqlcachelengthlimit`.

* --pinpoint-sql-cachelengthlimit
* PINPOINT_GO_SQL_CACHELENGTHLIMIT
* WithSQLCacheLengthLimit()
* type: int
* default: 2048
* unit: bytes
* dynamic

### SQL.CacheExpireHours
SQL.CacheExpireHours option sets how long a SQL UID stays in the SQL-UID cache
before the statement is registered with the collector again. The collector keeps
SQL UID metadata for a limited time (180 days by default), so in a process that
runs longer than that a cached UID could outlive its row and the web UI would
show an empty SQL for it until the agent restarted. Zero never expires an entry;
a negative value is a typo rather than a request for that, so it recovers the
default with a warning, as do values above 876000 (100 years). The SQL-ID, API and error caches have no expiry, as in
the Java agent.

This corresponds to the Java agent's `profiler.jdbc.sqlcacheexpirehours`.

* --pinpoint-sql-cacheexpirehours
* PINPOINT_GO_SQL_CACHEEXPIREHOURS
* WithSQLCacheExpireHours()
* type: int
* default: 168
* unit: hours

### SQL.ErrorCount
SQL.ErrorCount option sets how many SQL executions mark a span as failed, so an
N+1 query loop shows up as an error instead of merely a slow trace. A marked span
is drawn as a failure point in the scatter chart and counted in the failed
histogram of the URL statistics. A value of 0 turns the count off and a negative
value means the same, warned and published as 0. A span that has already failed
is never counted, so the mark cannot replace an error that is already recorded.
This corresponds to the Java agent's `profiler.sql.error.count` and
`profiler.sql.error.enable`, which collapse into this single option. Java never
range-checks its count (DefaultSqlCountService.java:15-25 uses the configured
limit as given), so `enable=true` with a count of 0 or less marks the very first
query as failed; that literal reading is deliberately not reproduced, because in
the merged option 0 is already taken by `enable=false`, leaving "off" as the only
consistent meaning a non-positive threshold can have here.
Like Java, the count lives on the trace root, so queries spread over async spans
add up (WrappedSpanEventRecorder.java:112, DefaultSqlCountService.java:16,21).
The failure is recorded under the `sql` cause, so `Span.ErrorMarkExclude: sql` keeps
the counting without the verdict - see
[Span.ErrorMarkExclude](#spanerrormarkexclude).

* --pinpoint-sql-errorcount
* PINPOINT_GO_SQL_ERRORCOUNT
* WithSQLErrorCount()
* type: int
* default: 100
* dynamic

### SQL.RemoveComments
SQL.RemoveComments option drops comments from the normalized SQL instead of
copying them through. Nothing is put in a removed comment's place, and the
newline that ends a `--` or `//` comment goes with it, so
`SELECT /*+ INDEX(t idx) */ * FROM t WHERE id = 10` normalizes to
`SELECT  * FROM t WHERE id = 0#`. A comment is not a token boundary either way,
so `SELECT/*c*/1` normalizes to `SELECT1` and the `1` is not extracted as a
parameter.

This corresponds to the Java agent's `profiler.jdbc.removecomments`, whose
effective default is also `true` - the key is absent from the distributed
`pinpoint.config`, and an unresolved placeholder leaves the field initializer in
place. Turning it off makes the Go agent keep comments as it did before this
option existed.

The option is **startup-only**. The normalized text is both the SQL id cache key
and the input to the SQL UID hash, so flipping it against a populated cache
would report one statement under two different ids.

Comments are part of the statement's identity when they carry an Oracle hint or
an ORM's trace tag, and removing them merges statements that differ only in that
tag. That is what the Java agent does, and matching it is what makes a Go and a
Java service report the same query as the same query.

* --pinpoint-sql-removecomments
* PINPOINT_GO_SQL_REMOVECOMMENTS
* WithSQLRemoveComments()
* type: bool
* default: true


### Log.Level
Log.Level option sets the level of log generated by the agent. 
Either trace, debug, info, warn, or error must be set. Any other value -
including `fatal` and `panic`, which the underlying logrus library would
accept and which would silence warn and error - is rejected with an error log,
and the level in effect is kept: the default at startup, the previous level on
a reload.

* --pinpoint-log-level
* PINPOINT_GO_LOG_LEVEL
* WithLogLevel()
* type: string
* default: "info"
* case-insensitive
* dynamic

The deprecated `LogLevel` key (flag `--pinpoint-loglevel`, env
`PINPOINT_GO_LOGLEVEL`) is an alias for `Log.Level` and is also dynamic; it is
ignored once `Log.Level` has been set from any source.

### Log.Output
Log.Output option sets the output file of log generated by the agent.
You can set stderr, stdout or file path. Lines are colored only when the
output is a terminal; a file never receives ANSI escapes. Log lines carry
`module` and `src` fields but no file/line caller information.

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
* max: 1024
* dynamic

### Error.NewThroughput
Error.NewThroughput option sets the max number of new exception chains recorded per second,
so that a burst of errors cannot crowd the exception metadata out of the agent's metadata queue
([Collector.Grpc.SenderQueueSize](#collectorgrpcsenderqueuesize)).
An error whose chain is already recorded is a continuation and is never limited.
0 means unlimited, and a negative value means the same, warned and published as 0.

When the limit is hit, the error loses its call stack and its `EXCEPTION_CHAIN_ID` annotation.
Everything else is unaffected: the span is still marked failed, and the error function id and
message are still recorded.

This corresponds to the Java agent's `profiler.exceptiontrace.new.throughput`.

* --pinpoint-error-newthroughput
* PINPOINT_GO_ERROR_NEWTHROUGHPUT
* WithErrorNewThroughput()
* type: int
* default: 1000
* dynamic

### Error.MaxChainDepth
Error.MaxChainDepth option sets how many links of an error's cause chain are recorded,
the error itself included. `Cause()` and `Unwrap()` are followed link by link until the
limit is reached; the links beyond it are not sent as exception metadata.

The chain comes from an arbitrary user error implementation, so the walk is bounded at 64
links whatever this option asks for: 0 or less, and anything above 64, mean that ceiling.
Java's `profiler.exceptiontrace.max.depth` corresponds, but defaults to 5.

The same value also bounds the exception entries recorded on one span across all of its
error chains (at least 10), so one chain is always recorded in full; entries beyond the
bound are dropped with a debug log, once per span.

* --pinpoint-error-maxchaindepth
* PINPOINT_GO_ERROR_MAXCHAINDEPTH
* WithErrorMaxChainDepth()
* type: int
* default: 64
* max: 64
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

### Http.Server.ProxyUserHeaderNames
Http.Server.ProxyUserHeaderNames lists the request headers a user-defined proxy writes its receive time into,
in the form `t=<epoch milliseconds>`. Each configured header present on a request is recorded as a proxy
annotation of type USER (code 4) with the header name as its app, the same as the Java agent's
`profiler.proxy.user.header.names`. A header whose `t=` is missing or not positive is not recorded.
The standard `Pinpoint-ProxyApache`, `Pinpoint-ProxyNginx` and `Pinpoint-ProxyApp` headers are always recorded
and need no configuration.

* --pinpoint-http-server-proxyuserheadernames
* PINPOINT_GO_HTTP_SERVER_PROXYUSERHEADERNAMES
* WithHttpServerProxyUserHeaderNames()
* type: string slice
* default: empty
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

Statistics are aggregated into 30 second ticks, and **only a tick that is over is
sent**: a tick is closed by the first request belonging to a newer one, or - when
traffic stops and no such request arrives - by its own 30 second window elapsing.
The send timer then carries whatever is over at that point. The send timer and the
tick boundary are not aligned, so sending a tick still inside its window would split
it across two consecutive messages and report a per-message max and average instead
of a per-tick one. Java's `UriStatCollectingJob` drains a queue only completed data
enters, for the same reason. **When nothing has been collected, no message is sent** -
an idle agent produces no URL statistics traffic at all. The tick still open when the
agent shuts down is flushed on the way out, so a clean stop does not lose it.

A completed tick is sent as soon as it is closed, not on the next timer expiry.
The send timer has no option of its own: it follows `Stat.CollectInterval`
(default 5 seconds), the way Java's `UriStatCollectingJob` runs on the agent stat
scheduler, so under traffic a tick leaves within milliseconds of its boundary
and, once traffic stops, the last tick is closed and sent within one
`Stat.CollectInterval`.

At most 4 completed ticks (two minutes) are retained while the stat stream is down.
Beyond that the oldest tick is dropped and the agent logs a rate-limited warning.

* --pinpoint-http-urlstat-enable
* PINPOINT_GO_HTTP_URLSTAT_ENABLE
* WithHttpUrlStatEnable()
* type: bool
* default: false
* dynamic

### Http.UrlStat.LimitSize
Http.UrlStat.LimitSize option sets the limit size of the URLs to be collected.
It caps the number of distinct URLs kept in one tick. Once the limit is reached,
URLs already in the tick keep being aggregated but every further new URL is dropped,
and the agent logs a rate-limited warning carrying the number of warnings it suppressed.

* --pinpoint-http-urlstat-limitsize
* PINPOINT_GO_HTTP_URLSTAT_LIMITSIZE
* WithHttpUrlStatLimitSize()
* type: int
* default: 1024
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)
* dynamic

### Http.UrlStat.QueueSize
Http.UrlStat.QueueSize option sets the size of the agent's URL statistics queue.
This queue buffers the per-request URL records waiting to be aggregated into a snapshot,
unlike Http.UrlStat.LimitSize which caps the number of distinct URLs kept in one tick.
When the queue is full the records are dropped, and the agent logs a rate-limited warning
carrying the cumulative number of dropped records.

* --pinpoint-http-urlstat-queuesize
* PINPOINT_GO_HTTP_URLSTAT_QUEUESIZE
* WithHttpUrlStatQueueSize()
* type: int
* default: 1024
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)

### Http.UrlStat.WithMethod
Http.UrlStat.WithMethod option adds http method as prefix to url string key.

* --pinpoint-http-urlstat-withmethod
* PINPOINT_GO_HTTP_URLSTAT_WITHMETHOD
* WithHttpUrlStatWithMethod()
* type: bool
* default: false
* dynamic

---

## Dynamic Configuration Reference

Options marked **dynamic** above are re-read when the config file changes,
with no application restart. The rest are read once at agent startup.

Two things make a reload not happen, and both are easy to miss:

* Reloading is driven by a **file watcher**, so it only works if the process
  was given a config file (`ConfigFile`). Command flags and environment
  variables are read once at startup and never re-read.
* Precedence still applies. An option also set by a command flag or an
  environment variable keeps that value; editing the file will not change it.

### Reloadable options

| Group | Options |
|---|---|
| Sampling | `Sampling.Type`, `Sampling.CounterRate`, `Sampling.PercentRate`, `Sampling.NewThroughput`, `Sampling.ContinueThroughput` |
| Span limits and error marking | `Span.MaxCallStackDepth`, `Span.MaxCallStackSequence`, `Span.EventChunkSize`, `Span.IgnoreErrors`, `Span.ErrorMark`, `Span.ErrorMarkExclude` |
| SQL | `SQL.TraceBindValue`, `SQL.MaxBindValueSize`, `SQL.TraceCommit`, `SQL.TraceRollback`, `SQL.TraceQueryStat`, `SQL.EnableRawSqlCache`, `SQL.CacheLengthLimit`, `SQL.ErrorCount` |
| Logging | `Log.Level` (and its deprecated alias `LogLevel`), `Log.Output`, `Log.MaxSize` |
| Errors | `Error.TraceCallStack`, `Error.CallStackDepth`, `Error.NewThroughput`, `Error.MaxChainDepth` |
| HTTP server | `Http.Server.StatusCodeErrors`, `Http.Server.ExcludeUrl`, `Http.Server.ExcludeMethod`, `Http.Server.RecordRequestHeader`, `Http.Server.RecordResponseHeader`, `Http.Server.RecordRequestCookie`, `Http.Server.RecordHandlerError`, `Http.Server.ProxyUserHeaderNames` |
| HTTP client | `Http.Client.RecordRequestHeader`, `Http.Client.RecordResponseHeader`, `Http.Client.RecordRequestCookie` |
| URL statistics | `Http.UrlStat.Enable`, `Http.UrlStat.LimitSize`, `Http.UrlStat.WithMethod` |

### Restart-only options

Identity (`ApplicationName`, `AgentId`, `AgentName`, `Uid.Version`,
`ServiceName`, `ApiKey`, `ApplicationType`), everything under `Collector.*`,
the span transport (`Span.QueueSize`, `Span.Batch.Enable`, `Span.BatchSize`,
`Span.BatchFlushInterval`, `Span.BatchCollectDeadline`,
`Span.BatchMaxConcurrentRequests`), `Stat.*`,
`Http.UrlStat.QueueSize`, `IsContainerEnv`, `ConfigFile`, `ActiveProfile`,
`SQL.RemoveComments`, `SQL.CacheSize`, `SQL.CacheExpireHours` and `Enable`.

`SQL.CacheSize` and `SQL.CacheExpireHours` are read once when the agent builds
its SQL caches (`NewAgent`); the C++ agent treats them as fixed for the same
reason.

`SQL.RemoveComments` is restart-only for a reason of its own: the normalized SQL
is the SQL id cache key and the SQL UID hash input, so a mid-process change would
report one statement under two ids.

`Enable` deserves a note: it cannot be reloaded, but `Agent.Shutdown()` stops a
running agent and `NewAgent()` starts a new one, both without a restart. See
[Troubleshooting](troubleshooting.md#stopping-and-resuming-the-agent).

A reload rebuilds the components derived from the changed options — the
sampler, the logger, the HTTP filters — behind an immutable snapshot, so an
in-flight request never sees a half-applied change. Watch for `src=config`
lines on save; a parse error leaves the previous values in place.

---

## Configuration Examples

### Development

Trace everything, log verbosely, and record the detail you want while writing
instrumentation.

```yaml
applicationName: "MyApp-Dev"
agentName: "dev-agent-1"

collector:
  host: "localhost"

sampling:
  type: "COUNTER"
  counterRate: 1          # 100%

log:
  level: "debug"          # also enables the shared-tracer check
  output: "stderr"

sql:
  traceBindValue: true
  traceCommit: true
  traceRollback: true

error:
  traceCallStack: true
  callStackDepth: 32

http:
  urlStat:
    enable: true
    withMethod: true
  server:
    recordRequestHeader: ["HEADERS-ALL"]
    recordResponseHeader: ["HEADERS-ALL"]
```

Keep `log.level: debug` for at least one run of any new instrumentation: the
API-contract checks that catch a tracer shared across goroutines only run at
`debug` and `trace`.

### Production

Sample a fraction, cap the peak, and log only what you would act on.

```yaml
applicationName: "MyApp"
# The agent id is generated per process, which is what you want when
# instances are ephemeral. Set agentName for a stable label instead.
agentName: "myapp-prod"

collector:
  host: "pinpoint-collector.internal"
  grpc:
    sslEnable: true
    trustCertFilePath: "/etc/ssl/certs/pinpoint-ca.pem"

sampling:
  type: "COUNTER"
  counterRate: 10         # 10%
  newThroughput: 100      # cap new transactions at 100/s
  continueThroughput: 200

log:
  level: "info"
  output: "/var/log/myapp/pinpoint.log"
  maxSize: 10

sql:
  traceBindValue: false   # bind values may contain personal data

error:
  traceCallStack: false   # the most expensive per-error work

http:
  urlStat:
    enable: true          # collected even for unsampled requests
    limitSize: 1024
  server:
    excludeUrl: ["/health", "/metrics", "/favicon.ico"]
    excludeMethod: ["OPTIONS"]
```

`Http.UrlStat.Enable` is the reason a 10% sampling rate is not a 10% view:
per-URL throughput and latency are aggregated for every request, sampled or
not.

### Containers

```yaml
applicationName: "MyApp"
# the agent id is generated per container
agentName: "myapp"
collector:
  host: "pinpoint-collector.monitoring.svc.cluster.local"
log:
  output: "stdout"        # let the platform collect it
sampling:
  type: "COUNTER"
  counterRate: 10
```

In a container the environment is usually the natural source, and it overrides
the config file:

```bash
PINPOINT_GO_APPLICATIONNAME=MyApp
PINPOINT_GO_AGENTNAME=myapp
PINPOINT_GO_COLLECTOR_HOST=pinpoint-collector.monitoring.svc.cluster.local
PINPOINT_GO_SAMPLING_COUNTERRATE=10
PINPOINT_GO_LOG_OUTPUT=stdout
```

The agent id cannot be pinned: the agent generates one per process, so every
replica is distinct. Use `AgentName` for the human-readable label. `IsContainerEnv` is detected
automatically; set it only if the detection is wrong.

If you want reloadable options in a container, you still need a config file:
mount one from a ConfigMap and point `ConfigFile` at it. Editing the ConfigMap
then changes the sampling rate or log level on running pods.

### Profiles

One file, several environments, selected at startup by `ActiveProfile`:

```bash
./myapp --pinpoint-configfile=pinpoint-config.yaml --pinpoint-activeprofile=real
```

See [ActiveProfile](#activeprofile) for the file layout.

---

## Best Practices

**Security**

* Never commit an `ApiKey`. Pass it through the environment; the startup config
  dump prints it as `****`.
* Turn `SQL.TraceBindValue` off wherever query parameters can carry personal
  data. It is on by default because it is the single most useful thing in a
  slow-query trace, which is exactly why it needs a deliberate decision.
* Record headers by name, not `HEADERS-ALL`, in production —
  `Authorization`, `Cookie` and `Set-Cookie` all land in the trace otherwise.
* Use TLS to the collector (`Collector.Grpc.SslEnable`). Leave
  `Collector.Grpc.TrustCertFilePath` empty only for a publicly-signed
  certificate; a private CA needs the path.

**High traffic**

* Sample rather than throttle after the fact: raise `Sampling.CounterRate` and
  set `Sampling.NewThroughput` so a spike cannot become a collector incident.
* Exclude the URLs that are noise — health checks, metrics endpoints, static
  assets — with `Http.Server.ExcludeUrl`. They are the bulk of the requests and
  none of the insight.
* Keep `Error.TraceCallStack` off; it is the costliest per-error work.
* For a very high span rate, try `Span.Batch.Enable`, which replaces the
  long-lived stream with batched unary sends.
* Leave `Log.Level` at `info` or `warn`. Debug logging adds per-event work.

**Getting it right**

* Read the startup config dump. It is the resolved configuration after all five
  sources are merged, and it settles most "why is this not taking effect"
  questions in one line.
* Prefer one config file plus environment overrides. Spreading the same option
  across flags, environment and file is how precedence surprises happen.
* Always pass the routed URL pattern, not the resolved path, so
  `Http.UrlStat.LimitSize` bounds something meaningful.

---

## Symptom → Key Index

| Symptom | Options to look at |
|---|---|
| Agent will not start | `ApplicationName`, `Uid.Version`, `Enable` |
| Nothing appears in the UI | `Collector.Host`, `Collector.AgentPort`, `Sampling.CounterRate`, `Enable` |
| Cannot connect / not registered | `Collector.Host`, the three ports, `Collector.Grpc.SslEnable`, `Collector.Grpc.TrustCertFilePath` |
| Too many traces / collector overloaded | `Sampling.CounterRate`, `Sampling.NewThroughput`, `Sampling.ContinueThroughput`, `Http.Server.ExcludeUrl` |
| Traces truncated mid-request | `Span.MaxCallStackDepth`, `Span.MaxCallStackSequence` |
| Spans dropped under load | `Span.QueueSize`, `Span.Batch.Enable`, `Span.BatchSize` |
| Agent using too much memory | `Span.QueueSize`, `Collector.Grpc.SenderQueueSize`, `Http.UrlStat.LimitSize`, `SQL.MaxBindValueSize`, `SQL.EnableRawSqlCache`, `SQL.CacheSize`, `SQL.CacheLengthLimit` |
| SQL metadata re-sent constantly / spans show unresolved SQL ids | Raise `SQL.CacheSize` above the number of distinct statements the application runs |
| Agent using too much CPU | `Sampling.CounterRate`, `Log.Level`, `Error.TraceCallStack` |
| No SQL detail in query spans | `SQL.TraceBindValue`, `SQL.TraceQueryStat`, `SQL.TraceCommit`, `SQL.TraceRollback` |
| Sensitive data visible in traces | `SQL.TraceBindValue`, `Http.Server.RecordRequestHeader`, `Http.Server.RecordRequestCookie` |
| Health checks flooding the URL list | `Http.Server.ExcludeUrl`, `Http.Server.ExcludeMethod` |
| No per-URL statistics | `Http.UrlStat.Enable`, `Http.UrlStat.LimitSize`, `Http.UrlStat.WithMethod` |
| Wrong requests marked as errors | `Http.Server.StatusCodeErrors`, `Http.Server.RecordHandlerError`, `Span.ErrorMark`, `Span.ErrorMarkExclude`, `Span.IgnoreErrors` |
| No error stack traces | `Error.TraceCallStack`, `Error.CallStackDepth` |
| Agent logs too quiet / too loud | `Log.Level`, `Log.Output`, `Log.MaxSize` |
| Config change has no effect | `ConfigFile`, `ActiveProfile`, and the [reloadable list](#reloadable-options) |

---

## Related Documentation

* [Quick Start](quick_start.md)
* [Custom Instrumentation](instrument.md)
* [Tracer, Span, and Annotation Contracts](api_contracts.md)
* [Plugin User Guide](plugin_guide.md)
* [Troubleshooting](troubleshooting.md)
