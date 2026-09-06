# Java Agent Feature Parity Decisions

The Java agent (`agent-module/profiler`) is the reference implementation, but
the Go agent does not follow it feature for feature: Go has no bytecode
instrumentation, no Guice module graph, and a much smaller config surface, so
some Java features cost far more here than they are worth. This file records
which Java-only features were reviewed, what was decided, and — for the ones
that were declined — what would make us revisit.

Add an entry when a Java feature is deliberately *not* ported. A feature that
is simply not written yet does not belong here.

---

## Summary

| Feature | Java reference | Decision |
|---|---|---|
| Per-URL sampler | `UrlTraceSampler`, `UrlSamplerConfig`, `TraceSamplerProvider` | **Declined** — see [below](#per-url-sampler--declined) |
| Tracing before agent registration | `AgentInfoSender`, `DefaultApplicationContext.start()` | **Declined** — see [below](#registration-before-tracing--declined) |
| SQL count per transaction | `DefaultSqlCountService` | **Adopted** — `SQL.ErrorCount` |
| SQL comment removal | `DefaultSqlNormalizer`, `DefaultJdbcOption` | **Adopted** — `SQL.RemoveComments` |
| Exception chain rate limiter | `ExceptionChainSampler` | **Adopted** — `Error.NewThroughput` |
| GC type and counts | `JvmGcType`, `GarbageCollectorMXBean` | **Diverges** — see [below](#gc-type-and-counts--diverges) |
| Span queue overflow policy | `SpanBatchGrpcDataSender` | **Same as Java** — a full send queue drops the oldest entry, as Java's default BATCH sender does (`queue.poll()` in `SpanBatchGrpcDataSender`); rejecting the newest is STREAM-mode-only behaviour, so head-drop is not a deviation |

---

## GC type and counts — diverges

**Java.** `PJvmInfo.gcType` in the AgentInfo and `PJvmGc.type` in every agent
stat both name the collector actually in use (`JvmGcType` is resolved from the
running `GarbageCollectorMXBean`), and `jvmGcOldCount` / `jvmGcOldTime` are the
*old generation* bean's `getCollectionCount()` and `getCollectionTime()`.

**Go.** There is no equivalent of any of the three. The runtime GC is a
concurrent, non-generational mark-sweep, so:

* **Type** is `JVM_GC_TYPE_UNKNOWN` in both messages, matching the C++ agent.
  Both used to say `JVM_GC_TYPE_CMS`, which was simply false — no consumer
  renders the stat field today (it is absent from
  `inspector-definition-for-agent.yml`), but a future one would have read a
  fabricated collector name, and the AgentInfo copy is what the agent's own
  detail view would show.
* **`jvmGcOldCount`** carries `runtime.MemStats.NumGC`, the count of *complete*
  cycles. Go has no generations, so there is no old-gen subset to report; the
  number covers everything Java would have split across young and old beans.
* **`jvmGcOldTime`** carries `runtime.MemStats.PauseTotalNs` in milliseconds,
  which is stop-the-world time only. Concurrent mark and sweep run alongside
  the application and are not included, whereas the JVM bean's
  `getCollectionTime()` counts the whole collection.

Both counters are cumulative since process start, like the JVM beans, and the
web's `delta` post-processor differentiates them correctly. The consequence is
only of scale: under comparable load a Go agent's GC time reads far lower than
a Java agent's, because it is measuring a smaller thing. Compare a Go agent
against other Go agents, not against a Java one.

**Revisit if** the collector gains a runtime-neutral GC type, or the inspector
gains a separate concurrent-GC series. Neither would change the numbers, only
how they are labelled.

---

## Exception chain rate limiter — adopted

**Java.** `ExceptionChainSampler` holds a Guava `RateLimiter` created from
`profiler.exceptiontrace.new.throughput` (default 1000/s). Every time
`DefaultExceptionRecorder` needs a *new* exception chain id it asks
`isNewSampled()`; a denied request returns the `DISABLED` sampling state, so
the throwable is neither recorded nor annotated with a chain id. Continuing an
already-sampled chain is free.

**Go before this change.** `span.traceCallStack` (`errors.go`) minted a chain id
from `agent.exceptionIdGen` unconditionally, and `EndSpan` enqueued one
`exceptionMeta` per failed span. The per-span chain list is capped at
`Error.MaxChainDepth` entries (at least 10), but nothing capped the *rate*.

**Why adopt.** The metadata channel is bounded and head-drops on overflow
(`agent.tryEnqueueMeta`), so the failure mode is not unbounded memory — it is
worse than that. Under an error burst, exception metadata crowds out the API,
string and SQL metadata queued on the same channel, and a dropped API id makes
the collector unable to render spans that reference it. Losing some exception
call stacks during a burst is a much better trade than losing the metadata the
rest of the trace is built from.

The cost is small: `golang.org/x/time/rate` is already a dependency of
`sampler.go`, and the decision point is the single `newId` branch in
`getExceptionChainId`.

**Option.** `Error.NewThroughput`, default 1000 (Java's default), `0` for
unlimited. Named after `Sampling.NewThroughput`, which limits the same way for
the same reason. See [Configuration](config.md#errornewthroughput).

---

## SQL comment removal — adopted

**Java.** `DefaultJdbcOption` initializes `removeComments = true` and binds it to
`profiler.jdbc.removecomments`. That key appears nowhere else in the pinpoint
repository and is absent from the distributed `pinpoint.config`, and
`ValueAnnotationProcessor` keeps the field initializer when a placeholder does
not resolve — so the effective default is `true`.
`SqlMetadataServiceProvider` passes it down through `DefaultCachingSqlNormalizer`
to `new DefaultSqlNormalizer(removeComments)`; the no-argument constructor that
defaults to `false` is only reached from the web and test paths.
`ParserContext` drops a `--` or `//` comment together with its terminating
newline, puts nothing in the removed comment's place, and does not treat the
comment as a number token boundary.

**Go before this change.** The normalizer always copied comments into the
output, with no option to do otherwise. Every statement carrying an Oracle hint
or an ORM-injected `/* trace:... */` tag therefore normalized to different text
than Java produced, and so landed on a different SQL id and a different SQL UID.
A service split across Go and Java reported one query as two.

**Why adopt.** The normalized text is what the collector keys a query on. Parity
here is not cosmetic — without it the two agents cannot agree on what a query
*is*. The change is a writer branch in the two comment consumers.

**Option.** `SQL.RemoveComments`, default `true` (Java's effective default).
Startup-only, because the normalized text is the SQL id cache key and the SQL
UID hash input. See [Configuration](config.md#sqlremovecomments).

**Upgrade note.** This is on by default, matching Java. SQL ids and UIDs change
for every statement that contains a comment, so such a query appears in the UI
as a new entry from the upgrade onward. Set `SQL.RemoveComments` to `false` to
keep the previous text.

---

## SQL count per transaction — adopted

**Java.** `DefaultSqlCountService.recordSqlCount` runs on every
execute-query-type span event. It counts SQL executions on the transaction's
`Shared` state and, at `profiler.sql.error.count` (default 100) or above, calls
`errorRecorder.recordError(ErrorCategory.SQL)`, which masks the transaction's
error code. A transaction that already failed is skipped, so the counter never
overwrites a real error. `profiler.sql.error.enable` (default true) turns the
whole thing off.

**Go before this change.** Nothing counted SQL per transaction. An N+1 query
loop produced a slow trace with hundreds of events and no marking of any kind.

**Why adopt.** It is the cheapest N+1 detector there is — one counter on the
span, checked where `SetSQL` already runs — and the server side needs nothing
new: `span.err` is what the Java agent's masked error code turns into on the
wire.

**Option.** `SQL.ErrorCount`, default 100 (Java's default), `0` to disable.
Java's two options collapse into one here, because Go has no
`ErrorCategory` bitmask to configure. See
[Configuration](config.md#sqlerrorcount).

**Upgrade note.** This is on by default, matching Java. A transaction that runs
100 or more statements and did not previously fail will now be marked failed —
visible in the scatter chart and in the URL statistics' failed histogram. Set
`SQL.ErrorCount` to `0` to keep the previous behaviour.

---

## Per-URL sampler — declined

**Java.** `UrlSamplerConfig` reads indexed properties —
`profiler.sampling.url.<n>.path`, `.counting.sampling-rate`,
`.percent.sampling-rate`, `.new.throughput`, `.continue.throughput` — and
`TraceSamplerProvider` builds one `TraceSampler` per entry.
`UrlTraceSampler.isNewSampled(urlPath)` picks the first entry whose Ant-style
pattern matches, falling back to the default sampler.

**Decision: not ported.** Four reasons, roughly in order of weight:

1. **The config system has no indexed keys.** Every Go option is registered up
   front by `AddConfig` in an `init` function and is reachable by a command
   flag and an environment variable derived from its name. `profiler.sampling.url.<n>.*`
   has no analogue; supporting it means either a second, pattern-matched config
   mechanism or one opaque encoded string option. Both are a bigger change than
   the feature.
2. **There is no Ant path matcher in the core module.** The one the agent has
   lives in `plugin/http` (`url.go`), which is a separate module that depends on
   the core — so the core cannot import it back. Porting the sampler means
   either duplicating the matcher or moving it into the core's public surface.
3. **The common case is already covered.** Per-URL sampling is used
   overwhelmingly to keep health checks, metrics endpoints and static assets out
   of the trace. `Http.Server.ExcludeUrl` does exactly that, with the Ant matcher
   already in place.
4. **Go samples on the raw path, not a URL template.** `NewSpanTracer` takes
   `rpcName` — for the HTTP plugins, `r.URL.Path`. The URL template arrives later,
   via `UrlStatEntry`, well after the sampling decision. Exact-match entries
   would therefore only ever match parameterless paths.

**Revisit if** the config system grows indexed or map-valued options for another
reason, or the Ant matcher moves into the core module for another reason. At
that point the sampler itself is small: `traceSampler` is already an interface
with two implementations, and `NewSpanTracerWithReader` already has the path in
hand.

---

## Registration before tracing — declined

**Java.** `DefaultApplicationContext.start()` calls `AgentInfoSender.start()`,
which only *schedules* the AgentInfo send (`Integer.MAX_VALUE` retries, spaced
`profiler.agentInfo.send.retry.interval`) and returns. Nothing gates the trace
path on the result: the `TraceContext` the interceptors use is already live, so
a span created before the collector ever accepted the AgentInfo is sampled,
recorded and sent, and the collector reconciles it when the metadata arrives.

**Go.** `connectGrpcServer` (`agent.go`) calls `registerAgentWithRetry` and only
then stores `enable`, opens the span/stat/command streams and starts the send
workers. Until registration succeeds `NewSpan` returns a no-op span, no stats
are collected and `Enable()` reports false.

**Decision: not ported.** Three reasons:

1. **Registration is the only place the connection's failures surface.** gRPC
   dials lazily, so a certificate this agent cannot verify, a plaintext
   fallback against a TLS collector and an application-level rejection are all
   invisible until the first RPC — and that first RPC is the registration. An
   attempt to start the workers first (`b0cf45c`) was reverted (`34ced4e`)
   because it broke seven integration tests that pin exactly this: every one of
   those failures left the agent reporting itself enabled, which turns a
   misconfiguration into a silently untraced process instead of a logged one.
2. **The C++ agent made the same call.** `GrpcAgent::registerAgentWithRetry`
   blocks `init_grpc_workers`, so both non-Java agents treat registration as
   the precondition. Changing Go alone would leave three agents with three
   different startup contracts.
3. **What the divergence actually costs is narrow.** Registration retries for
   as long as the process runs, so a collector that comes up later is picked up
   without a restart; only the spans created during the outage are lost, and
   Java loses those too whenever the collector is fully down. The behaviours
   differ only when the *agent* port alone is unreachable while the span port
   is fine.

**Mitigation.** The wait is explained rather than silent:
`registerAgentWithRetry` logs `still waiting for agent registration after
<n>ms (...): tracing stays disabled (NewSpan is a noop and no stats are
collected)` every `registrationWaitLogInterval` (30s), matching the C++ agent's
line word for word so one troubleshooting page covers both. See
[Troubleshooting](troubleshooting.md#verifying-agent-startup).

**Revisit if** those lazy-dial failures can be surfaced without registering —
a blocking dial, or a health probe before `enable` is stored. At that point the
workers could start first and the integration tests would still see a disabled
agent on a bad certificate, which is the only thing that made the first attempt
fail.
