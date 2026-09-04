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
| SQL count per transaction | `DefaultSqlCountService` | **Adopted** — `SQL.ErrorCount` |
| Exception chain rate limiter | `ExceptionChainSampler` | **Adopted** — `Error.NewThroughput` |

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
`maxErrorChainEntry` (10), but nothing capped the *rate*.

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
