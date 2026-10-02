# Development Guide

How to build, test and extend the agent itself. If you only want to instrument
an application, start with [Getting Started](getting_started.md) instead.

## Prerequisites

* **Go 1.25+** — the floor in `go.mod`. CI verifies 1.25 and 1.26.
* Nothing else for a normal build. Protobuf regeneration is the one exception
  and has [its own section](#regenerating-the-protobuf-code); the generated
  code is committed, so you do not need `protoc` to build or test.

## Repository layout

The repository is a set of **independent Go modules**, which is the single most
important thing to know before running anything:

| Path | Module | Why separate |
|---|---|---|
| `/` | `pinpoint-go-agent` | the agent; depends on nothing but gRPC and its own support libraries |
| `plugin/<name>/` | one module each | so instrumenting Gin does not pull in Kafka, Mongo and Elasticsearch |
| `example/` | own module | depends on the agent and two plugins, via `replace` |
| `test/it/` | own module | depends on `plugin/http`, which the agent module must not |
| `test/e2e/` | own module | depends on `plugin/http` and `plugin/grpc` |

The generated gRPC code is `internal/protobuf`, which Go lets only code under
the agent's own path import. The test modules build their collector stand-ins
from it, so their module paths sit under the agent's
(`github.com/pinpoint-apm/pinpoint-go-agent/v2/test/it`). `example/` stays
outside on purpose: like an application, it can build against the public API
only.

`go test ./...` from the repository root therefore does **not** run the plugin
or integration tests — each module has to be entered. That is what the loops
below are for.

## Build and test the agent

```bash
go build -v
```
```bash
go test -v
```
```bash
go test -race ./...
```

The agent's tracers are concurrency-sensitive, so `-race` is part of the
contract rather than an occasional extra. Run it before sending a change that
touches spans, the queue or the config snapshot.

## Test the plugins

Each plugin is its own module, so they run one at a time. This is the loop CI
uses, and the one to run locally before touching a plugin:

```bash
for dir in plugin/*/; do (cd "$dir" && go test -race ./) || echo "FAILED: $dir"; done
```

Only the module's own package is tested — the `example/` directories are
standalone `main` programs and do not build as part of the package.

## Integration tests (mock collector)

`test/it` starts a real in-process gRPC collector on three ephemeral ports and
drives a real agent against it. It needs **no Pinpoint collector and no network
access**, which makes it the right place to assert what the agent actually puts
on the wire.

```bash
cd test/it
go test ./...            # full suite, ~1 minute
go test -short ./...     # skips the URL-statistics test's 30s tick
go test -race ./...
go test -run TestSendsAllMetadataAndCompleteSpanShapes -v
```

Every received protobuf and its client metadata are copied into a thread-safe
snapshot, so a test asserts on the real message rather than on an internal
call. Coverage spans span lifecycle and chunking, async/goroutine spans,
propagation, v1/v3/v4 identity metadata, SQL metadata through the real driver,
agent and URL statistics, sampling and throughput limits, the `plugin/http`
helpers, and profiler commands over the real bidirectional stream. See
[test/it/README.md](/test/it/README.md).

### Goroutine leak profile

Go 1.26's experimental goroutine leak profile catches a worker that outlived
`Shutdown()` — the shape that `runtime.NumGoroutine()` comparisons can only
guess at. The check lives in `test/it`'s `TestMain` and is a no-op without the
experiment, so it costs the ordinary runs nothing:

```bash
cd test/it
GOEXPERIMENT=goroutineleakprofile go test -v -timeout 15m ./...
```

## End-to-end tests (live collector)

`test/e2e` runs the agent against a real collector, across separate processes —
separate because a Pinpoint agent is a process-global singleton, so one process
cannot play both upstream and downstream.

```bash
cd test/e2e
export PINPOINT_GO_COLLECTOR_HOST="your-collector-host"
./run_e2e.sh
```

`pinpoint-config.yaml` deliberately omits `Collector.Host`, making the
collector an explicit runtime decision. To exercise the harness without a
collector, `--local-collector` starts a bundled stub that accepts everything
and records nothing — a self-test of the harness, not of the protocol.

`load_test.py` is the load generator: with `--rps` a constant-arrival-rate test
that reports latency and scheduling lag, without it an unthrottled saturation
test. See [test/e2e/README.md](/test/e2e/README.md).

## Benchmarks

Performance-sensitive paths carry benchmarks next to their unit tests
(`benchmark_test.go`, `grpc_bench_test.go`, `stats_bench_test.go`,
`sql_util_bench_test.go`):

```bash
go test -run '^$' -bench . -benchmem
```
```bash
go test -run '^$' -bench BenchmarkSpan -benchmem -count 10
```

Use `-count 10` and compare with `benchstat`; a single run of a
nanosecond-scale benchmark tells you very little. The agent runs inside the
request path, so an allocation added per span event is a real regression —
`-benchmem` is not optional here.

## Regenerating the protobuf code

The generated code in `internal/protobuf/` is committed, so this is only needed
when the IDL changes:

```bash
./scripts/generate-protobuf.sh
```

The same run regenerates `test/testapp`, the gRPC test service the end-to-end
suite and the grpc examples talk over, from `test/testapp/testapp.proto`.

The script downloads pinned versions of `protoc`, `protoc-gen-go`,
`protoc-gen-go-grpc` and the gRPC mock generator into `.tools/`, so it does not
depend on what happens to be installed: each one has to report the pinned
version before it is used, and a copy elsewhere on `PATH` is not taken. That
matters because every generator stamps its own version into the files it
writes, so an older one rewrites the whole tree along with the header saying
which version wrote it.

Sources come from the `pinpoint-grpc-idl` submodule, so run
`git submodule update --init` first. `Log.proto` is excluded on purpose — it
describes a log-shipping service this agent does not implement, and generating
it would ship a client and a mock nothing calls.

The `testify` line in the mock headers is the version the mock generator itself
was built against, read out of its build info. It is not the version this
module requires, which only has to be new enough to compile what the generator
wrote.

## Adding a plugin

Follow an existing plugin of the same shape — a middleware plugin like
`plugin/gin`, a driver like `plugin/mysql`, a hook like `plugin/goredisv9`.
Each new plugin needs:

1. **Its own module.** `plugin/<name>/go.mod`, with the agent as a dependency.
   Never add the instrumented library to the agent's own `go.mod`. The module
   path carries the agent's major version, like every other plugin's
   (`.../plugin/<name>/v2`), and it requires the agent and any plugin it
   builds on at the version the other modules require, each with a `replace`
   to its directory. CI (`scripts/set-major-version.sh --check`) fails
   otherwise.
2. **Package name `pp<name>`.** `plugin/gin` is `ppgin`.
3. **A thin entry point.** Prefer the library's own seam — middleware, hook,
   observer, monitor, `RoundTripper` — over wrapping every call site.
4. **A `README.md`** with install, usage and a link to the full example, and an
   **`example/`** that builds and runs.
5. **Tests, run with `-race`.** The convention in the existing plugins is to
   assert against a real agent, and to cover the cases that break in
   production: an unsampled transaction, a disabled agent, a panic through the
   wrapper, an unmatched route, and concurrent requests.

The behavioral requirements are the same as for any instrumentation, and they
are worth re-reading before writing one:
[Tracer, Span, and Annotation Contracts](api_contracts.md), plus the
[checklist](instrument.md#checklist-for-a-new-instrumentation).

Two that plugin authors hit most often:

* **Pass through cleanly when there is nothing to trace.** A disabled agent or
  an unsampled transaction hands you a no-op tracer; the wrapper must still
  call the wrapped code and change nothing observable.
* **Never swallow a panic.** Close the span event (a `defer` does this) and let
  the panic propagate, so the framework's own recovery middleware behaves as
  the user configured it.

## Java and C++ agent parity

The Go agent is the third implementation of the same protocol, and a good
number of its non-obvious defaults exist to agree with the other two. The
user-facing documents state the resulting behavior; the reasoning and the
references live here.

**Aligned with the Java agent**

| Behavior | Java reference |
|---|---|
| `PSpan.err` is a bitmask of causes, OR-ed as they accumulate | `Shared.maskErrorCode` |
| a server layer that finds a tracer already in its context (a middleware installed twice, a framework middleware inside a wrapped handler, compile-time instrumentation outside the manual wrapper) reuses it: `pinpoint.NestedTracer` records the layer's span event on the existing span and ignores its `EndSpan` | `DefaultTraceFactory.checkAndGet` keeps the existing `Trace` ("already Trace Object exist.") instead of starting a second one for the request |
| an error on an async/goroutine tracer sets the flag on the root span | `ChildTrace` shares its parent's `TraceRoot` |
| `Inject()` omits a header it has no value for | `DefaultRequestTraceWriter`, which normalizes an empty value to `NOT_SET` |
| `s0` is written only by a tracer that stands for a real transaction | written for a trace created by `disableSampling()`; with no trace the interceptor returns before writing any header |
| inbound continuation requires TraceID + SpanID + pSpanID, checked in that order | `DefaultTraceHeaderReader.read`: `s0` short-circuits first, `Pinpoint-Flags` defaults to `0` |
| the two span id headers are checked for presence only | an unparseable value is kept as `SpanId.NULL` via `NumberUtils.parseLong` |
| a header present with an empty value is present | `DefaultTraceHeaderReader` tests for `null` alone; the C++ agent decides on `has_value()` |
| `AddMetric(MetricURLStat, ...)` is first-wins | `Shared.setUriTemplate` |
| a percent rate `<= 0` samples nothing, `>= 100` always samples | `PercentSamplerFactory`: `FalseSampler` and `TrueSampler` |
| events one level deeper than `Span.MaxCallStackDepth` are still recorded | `DefaultCallStack` |
| `SQL.CacheSize` sizes the SQL caches only; API and error caches stay at 1024 | `profiler.jdbc.sqlcachesize` sizes `SimpleCacheFactory.newSqlCache()` / `newSqlUidCache()`, while `newSimpleCache()` keeps its own default |
| `SQL.CacheLengthLimit` bypasses the UID and raw caches but not the SQL-ID cache | the bypass lives only in `UidCache`; the id cache from `SimpleCacheFactory.newSqlCache()` has no length check |
| the SQL error count lives on the trace root, so async spans add up | `WrappedSpanEventRecorder`, `DefaultSqlCountService` |
| `SQL.RemoveComments` defaults to on | `profiler.jdbc.removecomments` is absent from the distributed `pinpoint.config`, and the unresolved placeholder leaves the field initializer in place |
| the request query string annotation format | `HttpServletParameterExtractor` |
| the client URL annotation drops its query by default | `profiler.<plugin>.param` / `InterceptorUtils.getHttpUrl` — **Java defaults this on; the Go agent defaults it off**, because query strings routinely carry tokens and user ids |
| the gRPC flow control window and write buffer follow Java | `ClientOption` / `DefaultChannelFactory.setupClientOption`: a fixed 1 MiB window with BDP auto-tuning off. All three agents disable the idle timeout, but by different means, so the decision is shared and the value is not. The HTTP/2 mechanics are in `grpc.go` above `dialOptions` |

**Aligned with the C++ agent**

| Behavior | Note |
|---|---|
| unclosed events are ended by `EndSpan()` and still sent | Java drops the whole span; holes in the event sequence are worse for the collector than shifted durations |
| `Log.Output` defaults to `stdout` | it was `stderr` before this release |
| `Log.MaxBackups` exists, with no age or compression key | the C++ agent has no such setting, and a Go-only key would leave the two agents' config files disagreeing |
| `Log.MaxBackups` rejects 0 | the rotation library reads 0 as "keep every backup", which can fill the disk, while a reader of the C++ agent would take it as "keep none" |
| `Collector.Grpc.KeepAlivePermitWithoutCalls` defaults to false | agents older than this release behaved as if it were true |
| the SQL cache options are fixed at startup | the C++ agent fixes them too; see the read-once note above |
| the gRPC channel state log lines are worded identically | they are the log-only stand-in for Java's Channelz reporters |
| `ServerInfo` is a config key here | the C++ agent takes it through `AgentOptions.server_info` only |
| a statement over the 1 MiB normalization cap is dropped, not cut | a cut landing inside a literal loses the placeholder and yields a SQL id / UID no other agent computes; C++ `kMaxNormalizedSqlLength` uses the same value and the same drop policy |
| a rejected metadata send is not retried | Java reschedules a `PResult.success=false` like a transport error. Here, as in the C++ `GrpcMetadata::process_completed`, it is dropped — a rejection is a verdict on the payload — and the cache entry is released after one delay, so the next use registers a fresh id instead of every later span referencing an id the collector refused |
| while metadata give-ups follow one another, an item out of attempts keeps its cache entry for up to 30 retry delays | the first give-up since a delivery is still released at once; released at once throughout an outage, every hot key's next use minted a fresh id and spent a new attempt budget every couple of seconds for as long as the collector was down. A delivery of any other item ends the wait at the next delay, so re-registration is not held up once the collector is back |
| `Collector.AgentInfo.SendRetryInterval` is 3000 ms | Java's effective value is 300000 ms; registration gates tracing in this agent and in the C++ one, so both have to retry far more often |
| the lifecycle is one phase, not a pair of flags | the C++ agent's `started_` / `shutting_down_` / `init_failed_` under `lifecycle_mutex_`, held here in a single atomic instead. Tracing stops at the shutdown signal rather than at the end of the drain, so the drain cannot race a request path still producing |

**Configuration key mapping**

The user-facing [Configuration](config.md) reference describes each option on
its own terms. Where an option exists because the Java agent has one, this is
the key it came from.

| Go option | Java agent key |
|---|---|
| `Uid.Version` | `pinpoint.modules.uid.version` |
| `Collector.Grpc.ConnectionMaxAge` | `profiler.transport.grpc.loadbalancer.renew.period.millis` |
| `Collector.Grpc.StreamMaxAge` | `profiler.transport.grpc.span.sender.rpc.age.max.millis` — the +/-10% randomization comes from there too |
| `Collector.Grpc.IdleTimeout` | `ClientOption.idleTimeoutMillis`, set to 30 days and so in effect disabled; the C++ key is `Collector.Grpc.IdleTimeoutMs` |
| `Collector.Grpc.SenderQueueSize` | `profiler.transport.grpc.metadata.sender.executor.queue.size`; the C++ agent's key of the same name |
| `Sampling.Type: "COUNTING"` | the Java name for the counter sampler, kept as an alias of `"COUNTER"` |
| `Stat.CollectInterval` range | the Java agent's 1000-10000 bounds |
| `Span.IgnoreErrors` | `profiler.ignore-error-handler.<name>.class-name`, `.exception-message.contains`, `.nested=true` |
| `Span.ErrorMark` | `profiler.error.mark` — unset there means every cause, as an empty list does here |
| `Span.ErrorMarkExclude` | `profiler.error.mark.exclude` (`mark.removeAll(exclude)`, so an exclusion wins) |
| `SQL.CacheSize` | `profiler.jdbc.sqlcachesize`; C++ `Sql.CacheSize` |
| `SQL.CacheLengthLimit` | `profiler.jdbc.sqlcachelengthlimit` |
| `SQL.CacheExpireHours` | `profiler.jdbc.sqlcacheexpirehours` |
| `SQL.ErrorCount` | `profiler.sql.error.count` + `profiler.sql.error.enable`, merged |
| `SQL.RemoveComments` | `profiler.jdbc.removecomments` |
| `Error.NewThroughput` | `profiler.exceptiontrace.new.throughput` |
| `Error.MaxChainDepth` | `profiler.exceptiontrace.max.depth`, which defaults to 5 |
| `Http.Server.ProxyHeaderEnable` | `profiler.proxy.http.header.enable` |
| `Http.Server.ProxyUserHeaderNames` | `profiler.proxy.user.header.names`; the `t=` / `D=` format inference follows `UserRequestParser` |
| `Http.Server.RealIpHeader` | `profiler.server.realipheader` (`RealIpHeaderResolver`) — **Java trusts no header by default; this agent keeps `X-Forwarded-For` then `X-Real-Ip`** so existing deployments record the same address as before |
| `Http.Server.RealIpEmptyValue` | `profiler.server.realipemptyvalue` |
| `Http.Server.RecordRequestParam` | `profiler.server.tracerequestparam` |
| `Http.Client.RecordUrlQuery` | `profiler.<plugin>.param` |
| `Http.UrlStat.LimitSize` | `profiler.uri.stat.completed.data.limit.size` |

**Deliberate divergences**

* **Tracing waits for registration.** The Java agent traces whether or not
  registration has succeeded; this agent returns a no-op span and collects no
  stats until the collector accepts the AgentInfo, so a blocked agent port
  shows up as "no data at all" rather than as partial data, which is the easier
  failure to diagnose.
* **The root span's error flag is not deferred.** Java's ordinary
  `DefaultTrace` stores the `PSpan` at the root's close as well; only the
  `AsyncDefaultTrace` path, marked `LimitedPrivate("vert.x")`, awaits the last
  child. Deferring the root store here would be an extension past Java, not a
  parity fix.
* **`SQL.ErrorCount` merges two Java options.** Java never range-checks
  `profiler.sql.error.count`, so `enable=true` with a count of 0 fails the very
  first query. Here 0 already spells `enable=false`, which leaves "off" as the
  only consistent meaning of a non-positive threshold.
* **An empty SQL statement records nothing.** Java caches and counts `""` like
  any statement, but its commit and rollback interceptors never pass one. The
  `database/sql` wrapper here routes `Begin`, `Commit` and `Rollback` through
  `SetSQL` with no statement, so the guard keeps a transaction boundary from
  carrying an empty SQL annotation.
* **`SetSQL` bounds a caller-composed bind value list.** Java bounds nothing
  there. Here `args` comes from any caller and rides on a span that is dropped
  whole past the send message size; the agent's own driver wrappers compose
  lists within the bound and pass through untouched.
* **A malformed inbound trace id starts a new transaction.** Java takes the
  continue path on any non-null `Pinpoint-TraceID` and throws when it parses
  it later. An exception on the request path is a worse answer than a new
  trace, and routing a garbage id through the continue sampler, whose
  `isContinueSampled()` is unconditionally true, would bypass the configured
  sampling rate.
* **JVM-shaped stat fields carry Go values.** Go's GC is concurrent and
  non-generational, so `PJvmInfo.gcType` and `PJvmGc.type` are
  `JVM_GC_TYPE_UNKNOWN`, as in the C++ agent. `jvmGcOldCount` carries
  `runtime.MemStats.NumGC` and `jvmGcOldTime` carries `PauseTotalNs` in
  milliseconds, so both read lower than a JVM's.
* **No shutdown hook.** Java closes itself from a JVM shutdown hook. Go has no
  `atexit`, and a `signal.Notify` installed by default would change
  process-wide state behind the application's back, so `ShutdownOnSignal` is
  opt-in; the C++ agent's opt-in is `std::atexit`, which covers `exit()` but not
  a signal. See
  [Troubleshooting](troubleshooting.md#spans-missing-at-shutdown-or-on-a-rollout).

## Continuous integration

[`.github/workflows/ci.yml`](/.github/workflows/ci.yml) runs on every push and
pull request to `main`: the agent build and tests plus `test/it` on Go 1.25 and
1.26, the plugin loop above with `-race` on both versions, `test/it` under
`GOEXPERIMENT=goroutineleakprofile` on 1.26, and
`scripts/set-major-version.sh --check`. `test/e2e` is **not** in CI: it needs a
live collector. `fail-fast` is off on purpose, so a break on one Go version or
one plugin still reports the rest.

Before opening a pull request, the short version of CI:

```bash
scripts/set-major-version.sh --check && go test -race ./... && (cd test/it && go test ./...) && for dir in plugin/*/; do (cd "$dir" && go test -race ./) || echo "FAILED: $dir"; done
```

## Releasing

The published modules - the agent and every `plugin/<name>` - are released
together, at one version, by [`scripts/release.sh`](/scripts/release.sh). A
user's build ignores `replace` directives, so every requirement between these
modules has to name a version that is tagged. The release commit requires each
sibling at the release version, and every published module is tagged at that
commit (`v2.0.0`, `plugin/gin/v2.0.0`, ...):

```bash
scripts/release.sh prepare v2.0.0-rc.1   # in-repo requirements and version.go
git commit -am "[#noissue] bump version number up to v2.0.0-rc.1" && git push
# once CI passes on that commit:
scripts/release.sh tag v2.0.0-rc.1       # checks the commit, then tags HEAD
scripts/release.sh push v2.0.0-rc.1      # one atomic push of every tag
scripts/release.sh verify v2.0.0-rc.1    # fetch and build each module as a user would
```

Cut a pre-release first. The module proxy and the checksum database keep the
first content they see for a version, so a pushed tag cannot be moved: a
broken release is fixed by the next version, with a `retract` directive for
the broken one. `check` and `tag` refuse a version whose tags already exist
here or on the remote, and `push` refuses a commit that no branch of the remote
holds.

A new major version starts with
[`scripts/set-major-version.sh`](/scripts/set-major-version.sh) `N`, which
moves every module path to `/vN`. v1 fixes are released from the
`v1.4.0-patch` branch.

## Contributing

See [CONTRIBUTING.md](/CONTRIBUTING.md). Pull requests need a signed
Contributor License Agreement, and should not break the build or any test.
