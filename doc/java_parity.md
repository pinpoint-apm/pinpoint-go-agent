# Java Agent Feature Parity Decisions

The Java agent (`agent-module/profiler`) is the reference implementation, but
the Go agent does not follow it feature for feature: Go has no bytecode
instrumentation, no Guice module graph, and a much smaller config surface, so
some Java features cost far more here than they are worth. This file records
which Java-only features were reviewed, what was decided, and — for the ones
that were declined — what would make us revisit.

Add an entry when a Java feature is deliberately *not* ported. A feature that
is simply not written yet does not belong here.

**Cross-agent facts live here and nowhere else.** Statements of the form "Java
does X, C++ does Y, this agent does Z" go stale the moment one of the three
changes, and a stale one in a code comment is invisible until it misleads a
reviewer. Code comments state what *this* agent does and why; this file is the
one place that compares. The C++ agent keeps the same file at
`doc/java_parity.md` under the same rule, so the two read side by side.

---

## Summary

| Feature | Java reference | Decision |
|---|---|---|
| Per-URL sampler | `UrlTraceSampler`, `UrlSamplerConfig`, `TraceSamplerProvider` | **Declined** — see [below](#per-url-sampler--declined) |
| Tracing before agent registration | `AgentInfoSender`, `DefaultApplicationContext.start()` | **Declined** — see [below](#registration-before-tracing--declined) |
| SQL count per transaction | `DefaultSqlCountService` | **Adopted** — `SQL.ErrorCount` |
| Error cause categories in `err` | `ErrorCategory`, `ConfigurableErrorRecorder`, `ConfigurableErrorRecorderFactory` | **Adopted** — `Span.ErrorMark` / `Span.ErrorMarkExclude` |
| SQL comment removal | `DefaultSqlNormalizer`, `DefaultJdbcOption` | **Adopted** — `SQL.RemoveComments` |
| SQL cache size | `SimpleCacheFactory`, `profiler.jdbc.sqlcachesize` | **Adopted** — `SQL.CacheSize` sizes the SQL-ID, SQL-UID and raw SQL caches only; the api and error caches keep their fixed 1024, as Java's `newSimpleCache()` does. The C++ agent's key is `Sql.CacheSize`. |
| SQL normalization input cap | `SqlCacheService`, `profiler.jdbc.maxsqllength`; C++ `kMaxNormalizedSqlLength` | **Aligned with C++ (value), drop policy** — see [below](#sql-normalization-input-cap--aligned-with-c-value-drop-policy) |
| Exception chain rate limiter | `ExceptionChainSampler` | **Adopted** — `Error.NewThroughput` |
| Percent sampling rate of zero | `PercentSamplerFactory.createSampler` | **Adopted** — see [below](#percent-rate-of-zero--adopted) |
| URL statistics send unit | `UriStatCollectingJob`, `AsyncQueueingUriStatStorage` | **Adopted** — see [below](#url-statistics-send-unit--adopted) |
| Agent stat collection failure | `CollectJob.run()`, `StatMonitorJob.run()` | **Same as Java for the sample, exceeds Java for the scheduler** — see [below](#agent-stat-collection-failure-loses-one-sample--same-as-java) |
| GC type and counts | `JvmGcType`, `GarbageCollectorMXBean` | **Diverges** — see [below](#gc-type-and-counts--diverges) |
| Exception chain during overflow | `AbstractRecorder.recordException`, `DefaultExceptionRecorder` | **Diverges** — see [below](#exception-chain-during-overflow--diverges) |
| Span event sequence reservation | `DefaultCallStack.push` | **Aligned with C++** — see [below](#span-event-sequence-reservation--aligned-with-c) |
| Inbound trace continuation | `DefaultTraceHeaderReader.read`, `RequestTraceReader` | **Adopted** — see [below](#inbound-trace-continuation--adopted) |
| Proxy request headers | `DefaultProxyRequestRecorder`, `ApacheRequestParser`, `NginxRequestParser`, `AppRequestParser`, `UserRequestParser` | **Adopted** — see [below](#proxy-request-headers--adopted) |
| Acceptor host fallback | `ServerRequestRecorder.recordParentInfo` | **Adopted** — see [below](#acceptor-host-fallback--adopted) |
| Malformed inbound `Pinpoint-SpanID` | `DefaultTraceHeaderReader`, `NumberUtils.parseLong` | **Diverges** — see [below](#malformed-inbound-span-id--diverges) |
| Malformed inbound `Pinpoint-TraceID` | `DefaultTraceContext.createTraceId`, `TransactionIdUtils.parseTransactionId` | **Diverges** — see [below](#malformed-inbound-trace-id--diverges) |
| Order of the inbound header checks | `DefaultTraceHeaderReader.read` | **Same as Java** — `Pinpoint-Sampled: s0` is answered before the trace id and span id headers are looked at (`DefaultTraceHeaderReader.java:47-51`), so a peer that turned tracing off is obeyed even when its other headers are missing or broken |
| Malformed config value | `DefaultProfilerConfig.readInt` / `NumberUtils.parseInteger`, `ValueAnnotationProcessor` | **Aligned with C++** — a value that does not convert to its option's type is warned about and the option keeps its current value (`get_yaml<T>` in the C++ agent's `src/config.cpp`), where Java is split between a silent default fallback in `readInt`/`readLong` and a startup failure on an `@Value` injection |
| Retrying a rejected metadata send | `MetadataGrpcDataSender`, `RetryResponseStreamObserver` | **Diverges (aligned with C++)** — see [below](#retrying-a-rejected-metadata-send--diverges) |
| Span queue overflow policy | `SpanBatchGrpcDataSender` | **Same as Java** — a full send queue drops the oldest entry, as Java's default BATCH sender does (`queue.poll()` in `SpanBatchGrpcDataSender`); rejecting the newest is STREAM-mode-only behaviour, so head-drop is not a deviation |
| Stat queue overflow policy | `GrpcDataSender` (`StatGrpcDataSender`), `AsyncQueueingExecutor` | **Diverges (head-drop)** — see [below](#stat-queue-overflow-policy--diverges) |
| Metadata queue size | `GrpcTransportConfig`, `profiler.transport.grpc.metadata.sender.executor.queue.size` (1000) | **Adopted** — `Collector.Grpc.SenderQueueSize`, the C++ agent's key for the same queue, default 1000 as in both. The metadata queue used to borrow `Span.QueueSize`. The retry schedule keeps its own bound (`metaRetryQueueSize`, see [below](#retrying-a-rejected-metadata-send--diverges)) |
| URL stat input queue overflow policy | `AsyncQueueingUriStatStorage`, `AsyncQueueingExecutor` | **Diverges (head-drop)** — see [below](#url-stat-input-queue-overflow-policy--diverges) |
| Command channel RPC | `GrpcCommandService`, `SupportCommandCodeClientInterceptor`, `Header.SUPPORT_COMMAND_CODE` | **Aligned** — see [below](#command-channel-rpc--aligned) |
| Active trace registry cap | `DefaultActiveTraceRepository`, `DEFAULT_MAX_ACTIVE_TRACE_SIZE` (Caffeine `maximumSize`) | **Adopted, per shard** — see [below](#active-span-registry-cap--adopted-per-shard) |
| Automatic shutdown at process exit | `ShutdownHookRegister`, `DefaultAgent.close()` | **Diverges** — see [below](#automatic-shutdown-at-process-exit--diverges) |
| Worker lifecycle | `GrpcModuleLifeCycle`, `DefaultApplicationContext.start()/close()` | **Diverges (structure), same contract** — see [below](#worker-lifecycle--diverges-in-structure-same-contract) |
| Agent lifecycle phase | `DefaultAgent.start()/close()`; C++ `started_`/`shutting_down_`/`init_failed_` under `lifecycle_mutex_` | **Aligned with C++ (phases), diverges (one atomic, no mutex)** — see [below](#agent-lifecycle-phase--aligned-with-c-phases-one-atomic-instead-of-a-mutex) |
| gRPC channel arguments (flow control, write buffer, header list, connection renewal, idle timeout) | `ClientOption`, `DefaultChannelFactory.setupClientOption` | **Idle timeout disabled as in Java (value differs); the rest follow Java** — see [below](#grpc-channel-arguments--idle-timeout-disabled-as-in-java-the-rest-follow-java) |
| URI template recorded twice on one span | `DefaultShared.setUriTemplate`, `DefaultSpanRecorder.recordUriTemplate` | **Same as Java** — see [below](#uri-template-is-first-wins--same-as-java) |
| Locked parity invariants (11 groups) | `ParserContext`, `DefaultCallStack`, `GrpcSpanProcessorV2`, `Header`, `CountingSampler`, `UriStatHistogramBucket`, `BaseHistogramSchema`, `DefaultTransactionCounter`, `StringUtils`, `ClientOption` | **Verified identical** — see [below](#locked-parity-invariants--verified-identical) |

---

## Agent stat collection failure loses one sample — same as Java

**Java.** `CollectJob.run()` wraps the collection of one agent-stat snapshot in
`try/catch (Exception)`: a failure logs at WARN, skips that one snapshot, and
leaves the batch and the scheduler untouched. `StatMonitorJob.run()` then runs
its sub-jobs unprotected, so an exception escaping *there* cancels the
`scheduleAtFixedRate` task for the life of the process.

**This agent.** `collectAgentStatWorker` (`stats.go`) samples through
`agentStats.collect`, which recovers a panic in `getStats` the way `CollectJob`
catches: the panic costs exactly that tick's snapshot, the batch cursor is not
advanced, and the next tick fills the same slot. Failures are reported through a
throttled WARN (`collectFailures`, the `logThrottle` the malformed-header sites
use) rather than swallowed. `superviseWorker` stays as the backstop for a panic
outside that call, and that restart keeps the partial batch too: `collected` and
`batch` live on `agentStats`, and a restarted worker re-takes only the CPU/time
baseline (`resetBaseline`) while the first run cold-initializes (`init`). That is
the liveness edge this agent keeps over Java's `StatMonitorJob`. The C++ agent
applies the same policy (`AgentStats::runAgentStatsWorker`).

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

## SQL normalization input cap — aligned with C++ (value), drop policy

**Java.** `SqlCacheService` abbreviates the SQL text it publishes to
`profiler.jdbc.maxsqllength` (65536) *at cache time*, so no queued metadata item
carries more than 64KB of text. The cache *key* is the untruncated normalized
SQL (`DefaultCachingSqlNormalizer`), and normalization itself has no input cap.
Java's memory is bounded elsewhere: `UidCache` bypasses statements past
`profiler.jdbc.sqlcachelengthlimit`, and the id cache is a fixed-size LRU.

**C++.** `src/sql.cpp` cuts the normalization *input* at
`kMaxNormalizedSqlLength` (1 MiB, `src/sql.h`) and normalizes the rest; the
comment there calls it a "hard memory cap only, not the metadata cap". Cutting
inside a string literal drops the literal's closing quote, so the placeholder is
lost and the normalized text — hence the SQL id and SQL UID — differs from what
Java computes for the same statement (gap **N1** of the cross-agent review).

**Go before this change.** No input cap at all (gap **C1**). The 64KB
`maxSqlSize` bounded only the published text, as in Java, while the untruncated
normalized SQL was the key of `sqlCache`, `sqlUidCache` and `rawSqlCache` and
the `key` field of every queued `sqlMeta` / `sqlUidMeta`. One huge generated
statement broke the memory bound of every one of those.

**Decision.** `maxSqlNormalizeLength` = 1 MiB, the C++ value. A statement longer
than that is **dropped whole**: not counted toward `SQL.ErrorCount`, not
normalized, no SQL annotation, no metadata queued. `cacheSql` and `cacheSqlUid`
refuse a normalized key past the cap as well, since literal-heavy SQL normalizes
larger than it came in. The cut-and-normalize alternative was rejected because a
cut inside a literal makes the id / UID diverge from every other agent; dropping
never does — an over-cap statement simply has no SQL anywhere, which is also the
only outcome the server can aggregate consistently. There is no cut, so no
UTF-8 boundary is involved: a multibyte character straddling the cap puts the
statement past it.

The cap is deliberately far above `maxSqlSize`. The two are different things
and `Test_sqlNormalizer_NormalizesPastTheMetadataCap` locks the distinction: a
statement between 64KB and 1 MiB is still normalized whole and hashed to the UID
Java computes, and only its published text is abbreviated.

**Cross-agent contract.** The C++ agent's fix for gap N1 must use the same value
(1 MiB) and the same drop policy, so that an over-cap statement produces no SQL
id / UID in either agent rather than a different one in each.

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
partial beats losing it. That is the one place a partial tick is sent. The send
is deterministic, as it is in Java, where the scheduler runs the final
`UriStatCollectingJob` batch before the executor stops: `Shutdown` enqueues the
tick and then cancels the stop context, and `sendStatsWorker` drains whatever
the queue holds once the stop arrives instead of selecting between the two at
random - a plain two-way select dropped the last tick half the time.

**Send cadence.** `UriStatCollectingJob` has no timer of its own — it is a job
on the agent stat scheduler, so it polls the completed queue every
`profiler.jvm.stat.collect.interval` (5000 ms in code, 10000 ms in the release
profile), and `AsyncQueueingUriStatStorage`'s 2s queue-poll timeout closes a
trailing tick soon after its window ends. `sendUrlStatWorker` used to run a
fixed 30s ticker that was the only thing sending, so a tick closed at its
boundary waited up to 30s, and a trailing tick up to 30s to close plus the send.
Now `completeLocked` — the one place a tick lands on the completed queue —
signals a capacity-1 channel that the worker selects on beside its ticker, so a
completed tick is sent at once, and the ticker follows `Stat.CollectInterval`
rather than a second 30s constant. No separate key was added (the C++ agent
made the same choice and records the reasoning in its `doc/java_parity.md`,
"URL statistics send cadence"): with the wakeup in place the interval is not a
send cadence but the bound on closing the last tick of a quiet agent, and there
is no case for tuning that apart from the agent stat cadence — which is Java's
structure exactly. Java's 2s close is not matched separately: Java still sends
that tick on its next 5–10s scheduler run, while here close and send are one
event bounded by `Stat.CollectInterval` (5s default), so the defaults are equal
or better and a longer interval is the operator's choice for agent stats too.

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

## Proxy request headers — adopted

**Java.** `DefaultProxyRequestRecorder.record` runs every registered parser
over the request - `ApacheRequestParser` (`Pinpoint-ProxyApache`, type 3),
`NginxRequestParser` (`Pinpoint-ProxyNginx`, type 2), `AppRequestParser`
(`Pinpoint-ProxyApp`, type 1) and `UserRequestParser` (type 4, one instance
per header named in `profiler.proxy.user.header.names`, recording the header
name as the app) - and records a `PROXY_HTTP_HEADER` annotation for each result
whose `isValid()` holds. Every parser sets `valid` to false when `t=` is
missing or not positive. `NginxRequestParser.toReceivedTimeMillis` and
`toDurationTimeMicros` accept nginx's `$msec` / `$request_time` only in the
`sec.mmm` shape - a decimal point followed by exactly three digits - and yield
0 for anything else, including a value with no decimal point; the duration is
reported in microseconds. `AppRequestParser` runs the `app=` token through
`IdValidateUtils.validateId(app, 30)` and discards the header on failure.

**Go before this change.** `setProxyHeader` (`plugin/http/server.go`) was an
`if / else if` chain, so a request that crossed both an Apache and an nginx
proxy recorded only the Apache hop. There was no user parser and no
`Pinpoint-ProxyUser` support. nginx `D=` went through `strconv.Atoi`, which
fails on `0.123`, so the nginx proxy delay was always 0; nginx `t=` was a
`float64 * 1000`, which accepted values without a decimal point and could
round the millisecond. Nothing gated on `t=`: a header carrying only `D=`
recorded an annotation with `receivedTime` 0, drawn at the epoch. `app=` was
truncated to 32 runes and never checked for its character class.

**Now.** The four header kinds are independent `if`s, each appending its own
annotation. `Http.Server.ProxyUserHeaderNames` (the Java
`profiler.proxy.user.header.names` equivalent) names the user headers; each
one present is recorded as type 4 with the header name as the app. A header
whose `t=` is missing or not positive is dropped whole, so no annotation is
recorded for it. nginx `t=` and `D=` are read as `sec.mmm` digits into an
integer, exactly as Java does (`0.123` → 123000 µs; `0.1`, `123` and `0.1234`
→ 0). `app=` must pass `IsValidId(app, 30)` - the `[a-zA-Z0-9._-]` class and
at most 30 bytes - or the header is discarded. Apache `t=` (microseconds → ms)
and `D=` (microseconds) are unchanged.

**Upgrade note.** Requests behind more than one proxy now show every hop.
Proxy headers without a valid `t=`, an nginx `t=` without three decimals, and
an `app=` longer than 30 characters or outside the id character class are no
longer recorded at all, where they used to produce a zero or truncated
annotation. The C++ agent's `setProxyHeader` in `src/http.cpp` gets the same
treatment and the same configuration key.

---

## Acceptor host fallback — adopted

**Java.** `ServerRequestRecorder.recordParentInfo` records the
`Pinpoint-Host` header as the acceptor host and, when the header is absent,
falls back to `requestAdaptor.getAcceptorHost()` - the host the request
arrived on, which is also what it records as the end point.

**Go before this change.** `Extract` (`span.go`) set `acceptorHost` only when
the header was present. A continued trace whose caller did not send
`Pinpoint-Host` (a hand-rolled client, a header-stripping proxy) was sent with
an empty `acceptorHost` in its `PParentInfo`.

**Now.** `Extract` runs before the server plugins know the request host, so
the fallback lives in `SetEndPoint`: it fills `acceptorHost` with the end point
when nothing set it first. A `Pinpoint-Host` header and an explicit
`SetAcceptorHost` still win. Root spans are unaffected; they carry no
`PParentInfo` at all.

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

## Retrying a rejected metadata send — diverges

**Java.** `MetadataGrpcDataSender` sends each metadata item as a unary RPC and
hands every failure to `RetryResponseStreamObserver`. `onNext` treats a reply
with `PResult.success=false` exactly like a transport error: the send is
rescheduled on a `HashedWheelTimer` (`retryDelayMillis`, up to `maxAttempts`).
No thread waits for the retry, and there is no cap on how many sends are in
flight or waiting. Nothing is cached on the agent side for the retry to
invalidate, so a permanently rejecting collector costs Java one RPC per attempt
per item and nothing else.

**C++.** `GrpcMetadata::process_completed` (`src/grpc.cpp`) does *not* retry a
rejection. It is dropped like a non-retryable status, and the item's cache
entry is released through `schedule_cache_release`: parked in the time-ordered
`retry_queue` for one `meta_retry_delay` and released when it comes due. The
delay is the point — releasing inline made the very next span miss the cache,
register the same id and meet the same rejection, one round trip per span.
Transport failures go to the same `retry_queue`, which has its own bound
(`meta_retry_queue_size`, 1000) separate from the new-metadata queue and
head-drops when full; a retry never holds a permit while it waits.

**Go.** Same as C++. `metaResult` (`grpc.go`) turns `PResult.success=false`
into a `FailedPrecondition` error, which `metaVerdictOf` classifies as
`metaRejected`: no retry, and `sendMetaWorker` parks the item in
`agent.metaRetry` (`agent.go`) as a release-only entry, dropping the cache
entry one `metaRetryDelay` (1s) later. A transport failure with budget left
(`metaRetryLater`) parks in the same schedule and is re-sent from there; the
send goroutine returns its in-flight permit as soon as the attempt ends. The
schedule is bounded by `metaRetryQueueSize` (1000), separate from `metaChan`,
and a full schedule evicts its oldest entry and releases that entry's cache
slot — the same policy `tryEnqueueMeta` applies to `metaChan`. Exhausting the
attempt budget (`metaGiveUp`) releases the entry at once, as the C++ agent's
`retry_or_drop` does.

**Why diverge from Java.** A rejection is a verdict on the payload — the
collector read the bytes and said no — so resending the same bytes is load on
the collector for the same answer. Both ports drop it. The delayed release keeps
Java's recovery path (a later span registers the id again and sends a *new*
request, the only thing that can produce a different answer) while capping the
probe rate at one per delay per id.

**Why the schedule has its own budget.** The Go agent used to retry inside the
goroutine holding one of the `metaMaxConcurrentRequests` permits, waiting in
`backOffUntilReady` for the channel to recover. Under an outage four failed
sends pinned every permit, `sendMetaWorker` parked on the permit acquisition,
`metaChan` overflowed, its head-drop released the dropped item's cache entry,
and the next span registered the same item again — a drop-feeds-inflow loop
that lasted as long as the outage. Java never has this problem because its
timer holds no thread and its sender has no queue bound; the C++ agent avoids
it with two bounds (the `meta_retry_queue_size` comment in `src/grpc.h`
records the same loop). Two bounds is the Go answer as well.

**Locked by** `Test_sendMetaWorker_outageDoesNotAmplifyThroughCacheRelease`
(`grpc_scenario_test.go`): with the collector Unavailable throughout, every
queued item is attempted once, new metadata is not starved by the parked
retries, `metaChan` drops nothing, and only the entries the full schedule
evicted lose their cache slot. `Test_sendMetaWorker_releasesCacheOnCollectorRejection`
(`grpc_test.go`) locks the delayed release.

---

## Stat queue overflow policy — diverges

**Java.** `StatGrpcDataSender` hands each `PStatMessage` to an
`AsyncQueueingExecutor` whose bounded `LinkedBlockingQueue` rejects the
*newest* item when full (`offer()` fails, the item is counted and dropped);
what is already queued is never touched.

**C++.** The stat queue does not drop at all: `GrpcAgentStats` pushes onto a
bounded queue and a full queue blocks the producer.

**Go.** `enqueueStat` (`agent.go`) head-drops: a full `statChan` gives up its
oldest record and the freed slot goes to the new one, the same policy
`tryEnqueueMeta` applies to `metaChan` and the span queue applies to its
shards. An overflow costs exactly one record, as in Java; which record differs.
The eviction and the re-insert both use non-blocking selects, so a consumer
draining concurrently or a producer racing for the freed slot costs nothing
extra — the racing producer's loss is the new record, still one per overflow.
The drop is counted and reported at the enqueue site because the stat worker
is parked in `newStatStreamWithRetry` during a collector outage, exactly when
the drops happen.

**Why diverge.** Agent stat is a time series the collector plots by timestamp:
under a collector outage the oldest queued batch is the one that will be
stalest when the stream recovers, and the newest is the one a dashboard is
waiting on. Java's drop-newest is the default of its executor, not a decision
about stat data. Blocking as C++ does is not an option here because
`enqueueStat` runs on the stat collector's tick, and stalling it would stall
the url stat tick queued through the same call.

**Locked by** `Test_agent_enqueueStatOverflowLosesExactlyOneRecord`,
`Test_agent_enqueueStatCountsEveryDroppedRecord` and
`Test_agent_enqueueStatReturnsWhenDropRaceLeavesQueueEmpty` (`agent_test.go`).

## URL stat input queue overflow policy — diverges

**Java.** `AsyncQueueingUriStatStorage` stores each finished request's URL
record through an `AsyncQueueingExecutor`, whose bounded queue rejects the
*newest* record when full and leaves the queued ones alone.

**C++.** `UrlStat` (`src/url_stat.cpp`) does the same: a full per-thread queue
drops the record being added.

**Go.** `enqueueUrlStat` (`agent.go`) head-drops, exactly as `enqueueStat`
does — see [above](#stat-queue-overflow-policy--diverges) for the shape and
the race handling. This is the *input* queue between the request path and the
aggregator; the completed-tick queue that feeds the stat sender keeps its own
oldest-tick drop (`Test_urlStatCompletedQueueDropsTheOldestTickAtTheCap`), and
the per-snapshot pattern cap (`Http.UrlStat.LimitSize`) is a third, separate
bound.

**Why diverge.** Two queues in one agent with opposite overflow policies would
be a puzzle for whoever reads the drop warning; the stat queue head-drops, and
the two share `dropReporter` and its "oldest overwritten" wording. The loss is
one record per overflow, as in Java and C++; only which record differs, and for
a histogram sampled every tick the newest request is the one that still
belongs to the tick about to be sent.

**Locked by** `Test_agent_enqueueUrlStatOverflowLosesExactlyOneRecord`,
`Test_agent_enqueueUrlStatCountsEveryDroppedRecord`,
`Test_agent_enqueueUrlStatCountsDropsFromConcurrentProducers` and
`Test_agent_enqueueUrlStatReturnsWhenDropRaceLeavesQueueEmpty`
(`agent_test.go`).

## Command channel RPC — aligned

**Java.** `GrpcCommandService` (`profiler/receiver/grpc`) opens the command
stream with `ProfilerCommandServiceStub.handleCommandV2`. The commands the agent
serves travel as gRPC metadata, not as a message: `SupportCommandCodeClientInterceptor`
joins the codes with `Header.SUPPORT_COMMAND_CODE_DELIMITER` (`;`) into the
`supportCommandCode` header, and the collector's `handleCommandV2` registers the
connection from that header as soon as the stream is ready. The IDL marks
`HandleCommand` as `deprecated = true` (`proto/v1/Service.proto`), and the
collector's V1 handler **disconnects** a stream that carries the header.

**C++.** The same: `HandleCommandV2` with `support_command_code_header()`
(`src/grpc.cpp`), `kSupportedCommandCodes` kept in ascending order.

**This agent.** `newHandleCommandStream` (`grpc.go`) calls `HandleCommandV2`
on a context from `commandMetadataContext`, which is the usual agent header
set plus `supportcommandcode` — the key is lower case because grpc-go
lower-cases metadata keys, which is how the Java `Metadata.Key` reads it off
the wire anyway. The value is `supportedCommandCodes` joined with `;` in
ascending order: `710;730;740;750`, i.e. ECHO, ACTIVE_THREAD_COUNT,
ACTIVE_THREAD_DUMP and ACTIVE_THREAD_LIGHT_DUMP, the four commands
`serveCommandStream` (`command.go`) dispatches. No `PCmdServiceHandshake` is
sent and there is no V1 fallback: the RPC and the header were switched
together, because a V1 stream with the header is dropped and a V2 stream
without it is rejected with `INVALID_ARGUMENT`.

Registration therefore no longer depends on a first `Send` succeeding —
opening the stream is the registration. `serveCommandStream` goes straight to
`Recv` and a stream the collector closes at once is paced by the reconnect
back-off exactly as a failed handshake used to be.

**Locked by** `Test_supportCommandCodeHeader_format`,
`Test_commandMetadataContext_carriesSupportCommandCode` and
`Test_cmdGrpc_newHandleCommandStream_usesV2WithHeader` (`grpc_test.go`), and
end to end by `TestRegistersAgentAndMaintainsPingAndCommandStreams`
(`test/it/registration_test.go`), which reads the header off the mock
collector's V2 stream.

---

## Active span registry cap — adopted, per shard

**Java.** `DefaultActiveTraceRepository` keeps in-flight traces in a Caffeine
cache built with `maximumSize(DEFAULT_MAX_ACTIVE_TRACE_SIZE)` (`1024 * 10`).
When instrumentation forgets to end a trace the cache evicts entries instead of
growing, so an instrumentation bug costs histogram accuracy, not memory. The
size is a constant, not a configuration option.

**This agent.** `activeSpanRegistry` (`stats.go`) is 32 shards of
`map[int64]time.Time`, so a span that is never ended leaves a real entry
behind. `activeSpanMaxSize` (10240) is applied per shard as
`activeSpanShardMaxSize` (320): span ids are random, so the shards fill evenly
and the registry as a whole holds the Java figure, without a registry-wide lock
or counter on the store path. A `store` into a full shard first deletes one
existing entry — the one Go's randomized map iteration yields first, the same
arbitrary victim Java's approximate policy amounts to for a stream of one-shot
keys — so the span being registered is always present afterwards, and the
victim's later `remove` is a harmless delete of a missing key. Refusing the new
span instead would freeze the histogram on the leaked entries and hide every
live request. Each eviction is reported through a throttled WARN
(`activeSpanEvictLog`, the `logThrottle` the malformed-header sites use) naming
the registry size, the cap and the lifetime eviction count, so the operator can
suspect a missing `EndSpan`. Not configurable, as in Java.

**C++.** The C++ agent cannot evict: its registrations are intrusive nodes
owned by the span and merely linked into a shard list, so the registry
unlinking one would race the owner. It counts registrations and logs the same
rate-limited WARN past 10240 (`AgentStats::kActiveSpanWarnThreshold`), without
a cap — a leaked node there is memory the span already owns, so the leak is the
span's, not the registry's.

---

## Automatic shutdown at process exit — diverges

**Java.** `ShutdownHookRegister` installs a JVM shutdown hook that calls
`DefaultAgent.close()`, so the agent flushes its queues and reports its end
time however the JVM stops — a normal return, `System.exit`, or `SIGTERM`
(which the JVM turns into an orderly shutdown). A Java user never has to think
about it.

**Go.** There is no equivalent, and none is installed by default. `Shutdown()`
is the only path that sends the queued spans and the agent's end time, and it
runs only when called. The user's `defer agent.Shutdown()` covers a normal
return from `main()`; a signal with its default handling and `os.Exit` both
skip deferred functions, so a Kubernetes rollout — a `SIGTERM` — loses the
last spans of every pod unless the host arranges otherwise.

The agent offers `ShutdownOnSignal(agent, sigs...) (stop func())` as an
**opt-in, off by default** replacement: it calls `Shutdown()` on the given
signals (`SIGTERM` and `SIGINT` when none are given), restores the default
disposition with `signal.Stop` and re-raises the signal so the process still
exits with `128+signum`, and returns a function that removes the watcher. It
never calls `os.Exit`. Making it a `ConfigOption` was considered and rejected:
the config surface is also fed by files and environment variables, and
`signal.Notify` is process-wide state — it disables the default handling of
every signal it is given, so a value in a config file could silently change
how the whole program reacts to `SIGTERM`, collide with the host's own handler,
or (if the signal were consumed and not re-raised) leave a process that
ignores `SIGTERM` until `SIGKILL`. Signal ownership belongs to the host, so the
host has to call the helper in code. A host with its own handler should call
`Shutdown()` from it instead of using the helper.

`os.Exit` cannot be covered by any means: Go has no `atexit` and no runtime
exit hook. That limit is documented in `doc/troubleshooting.md`.

**C++.** The C++ agent (`pinpoint-cpp-agent-claude`) has the same gap and
reached the same conclusion — off by default, opt-in — with a different
mechanism and a different reason. Its opt-in is a `std::atexit` registration
flag on `AgentOptions` plus an RAII guard, and it installs no signal handler at
all, because the work a shutdown does (joining threads, tearing down gRPC) is
not async-signal-safe. Its reason for defaulting off is the embedded-library
contract described at `global_agent()` in its `src/agent.cpp`: joining threads
and tearing down gRPC during static destruction is not safe. Go has neither
problem — a goroutine reading from a `signal.Notify` channel is ordinary code,
and there is no static destruction — but has the signal-ownership problem
instead. Same policy, different grounds and different means: Go covers signals
but not `os.Exit`; C++ covers `exit()` but not signals.

**Revisit if** Go gains a runtime exit hook, or the collector starts inferring
an agent's end from the loss of its ping stream, which would remove the
"still alive in the UI" half of the loss.

---

## Worker lifecycle — diverges in structure, same contract

**Java.** `GrpcModuleLifeCycle` (`agent-module/profiler`, `context/module/`)
holds the collector-facing components — the agent/metadata/span/stat data
senders, the `ChannelFactory` instances, the executors and the command
service — as `Provider` fields, and `DefaultApplicationContext.start()` and
`close()` walk them in a fixed order. Every sender is a class with its own
thread or executor, and `close()` calls each one by name; there is no worker
count anywhere because there is nothing to count — the JVM's shutdown
hook (see [above](#automatic-shutdown-at-process-exit--diverges)) runs
`close()` and each component owns its own thread's join.

**Go.** The equivalent of that lifecycle is a set of goroutines under one
`sync.WaitGroup`. `connectGrpcServer` (`agent.go`) starts them once
registration has succeeded; `shutdownAgent` waits for them with a bounded
`waitTimeout(&agent.workerWg, shutdownTimeout)`. The workers are declared in
**one table**, `workerTable()`, whose entries carry the worker's name, its body
and a `when` predicate:

| Worker | `when` |
|---|---|
| `ping`, `command`, `meta`, `collect agent stat`, `collect uri stat`, `send uri stat`, `send stats` | always |
| `span batch` | `Span.Batch.Enable` is true |
| `span` | `Span.Batch.Enable` is false |
| `agent info refresh` | `Collector.AgentInfo.RefreshInterval` > 0 |

`startWorkers` iterates the table, increments `workerWg` by one immediately
before each `go superviseWorker(name, body)`, and `superviseWorker` releases
that slot with a deferred `Done` on its final exit. The count therefore matches
the goroutines by construction. Before the table, `connectGrpcServer` called
`workerWg.Add(8)` with a hand-counted literal followed by a list of `go`
statements and two branches — a normal change that added or made a worker
conditional could silently leave every `Shutdown` waiting out its whole
deadline (`Add` too large) or panic the `WaitGroup` (`Add` too small), and the
compiler catches neither. The C++ agent has the same list maintained by hand in
six places.

The `Add` stays on the `connectGrpcServer` goroutine rather than moving into
`superviseWorker`: `shutdownAgent` waits on `connectWg` before it waits on
`workerWg`, which is what guarantees every `Add` has happened before the `Wait`
begins. The names are unchanged — `doc/troubleshooting.md` and the
`start/restart <name> goroutine` log lines are keyed by them — and
`superviseWorker` (panic recovery, `workerRestartDelay`, the stop-signal and
`enable` checks, the restart log) is untouched.

The collector connections (`agentGrpc`, `spanGrpc`, `statGrpc`, `cmdGrpc`) are
**not** part of the table. They are Java's `ChannelFactory`/sender pairs, not
its threads: nothing counts them, they are closed by `closeGrpc` under a nil
guard on every path including a failed connect, and their close order in
`shutdownAgent` (`cmdGrpc` first, to end the command stream's listening state)
is a constraint on the teardown, not on the spawn. Folding them into a second
table would have widened this change past the worker start/stop code for no
correctness gain.

**Deadline overrun.** Java's `close()` joins each component by name, so a
stuck one is visible in a thread dump. A `WaitGroup` reports nothing — not even
how many goroutines remain — so `startWorkers` also allocates one atomic
running flag per started worker (`workerStates`), which `superviseWorker` sets
on entry and clears with a deferred store on its final exit. When
`waitTimeout` misses `shutdownTimeout`, the warning names them:
`shutdown timeout(3s) exceeded, abandon in-flight workers: send stats, agent
info refresh`. The in-time path logs nothing extra; the cost is two atomic
stores per worker lifetime and a linear scan of an under-ten-entry slice, so
there is no lock.

**Revisit if** a worker ever needs to start outside `connectGrpcServer` (a
lazily started worker, or one restarted by a config reload): the table's
invariant is that every `Add` precedes `connectWg.Done`, and such a worker
would need its own accounting and its own running flag.

---

## Agent lifecycle phase — aligned with C++ (phases), one atomic instead of a mutex

**Java.** `DefaultAgent` has no lifecycle state of its own: `start()` and
`close()` walk the components in order, and each component keeps whatever
flag it needs. There is nothing an operator can read to tell "still
registering" from "closed" except the log.

**C++.** `agent.h` keeps three atomics - `started_`, `shutting_down_` (never
cleared) and `init_failed_` - and a `lifecycle_mutex_` that `Start()` and
`do_shutdown()` take so a torn-down agent cannot be revived and a shutdown
racing a still-running `Start()` sees a consistent trio. `isExiting()` and
`initFailed()` expose two of the three.

**Go.** One value, `lifecycle` in `lifecycle.go`: an atomic `agentPhase` in
`registering`, `running`, `stopping`, `stopped` or `failed`. Before it the
same phases were the four combinations of two bools (`enable`, `shutdown`),
set from three places and read at some thirty, with "registering" only
expressible as `!enable && !shutdown`.

| C++ | Go phase |
|---|---|
| `!started_ && !init_failed_` | `registering` |
| `started_` | `running` |
| `shutting_down_` with workers still draining | `stopping` |
| `shutting_down_`, teardown done | `stopped` |
| `init_failed_` | `failed` |

The phase moves only through `transitionTo`, which applies a fixed forward
table (`validTransitions`) by CAS and refuses, with a warning, anything else -
this is what the C++ mutex buys for `Start()`-after-shutdown, without a lock.
The atomic rather than a mutex-guarded enum: `tracingEnabled` is read by
`NewSpanTracer` and every cache and enqueue on the request path, and by every
worker loop iteration; a mutex there would be a lock per span. Readers use
three named predicates - `tracingEnabled` (request path; also `Enable()`),
`workerContinues` (worker loops), `stopping` (registration and reconnect
loops) - each reproducing exactly the bool it replaced, including that the
request path still records during `stopping`, since the drain is meant to send
what it produces until the phase reaches `stopped`.

`failed` is kept as its own phase, as `init_failed_` is: `connectGrpcServer`'s
release defer already special-cased it (and it is the phase an operator has to
tell apart from `registering` - one is fixed by waiting, the other by fixing
the configuration). Every transition is logged at Info, so the phase history
of an agent that "does not send" is in the log; no method was added to the
`Agent` interface, since adding one breaks every external implementer.

The other lifecycle mechanisms are unchanged and deliberately not folded in:
`stopCtx` wakes goroutines blocked in a wait (a polled phase cannot),
`shutdownOnce`/`stopOnce` serialize, the two `WaitGroup`s join, and
`workerStates` names the workers still running at a deadline overrun.

---

## gRPC channel arguments — idle timeout disabled as in Java, the rest follow Java

Keepalive and the message-size limits are locked (group 11 below). The other
channel arguments `ClientOption` carries are compared here, knob by knob. The
HTTP/2 and grpc-go mechanics behind each choice (the two receive windows, why
BDP auto-tuning turns off, what the idle manager counts as activity) are
documented in `grpc.go` above `dialOptions`; this section holds only what the
other two agents do.

**Java.** `DefaultChannelFactory.setupClientOption` applies `ClientOption` to a
`NettyChannelBuilder`: `flowControlWindow(1 MiB)`, which sets both HTTP/2
receive windows and turns Netty's auto-tuning off; `maxInboundMetadataSize(8
KB)`; `WRITE_BUFFER_WATER_MARK` low / high (16 / 32 MiB); and
`idleTimeout(idleTimeoutMillis)` where `ClientOption.IDLE_TIMEOUT_MILLIS_DISABLE`
is 30 days. The constant is named as a disable sentinel and nothing in the
agent overrides it. Connection renewal
(`profiler.transport.grpc.loadbalancer.renew.period.millis`) is off by default.

**C++.** `make_channel_arguments` (`src/grpc.cpp`) leaves flow control, header
list size and write buffer at the gRPC C-core defaults so the BDP estimator can
size the window. It sets `GRPC_ARG_CLIENT_IDLE_TIMEOUT_MS` to `INT_MAX`, the
C-core "unlimited" value, because the C-core default is 30 minutes;
`Collector.Grpc.IdleTimeoutMs` (default 0 = disabled, minimum 1000 when set)
re-enables it.

**This agent.**

| Knob | Java | C++ (gRPC C-core) | Go (grpc-go v1.82.1) | Decision |
|---|---|---|---|---|
| Flow control window | fixed 1 MiB, auto-tuning off | unset: BDP probing on | fixed 1 MiB on both windows, BDP estimator off (`Collector.Grpc.FlowControlWindow`) | follows Java |
| Write buffer | Netty watermarks 16 / 32 MiB | unset: the C-core knob is a no-op without `GRPC_WRITE_BUFFER_HINT` | 1 MiB transport write buffer (`Collector.Grpc.WriteBufferSize`) | follows Java's intent; the knobs are not the same mechanism, so the value is not locked |
| Max header list size | 8 KB inbound | unset: default is already 8 KB soft / 16 KB hard | 8 KB inbound (`Collector.Grpc.MaxHeaderListSize`) | follows Java |
| Connection renewal | `loadbalancer.renew.period.millis`, off by default | `Collector.Grpc.ChannelMaxAgeMs`, off by default | `Collector.Grpc.ConnectionMaxAge`, off by default | same as Java, locked (group 11) |
| Idle timeout | 30 days (disabled) | `INT_MAX` (disabled) by default; `Collector.Grpc.IdleTimeoutMs` re-enables | `WithIdleTimeout(0)` (disabled) by default; `Collector.Grpc.IdleTimeout` re-enables | **disabled, as in Java**; the value is not locked |

All three agents disable the idle timeout. grpc-go's unset default is 30
minutes (`dialoptions.go` `defaultDialOptions`, v1.82.1), and `WithIdleTimeout`
documents zero as the disable value, so this agent passes 0 rather than
Java's 30 days: the decision is shared, the sentinel is runtime-specific. The
decision is not locked in `Test_javaParityLock_GrpcChannelDefaults` because
the three values differ (30 days / `INT_MAX` / 0); the C++ agent locks its
decision the same way, by its own value.

Why the default matters more here than in Java: this agent's channels are
quiet by nature (stat every 5 s on a long-lived stream, agent info every 24 h,
spans only with traffic), and in `Span.Batch.Enable` mode the span channel
carries unary RPCs only, so an application with no traffic reaches the
30-minute default on that channel. The trade-off considered and rejected: an
idle reconnect resolves DNS again through the `passthrough` scheme and would
pick up a moved collector, but `Collector.Grpc.ConnectionMaxAge` already
provides that while traffic flows and without dropping the keepalive pings in
between, so the DNS argument did not outweigh the lost keepalive. Disabling
idling does not by itself keep pings flowing on a connection with no open
stream, because `Collector.Grpc.KeepAlivePermitWithoutCalls` defaults to
false; that policy is unchanged and out of scope for this decision.

**Revisit if** Java stops disabling the idle timeout, or if grpc-go changes
its default or the meaning of zero — then the row moves into group 11 or gets
its own divergence entry.

## URI template is first-wins — same as Java

**Java.** `DefaultShared.setUriTemplate(uriTemplate)` is an atomic
`null -> value` compare-and-set: the first recorder to name the URI template
owns it, and later plain calls are ignored. `setUriTemplate(uriTemplate, true)`
(the `force` overload `DefaultSpanRecorder.recordUriTemplate` exposes) is a
plain set for the host that has to replace an early guess with the route it
eventually matched. The HTTP method and status code travel separately
(`setHttpMethod`, `HttpStatusCodeRecorder`) and are plain last-wins setters.

**This agent.** `span.collectUrlStat` and `noopSpan.collectUrlStat` used to
assign `span.urlStat = stat`, so the last `AddMetric(MetricURLStat, ...)` won
and a framework's matched route could be replaced by a later, less precise
layer. Both now go through `mergeUrlStat` and follow Java: once the span holds
a real `Url`, a later call keeps it and refreshes only `Method` and `Status`.
The `urlStatUnknown` stand-in written for an empty `Url` is Java's `null`, not a
value, so a later real `Url` still fills it in. `MetricURLStatForce` is the
`force = true` overload — a new metric key rather than a field on
`UrlStatEntry`, so the exported struct and existing callers are untouched. The
`Http.UrlStat.Enable` gate, the `warnIfFinished("AddMetric")` early return and
the unsampled span's `withStats` gate are unchanged.

The scope is the `Url` only, on purpose. One entry carries
`(Url, Method, Status)` behind a single pointer, so the alternative was making
the whole entry first-wins. That would have frozen the status code at whatever
the first caller passed — typically `0`, because the framework records the
route before the response exists — and departed from Java, where the status
code is the last recorder's. Keeping the `Url` first-wins and the rest
last-wins reproduces Java's per-field semantics inside the existing structure.
As a consequence the span no longer keeps the caller's pointer or writes the
stand-in into the caller's struct: the entry is copied on every call.

**C++.** The same policy, adopted first: `SpanImpl::recordUrlStat` keeps a
non-empty pattern and refreshes the method and status code, and `ForceUrlStat()`
/ `pt_span_force_url_stat()` is the force overload.

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
| 4 | async id / span id sentinels | `DefaultAsyncIdGenerator`, `bootstrap/context/SpanId.NULL` | async id `0` and span id `-1` are reserved for "absent"; a drawn id is redrawn until it is not the sentinel; a drawn span id spans the whole `int64` range, negatives included, as Java's `SpanId` does | `…AsyncIdSentinel` | `…Sentinels`, `…GeneratedSpanIdIsNeverTheSentinel`, `TestSpan_GeneratedSpanIdCoversFullInt64Range` (`span_test.go`) |
| 5 | propagation headers and transaction id | `Header`, `TransactionIdUtils`, `sampler/SamplingFlagUtils`, `AnnotationKey` | all ten `Pinpoint-*` header names; `agentId^startTime^sequence`; the agent-id character class; the parser stopping at the third delimiter; only the exact string `"s0"` disabling sampling; the annotation keys the agent emits (12 / 20 / 25 / 40 / 46 / 300 / −52) | `…PropagationHeaderNames`, `…AnnotationKeys`, `…TransactionIdFormat`, `…TransactionIdParsing`, `…SampledHeaderEncoding` | `…PropagationHeaderNames`, `…AnnotationKeys`, `…TransactionIdFormat`, `…TransactionIdParsing`, `…SampledHeaderEncoding` |
| 6 | sampling formulas | `sampler/CountingSampler`, `PercentRateSampler`, `PercentSamplerFactory` | counting tests the **pre-increment** value, so the first request of the process is sampled and every rate-th one after it; the percent admission window is `(0, rate]`; the percentage is multiplied by 100 and truncated; rate 0 / 1 / 100 are the False- and TrueSampler cases; a negative rate is clamped, never promoted to unsigned | `…CountingSamplerPhase`, `…CountingSamplerEdgeRates`, `…PercentSamplerWindow`, `…PercentSamplerEdgeRates` | `…CountingSamplerPhase`, `…CountingSamplerEdgeRates`, `…PercentSamplerWindow`, `…PercentSamplerRateTruncation` |
| 7 | URI histogram layout | `common/trace/UriStatHistogramBucket.Layout`, `AsyncQueueingUriStatStorage`, `URITemplate.NULL_URI` | the eight bucket bounds (100 / 300 / 500 / 1000 / 3000 / 5000 / 8000 / ∞); `bucketVersion = 0`; a 30s tick aligned to the epoch boundary; at most four completed snapshots; an all-zero histogram travels as an empty message while a single 0 ms sample does not; the no-URI stand-in key `/NULL` | `…UrlStatHistogramBuckets`, `…UrlStatWindow`, `…UrlStatUnknownKey`, `…UrlStatEmptyHistogram` | `…UrlStatHistogramBuckets`, `…UrlStatWindow`, `…UrlStatEmptyHistogram`, `…UrlStatUnknownKey` |
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
- **Flow-control window, write buffer, max header list size, idle timeout** —
  Java pins the first three (`ClientOption`) and the Go agent follows; the C++
  agent leaves them at the gRPC C-core defaults so the BDP estimator can tune
  the window. The idle timeout is disabled in all three, but by three different
  values (30 days / `INT_MAX` / 0), so the decision is shared and the value is
  not — see [gRPC channel arguments](#grpc-channel-arguments--idle-timeout-disabled-as-in-java-the-rest-follow-java).
- **Stat collect interval** — the locked 5000 ms is Java's *code* default
  (`DefaultMonitorConfig`); Java's release profile ships 10000 ms.
- **URL statistics send cadence** — not a constant of its own in any of the
  three. Java polls on the stat scheduler (5–10s); both ports now send a
  completed tick the moment it is closed and time their trailing-tick close by
  the stat collect interval (`Stat.CollectInterval` here, `Stat.BatchInterval`
  in C++) — see [URL statistics send unit](#url-statistics-send-unit--adopted).
