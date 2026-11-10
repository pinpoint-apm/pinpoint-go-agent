# Migrating from v1 to v2

v2 changes the import path of every module, so moving to it is a deliberate
step: `go get -u` keeps an application that imports v1 on v1. This guide covers
what changes for an application coming from v1.4.x, in the order to do it. The
changes that matter to an application are listed in the
[changelog](/CHANGELOG.md).

* [1. Check the requirements](#1-check-the-requirements)
* [2. Move every import to v2 at once](#2-move-every-import-to-v2-at-once)
* [3. Fix what no longer compiles](#3-fix-what-no-longer-compiles)
* [4. Check the configuration](#4-check-the-configuration)
* [5. Expect some recorded data to change](#5-expect-some-recorded-data-to-change)
* [6. Verify](#6-verify)

## 1. Check the requirements

v2 needs Go 1.25 or newer, up from 1.21; `go get` raises the `go` line of your
`go.mod` to match.

It also needs a Pinpoint 3.1.0 or newer collector, up from 2.4.0. The agent
sends spans with the unary `SendSpanBatch` RPC, which a collector implements
from 3.1.0; v1's long-lived `SendSpan` stream is gone. Against an older
collector every batch fails, the agent logs
`SendSpanBatch failed - N spans dropped`, and nothing reaches the UI. Upgrade
the collector before the agent.

## 2. Move every import to v2 at once

From v2 on, Go puts the major version in the module path, so the agent and each
plugin have a new one. The package names stay (`pinpoint`, `pphttp`, `ppgin`,
...), so for every plugin v2 still has, only import lines change:

| v1 | v2 |
|---|---|
| `github.com/pinpoint-apm/pinpoint-go-agent` | `github.com/pinpoint-apm/pinpoint-go-agent/v2` |
| `github.com/pinpoint-apm/pinpoint-go-agent/plugin/<name>` | `github.com/pinpoint-apm/pinpoint-go-agent/plugin/<name>/v2` |

Five v1 plugins have no v2 module. Move off them first: the rewrite below
turns their imports into v2 paths that no module provides, and `go mod tidy`
stops on them.

| v1 | Move to |
|---|---|
| `plugin/sarama` (`ppsarama`, Shopify/sarama) | IBM/sarama upstream and `plugin/sarama-IBM` (`ppsaramaibm`, the same API) |
| `plugin/goredis` (`ppgoredis`, go-redis v6) | go-redis v8 or v9 with `plugin/goredisv8` or `plugin/goredisv9`: add `NewHook(opts)` to a plain client with `AddHook` instead of creating a `ppgoredis.NewClient`, and pass the context to each command instead of `WithContext` |
| `plugin/goredisv7` (`ppgoredisv7`, go-redis v7) | go-redis v8 or v9 with `plugin/goredisv8` or `plugin/goredisv9`: `NewHook` and `NewClusterHook` are added as before, and the context goes to each command instead of `WithContext` |
| `plugin/mssql` (`ppmssqldb`, denisenkom/go-mssqldb) | microsoft/go-mssqldb with `plugin/mssql-microsoft`; open with `sql.Open("mssql-microsoft-pinpoint", dsn)` instead of `"sqlserver-pinpoint"` |
| `plugin/echo` (`ppecho`, echo v3) | echo v4 or v5 with `plugin/echov4` or `plugin/echov5`: echo v3 is end-of-life upstream with an unpatched advisory |

go-redis v6 and v7 are end-of-life upstream too. Import a replacement under the
v1 path shown; the rewrite below moves it to v2 with the rest.

This rewrites every such import under the current directory, then lets
`go mod tidy` swap the requirements:

```bash
grep -rl --include='*.go' --exclude-dir=vendor 'github.com/pinpoint-apm/pinpoint-go-agent' . |
  xargs perl -pi -e 's{"github\.com/pinpoint-apm/pinpoint-go-agent(/plugin/[A-Za-z0-9_-]+)?"}{"github.com/pinpoint-apm/pinpoint-go-agent$1/v2"}g'
go mod tidy
```

Move all of them in one change, including the ones your own libraries import.
v1 and v2 are different modules to Go, and both register the same protobuf
files, so a binary that links one module of each panics before `main` runs:

```
panic: proto: file "v1/Annotation.proto" is already registered
```

`go mod why -m github.com/pinpoint-apm/pinpoint-go-agent` names the package
that still pulls v1 in, and the same works for a plugin's v1 module path.
Setting `GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn` gets such a binary to
start, but not to work: each version keeps its own agent, so the v1 plugins
find none started and record nothing. Do not use it to put the migration off.

## 3. Fix what no longer compiles

| v1 | v2 | What to do |
|---|---|---|
| `DistributedTracingContextReader.Get(key string) string` | `Get(key string) (string, bool)` | report whether the carrier holds the key, as below |
| `SpanRecorder.SetError(e error)`, `SetFailure()` | `SetError(e error, errorName ...string)`, `SetFailure(category ...ErrorCategory)` | nothing for a caller; an implementation of the interface, such as a mock, declares the new parameters |
| `pinpoint.WithAgentId(id)`, `pinpoint.CfgAgentID` | removed | drop them; see [The agent id](#the-agent-id) |
| `.../protobuf`, `.../protobuf/mock`, `.../asm` | moved under `internal/` | nothing replaces them: they are the agent's wire format and its goroutine pointer helper, not API |
| `pphttp.WrapResponseWriter` returns the unexported `*responseWriter` | returns `http.ResponseWriter` | nothing where the result was used as an `http.ResponseWriter` |
| `pinpoint.NewTestAgent(config, t)` | `NewTestAgent(config)` | drop the `*testing.T`; it was never used |
| `pinpoint.WithSamplingRate(n)` | removed | `WithSamplingCounterRate(n)` |
| `pplogrus.WithField(tracer)` | removed | `pplogrus.NewField(tracer)` |
| `Tracer.NewAsyncSpan()` | removed | `NewGoroutineTracer()` |
| `Tracer.JsonString()`, `Tracer.AsyncSpanId()` | removed | `pinpoint.SpanJSON(tracer)` for the first; nothing replaces the second |
| `tracer.AddMetric(pinpoint.MetricURLStat, entry)` | removed | `tracer.RecordUrlStat(entry)`, with the `UrlStatEntry` passed by value; an implementation of `Tracer`, such as a mock, renames the method |
| `pinpoint.UrlStatEntry.Status` | removed | drop the field; mark an error status on the span with `SetFailure(pinpoint.ErrorCategoryHttpStatus)` |
| `pphttp.CollectUrlStat(tracer, url, method, status)` | `CollectUrlStat(tracer, url, method)` | drop the status argument; the status already marks the span through `Http.Server.StatusCodeErrors` |
| `ppsaramaibm.HeaderAsyncSpanId` | removed | nothing: no v2 release writes or reads that header |
| `Tracer.Extract(reader)` | removed | create the span with `Agent.NewSpanTracerWithReader(operation, rpcName, reader)` |
| `pphttp.WrapHandle(agent, name, pattern, h)`, `WrapHandleFunc(...)` | removed | `pphttp.WrapHandler(h)`, `WrapHandlerFunc(f)`; register under the pattern as before |
| `ppbeego.Middleware()` | removed | `web.InsertFilterChain("/*", ppbeego.ServerFilterChain())` |
| `ppbeego.DoRequest(tracer, req)` | removed | `httplib.AddDefaultFilter(ppbeego.ClientFilterChain(nil))` once, and build `req` with `httplib.NewBeegoRequestWithCtx(pinpoint.NewContext(ctx, tracer), url, method)` |
| `ppsaramaibm.ConsumeMessage`, `WrapConsumerMessage`, `NewConsumer`, `WrapPartitionConsumer` (also in `ppsarama`) | removed | `ConsumeMessageContext(handler, ctx, msg)` on a raw `sarama` consumer, with `ppsaramaibm.NewContext` carrying the broker addresses |
| `ppsaramaibm.WithContext(ctx, producer)`, `producer.WithContext(ctx)` (also in `ppsarama`) | removed | `SendMessageContext`, `SendMessagesContext`, `InputContext` with the context; `SendMessage`, `SendMessages` and `Input` produce without tracing |

A carrier of your own - a message queue's headers, say - now tells a key held
with an empty value from a key it does not hold. The difference decides whether
a trace continues across a proxy that blanks a header instead of dropping it
([contract 10](api_contracts.md#10-no-op-and-unsampled-tracers-are-deliberately-silent)):

```go
// v1
func (r headerReader) Get(key string) string {
	return r.headers[key]
}

// v2
func (r headerReader) Get(key string) (string, bool) {
	v, ok := r.headers[key]
	return v, ok
}
```

A source that cannot tell the two apart reports a value it has as present and
an empty one as absent: `return v, v != ""`. An `http.Header` can tell them
apart, and `pinpoint.HttpHeaderReader(h)` reports a header held with an empty
value as present.

One change still compiles but stops tracing. `(*ppgomemcache.Client).WithContext(ctx)`
returns a copy of the client bound to the context's tracer and leaves the
receiver as it was; v1 bound the receiver and returned nothing. A v1 call on a
line of its own still compiles, and the calls after it record nothing. Use the
copy for the request's calls:

```go
// v1
mc.WithContext(ctx)
mc.Get("foo")

// v2
c := mc.WithContext(ctx)
c.Get("foo")
```

## 4. Check the configuration

### The agent id

The agent id is no longer configurable: every process generates its own
([AgentId](config.md#agentid)), so each start shows up in the Pinpoint UI as a
new agent of the application. [AgentName](config.md#agentname) is the stable,
human-readable label. Remove the old setting wherever it is:

| Where | In v2 |
|---|---|
| `AgentId` in the config file | ignored |
| `PINPOINT_GO_AGENTID` | ignored |
| `--pinpoint-agentid` | ignored, like any unknown `--pinpoint-` flag |
| `pinpoint.WithAgentId()` | does not compile |

The deprecated `LogLevel` key is gone too: `LogLevel` in the config file,
`PINPOINT_GO_LOGLEVEL` and `--pinpoint-loglevel` are ignored. Use `Log.Level`,
`PINPOINT_GO_LOG_LEVEL` and `--pinpoint-log-level`.

### Defaults that changed

| Option | v1 | v2 default | To keep the v1 behavior |
|---|---|---|---|
| [Uid.Version](config.md#uidversion) (new) | no service name; ApplicationName up to 24 bytes | `v4`: [ServiceName](config.md#servicename) is required, and a 3.1.x collector refuses the agent without an [ApiKey](config.md#apikey) | `v1`, or `v3` for ApplicationName up to 254 bytes |
| [Log.Output](config.md#logoutput) | `stderr` | `stdout` | `stderr` |
| [Collector.Grpc.DnsResolverEnable](config.md#collectorgrpcdnsresolverenable) (new) | one address per collector host | `true`: every address of the host, resolved again when a connection fails, drops or is [renewed](config.md#collectorgrpcconnectionmaxage) | `false` |
| [Collector.Grpc.KeepAlivePermitWithoutCalls](config.md#collectorgrpckeepalivepermitwithoutcalls) (new) | keepalive pings with no stream open | `false`: pings only while a stream is open | `true` |
| [Http.Client.RecordUrlQuery](config.md#httpclientrecordurlquery) (new) | `HTTP.URL` keeps the query | `false`: cut at the `?` | `true` |
| [SQL.RemoveComments](config.md#sqlremovecomments) (new) | comments kept in the SQL | `true`: comments removed | `false` |
| [Http.UrlStat.LimitSize](config.md#httpurlstatlimitsize) | 1024 | 1000 | 1024 |
| [Stat.CollectInterval](config.md#statcollectinterval) | any value, no range check | 1000 to 10000 ms; a value outside falls back to the 5000 ms default | none |

## 5. Expect some recorded data to change

Some of the changes above show in the Pinpoint UI for the same traffic:

* URL statistics recorded without a URI template are keyed `/NULL` instead of
  `UNKNOWN_URL`; the history under the old key does not carry over.
* The `HTTP.URL` of an outgoing request has no query string
  (`Http.Client.RecordUrlQuery`).
* A statement with comments normalizes to a different text, so it gets a new
  SQL id (`SQL.RemoveComments`). A statement longer than 1 MiB is not recorded
  at all.
* Every process start registers a new agent id ([The agent id](#the-agent-id)).

## 6. Verify

```bash
go list -m all | grep pinpoint-go-agent
```

lists only `/v2` modules. At startup the agent logs
`agent registered`, on stdout now. If it registers but no transaction
shows up, look for `SendSpanBatch failed` in its log (see
[Check the requirements](#1-check-the-requirements)), then work through
[No Data in the Pinpoint UI](troubleshooting.md#no-data-in-the-pinpoint-ui).

v1 keeps its import path, so an application that stays on it is unaffected; v1
fixes are released from the `v1.4.0-patch` branch.
