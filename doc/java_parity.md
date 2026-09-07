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
| Error cause categories in `err` | `ErrorCategory`, `ConfigurableErrorRecorder`, `ConfigurableErrorRecorderFactory` | **Adopted** — `Span.ErrorMark` / `Span.ErrorMarkExclude` |
| SQL comment removal | `DefaultSqlNormalizer`, `DefaultJdbcOption` | **Adopted** — `SQL.RemoveComments` |
| Exception chain rate limiter | `ExceptionChainSampler` | **Adopted** — `Error.NewThroughput` |
| Percent sampling rate of zero | `PercentSamplerFactory.createSampler` | **Adopted** — see [below](#percent-rate-of-zero--adopted) |
| URL statistics send unit | `UriStatCollectingJob`, `AsyncQueueingUriStatStorage` | **Adopted** — see [below](#url-statistics-send-unit--adopted) |
| GC type and counts | `JvmGcType`, `GarbageCollectorMXBean` | **Diverges** — see [below](#gc-type-and-counts--diverges) |
| Exception chain during overflow | `AbstractRecorder.recordException`, `DefaultExceptionRecorder` | **Diverges** — see [below](#exception-chain-during-overflow--diverges) |
| Span event sequence reservation | `DefaultCallStack.push` | **Aligned with C++** — see [below](#span-event-sequence-reservation--aligned-with-c) |
| Inbound trace continuation | `DefaultTraceHeaderReader.read`, `RequestTraceReader` | **Adopted** — see [below](#inbound-trace-continuation--adopted) |
| Malformed inbound `Pinpoint-SpanID` | `DefaultTraceHeaderReader`, `NumberUtils.parseLong` | **Diverges** — see [below](#malformed-inbound-span-id--diverges) |
| Malformed inbound `Pinpoint-TraceID` | `DefaultTraceContext.createTraceId`, `TransactionIdUtils.parseTransactionId` | **Diverges** — see [below](#malformed-inbound-trace-id--diverges) |
| Order of the inbound header checks | `DefaultTraceHeaderReader.read` | **Same as Java** — `Pinpoint-Sampled: s0` is answered before the trace id and span id headers are looked at (`DefaultTraceHeaderReader.java:47-51`), so a peer that turned tracing off is obeyed even when its other headers are missing or broken |
| Malformed config value | `DefaultProfilerConfig.readInt` / `NumberUtils.parseInteger`, `ValueAnnotationProcessor` | **Aligned with C++** — a value that does not convert to its option's type is warned about and the option keeps its current value (`get_yaml<T>` in the C++ agent's `src/config.cpp`), where Java is split between a silent default fallback in `readInt`/`readLong` and a startup failure on an `@Value` injection |
| Span queue overflow policy | `SpanBatchGrpcDataSender` | **Same as Java** — a full send queue drops the oldest entry, as Java's default BATCH sender does (`queue.poll()` in `SpanBatchGrpcDataSender`); rejecting the newest is STREAM-mode-only behaviour, so head-drop is not a deviation |
| Locked parity invariants (11 groups) | `ParserContext`, `DefaultCallStack`, `GrpcSpanProcessorV2`, `Header`, `CountingSampler`, `UriStatHistogramBucket`, `BaseHistogramSchema`, `DefaultTransactionCounter`, `StringUtils`, `ClientOption` | **Verified identical** — see [below](#locked-parity-invariants--verified-identical) |

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

## Exception chain during overflow — diverges

**What differs.** When `SetError` is called after the call stack has
overflowed, Java still records the throwable's exception chain; this agent only
raises the transaction's failure flag and keeps no record of the exception
itself.

**Java.** `AbstractRecorder.recordException`
(`AbstractRecorder.java:62-64`) calls `recordDetailedException` *before*
anything else, and `WrappedSpanEventRecorder.recordDetailedException`
(`WrappedSpanEventRecorder.java:169-171`) forwards it to
`DefaultExceptionRecorder.recordException`
(`DefaultExceptionRecorder.java:73-83`), which pushes the throwable onto the
per-trace `ExceptionContext` and flushes it on `close()`. During overflow
`DefaultCallStack.newInstance` hands back a `DisableSpanEvent` that
`traceBlockEnd` discards, so what is lost is only what was written *onto that
event* — the `EXCEPTION_CHAIN_ID` annotation and the `setExceptionInfo` class
id and message. The chain leaves by the `ExceptionContext` path, which does not
go through the span event at all, and `recordError(ErrorCategory.EXCEPTION)`
marks the trace root as it would anywhere else.

**This agent.** `overflowSpanEvent.SetError` applies the `Span.IgnoreErrors`
filter and, if the error survives it, marks the root's `err` with the
exception cause, exactly as a recorded event would. No exception info, no
annotation, no chain entry. The C++ agent's `DisabledSpanEvent::SetError`
follows the same policy.

**Decision: deliberate simplification.** Overflow is a profiling depth limit,
not a judgement about the transaction, so the failure flag stays — but omitting
the detailed record past the depth limit is the consistent reading of that
limit. Exception chains are also the expensive kind of record: every entry
carries a full string call stack, and overflow is by definition the situation
where events are arriving faster than the agent chose to keep them.

**Revisit if** a real investigation is reported where this divergence got in
the way — a failure whose only exception detail was raised past the depth
limit.

---

## Span event sequence reservation — aligned with C++

**What is the same.** Every span event carries a `sequence` no other event of
that span carries. What differs is how the three ports get there: Java relies
on a single-threaded contract, the two ports make the counter itself atomic.

**Java.** `DefaultCallStack` is not synchronized and does not need to be. A
`Trace` belongs to one thread, so `push` can do `element.setSequence(sequence++)`
and set the depth from its own element count, both inside `push`, and the
numbering is trivially unique. An async trace gets a `CallStack` of its own.

**This agent.** A span here may legitimately be driven from several goroutines
of one call stack — a gRPC client stream runs on whatever goroutines the
application picks, `gocql` runs observers on speculative-execution goroutines,
`pgxpool` dials on a background one — so the contract Java leans on does not
hold and `span.reserveEventPosition` (`span.go`) claims the pair with
`eventSequence.Add(1)` and `eventDepth.Add(1)`, one atomic step each. The C++
agent's `Span::nextEventSequenceAndDepth` (`src/span.h`) is the same two
`fetch_add`s. `newSpanEvent` records what the reservation returned; nothing
reads the live counters to number an event.

Two details follow from the reservation being the numbering:

- The overflow decision is made on the reserved pair rather than on a load
  taken before the push, so it judges the position the event will actually
  carry. The predicate is unchanged (group 2 of the locked invariants). A
  refused event gives its depth back — `span.releaseEventPosition`, the
  equivalent of the C++ `finish()` decrementing the depth of an event it did
  not keep — because no `spanEvent` exists to release it in `end()`, and gives
  its sequence back too while it is still the last one handed out.
- `optimizeSpanEvents` sorts with `slices.SortStableFunc`. The chunk's depth
  compression and `startElapsed` deltas each read the event next to them, so
  should a tie ever appear the order they see is the order the events were
  recorded in rather than one that varies run to run.

**Locked by** `Test_span_NewSpanEvent_ConcurrentSequencesAreUnique`
(`span_test.go`): events opened and closed from many goroutines of one span
come out holding `0..N-1` with no number handed out twice.

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
Java's two options collapse into one here: `profiler.sql.error.enable` has no
counterpart, because `0` already means off. The count marks the transaction
under the `sql` cause, so `Span.ErrorMarkExclude: sql` keeps the counting without
the verdict — see [Error cause categories](#error-cause-categories--adopted).
See [Configuration](config.md#sqlerrorcount).

**Upgrade note.** This is on by default, matching Java. A transaction that runs
100 or more statements and did not previously fail will now be marked failed —
visible in the scatter chart and in the URL statistics' failed histogram. Set
`SQL.ErrorCount` to `0` to keep the previous behaviour.

---

## Error cause categories — adopted

**Java.** `err` on the wire is a bitmask of causes, not a flag.
`ErrorCategory` (`commons/.../trace/ErrorCategory.java`) defines
`UNKNOWN = 1 << 0`, `EXCEPTION = 1 << 1`, `HTTP_STATUS = 1 << 2` and
`SQL = 1 << 3`, and `ConfigurableErrorRecorder.recordError` ORs the recorded
category into the transaction's error code
(`traceRoot.getShared().maskErrorCode(errorCategory.getBitMask())`, an atomic
`x | mask` in `DefaultShared.java:69-72`). Which categories are allowed to
fail a transaction is configurable:
`ConfigurableErrorRecorderFactory.getEnabledTypes` reads
`profiler.error.mark` (unset means every category) and
`profiler.error.mark.exclude`, removes the second from the first and re-adds
`UNKNOWN`, and `recordError` then masks nothing for a category outside that
set. This is the **default** path — `profiler.error.enable` defaults to `true`
(`ErrorRecorderConfig.java`), so `ApplicationContextModuleFactory` loads the
`ConfigurableErrorRecorderModule`. The flat `maskErrorCode(1)` of
`SimpleErrorRecorder` is only reached with `profiler.error.enable=false`.
The recording sites are `AbstractRecorder.recordException` → `EXCEPTION`,
`HttpStatusCodeRecorder.record` → `HTTP_STATUS` and
`DefaultSqlCountService.recordSqlCount` → `SQL`.

**Go before this change.** Every failure stored a flat `1`, whatever caused
it — Java's `profiler.error.enable=false` behaviour, which is not Java's
default. The server could tell that a transaction had failed but not why, and
neither `profiler.error.mark` nor `profiler.error.mark.exclude` had any
counterpart, so a policy as ordinary as "a 5xx is not a transaction failure"
could not be expressed at all.

**Why adopt.** The bit values are a wire contract the collector already reads,
and sending `1` for a 5xx is not a smaller version of the contract but a
different statement about the transaction. The change is small where it
matters: `span.err` was already atomic, so accumulating causes is an atomic OR
(`atomic.Int32.Or`, the same operation as Java's `getAndUpdate(x -> x | mask)`),
and the four sites that used to store the flat flag now funnel through a single
`markSpanError`.

**This agent.** `ErrorCategory` is exported with Java's four bit values;
`span.markSpanError(category)` is the single point that writes `span.err`, ORs
the category into the **trace root** (rule 9 of
[API contracts](api_contracts.md#9-error-recording)) and skips a category the
operator disabled. The causes follow Java's recording sites: `SetError` on a
span or an event — including an overflowed event, a recovered panic and a SQL
driver error — is `exception`, a status in `Http.Server.StatusCodeErrors` is
`http-status`, the `SQL.ErrorCount` limit is `sql`, and a bare `SetFailure()`
is `unknown`. An excluded cause drops the verdict only: the annotation, the
exception info and the SQL counting all stay, and `err`, the scatter failure
point and the URL statistics failed histogram move together — including on
unsampled requests, whose URL statistics come from the same options.

**Option.** `Span.ErrorMark` and `Span.ErrorMarkExclude`, both unset by default, which
enables every cause as Java's unset `profiler.error.mark` does. See
[Configuration](config.md#spanerrormark).

**Upgrade note.** `err` values change for every failed transaction: a
transaction that failed on an exception now reports `2` rather than `1`, a
5xx reports `4`, the SQL count reports `8`, and a transaction with several
causes reports their OR. Anything reading `err == 1` should read `err != 0`
instead — which is what the server already does: the scatter chart compares
against `EXCEPTION_NONE` (`Dot.getStatus()`), and the transaction view resolves
the bits into the causes it displays (`ErrorCategoryResolver.resolve`, which
masks `UNKNOWN` out of that display). There is no option that restores
the flat `1`; Java has one (`profiler.error.enable=false`) and this agent
deliberately does not port the non-default path.

---

## Percent rate of zero — adopted

**Java.** `PercentSamplerFactory.parseSamplingRate`
(`PercentSamplerFactory.java:56-58`) truncates the configured rate to
hundredths of a percent — `(long)(rate * 100)` — and `createSampler`
(`PercentSamplerFactory.java:40-48`) picks one of three samplers from the
truncated value: `<= 0` gives `FalseSampler` (never sample), `>= 10000` gives
`TrueSampler` (always sample), anything between gives `PercentRateSampler`.
`PercentRateSampler` itself rejects both ends in its constructor
(`PercentRateSampler.java:38-41`), because the factory never sends them there.

| Configured | `(long)(v*100)` | Java |
|---|---|---|
| `-1` | -100 | never |
| `0` | 0 | never |
| `0.005` | 0 | never |
| `0.01` | 1 | 0.01% |
| `50` | 5000 | 50% |
| `100` | 10000 | always |
| `150` | 15000 | always |

**Go before this change.** `newPercentSampler` raised every non-negative rate
below `0.01` — including exactly `0` — up to `0.01`, so the configured "off"
still sampled one transaction in ten thousand. The `rate == 0` guard in
`isSampled` was unreachable dead code.

**Now.** The clamp is gone; only `< 0 -> 0` and `> 100 -> 100` remain, the
latter warned about as the C++ agent does. The
truncation in `newPercentSampler` is Java's `parseSamplingRate`, and the two
guards in `isSampled` — `rate == 0` and `rate >= 10000` — are its `FalseSampler`
and `TrueSampler`. All three branches were already there, zero just could not
reach them. A positive rate below `0.01` logs a warning on the way to "never
sample": Java does that silently, but a rate that looks enabled and collects
nothing is worth saying out loud. An explicit `0` is deliberate and stays quiet.

**Upgrade note — breaking.** `Sampling.PercentRate: 0`, and any positive rate
below `0.01`, now stops trace collection completely; it used to sample 0.01%. A
deployment that relied on that floor must set `0.01` explicitly.

---

## URL statistics send unit — adopted

**Java.** `UriStatCollectingJob.run` (`UriStatCollectingJob.java:49-61`) polls
`uriStatStorage` and stops at the first `null` — so it sends nothing at all
when nothing has been collected. What it polls is a completed-only queue:
`AsyncQueueingUriStatStorage.poll` delegates to `pollCompletedData`, which
returns `snapshotQueue.poll()` (`AsyncQueueingUriStatStorage.java:82-83,188-189`).
The tick still being collected is held by `snapshotManager` until
`checkAndFlushOldData` (`AsyncQueueingUriStatStorage.java:162-165`) moves it
onto that queue at a tick boundary. Completed → queue → send; the tick in
progress never leaves.

**Go before this change.** `takeSnapshot` swapped out the whole snapshot on
every send, tick in progress included, and `sendUrlStatWorker` sent the result
unconditionally. The tick interval (30s) and the send interval are free-running
against each other, so a tick was routinely cut wherever the send timer landed
and shipped as two `PAgentUriStat` messages. The collector aggregates by
`(uri, tick)` so the counts still add up, but the per-tick `max` and the average
implied by `total`/count are computed per message — a split tick reported those
for each half instead of for the tick. An agent serving no traffic still sent an
empty message every 30 seconds.

**Now.** `urlStats` keeps the tick in progress separate from a queue of closed
ticks, the way the C++ agent's `UrlStats::addLocked` does
(`src/url_stat.cpp:100-121`): the first entry of a strictly newer tick closes
the current one onto the queue. `takeSnapshot(false)` drains that queue,
`flushUrlStat` skips the send when the result is empty, and the queue is capped
at 4 closed ticks — matching Java's `snapshotQueue` capacity — dropping the
oldest with a rate-limited warning when the stat stream is not draining.

Entry arrival cannot be the only thing that closes a tick, because the last tick
of a burst has no newer entry coming. Java does not depend on one either: what
moves its tick onto the queue is `checkAndFlushOldData`
(`AsyncQueueingUriStatStorage.java:162-165`), reached at a tick boundary rather
than on a store. So `takeSnapshot(false)` also takes the tick in progress once
its own window has elapsed — the window is past, so nothing that can still
legitimately join the tick is coming, and taking it then is not the split the
arrival cut exists to avoid.

`Shutdown` calls `flushUrlStat(true)`, which takes the tick in progress whatever
its window: the stop cuts it short and no later send is coming, and shipping it
partial beats losing it. That is the one place a partial tick is sent.

---

## Malformed inbound span id — diverges

**Java.** `DefaultTraceHeaderReader` (`DefaultTraceHeaderReader.java:64-70`)
runs both span id headers through `NumberUtils.parseLong(str, SpanId.NULL)`, so
a value that will not parse becomes `SpanId.NULL` — `-1` (`SpanId.java:27`) —
and the span is recorded with that id.

**Go.** A present-but-unparseable `Pinpoint-SpanID` gets a **freshly generated
span id** (`span.go`, `Extract`), with a throttled warning naming the header.
`Pinpoint-pSpanID` does follow Java and falls back to `-1`, which is a real
value there: it means "this span is a root".

**Why.** A span id is the node's own identity in the trace; a parent span id is
a pointer to another node. Leaving every unparseable span id at `-1` gives every
such request the *same* identity, and the collector cannot tell them apart from
each other or from a genuine root — the whole set collapses onto one node in the
call tree. A generated id keeps them distinct and the surrounding trace intact;
only the link to this one hop is lost, which is the information the broken
header actually destroyed. The C++ agent makes the same choice, for the same
reason.

Note that this only covers a header whose **value** is broken. A header that is
**absent** is a different case: the request then does not continue a trace at
all — see [api_contracts.md](api_contracts.md).

---

## Malformed inbound trace id — diverges

**Java.** `DefaultTraceHeaderReader` (`DefaultTraceHeaderReader.java:55`) tests
`transactionId == null` and nothing else, so a present-but-unparseable value —
including an **empty string** — takes the continue path. The parse happens later,
in `DefaultTraceContext.createTraceId` (`DefaultTraceContext.java:227-231`) ->
`TransactionIdUtils.parseTransactionId`, which **throws**
`IllegalArgumentException("agentIndex not found:")` on a value with no `^`
separator (`TransactionIdUtils.java:84-90`).

**Go.** `continueHeaders` requires the trace id to parse. A blank or malformed
value is treated as no trace id at all: the request starts a new transaction,
with a throttled warning for a non-empty one.

**Why.** An exception on the request path is a worse answer than a new trace to
a header this agent did not write and cannot fix. The request is still served
and still traced; only its link to a trace that could not be identified is lost.
Sending it through the *continue* sampler instead would be worse still —
`isContinueSampled()` is unconditionally true, so any garbage `Pinpoint-TraceID`
would bypass the configured sampling rate.

---

## Inbound trace continuation — adopted

**Java.** `DefaultTraceHeaderReader.read` (`DefaultTraceHeaderReader.java:44-76`)
returns `ContinueTraceHeader` only when `Pinpoint-TraceID`, `Pinpoint-pSpanID`
and `Pinpoint-SpanID` are all present; any one missing returns
`NewTraceHeader`. `RequestTraceReader` (`RequestTraceReader.java:57-83`) turns
that one verdict into both the trace object (`continueTraceObject` vs
`newTraceObject`) and the sampler that is asked.

**Go before this change.** A parseable `Pinpoint-TraceID` alone was enough. A
peer that sent only the trace id got a continued trace: a non-root span pointing
at a parent that exists in no trace, or a default parent span id under a trace
with no node above it — and a continue-sampler slot spent on that hop.

**Now.** `continueHeaders` (`span.go`) requires all three, and both the sampler
choice (`agent.go`, `NewSpanTracerWithReader`) and `Extract` call it, so the two
cannot disagree about which trace a request belongs to. The check for the two
span id headers is presence-only, matching Java; `Pinpoint-Flags` is not part of
the decision and still defaults to `0`.

**Upgrade note — breaking.** A peer that sends only `Pinpoint-TraceID` now
starts a **new transaction** where it used to continue one. Calls from such a
peer appear **broken in two** in the distributed trace view; no data is lost,
but one trace becomes two. Fix it at the source: have the peer send
`Pinpoint-SpanID` and `Pinpoint-pSpanID` as well. This agent's `Inject()`
already writes all three, as does the C++ agent's `InjectContext`, so only
hand-rolled clients and header-stripping proxies are affected.

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

---

## Locked parity invariants — verified identical

Everything else in this file records a place where the three agents deliberately
differ. This section is the opposite list: values and algorithms that a
cross-agent review verified to be **identical** in the Java agent, the C++ agent
and the Go agent, and that are now pinned by an assertion suite in each port so
they cannot drift back apart unnoticed.

The suites are `test/test_java_parity_lock.cpp` (C++) and
`java_parity_lock_test.go` (Go). They are organised into the same eleven groups,
in the same order, as the table below. Where an older suite already covered a
group, the lock file cross-references it instead of duplicating it — the table's
"locked by" column names whichever file holds the assertions.

**Changing a locked value is a three-agent change.** If one of these assertions
fails, either the change is wrong, or all three implementations, this table and
both suites move in the same pull request. A locked value that has to differ
stops being locked: delete its row here, delete the assertion, and add a
divergence entry above saying why.

| # | Group | Java reference | What is locked | Locked by (C++) | Locked by (Go) |
|---|---|---|---|---|---|
| 1 | SQL normalization state machine | `commons-profiler` `sql/ParserContext.parse`, `DefaultSqlNormalizer` | `<n>#` / `<n>$` substitution drawing from **one shared index counter**; `,,` escaping of a comma inside a literal; `''` consuming no index; an unterminated literal emitting no placeholder; `#` not being a comment; `/*/`; `$`+digit staying an identifier; whitespace preserved; normalization not idempotent | `test_sql.cpp` (`SqlTest.JavaParityGoldenCases`, ported `JavaDefault*`) · `test_java_parity_lock.cpp` (`SqlNormalizer*`) | `sql_util_test.go` · `java_parity_lock_test.go` (`…SqlNormalizerGoldenCases`, `…SqlNormalizerSharedIndexCounter`, `…SqlNormalizerIsNotIdempotent`, `…SqlNormalizerWhitespaceIsNotNormalized`, `…SqlNormalizerRemoveComments`) |
| 2 | span event depth / sequence numbering | `DefaultCallStack.isOverflow`, `DefaultInstrumentConfig`, `pinpoint-root.config` | depth 64 / sequence 5000 / event chunk 20; deepest recorded level is `maxDepth + 1`; exactly `maxSequence` events recorded; `-1` means unlimited | `…SpanEventLimitDefaults`, `…SpanEventOverflowBoundaries` | `…SpanEventLimitDefaults`, `…SpanEventLimitFloors`, `…SpanEventOverflowDecision` |
| 3 | span chunk serialization | `context/compress/GrpcSpanProcessorV2` | `keyTime` — final chunk keys off the span's start time, a non-final chunk off its first event; `startElapsed` is the delta to the previous event (to `keyTime` for the first); the chunk is sorted by sequence before serialization; a non-final chunk carries the `endPoint` it was cut with | `test_span.cpp` (`SpanChunkOptimizeMultipleEventsTest`, `SpanChunkOptimizeNonFinalKeyTimeTest`, `SpanChunkEndPointSnapshotTest`) | `…ChunkKeyTimeAndStartElapsed`, `…ChunkSortsBySequence`, `…ChunkSnapshotsEndPoint` |
| 4 | async id / span id sentinels | `DefaultAsyncIdGenerator`, `bootstrap/context/SpanId.NULL` | async id `0` and span id `-1` are reserved for "absent"; a drawn id is redrawn until it is not the sentinel | `…AsyncIdSentinel` | `…Sentinels`, `…GeneratedSpanIdIsNeverTheSentinel` |
| 5 | propagation headers and transaction id | `Header`, `TransactionIdUtils`, `sampler/SamplingFlagUtils`, `AnnotationKey` | all ten `Pinpoint-*` header names; `agentId^startTime^sequence`; the agent-id character class; the parser stopping at the third delimiter; only the exact string `"s0"` disabling sampling; the annotation keys the agent emits (12 / 20 / 25 / 40 / 46 / 300 / −52) | `…PropagationHeaderNames`, `…AnnotationKeys`, `…TransactionIdFormat`, `…TransactionIdParsing`, `…SampledHeaderEncoding` | `…PropagationHeaderNames`, `…AnnotationKeys`, `…TransactionIdFormat`, `…TransactionIdParsing`, `…SampledHeaderEncoding` |
| 6 | sampling formulas | `sampler/CountingSampler`, `PercentRateSampler`, `PercentSamplerFactory` | counting tests the **pre-increment** value, so the first request of the process is sampled and every rate-th one after it; the percent admission window is `(0, rate]`; the percentage is multiplied by 100 and truncated; rate 0 / 1 / 100 are the False- and TrueSampler cases; a negative rate is clamped, never promoted to unsigned | `…CountingSamplerPhase`, `…CountingSamplerEdgeRates`, `…PercentSamplerWindow`, `…PercentSamplerEdgeRates` | `…CountingSamplerPhase`, `…CountingSamplerEdgeRates`, `…PercentSamplerWindow`, `…PercentSamplerRateTruncation` |
| 7 | URI histogram layout | `common/trace/UriStatHistogramBucket.Layout`, `AsyncQueueingUriStatStorage`, `URITemplate.NULL_URI` | the eight bucket bounds (100 / 300 / 500 / 1000 / 3000 / 5000 / 8000 / ∞); `bucketVersion = 0`; a 30s tick aligned to the epoch boundary; at most four completed snapshots; an all-zero histogram travels as an empty message while a single 0 ms sample does not; the no-URI stand-in key `/NULL` | `…UrlStatHistogramBuckets`, `…UrlStatWindow`, `…UrlStatUnknownKey`, `…UrlStatEmptyHistogram` | `…UrlStatHistogramBuckets`, `…UrlStatWindow`, `…UrlStatEmptyHistogram`, `…UrlStatUnknownKey` (skipped — see below) |
| 8 | active trace histogram layout | `common/trace/BaseHistogramSchema` NORMAL schema | the four slots at 1000 / 3000 / 5000 ms with an **inclusive** upper bound, so a span at exactly 1000 ms is still "fast" | `…ActiveTraceHistogram` | `…ActiveTraceHistogram` |
| 9 | transaction counters | `context/id/DefaultTransactionCounter` | all six counters (sampled/unsampled/skipped × new/continuation) exist and drain independently, and a drain resets them | `test_stat.cpp` (`SamplingCountersTest`, `AllCountersMixedIncrementTest`, `CollectResetsCountersBetweenCallsTest`) | `…TransactionCounters` |
| 10 | message truncation format | `StringUtils.abbreviate`, `AbstractRecorder.recordException` | a value within the cap is returned verbatim; a longer one keeps its first *n* bytes and gains a `...(original length)` suffix; the caps 256 (span / span event error) and 65536 (SQL metadata text); the cut lands on a UTF-8 boundary so the result stays valid for protobuf | `…TruncationFormat`, `…TruncationCutsOnAUtf8Boundary`, `…MessageLimits` | `…TruncationFormat`, `…TruncationCutsOnARuneBoundary`, `…MessageLimits` |
| 11 | gRPC channel constants | `grpc/.../client/config/ClientOption`, `GrpcTransportConfig`, `AgentInfoSender`, `pinpoint-root.config` | collector ports 9991 / 9992 / 9993; keepalive 30s / 60s without permit-without-stream; 4 MiB max message; connection and stream renewal off; AgentInfo refresh 24h with 3 tries per attempt; span batch 20 / 1000 ms / 500 ms / 10 concurrent; stat 5000 ms × 6; SQL cache limit 2048, expiry 168h, bind value 1024, error count 100 | `…CollectorPortDefaults`, `…GrpcChannelDefaults`, `…AgentInfoSchedule`, `…SpanBatchDefaults`, `…StatCollectionDefaults`, `…SqlCacheDefaults` | `…CollectorPortDefaults`, `…GrpcChannelDefaults`, `…ReconnectBackoff`, `…AgentInfoSchedule` |

### Deliberately not locked

These sit next to locked values and look like they belong in the table. They do
not, because the agents knowingly differ; each has its own entry above or in
`doc/config.md`.

- **AgentInfo send retry interval** — 3000 ms in both ports against Java's
  effective 300000 ms (`profiler.agentInfo.send.retry.interval`). Registration
  gates tracing in both ports, so it has to retry far more often. The 24h
  refresh and the 3 tries per attempt *are* locked; the retry interval is
  asserted at its port value with a comment pointing here.
- **Span batch size** — 20 in Java's shipped config and in the C++ agent, 50 in
  the Go agent.
- **Flow-control window, write buffer, max header list size** — Java pins them
  (`ClientOption`) and the Go agent follows; the C++ agent leaves them at the
  gRPC C-core defaults so the BDP estimator can tune the window.
- **Stat collect interval** — the locked 5000 ms is Java's *code* default
  (`DefaultMonitorConfig`); Java's release profile ships 10000 ms.
- **URL statistics send cadence** — Java polls on the stat scheduler (5–10s),
  both ports use a dedicated 30s timer.

### Skipped assertions

Two Go assertions are written but skipped, each naming the gap it waits on. They
are the fastest way to see whether a fix landed: delete the `t.Skip` line.

- `Test_javaParityLock_ChunkDepthCompression` — gap **S4**. Java
  (`GrpcSpanProcessorV2`) and the C++ agent seed the previous depth on the first
  event of a chunk; the Go agent does not, so the second event of every chunk is
  compared against 0 and never compressed. The wire bytes differ, the meaning
  does not.
- `Test_javaParityLock_UrlStatUnknownKey` — gap **U2**. Java's
  `URITemplate.NULL_URI` is `/NULL` and the C++ agent copies it verbatim; the Go
  agent writes `UNKNOWN_URL`, so a mixed deployment splits its "no URI recorded"
  traffic across two server-side keys.
