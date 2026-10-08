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
Options marked **dynamic** below are re-read when the config file changes, with no application restart.
The rest are read once at agent startup.

A reload keeps the initial precedence: an option given by command line flag or environment variable is not
overwritten by the config file, and neither is a value set through `Config.Set()`.
Only options whose current value came from the config file, a profile, a config function or the default are updated.
A dynamic option deleted from the file goes back to the value it had before the file set it: the config function's, or the default.
`Config.Set()` on a non-dynamic option stores the value and logs a warning; the agent applies it after a restart.

Two things make a reload not happen, and both are easy to miss:

* The agent **polls the config file** - its modification time and size, once a
  second - so a change takes up to that long to apply, and it only works if
  the process was given a config file (`ConfigFile`). Command flags and
  environment variables are read once at startup and never re-read.
* Precedence still applies. An option also set by a command flag or an
  environment variable keeps that value; editing the file will not change it.

A reload rebuilds the components derived from the changed options (the
sampler, the logger, the HTTP filters) behind an immutable snapshot, so an
in-flight request never sees a half-applied change. Watch for `src=config`
lines on save; a parse error leaves the previous values in place.

`Enable` cannot be reloaded, but `Agent.Shutdown()` stops a running agent and
`NewAgent()` starts a new one, both without a restart. See
[Troubleshooting](troubleshooting.md#stopping-and-resuming-the-agent).

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
Every key has the same three spellings, derived from the key:

| Source | Spelling | `Sampling.PercentRate` |
|---|---|---|
| command flag | `--pinpoint-` + key, lowercase, `.` to `-` | `--pinpoint-sampling-percentrate` |
| environment variable | `PINPOINT_GO_` + key, uppercase, `.` to `_` | `PINPOINT_GO_SAMPLING_PERCENTRATE` |
| config function | `With` + key without `.`, as `pinpoint.ConfigOption` | `WithSamplingPercentRate()` |

Five functions deviate from the rule: `WithAppName()` (ApplicationName), `WithAppType()` (ApplicationType),
`WithHttpServerStatusCodeError()`, `WithHttpServerRecordRespondHeader()` and `WithHttpClientRecordRespondHeader()`.
The `Mongo.*` functions live in the mongodriver plugin packages, not in `pinpoint`.
Each option below lists its environment variable, then its value type and additional information.

### ConfigFile
The config options below can be saved to the config file is set by ConfigFile option.
The file's extension names its format: YAML (`.yaml`, `.yml`), JSON (`.json`)
or properties (`.properties`, `.props`, `.prop`). Configuration keys used in
config files are case-insensitive. A properties file is `key=value` lines with
`#` comments: no escapes, line continuations or `${...}` references.

A file that does not exist is skipped with a log line and picked up by the watcher once it appears,
so every example can name one. A file that exists but cannot be read - a syntax error, an unsupported
extension - is logged as well and returned as `NewConfig`'s error; the returned `Config` is still complete,
on defaults and the other sources, so `NewAgent` works with it either way.

* PINPOINT_GO_CONFIGFILE
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
  percentRate: 1
log:
  level: "error"
```

* [YAML File Example](/example/pinpoint-config.yaml)
* [JSON File Example](/example/pinpoint-config.json)
* [Properties File Example](/example/pinpoint-config.prop)

### ActiveProfile
The configuration profile feature is supported.
You can set the profile in the config file and specify the profile to activate with the ActiveProfile option.

* PINPOINT_GO_ACTIVEPROFILE
* string
* case-insensitive

The example below shows that config file and profile are set by command flag.
```
--pinpoint-configfile=pinpoint-config.json --pinpoint-activeprofile=dev
```
A flag that takes a value also accepts the `--pinpoint-key value` form; a value that starts with `-` needs the `=` form.
Boolean flags take no value token (`--pinpoint-enable` or `--pinpoint-enable=false`). An unknown `--pinpoint-*` flag is ignored.
The agent reads its flags from `os.Args` without removing them, so an application that parses its own flags with the
standard `flag` package still sees them and rejects them; such an application configures the agent through environment
variables or the config file instead.
```json
{
  "applicationName": "JsonAppName",
  "log": {
    "level": "debug"
  },
  "profile": {
    "dev": {
      "collector": {
        "host": "dev.collector.host"
      },
      "sampling": {
        "type": "PERCENT",
        "percentRate": 100
      }
    },
    "real": {
      "collector": {
        "host": "real.collector.host"
      },
      "sampling": {
        "type": "percent",
        "percentRate": 1
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

* PINPOINT_GO_APPLICATIONNAME
* string
* case-sensitive

### ApplicationType
ApplicationType option sets the application type.

* PINPOINT_GO_APPLICATIONTYPE
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
The maximum length is 255 bytes for Uid.Version v1 and v3, and 254 bytes for v4,
and it must match `[a-zA-Z0-9\._\-]+`.
A value that is too long or has invalid characters does not stop the agent:
it is logged at warn and the generated AgentId is used instead.
Check the agent log for that warning if the agent shows up under an AgentId you did not expect.
See [Identity Versions](#identity-versions).

* PINPOINT_GO_AGENTNAME
* string
* case-sensitive

### Uid.Version
Uid.Version option selects the agent identity format used to identify the agent to Pinpoint collector.
Supported values are v1, v3 and v4.
The default is v3, and unknown or empty values fall back to v3.

**v4 is not usable at this time.**
The v4 identity protocol is implemented in the agent, but it has not been released on the Pinpoint server side yet,
so no collector accepts it.
Use v1 or v3; the v4 details below are documented for when server-side support ships.

* PINPOINT_GO_UID_VERSION
* string
* default: "v3"
* case-insensitive

#### Identity Versions

| | v1 | v3 (default) | v4 |
|---|---|---|---|
| ApplicationName | **required**, max 24 bytes | **required**, max 254 bytes | **required**, max 254 bytes |
| AgentId | not configurable, always auto-generated | same as v1 | same as v1 |
| AgentName | optional, max 255 bytes; falls back to AgentId when unset or invalid | same as v1 | optional, max 254 bytes; falls back to AgentId when unset or invalid |
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
AgentName is not required, so an invalid one warns and falls back to AgentId instead of aborting.

The `socketid` header is not listed above because it is not part of the identity headers;
it is added by the ping stream for every version.

### ServiceName
ServiceName option sets the service name reported to Pinpoint collector.
It is used only when Uid.Version is v4, where it is required and its maximum length is 254 bytes.
It is ignored for v1 and v3.
If it is not set, has invalid characters, or the maximum length is exceeded, agent startup fails.
Note that v4 is not usable at this time, so this option currently has no effect. See [Uid.Version](#uidversion).

* PINPOINT_GO_SERVICENAME
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

* PINPOINT_GO_APIKEY
* string
* default: ""
* case-sensitive

### Collector.Host
Collector.Host option sets the host address of Pinpoint collector.

* PINPOINT_GO_COLLECTOR_HOST
* string
* default: "localhost"
* case-sensitive

### Collector.AgentPort
Collector.AgentPort option sets the agent port of Pinpoint collector.

* PINPOINT_GO_COLLECTOR_AGENTPORT
* int
* default: 9991

### Collector.SpanPort
Collector.SpanPort option sets the span port of Pinpoint collector.

* PINPOINT_GO_COLLECTOR_SPANPORT
* int
* default: 9993

### Collector.StatPort
Collector.StatPort option sets the stat port of Pinpoint collector.

* PINPOINT_GO_COLLECTOR_STATPORT
* int
* default: 9992

### Collector.AgentInfo.RefreshInterval
Collector.AgentInfo.RefreshInterval option sets the cycle for re-sending the agent information to the collector.
If it is 0 or less, the agent information is sent only once at agent startup.

* PINPOINT_GO_COLLECTOR_AGENTINFO_REFRESHINTERVAL
* type: int
* default: 86400000 (24 hours)
* unit: milliseconds

### Collector.AgentInfo.SendRetryInterval
Collector.AgentInfo.SendRetryInterval option sets the wait between agent information send retries.
It paces two loops: the registration retry at agent startup, which repeats until the collector accepts the AgentInfo, and the retries within one periodic refresh cycle.
The wait is randomized by +/-30% so agents restarted together do not retry in lockstep, and it does not escalate - a collector that keeps rejecting the registration is polled at this interval for as long as the process runs.
Only the refresh use has no effect if Collector.AgentInfo.RefreshInterval is 0.

* PINPOINT_GO_COLLECTOR_AGENTINFO_SENDRETRYINTERVAL
* type: int
* default: 3000
* unit: milliseconds

### Collector.AgentInfo.MaxTryPerAttempt
Collector.AgentInfo.MaxTryPerAttempt option sets the max number of agent information sends per refresh cycle.
It has no effect if Collector.AgentInfo.RefreshInterval is 0.

* PINPOINT_GO_COLLECTOR_AGENTINFO_MAXTRYPERATTEMPT
* type: int
* default: 3

### Collector.AgentInfo.SendArgs
Collector.AgentInfo.SendArgs option sets whether the process's command line arguments are sent to the
collector with the agent information, where the Pinpoint web shows them on the server map. Turn it off
when the command line carries a password or a token: the arguments are sent as they are, on every
registration and refresh.

* PINPOINT_GO_COLLECTOR_AGENTINFO_SENDARGS
* type: bool
* default: true

### Collector.Grpc.KeepAliveTime
Collector.Grpc.KeepAliveTime option sets the interval in milliseconds after which the agent sends an HTTP/2
keepalive ping on an idle collector connection.
The options below apply equally to every collector connection (agent, metadata, span, stat and command channels).

* PINPOINT_GO_COLLECTOR_GRPC_KEEPALIVETIME
* int
* default: 30000

### Collector.Grpc.KeepAliveTimeout
Collector.Grpc.KeepAliveTimeout option sets the time in milliseconds the agent waits for a keepalive ping ack
before closing the connection.

* PINPOINT_GO_COLLECTOR_GRPC_KEEPALIVETIMEOUT
* int
* default: 60000

### Collector.Grpc.KeepAlivePermitWithoutCalls
Collector.Grpc.KeepAlivePermitWithoutCalls option sets whether keepalive pings are sent even when there is no
active stream.
The default is false. Agents older than this release always behaved as if it were true.

* PINPOINT_GO_COLLECTOR_GRPC_KEEPALIVEPERMITWITHOUTCALLS
* type: bool
* default: false

### Collector.Grpc.MaxSendMessageSize
Collector.Grpc.MaxSendMessageSize option sets the max size in bytes of a gRPC message the agent can send.

* PINPOINT_GO_COLLECTOR_GRPC_MAXSENDMESSAGESIZE
* int
* default: 4194304

### Collector.Grpc.MaxReceiveMessageSize
Collector.Grpc.MaxReceiveMessageSize option sets the max size in bytes of a gRPC message the agent can receive.

* PINPOINT_GO_COLLECTOR_GRPC_MAXRECEIVEMESSAGESIZE
* int
* default: 4194304

### Collector.Grpc.FlowControlWindow
Collector.Grpc.FlowControlWindow option sets the initial HTTP/2 flow-control window size in bytes.

* PINPOINT_GO_COLLECTOR_GRPC_FLOWCONTROLWINDOW
* int
* default: 1048576

### Collector.Grpc.WriteBufferSize
Collector.Grpc.WriteBufferSize option sets the gRPC transport write buffer size in bytes.

* PINPOINT_GO_COLLECTOR_GRPC_WRITEBUFFERSIZE
* int
* default: 65536

### Collector.Grpc.MaxHeaderListSize
Collector.Grpc.MaxHeaderListSize option sets the max size in bytes of gRPC response headers the agent accepts.

* PINPOINT_GO_COLLECTOR_GRPC_MAXHEADERLISTSIZE
* int
* default: 8192

### Collector.Grpc.SslEnable
Collector.Grpc.SslEnable option enables TLS on all gRPC channels
(agent, metadata, span, stat) to Pinpoint collector.
When disabled (default), the agent connects in plaintext as before.

* PINPOINT_GO_COLLECTOR_GRPC_SSLENABLE
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

* PINPOINT_GO_COLLECTOR_GRPC_TRUSTCERTFILEPATH
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
The default 0 keeps a working connection for as long as the agent runs.

Spreading across instances needs the `dns` resolver
([Collector.Grpc.DnsResolverEnable](#collectorgrpcdnsresolverenable), the default), which supplies the
addresses a replacement connection can pick from. That resolver re-resolves at most once every 30
seconds, so a ConnectionMaxAge shorter than that rotates connections faster than the address list
refreshes: the rotations are still make-before-break and still spread over the addresses already
resolved, they just cannot see a record change sooner than the resolver does. Renewal periods are
minutes in practice, where this does not arise.

* PINPOINT_GO_COLLECTOR_GRPC_CONNECTIONMAXAGE
* int
* default: 0
* unit: milliseconds

### Collector.Grpc.DnsResolverEnable
Collector.Grpc.DnsResolverEnable option selects the gRPC name resolver used for the collector target.
The default true uses the `dns` resolver: the collector host is resolved into the channel's address list,
so a host with several A records gives the agent every collector instance to pick from and to fail over to,
and the list is re-resolved as the records change. An IP literal, or a name in `/etc/hosts` such as the
default `localhost`, works unchanged.
false falls back to the legacy `passthrough` scheme, which hands the address to the dialer untouched:
the host is resolved once per new connection, so the channel only ever holds a single address and
[Collector.Grpc.ConnectionMaxAge](#collectorgrpcconnectionmaxage) cannot spread connections across
instances. Set it to false only to roll the `dns` resolver back without a redeploy.

* PINPOINT_GO_COLLECTOR_GRPC_DNSRESOLVERENABLE
* bool
* default: true

### Collector.Grpc.StreamMaxAge
Collector.Grpc.StreamMaxAge option sets the max age in milliseconds of the long-lived ping, stat and command
streams.
A stream older than this is closed normally and reopened before the next send, so no stat is dropped;
the command stream, which waits on the collector, is reopened when its age runs out.
The age is randomized by +/-10%.
The default 0 keeps a stream open until it fails.

* PINPOINT_GO_COLLECTOR_GRPC_STREAMMAXAGE
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
traffic reaches on the span channel, which only carries unary SendSpanBatch requests.
Note that with [Collector.Grpc.KeepAlivePermitWithoutCalls](#collectorgrpckeepalivepermitwithoutcalls) at its
default false, a connection with no open stream sends no keepalive pings even when idling is disabled.
A negative value is treated as 0.

* PINPOINT_GO_COLLECTOR_GRPC_IDLETIMEOUT
* int
* default: 0
* unit: milliseconds

### Collector.Grpc.SenderQueueSize
Collector.Grpc.SenderQueueSize option sets the size of the agent's metadata queue: the API, string, SQL and
exception metadata registered by spans and waiting to be sent to the collector.
It used to share [Span.QueueSize](#spanqueuesize). While the queue is full a span that would register new
metadata records none for that one use - no id is minted and nothing is cached, so the next use registers
it once there is room - and the agent logs a rate-limited warning carrying the cumulative number of refused
items. Items already queued keep their ids.
The retry schedule for failed metadata sends has its own fixed bound of 1000 and is not affected.

* PINPOINT_GO_COLLECTOR_GRPC_SENDERQUEUESIZE
* type: int
* default: 1000
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)

### Collector.Grpc.SpanBatchSize
Collector.Grpc.SpanBatchSize option sets the max number of spans per SendSpanBatch request.
Spans are always sent in unary SendSpanBatch requests, which a collector implements from Pinpoint 3.1.0. Against
an older one every batch fails and its spans are dropped, with `SendSpanBatch failed - N spans dropped` in the
agent log.

* PINPOINT_GO_COLLECTOR_GRPC_SPANBATCHSIZE
* type: int
* default: 50
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)

### Collector.Grpc.SpanBatchFlushInterval
Collector.Grpc.SpanBatchFlushInterval option sets how long span batch sender waits for an available request permit.

* PINPOINT_GO_COLLECTOR_GRPC_SPANBATCHFLUSHINTERVAL
* type: int
* default: 1000
* unit: milliseconds

### Collector.Grpc.SpanBatchCollectDeadline
Collector.Grpc.SpanBatchCollectDeadline option sets how long span batch sender collects additional spans after the first span arrives.

* PINPOINT_GO_COLLECTOR_GRPC_SPANBATCHCOLLECTDEADLINE
* type: int
* default: 500
* unit: milliseconds

### Collector.Grpc.SpanBatchMaxConcurrentRequests
Collector.Grpc.SpanBatchMaxConcurrentRequests option sets the max number of concurrent SendSpanBatch requests.
The same number bounds the metadata sends (API, string, SQL and exception
metadata) the agent has in flight at once. Exception metadata is one request
per failed span when `Error.TraceCallStack` is on, so raise this together with
`Error.NewThroughput` when the metadata queue overflows.

* PINPOINT_GO_COLLECTOR_GRPC_SPANBATCHMAXCONCURRENTREQUESTS
* type: int
* default: 10

### Sampling.Type
Sampling.Type option sets the type of agent sampler.
Either "COUNTER" or "PERCENT" must be specified. "COUNTING" is accepted as an
alias of "COUNTER".

An unrecognized type falls back to "COUNTER" and keeps
[Sampling.CounterRate](#samplingcounterrate) as configured, so a typo does not
turn sampling off; the warning it logs names the type and the rate that were
applied.

* PINPOINT_GO_SAMPLING_TYPE
* string
* default: "COUNTER"
* case-insensitive
* dynamic

### Sampling.CounterRate
Sampling.CounterRate option sets the counter sampling rate.
Sample 1/rate. In other words, if the rate is 1, then it will be 100% and if it is 100, it will be 1% sampling.

* PINPOINT_GO_SAMPLING_COUNTERRATE
* int
* default: 1
* valid range: 0 (never sample) or a positive N (one transaction in N)
* dynamic

### Sampling.PercentRate
Sampling.PercentRate option sets the sampling rate for a 'percent sampler'.
The rate is truncated to hundredths of a percent, and a truncated rate of 0
samples no new transaction at all - so `0`, a negative rate, and any positive
rate below `0.01` (e.g. `0.005`) all turn percent sampling off; a rate below
`0.01` also logs a warning, because a positive rate that samples nothing is
more often a typo than an intent. A rate of `100` or above always samples; a
rate above `100` is clamped to it with a warning, because a rate over the
documented maximum reads as a misuse of the option rather than an intent.

* PINPOINT_GO_SAMPLING_PERCENTRATE
* float
* default: 100
* valid range: 0 ~ 100 (0 = no sampling)
* dynamic

### Sampling.NewThroughput
Sampling.NewThroughput option sets the new TPS for a 'throughput sampler'.

* PINPOINT_GO_SAMPLING_NEWTHROUGHPUT
* type: int
* default: 0
* dynamic

### Sampling.ContinueThroughput
Sampling.ContinueThroughput option sets the cont TPS for a 'throughput sampler'.

* PINPOINT_GO_SAMPLING_CONTINUETHROUGHPUT
* type: int
* default: 0
* dynamic

### Span.QueueSize
Span.QueueSize option sets the size of agent's span queue for gRPC.
It sizes the span queue only; the metadata queue is sized by
[Collector.Grpc.SenderQueueSize](#collectorgrpcsenderqueuesize) and the stat queue by [Stat.QueueSize](#statqueuesize).

* PINPOINT_GO_SPAN_QUEUESIZE
* type: int
* default: 1024
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)

### Span.EventChunkSize
Span.EventChunkSize option sets the size of span event chunk for gRPC.

* PINPOINT_GO_SPAN_EVENTCHUNKSIZE
* type: int
* default: 20
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)
* dynamic

### Span.MaxCallStackDepth
Span.MaxCallStackDepth option sets the max callstack depth of a span, if -1 is unlimited and min is 2. A value above 2147483647 (MaxInt32) is unlimited too.
Events nested one level deeper than this value are still recorded and the next level overflows (with the default 64, up to 65 levels are recorded).

* PINPOINT_GO_SPAN_MAXCALLSTACKDEPTH
* type: int
* default: 64
* dynamic

### Span.MaxCallStackSequence
Span.MaxCallStackSequence option sets the max callstack sequence of a span, if -1 is unlimited and min is 4. A value above 2147483647 (MaxInt32) is unlimited too.

* PINPOINT_GO_SPAN_MAXCALLSTACKSEQUENCE
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

* PINPOINT_GO_SPAN_IGNOREERRORS
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
means every cause. An unrecognised name is warned about and ignored, so a typo narrows nothing.

The unknown cause (`1`) is not nameable and is always marked: it is what
`SpanRecorder.SetFailure()` records when its caller names no cause, and excluding it would
amount to "never fail a transaction".

| Cause | Recorded by |
|---|---|
| `exception` | `SpanRecorder.SetError` and `SpanEventRecorder.SetError`, wherever they are called - a plugin, a SQL driver error, a recovered panic |
| `http-status` | a response status listed in `Http.Server.StatusCodeErrors` |
| `sql` | the `SQL.ErrorCount` limit on one transaction |
| `unknown` | `SpanRecorder.SetFailure()` called with no cause |

* PINPOINT_GO_SPAN_ERRORMARK
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
removed from whatever `Span.ErrorMark` allows, so an exclusion wins over an inclusion.
Entries are spelled as in `Span.ErrorMark`, and the unknown cause cannot be excluded. `Span.ErrorMarkExclude: http-status` is how to say "a 5xx is not a
transaction failure" while keeping exceptions and the SQL count.

An excluded cause is dropped from the verdict and from nothing else: the HTTP status
annotation is still recorded, the exception info and its chain are still recorded, and the
SQL statements are still counted. What changes is `err`, the failure point in the scatter
chart and the failed histogram of the URL statistics - the three move together, so they
never disagree about the same request.

`Span.IgnoreErrors` excludes individual errors by type and message; this option excludes a
whole cause, however it was recorded.

* PINPOINT_GO_SPAN_ERRORMARKEXCLUDE
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

* PINPOINT_GO_STAT_COLLECTINTERVAL
* type: int
* default: 5000
* unit: milliseconds
* range: 1000 ~ 10000 (an out-of-range value falls back to the default with a warning log)

### Stat.BatchCount
Stat.BatchCount option sets batch delivery units for collected statistics.

* PINPOINT_GO_STAT_BATCHCOUNT
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

* PINPOINT_GO_STAT_QUEUESIZE
* type: int
* default: 1024
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)

### SQL.TraceBindValue
SQL.TraceBindValue option enables bind value tracing for SQL Driver.

* PINPOINT_GO_SQL_TRACEBINDVALUE
* type: bool 
* default: true
* dynamic

### SQL.MaxBindValueSize
SQL.MaxBindValueSize option sets the max length of traced bind value for SQL Driver.
It applies to bind values only. A truncated list ends with a
`...(number of bind values)` marker, appended past the limit. The parameters
extracted by SQL normalization are never truncated, because the server splits
them on `,` to restore the original statement. Only
the SQL text published as metadata is truncated, at 64KB, and it carries a
`...(original length)` marker.

A negative value turns bind value tracing off entirely - the size becomes 0 and
`SQL.TraceBindValue` is set to false - and logs a warning.

* PINPOINT_GO_SQL_MAXBINDVALUESIZE
* type: int
* default: 1024
* range: 0 ~ 262144 (a larger value is clamped with a warning log; the ceiling
  is a sixteenth of the 4MB gRPC message limit, so one span event's bind values
  cannot fill a whole span message)
* unit: bytes
* dynamic

### SQL.TraceCommit
SQL.TraceCommit option enables commit tracing for SQL Driver.

* PINPOINT_GO_SQL_TRACECOMMIT
* type: bool
* default: true
* dynamic

### SQL.TraceRollback
SQL.TraceRollback option enables rollback tracing for SQL Driver.

* PINPOINT_GO_SQL_TRACEROLLBACK
* type: bool
* default: true
* dynamic

### SQL.TraceQueryStat
SQL.TraceQueryStat option enables trace SQL query statistics.

* PINPOINT_GO_SQL_TRACEQUERYSTAT
* type: bool
* default: false
* dynamic

### SQL.EnableRawSqlCache
SQL.EnableRawSqlCache option enables caching of SQL normalization results keyed by the raw SQL text,
so repeatedly executed statements skip re-parsing.
Consider disabling it if your application inlines literal values into every query
instead of using bind variables, since such queries never repeat and every call
pays a small cache-miss overhead.

* PINPOINT_GO_SQL_ENABLERAWSQLCACHE
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
[SQL.CacheLengthLimit](#sqlcachelengthlimit) for the raw SQL cache, and 16-byte
hashes per entry for the id and UID caches. The valid range is 1 to
65536; a value outside it is logged and the default is used.

The option applies to the SQL caches only. The API and error caches keep their
fixed 1024 entries. The option is read once at agent startup: resizing the
caches while spans are in flight would orphan the ids those spans already
carry.

* PINPOINT_GO_SQL_CACHESIZE
* type: int
* default: 1024

### SQL.CacheLengthLimit
SQL.CacheLengthLimit option sets the max length of a raw SQL statement whose
normalization is memoized by the raw SQL cache
([SQL.EnableRawSqlCache](#sqlenablerawsqlcache)). A statement at or above this
length is normalized again on every execution, so a few huge generated statements
cannot hold the cache - which keeps the text itself as key and value - for the
life of the process. A limit of 0 memoizes nothing. A limit of exactly -1
memoizes every statement regardless of length; any other negative value is treated
as a typo and recovers the default, with a warning.

The limit does **not** apply to the SQL-ID and SQL-UID metadata caches. Both key
on a 128-bit hash of the normalized statement, so an entry costs the same
whatever the statement's length, and bypassing them would re-send the metadata
on every execution of a long statement - for the id cache under a fresh id each
time, so the same query would appear in the UI as a separate entry per execution.

* PINPOINT_GO_SQL_CACHELENGTHLIMIT
* type: int
* default: 2048
* unit: bytes

### SQL.CacheExpireHours
SQL.CacheExpireHours option sets how long a SQL UID stays in the SQL-UID cache
before the statement is registered with the collector again. The collector keeps
SQL UID metadata for a limited time (180 days by default), so in a process that
runs longer than that a cached UID could outlive its row and the web UI would
show an empty SQL for it until the agent restarted. Zero never expires an entry;
a negative value is a typo rather than a request for that, so it recovers the
default with a warning, as do values above 876000 (100 years). The SQL-ID, API
and error caches have no expiry.

* PINPOINT_GO_SQL_CACHEEXPIREHOURS
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
The count lives on the trace root, so queries spread over async spans add up.
The failure is recorded under the `sql` cause, so `Span.ErrorMarkExclude: sql` keeps
the counting without the verdict - see
[Span.ErrorMarkExclude](#spanerrormarkexclude).

* PINPOINT_GO_SQL_ERRORCOUNT
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

Turning it off makes the agent keep comments as it did before this option
existed.

The option is **startup-only**. The normalized text is both the SQL id cache key
and the input to the SQL UID hash, so flipping it against a populated cache
would report one statement under two different ids.

Comments are part of the statement's identity when they carry an Oracle hint or
an ORM's trace tag, and removing them merges statements that differ only in that
tag.

* PINPOINT_GO_SQL_REMOVECOMMENTS
* type: bool
* default: true


### Log.Level
Log.Level option sets the level of log generated by the agent. 
Either trace, debug, info, warn, or error must be set. Any other value -
including `fatal` and `panic`, which the underlying logrus library would
accept and which would silence warn and error - is rejected with an error log,
and the level in effect is kept: the default at startup, the previous level on
a reload.

* PINPOINT_GO_LOG_LEVEL
* type: string
* default: "info"
* case-insensitive
* dynamic

### Log.Output
Log.Output option sets the output file of log generated by the agent.
You can set stderr, stdout or file path. Lines are colored only when the
output is a terminal; a file never receives ANSI escapes. Log lines carry
`module` and `src` fields but no file/line caller information.

* PINPOINT_GO_LOG_OUTPUT
* type: string
* default: "stdout" (it used to be "stderr")
* case-insensitive
* dynamic

### Log.MaxSize
Log.MaxSize option sets the max size of log file. The unit of value is MB.
When the file reaches this size it is rotated; `Log.MaxBackups` says how many
rotated files are kept, so the agent log takes at most
`Log.MaxSize x (Log.MaxBackups + 1)` MB of disk (20 MB with the defaults).
A value below 1 recovers the default.

* PINPOINT_GO_LOG_MAXSIZE
* type: int
* default: 10
* dynamic

### Log.MaxBackups
Log.MaxBackups option sets the number of rotated log files kept beside the
current one. Rotated files are kept until this many exist, whatever their age,
and are not compressed; neither is configurable.

A value below 1, including 0, is out of range and recovers the default with a
warning, because 0 would otherwise mean "keep every backup" and fill the disk
rather than "keep none". Rotation with no history is `Log.MaxSize` alone.

* PINPOINT_GO_LOG_MAXBACKUPS
* type: int
* default: 1
* dynamic

### Error.TraceCallStack
Error.TraceCallStack option enables trace callstack dump when a error occurs.

* PINPOINT_GO_ERROR_TRACECALLSTACK
* type: bool
* default: false
* dynamic

### Error.CallStackDepth
Error.CallStackDepth option sets the max depth of callstack to be dumped.

* PINPOINT_GO_ERROR_CALLSTACKDEPTH
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

* PINPOINT_GO_ERROR_NEWTHROUGHPUT
* type: int
* default: 1000
* dynamic

### Error.MaxChainDepth
Error.MaxChainDepth option sets how many links of an error's cause chain are recorded,
the error itself included. `Cause()` and `Unwrap()` are followed link by link until the
limit is reached; the links beyond it are not sent as exception metadata.

The chain comes from an arbitrary user error implementation, so the walk is bounded at 64
links whatever this option asks for: 0 or less, and anything above 64, mean that ceiling.

The same value also bounds the exception entries recorded on one span across all of its
error chains (at least 10), so one chain is always recorded in full; entries beyond the
bound are dropped. The first drop is warned about once per span, and the span logs how
many entries it dropped in total when it ends.

* PINPOINT_GO_ERROR_MAXCHAINDEPTH
* type: int
* default: 64
* max: 64
* dynamic

### ServerInfo
ServerInfo option sets the server description sent in the agent information
(`PServerMetaData.serverInfo`) and shown on the server information view of the
Pinpoint UI. If it is not set, "Go Application" is sent.

The value is read at startup and sent with every agent information send, so a
change to it reaches the collector with the next `Collector.AgentInfo.RefreshInterval`
cycle at the earliest, not on the change itself.

The other server metadata, the service information list, is a list of named
lists and has no config file spelling. It is set with `WithServiceInfo()`; see
[API Contracts](api_contracts.md#13-server-metadata).

* PINPOINT_GO_SERVERINFO
* type: string
* default: "" (sends "Go Application")

### IsContainerEnv
IsContainerEnv option sets whether the application is running in a container environment or not.
If this is not set, the agent automatically checks it.

* PINPOINT_GO_ISCONTAINERENV
* type: bool
* default: false

### Enable
Enable option enables the agent is operational state.
If this is set as false, the agent doesn't start working.

* PINPOINT_GO_ENABLE
* type: bool
* default: true

### Http.Server.StatusCodeErrors
Http.Server.StatusCodeErrors option sets HTTP status code with request failure.
Refer https://pinpoint-apm.gitbook.io/pinpoint/documents/http-status-code-failure.

* PINPOINT_GO_HTTP_SERVER_STATUSCODEERRORS
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

* PINPOINT_GO_HTTP_SERVER_EXCLUDEURL
* type: string slice
* case-sensitive
* dynamic

### Http.Server.ExcludeMethod
Http.Server.ExcludeMethod option sets HTTP Request methods to exclude from tracking.

* PINPOINT_GO_HTTP_SERVER_EXCLUDEMETHOD
* type: string slice
* case-insensitive
* dynamic

### Http.Server.RecordRequestHeader
Http.Server.RecordRequestHeader option sets HTTP request headers to be logged on the server side.
If sets to "HEADERS-ALL", it records all request headers.

* PINPOINT_GO_HTTP_SERVER_RECORDREQUESTHEADER
* type: string slice
* case-insensitive
* dynamic

### Http.Server.RecordResponseHeader
Http.Server.RecordResponseHeader option sets HTTP response headers to be logged on the server side.
If sets to "HEADERS-ALL", it records all response headers.

* PINPOINT_GO_HTTP_SERVER_RECORDRESPONSEHEADER
* type: string slice
* case-insensitive
* dynamic

### Http.Server.RecordRequestCookie
Http.Server.RecordRequestCookie option sets HTTP request cookies to be logged on the server side.
If sets to "HEADERS-ALL", it records all request cookies.

* PINPOINT_GO_HTTP_SERVER_RECORDREQUESTCOOKIE
* type: string slice
* case-insensitive
* dynamic

### Http.Server.RecordHandlerError
Http.Server.RecordHandlerError sets whether to record the error returned by http handler.

* PINPOINT_GO_HTTP_SERVER_RECORDHANDLERERROR
* type: bool
* default: true
* dynamic

### Http.Server.ProxyHeaderEnable
Http.Server.ProxyHeaderEnable turns the recording of proxy request headers on or off: `Pinpoint-ProxyApache`,
`Pinpoint-ProxyNginx`, `Pinpoint-ProxyApp` and the headers named by `Http.Server.ProxyUserHeaderNames`.

* PINPOINT_GO_HTTP_SERVER_PROXYHEADERENABLE
* type: bool
* default: true
* dynamic

### Http.Server.ProxyUserHeaderNames
Http.Server.ProxyUserHeaderNames lists the request headers a user-defined proxy writes its receive time into.
Each configured header present on a request is recorded as a proxy annotation of type USER (code 4) with the
header name as its app. The header may have been written by any of the three proxies, so the format of `t=`
and `D=` is inferred from the value: an apache microsecond epoch, an nginx `sec.mmm` or an application's
millisecond epoch. A header whose `t=` is missing or not positive is not recorded.
The standard `Pinpoint-ProxyApache`, `Pinpoint-ProxyNginx` and `Pinpoint-ProxyApp` headers are always recorded
and need no configuration.

* PINPOINT_GO_HTTP_SERVER_PROXYUSERHEADERNAMES
* type: string slice
* default: empty
* dynamic

### Http.Server.RealIpHeader
Http.Server.RealIpHeader lists, in order, the request headers the client address (`RemoteAddr`) is taken from.
The first header present whose value yields an address wins. A header named `Forwarded` (RFC 7239) is parsed
for its `for=` token, with a trailing `:port` removed; every other header contributes its first comma-separated
hop. When no header yields an address the socket address is recorded, port stripped.

**The default trusts `X-Forwarded-For` then `X-Real-Ip`**, so existing deployments record the same address
as before. Set an empty list to trust none, or list your edge's header first (`CF-Connecting-IP`,
`True-Client-IP`, `Forwarded`).

* PINPOINT_GO_HTTP_SERVER_REALIPHEADER
* type: string slice
* default: X-Forwarded-For, X-Real-Ip
* dynamic

``` yaml
Http:
  Server:
    RealIpHeader: [CF-Connecting-IP, X-Forwarded-For]
```

### Http.Server.RealIpEmptyValue
Http.Server.RealIpEmptyValue is the header value that counts as absent when resolving the client address.
A candidate equal to it (case-insensitive, typically `unknown`) is skipped and the next header is tried.

* PINPOINT_GO_HTTP_SERVER_REALIPEMPTYVALUE
* type: string
* default: empty
* dynamic

### Http.Server.RecordRequestParam
Http.Server.RecordRequestParam turns the recording of the request query string on or off. When on, the query
string of a sampled request is recorded as annotation 41 (`HTTP.PARAM`) as `k=v&k=v`, percent-decoded,
each key and value cut to 64 characters and the whole string to 512, with `...` marking every cut.
**It defaults to off** because query strings routinely carry tokens, session ids and user ids.

* PINPOINT_GO_HTTP_SERVER_RECORDREQUESTPARAM
* type: bool
* default: false
* dynamic

``` yaml
Http:
  Server:
    RecordRequestParam: true
```

### Http.Client.RecordRequestHeader
Http.Client.RecordRequestHeader option sets HTTP request headers to be logged on the client side.
If sets to "HEADERS-ALL", it records all request headers.

* PINPOINT_GO_HTTP_CLIENT_RECORDREQUESTHEADER
* type: string slice
* case-insensitive
* dynamic

### Http.Client.RecordResponseHeader
Http.Client.RecordResponseHeader option sets HTTP response headers to be logged on the client side.
If sets to "HEADERS-ALL", it records all response headers.

* PINPOINT_GO_HTTP_CLIENT_RECORDRESPONSEHEADER
* type: string slice
* case-insensitive
* dynamic

### Http.Client.RecordRequestCookie
Http.Client.RecordRequestCookie option sets HTTP request cookies to be logged on the client side.
If sets to "HEADERS-ALL", it records all request cookies.

* PINPOINT_GO_HTTP_CLIENT_RECORDREQUESTCOOKIE
* type: string slice
* case-insensitive
* dynamic

### Http.Client.RecordUrlQuery
Http.Client.RecordUrlQuery sets whether the client URL annotation (`HTTP.URL`, `GET http://host/path`) keeps
its query string. By default the URL is recorded up to the `?`; the fragment, endpoint and destination are
unaffected. **It defaults to off** because query strings routinely carry tokens, session ids and user ids.

* PINPOINT_GO_HTTP_CLIENT_RECORDURLQUERY
* type: bool
* default: false
* dynamic

``` yaml
Http:
  Client:
    RecordUrlQuery: true
```

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
of a per-tick one. **When nothing has been collected, no message is sent** -
an idle agent produces no URL statistics traffic at all. The tick still open when the
agent shuts down is flushed on the way out, so a clean stop does not lose it.

A completed tick is sent as soon as it is closed, not on the next timer expiry.
The send timer has no option of its own: it follows `Stat.CollectInterval`
(default 5 seconds), so under traffic a tick leaves within milliseconds of its
boundary and, once traffic stops, the last tick is closed and sent within one
`Stat.CollectInterval`.

At most 4 completed ticks (two minutes) are retained while the stat stream is down.
Beyond that the oldest tick is dropped and the agent logs a rate-limited warning.

* PINPOINT_GO_HTTP_URLSTAT_ENABLE
* type: bool
* default: false
* dynamic

### Http.UrlStat.LimitSize
Http.UrlStat.LimitSize option sets the limit size of the URLs to be collected.
It caps the number of distinct URLs kept in one tick. Once the limit is reached,
URLs already in the tick keep being aggregated but every further new URL is dropped,
and the agent logs a rate-limited warning carrying the number of warnings it suppressed.

* PINPOINT_GO_HTTP_URLSTAT_LIMITSIZE
* type: int
* default: 1000
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)
* dynamic

### Http.UrlStat.QueueSize
Http.UrlStat.QueueSize option sets the size of the agent's URL statistics queue.
This queue buffers the per-request URL records waiting to be aggregated into a snapshot,
unlike Http.UrlStat.LimitSize which caps the number of distinct URLs kept in one tick.
When the queue is full the records are dropped, and the agent logs a rate-limited warning
carrying the cumulative number of dropped records.

* PINPOINT_GO_HTTP_URLSTAT_QUEUESIZE
* type: int
* default: 1024
* range: 1 ~ 65536 (an out-of-range value falls back to the default with a warning log)

### Http.UrlStat.WithMethod
Http.UrlStat.WithMethod option adds http method as prefix to url string key.

* PINPOINT_GO_HTTP_URLSTAT_WITHMETHOD
* type: bool
* default: false
* dynamic

---

### Mongo.RecordCommand
Mongo.RecordCommand option sets whether the MongoDB plugins (mongodriver,
mongodriverv2) record the command document on the span event, converted to
extended JSON. The conversion runs on the request goroutine for every sampled
command; turn it off when the document is not needed in the trace.

* PINPOINT_GO_MONGO_RECORDCOMMAND
* type: bool
* default: true
* dynamic

### Mongo.CommandMaxSize
Mongo.CommandMaxSize option sets the size in bytes the recorded command
document is cut to. A command whose BSON is larger than this is not converted
at all and is recorded as a one-line description instead, so a lower value also
lowers the cost of large commands. A value of 0 or less keeps the default.

* PINPOINT_GO_MONGO_COMMANDMAXSIZE
* type: int
* default: 65536
* dynamic

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
  type: "PERCENT"
  percentRate: 100        # 100%

log:
  level: "debug"
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
`called after EndSpan` warnings of the API-contract checks are logged at
`debug`. The check for a tracer shared across goroutines warns at every level.

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
  type: "PERCENT"
  percentRate: 1          # 1%
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

`Http.UrlStat.Enable` is the reason a 1% sampling rate is not a 1% view:
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
  type: "PERCENT"
  percentRate: 1
```

In a container the environment is usually the natural source, and it overrides
the config file:

```bash
PINPOINT_GO_APPLICATIONNAME=MyApp
PINPOINT_GO_AGENTNAME=myapp
PINPOINT_GO_COLLECTOR_HOST=pinpoint-collector.monitoring.svc.cluster.local
PINPOINT_GO_SAMPLING_TYPE=PERCENT
PINPOINT_GO_SAMPLING_PERCENTRATE=1
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

See [Troubleshooting](troubleshooting.md#high-cpu-usage-or-slow-responses):
sample rather than throttle, exclude the noise URLs, keep `Error.TraceCallStack`
off and `Log.Level` at `info`.

**Getting it right**

* Read the startup config dump. It is the resolved configuration after all five
  sources are merged, and it settles most "why is this not taking effect"
  questions in one line.
* Prefer one config file plus environment overrides. Spreading the same option
  across flags, environment and file is how precedence surprises happen.
* Always pass the routed URL pattern, not the resolved path, so
  `Http.UrlStat.LimitSize` bounds something meaningful.
