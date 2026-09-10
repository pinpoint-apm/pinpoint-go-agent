# Pinpoint Go Agent — Tracer, Span, and Annotation Contracts

The public API is deliberately thin: `Tracer` is a handle onto a mutable call
stack, and the recorders it hands back (`SpanRecorder`, `SpanEventRecorder`,
`Annotation`) are views onto whatever is currently on that stack. That makes
the API cheap, and it means the rules below are the caller's to keep. Breaking
one does not panic — the agent logs a warning and drops or degrades the trace,
which is much harder to notice.

Read this alongside [Custom Instrumentation](instrument.md), which shows the
happy path, and [Configuration](config.md) for the limits referenced here.

Every rule below is enforced (or detected) in the agent, so the "what happens
on misuse" notes describe real behavior, not hypotheticals.

---

## 1. A Tracer Tracks One Goroutine

`Tracer` instruments a **single call stack**. Its event stack is not a
concurrent structure that arbitrary goroutines may push and pop; sharing one
tracer across goroutines interleaves their events and corrupts the call stack.

```go
// DON'T: two goroutines pushing events onto one tracer
go func() { defer tracer.NewSpanEvent("worker").EndSpanEvent(); work() }()
```

Create a tracer per goroutine instead:

```go
// DO: each goroutine gets its own tracer
go func(t pinpoint.Tracer) {
    defer t.EndSpan() // required
    defer t.NewSpanEvent("worker").EndSpanEvent()
    work()
}(tracer.NewGoroutineTracer())
```

Or let the wrapper do it, which is the recommended form because it also ends
the span for you:

```go
f := tracer.WrapGoroutine("worker", func(ctx context.Context) { work(ctx) }, ctx)
go f()
```

**What happens on misuse:** the agent records the goroutine id of the first
`NewSpanEvent()` call and, on a call from a different goroutine, warns
`span is shared by more than one goroutine` (throttled) and **still records the
event**, so the shape of the trace does not depend on whether the check ran.
The check runs at every log level; it is gated only on the goroutine id being
readable, an offset into the runtime's `g` struct resolved at startup
(`goIdOffset > 0` in `span.go`). When that resolution fails there is no
detection and a shared tracer degrades silently.

## 2. A Goroutine Tracer Needs an Active Span Event

`NewGoroutineTracer()` links the new call stack to the event that spawned it.
If no span event is on the stack there is nothing to link to.

```go
tracer := pinpoint.FromContext(r.Context())
// DON'T: no active event
go func(t pinpoint.Tracer) { ... }(tracer.NewGoroutineTracer())
```

```go
tracer := pinpoint.FromContext(r.Context())
defer tracer.NewSpanEvent("fanout").EndSpanEvent()
// DO: created under an active event
go func(t pinpoint.Tracer) { ... }(tracer.NewGoroutineTracer())
```

**What happens on misuse:** the agent warns `abnormal span - has no event` and
returns `NoopTracer()`. The goroutine runs correctly and records nothing.

## 3. End Exactly Once, and Record Before Ending

`Tracer.EndSpan()` finalizes the span, computes its elapsed time, enqueues its
final chunk and its URL statistics. Nothing recorded after it is sent.

```go
tracer := agent.NewSpanTracerWithReader("HTTP Server", r.URL.Path, pinpoint.HttpHeaderReader(r.Header))
defer tracer.EndSpan()          // exactly once, on every path

span := tracer.Span()
span.SetEndPoint(r.Host)        // record before EndSpan runs
span.Annotations().AppendInt(pinpoint.AnnotationHttpStatusCode, status)
```

`defer` is the only form that survives an early `return` or a panic. A second
call would double-count the response time, re-enqueue the URL stat and send a
second final chunk under the same span id, so the agent refuses it: it warns
`abnormal span - EndSpan already called` and returns.

For goroutine tracers the same rule applies to each tracer separately —
`WrapGoroutine()` is the exception, because the wrapper calls `EndSpan()` when
the wrapped function returns. Do not call it yourself on a wrapped tracer.

**What happens on misuse:** the late call is dropped, not applied. Every span
recorder setter — `SetError`, `SetFailure`, `SetServiceType`, `SetRpcName`,
`SetRemoteAddress`, `SetEndPoint`, `SetAcceptorHost`, `SetLogging`,
`Annotations` and `AddMetric` — returns without writing once `EndSpan()` has
run, and warns `abnormal span - <setter> called after EndSpan` at debug level;
the span event setters do the same after `EndSpanEvent()`. The lifecycle
calls — `NewSpanEvent`, `EndSpanEvent`, `Inject`, `NewAsyncSpan`,
`NewGoroutineTracer` and `WrapGoroutine` — are dropped too: `NewSpanEvent`
returns the tracer without recording an event, `Inject` writes no headers and
the async constructors return a no-op tracer, with a throttled warning
`abnormal span - <call> called after EndSpan`. Dropping is not just tidiness:
the final chunk is already on its way to the sender goroutine, so the write
could not be sent, and applying it would race the sender reading the same
field. An event created after the end would be worse still: once twenty of
them accumulated the agent would send a non-final chunk behind the final one,
which the collector protocol forbids.

## 4. End Span Events in Nesting (LIFO) Order

The event stack is a stack. `EndSpanEvent()` pops the innermost event, so
events must be closed in the reverse order they were opened.

```go
defer tracer.NewSpanEvent("outer").EndSpanEvent()
func() {
    defer tracer.NewSpanEvent("inner").EndSpanEvent()
    work()
}()
```

Pairing `NewSpanEvent` and `EndSpanEvent` in one `defer` statement makes
mis-nesting hard to write, and is why the examples in this documentation are
written that way.

**What happens on misuse:** events left open when `EndSpan()` runs are ended by
`EndSpan()` itself and still sent with the span; the agent warns
`abnormal span - N unclosed event(s) ended by EndSpan`. Their end time is when
`EndSpan()` ran, not when the work actually finished, so their durations are
wrong — but they are kept, because their sequence numbers were already handed
out and a span whose event sequence has holes makes the collector rebuild the
call tree against parents that never arrive. This follows the C++ agent; the
Java agent instead drops the whole span. An extra `EndSpanEvent()` on an empty
stack pops nothing and warns `abnormal span - has no event`.

A mis-nested `EndSpanEvent()` — one meant for `outer` while `inner` is still
open — is **not detected**: the call takes no target, so the agent ends `inner`
with `outer`'s end time and the stack is one event deeper than the caller
thinks. The next `EndSpanEvent()` ends `outer` and the trace looks plausible
with the durations shifted by one event. If you hold the recorder of the event
you are ending, use `pinpoint.EndSpanEventOf(tracer, se)` instead: it ends the
innermost event exactly like `EndSpanEvent()`, but when that event is not `se`
it warns `abnormal span - EndSpanEventOf ended <inner> instead of <outer>` with
a stack dump (throttled, one dump per interval). It does not unwind to `se`;
whatever is left open is ended by `EndSpan()` as above.

```go
tracer.NewSpanEvent("outer")
se := tracer.SpanEvent()
defer pinpoint.EndSpanEventOf(tracer, se)
```

## 5. Recorders Are Views, Not Owned Objects

`Tracer.Span()` returns a recorder for the span; `Tracer.SpanEvent()` returns a
recorder for the **innermost currently active** event. Neither is a value to
hold on to: after the event ends, a retained `SpanEventRecorder` writes into an
event that is already on its way to the collector.

```go
// DON'T: the handle outlives the event it points at
se := tracer.SpanEvent()
tracer.EndSpanEvent()
se.SetError(err)               // too late; dropped
```

```go
// DO: fetch the recorder where it is used
tracer.NewSpanEvent("query")
tracer.SpanEvent().SetSQL(sql, args)
tracer.SpanEvent().SetError(err)
tracer.EndSpanEvent()
```

The same holds for `Annotation` handles from `Annotations()`. A handle taken
while the span or event was live keeps pointing at the real collector, so it
cannot be turned into a no-op after the fact the way `Annotations()` itself is
— instead the collector is **sealed** when its owner ends, and every later
`Append*` on the retained handle is dropped.

```go
// DON'T: the handle outlives the event it points at
a := tracer.SpanEvent().Annotations()
tracer.EndSpanEvent()
a.AppendString(key, value)     // sealed; dropped
```

**What happens on misuse:** with no active event, `SpanEvent()` warns
`abnormal span - has no event` and returns a no-op recorder, so calls on it are
silently dropped rather than crashing. A retained handle used past the end
warns `abnormal span - annotation <key> appended after end` at debug level.
The span is sealed at the very end of `EndSpan()`, after its final chunk is
enqueued and its URL stat read, so nothing recorded on time is lost.

## 6. Event Depth and Count Limits (Overflow)

Two limits bound the size of a single span:

| Limit | Option | Default | Meaning |
|---|---|---|---|
| depth | `Span.MaxCallStackDepth` | 64 | max nesting of concurrently open events; one level deeper than the value is still recorded (65 at the default), as in Java's `DefaultCallStack` |
| sequence | `Span.MaxCallStackSequence` | 5000 | max total events in one span |

Both accept `-1` for unlimited; minimums are 2 and 4 respectively. Both are
[dynamic](config.md#dynamic-configuration).

Once either is exceeded the span **overflows**, and for the duration of the
overflow:

* `NewSpanEvent()` records nothing; it only counts the nesting so that the
  matching `EndSpanEvent()` unwinds correctly.
* `SpanEvent()` returns a no-op recorder, so annotations and SQL on the
  overflowed events are dropped; an error is not recorded on the event either,
  but it still marks the span failed (see the next bullet).
  `SetDestination()` is the one exception: the value is kept for `Inject()`
  (see below) and nothing else.
* `SpanEventRecorder.SetError()` records nothing on the event - no exception
  info, no annotation, no exception chain - but still marks the span failed
  under the exception cause (`PSpan.err`, URL stat, scatter), subject to
  `Span.IgnoreErrors` as usual.
  Overflow is a profiling limit, not a verdict on the transaction.
* `SpanRecorder.SetError()` is unaffected: the span level error is recorded as
  normal.
* `NewGoroutineTracer()` returns `NoopTracer()`.
* `Inject()` **still writes** the distributed tracing headers, `Pinpoint-Host`
  included. Overflow limits profiling detail; it is not a sampling decision.
  Dropping the headers would make the downstream node start a fresh
  transaction and cut the call chain, and dropping `Pinpoint-Host` would leave
  it unable to fill in `acceptorHost`, `endPoint` and `remoteAddr`. The
  transaction stays intact and only the caller-side event link is lost.

The span itself, its own annotations, and every event recorded before the
overflow are sent normally. The agent logs
`callStack maximum depth/sequence exceeded` **once per span** — a span that
overflows usually overflows thousands of times, and one line per event would
be its own outage.

Overflow is a symptom, not a tuning knob: a request that legitimately makes
5000 traced calls is usually a loop that should record one event around the
loop rather than one per iteration.

## 7. Annotation Rules

* Keys are the `Annotation*` constants (see
  [Annotations](instrument.md#annotations)), or your own key registered in the
  Pinpoint web's annotation key list. Unknown keys are transmitted but render
  as a bare number.
* Pick the `Append*` method that matches the value shape. There is no implicit
  conversion, and a mismatched shape renders as an empty or garbled annotation
  in the UI.
* `AppendBytesStringString()` **copies** the byte slice, so the caller may
  reuse or mutate the buffer immediately after the call. Every other
  `Append*` method takes values.
* Annotations are recorded on whatever the recorder points at — the span, or
  the innermost active event. Rule 5 applies.
* Annotate before the span or event ends. Rule 3 applies: after the end,
  `Annotations()` returns a no-op collector and a handle taken before it is
  sealed, so the late annotation is dropped either way.
* `SpanEventRecorder.SetSQL("", args)` is ignored: an empty statement records
  no SQL annotation and does not count toward `SQL.ErrorCount`. The
  `database/sql` wrapper relies on this for `Begin`, `Commit` and `Rollback`
  events, which reach `SetSQL` with no statement. See
  [Java parity](java_parity.md#empty-sql-statement--diverges).
* `SpanEventRecorder.SetSQL(sql, args)` bounds `args`, and only `args`. A
  caller that composes the bind value list itself gets it cut to roughly twice
  `SQL.MaxBindValueSize` — the room the agent's own driver wrappers need for
  the values they abbreviate and for their markers — with an
  `...(original byte length of args)` marker on the cut. The number is the
  length of the string passed in, not a bind value count and not one value's
  length, so it does not mean what the `...(n)` markers inside a list composed
  by the `database/sql` or pgx wrapper mean; those lists are within the bound
  by construction and are never cut here. `sql` itself is bounded elsewhere
  (`SQL.CacheLengthLimit`, the normalization cap), and the normalized
  parameters are never cut. See
  [Java parity](java_parity.md#setsql-bounds-a-caller-composed-bind-value-list--diverges).

## 8. Keep Operation and Error Names Low-Cardinality

Operation names (`NewSpanTracer`, `NewSpanEvent`) and error names are interned:
the agent assigns each distinct string an id and sends the string to the
collector once as API metadata. A name that varies per request turns that cache
into an unbounded map and floods the collector's API list, which is also what
makes the Pinpoint UI's call tree unreadable.

```go
// DON'T: a distinct API entry per user
tracer.NewSpanEvent("getUser:" + userId)
```

```go
// DO: fixed name, variable data as an annotation
tracer.NewSpanEvent("getUser")
tracer.SpanEvent().Annotations().AppendString(pinpoint.AnnotationArgs0, userId)
```

The same applies to the `rpcName` passed to `NewSpanTracer()`: pass the routed
URL **pattern** (`/users/{id}`), not the resolved path (`/users/1234`). The
framework plugins do this for you where the framework exposes the pattern.

## 9. Error Recording

* `SpanRecorder.SetError(err, errorName...)` marks the transaction failed.
  `SpanEventRecorder.SetError(err, errorName...)` marks one event failed **and
  the transaction with it** (`PSpan.err`, the URL stat failed histogram and the
  scatter failure point), as the Java agent does; the optional name groups
  errors in the UI and is subject to rule 8.
* `PSpan.err` is a **bitmask of causes**, not a flag: `ErrorCategoryUnknown`
  (1), `ErrorCategoryException` (2), `ErrorCategoryHttpStatus` (4) and
  `ErrorCategorySql` (8), OR-ed together as the Java agent's
  `Shared.maskErrorCode` accumulates them, so a request that threw and
  returned 5xx reports 6. Both `SetError` forms record the exception cause, a
  status in `Http.Server.StatusCodeErrors` records the http-status cause and
  the `SQL.ErrorCount` limit records the sql cause. Read a failure as
  `err != 0`, never `err == 1`. `Span.ErrorMark` and `Span.ErrorMarkExclude` decide
  which causes are allowed to fail a transaction at all; a disabled cause
  records nothing in `err`, in the URL statistics or in the scatter chart,
  while its annotation, exception info and SQL counting are unaffected.
* An error recorded on a goroutine or async tracer (`SetError`, `SetFailure`,
  an event `SetError`, the `SQL.ErrorCount` limit) fails the **root** span:
  an async span goes out as a `PSpanChunk`, which has no `err` field, so the
  flag is stored on the root the way Java's `ChildTrace` shares its parent's
  `TraceRoot`. Only the flag moves; the error message and exception chain stay
  on the tracer that recorded them. The root's final chunk goes out at the
  root's own `EndSpan()`, so a child that fails **after** the root ended is not
  reflected in `PSpan.err` or the URL stat. Java's ordinary trace behaves the
  same way: `DefaultTrace.close()` (`DefaultTrace.java:181-199`) calls
  `logSpan()` and stores the `PSpan` at the root's close, and that is what
  every normal entry point builds (`DefaultBaseTraceFactory.java:86,102,114`
  → `newDefaultTrace()` at `:191`). Java's deferred store exists only on the
  `AsyncDefaultTrace` path, whose `close()` awaits the last child through
  `SpanAsyncStateListener` (`AsyncDefaultTrace.java:24-31`); its entry points
  are `DefaultBaseTraceFactory.java:148,161`, both marked
  `@InterfaceAudience.LimitedPrivate("vert.x")`. Deferring the root store here
  would therefore be an extension past Java, not a parity fix. An **unsampled**
  transaction follows the same rule: its async children carry a link to the
  root, so the failure lands on the root's URL stat, as Java's
  `continueDisableAsyncContextTraceObject` hands the child the parent's
  `LocalTraceRoot` (`DefaultBaseTraceFactory.java:139-145`).
* A `nil` error is ignored by both, so the common
  `tracer.SpanEvent().SetError(err)` after a call needs no guard.
* `SetFailure(category...)` marks failure without an error message — the right
  call for an HTTP status that counts as an error but carries no Go `error`.
  The optional category is the cause recorded in `PSpan.err`, defaulting to
  `ErrorCategoryUnknown`; only the first one given is used.
* Call-stack capture (`Error.TraceCallStack`) prefers the stack the error
  carries itself, i.e. errors implementing `StackTrace() errors.StackTrace`
  such as `github.com/pkg/errors` errors. An error without one — a plain
  `errors.New` — is recorded with exactly `Error.CallStackDepth` frames
  captured at the `SetError` call site.
* `Cause()` and `Unwrap()` (`fmt.Errorf("%w")`) chains are walked to build
  the exception chain, bounded at 64 links so a self-referential or cyclic
  user error cannot hang the request goroutine. A multi-unwrap error
  (`errors.Join`, `Unwrap() []error`) contributes its **first element only**:
  the chain is a single line of causes, as Java's `getCause()` is. As in the
  Java agent, every link is sent under one exception id with `exceptionDepth`
  0 for the recorded error and 1..n down the chain, `exceptionClassName` set
  to the `SetError` name or the error's Go type name (e.g. `errors.withStack`),
  and `startTime` set to the failed span event's start time.

## 10. No-op and Unsampled Tracers Are Deliberately Silent

Several situations hand back a tracer that records nothing. They are **not
interchangeable**: an *unsampled span* stands for a real transaction that lost
the sampling decision, while a *no-op tracer* stands for no transaction at all.

| Situation | Result | Kind |
|---|---|---|
| the transaction was not sampled | unsampled span; `IsSampled()` is false | unsampled |
| an inbound request arrived with `Pinpoint-Sampled: s0` | unsampled span | unsampled |
| the URL or method is excluded from tracking | `NoopTracer()` | no-op |
| no tracer in the context | `FromContext()` returns `NoopTracer()` | no-op |
| agent disabled, not yet created, or startup failed | `GetAgent()` returns `NoopAgent()`, whose tracers are no-ops | no-op |

Every method on both is safe to call and does nothing, which is the point:
instrumentation code needs no `nil` checks and no sampling branches.

```go
// This is correct and complete; no guard is needed.
tracer := pinpoint.FromContext(ctx)
defer tracer.NewSpanEvent("query").EndSpanEvent()
```

### The two kinds differ on `Inject()`

`Pinpoint-Sampled: s0` is an instruction to the callee: *do not trace this
transaction*. Only a tracer that stands for a transaction may issue it.

| Kind | `Inject()` writes | Why |
|---|---|---|
| unsampled span | `Pinpoint-Sampled: s0` | the decision not to sample this transaction is real and must hold for the whole call tree, or the downstream node samples it back into existence |
| no-op tracer | **nothing** | there is no transaction and no decision; the callee is an entry point and stays free to start its own |

A call out of an excluded-URL handler, a batch job, or any code path whose
context never carried a tracer therefore looks to the callee exactly like a
call from an uninstrumented client, which is what it is. Suppressing tracing
across those services instead would be silent and hard to diagnose.

This matches Java, where `s0` is written only for a real trace created by
`disableSampling()`; with no trace object the interceptor returns before
writing any header.

An async or goroutine tracer forked from an unsampled span inherits the
unsampled marker, so calls made from that goroutine keep propagating `s0`. One
forked from a no-op tracer stays a no-op.

### `Inject()` omits a header it has no value for

The header set is not fixed. `Inject()` writes a header only when it has
something to put in it, matching Java's `DefaultRequestTraceWriter`, which
normalizes an empty value to `NOT_SET` and writes nothing:

| Header | Written when |
|---|---|
| `Pinpoint-TraceID`, `-SpanID`, `-pSpanID`, `-Flags`, `-pAppName`, `-pAppType` | always, on a sampled span |
| `Pinpoint-Sampled` | only by an unsampled span (`s0`) |
| `Pinpoint-pServiceName` | `Span.ServiceName` is set (v4 collectors) |
| `Pinpoint-Host` | a destination was recorded on the event, or - while overflowed - on the span |
| `Pinpoint-pAppNamespace` | never; this agent has no namespace to send |

An empty value is not a neutral one. A Java receiver configured with
`profiler.cluster.namespace` accepts a missing `Pinpoint-pAppNamespace` for
backward compatibility but rejects an empty one, and answers the mismatch by
starting a **new trace** - which cuts the call chain at the Go->Java hop.

A `DistributedTracingContextWriter` must therefore not assume every header
arrives on every call. Writers that append (`metadata.AppendToOutgoingContext`
in the gRPC plugin) or that write into a fresh carrier need no care. A writer
handed a carrier that may already hold headers from an earlier injection must
**clear the `Pinpoint-` keys first**: overwriting only what it is given leaves
the omitted ones behind to be read as this injection's own. The sarama
producer writer does this, because the retry pattern re-sends the same
message object.

### An inbound request continues a trace only with all three headers

`Extract()` treats a request as a continuation of an existing trace only when
**all three** of these are present:

| Header | Requirement |
|---|---|
| `Pinpoint-TraceID` | present **and parseable** as `agentId^startTime^sequence` |
| `Pinpoint-SpanID` | present (any value, including an empty one) |
| `Pinpoint-pSpanID` | present (any value, including an empty one) |

Anything else starts a **new transaction**: a fresh transaction id, a fresh span
id, `parentSpanId = -1`, and none of the other inbound `Pinpoint-` headers read.

This is Java's decision, taken in the same order
(`DefaultTraceHeaderReader.read`, `DefaultTraceHeaderReader.java:44-76`):

1. `Pinpoint-Sampled: s0` -> tracing disabled for this request, before anything
   else is looked at (`DefaultTraceHeaderReader.java:47-51`)
2. no `Pinpoint-TraceID` -> new trace
3. no `Pinpoint-pSpanID` -> new trace
4. no `Pinpoint-SpanID` -> new trace
5. otherwise -> continue, with `Pinpoint-Flags` defaulting to `0` when absent
   (`DefaultTraceHeaderReader.java:71-72`)

A trace id on its own names a transaction but not a position inside it.
Continuing on it alone records a non-root span whose parent is in no trace, and
spends a continue-sampler slot - `isContinueSampled()` is unconditionally true -
on a hop that does not exist. Header-stripping proxies, gateways and hand-rolled
clients produce exactly that shape.

The two span id headers are checked for **presence only**, as Java does: a value
that will not parse still describes a hop, and Java keeps it as `SpanId.NULL`
(`SpanId.java:27`) via `NumberUtils.parseLong`. Where this agent parses one of
them differently from Java, see [java_parity.md](java_parity.md).

A header **present with an empty value** is present. Java tests the header for
`null` alone (`DefaultTraceHeaderReader.java:55`) and the C++ agent decides on
`has_value()`, so both continue the trace across a proxy that blanks
`Pinpoint-SpanID` rather than dropping it.

The carrier answers that question. `DistributedTracingContextReader.Get` returns
**`(string, bool)`**: the value, and whether the carrier holds the key at all.
A header held with an empty value is `("", true)` and continues the trace; one
the carrier does not hold is `("", false)` and starts a new transaction.

| Carrier | Presence from |
|---|---|
| `net/http.Header`, via `pinpoint.HttpHeaderReader` | the header map |
| gRPC metadata (`plugin/grpc`) | `metadata.ValueFromIncomingContext` |
| fasthttp request header, via `ppfasthttp.HeaderReader` | `RequestHeader.PeekAll` |
| sarama record headers (`plugin/sarama`, `plugin/sarama-IBM`) | the header slice |
| kratos `transport.Header` (`plugin/kratos`, `plugin/kratosv3`) | **nothing** - value only |
| `noopDistributedTracingContextReader` | **nothing** - every key absent |

A carrier over a source that hands out a value and nothing else - kratos's
`transport.Header` is the one in this repo - reports what it has as present and
an empty value as absent (`v, v != ""`). Such a request starts a new
transaction, exactly as it did before `Get` reported presence. Presence can only
come from a source that has it.

`Pinpoint-TraceID` does not follow this: it must **parse**, so a blank trace id
starts a new transaction even from a carrier that reports it as present. It
names no transaction to continue - see
[java_parity.md](java_parity.md#malformed-inbound-trace-id--diverges).

#### Implementing a carrier

`Get` returning `(string, bool)` is a **breaking change**: a carrier written
against the old `Get(key string) string` no longer satisfies
`DistributedTracingContextReader` and fails to compile. Two shapes to update to:

```go
// A source that can report presence.
func (c myCarrier) Get(key string) (string, bool) {
    v, ok := c.header[key]
    return v, ok
}

// A source that hands out a value only: the pre-existing reading.
func (c myCarrier) Get(key string) (string, bool) {
    v := c.header.Get(key)
    return v, v != ""
}
```

`net/http.Header` is not a carrier itself any more - a stdlib type cannot carry
the second result - so wrap it in `pinpoint.HttpHeaderReader(req.Header)`. The
http plugin's `NewHttpServerTracer` already does.

The same decision drives **both** the sampler choice (`NewSpanTracerWithReader`)
and the context extraction (`Extract`), through one function, `continueHeaders`.
They must not be able to disagree: a request routed through the continue sampler
but extracted as a new transaction lets a peer bypass the configured sampling
rate.

### Statistics

Response times and URL statistics are collected for unsampled spans, which is
why `Http.UrlStat.Enable` gives useful numbers at low sampling rates. No-op
tracers collect nothing, so an excluded URL is absent from these statistics as
well as from traces.

`AddMetric(MetricURLStat, *UrlStatEntry)` may be called more than once on a
span. The `Url` is **first-wins**, as Java's `Shared.setUriTemplate`: once the
span holds a non-empty `Url`, later calls keep it and refresh only `Method` and
`Status`. An empty `Url` does not claim the slot, so a later real one still
fills it. `AddMetric(MetricURLStatForce, *UrlStatEntry)` replaces the `Url`
(Java's `setUriTemplate(value, force = true)`). The entry is copied; the span
neither keeps the caller's pointer nor writes into it. See
[java_parity.md](java_parity.md#uri-template-is-first-wins--same-as-java).

The no-op tracer is a **process-wide singleton**. Its methods only ever read
its fields; anything that writes per-request state must be gated on the span
being a per-request one, or concurrent handlers race.

`IsSampled()` exists for the rare case where the instrumentation itself is
expensive - serializing a payload to annotate, for example. Use it to skip that
work, not to decide whether to trace.

## 11. Context Carries a Tracer, Not a Span

`NewContext()`/`FromContext()` move a `Tracer` across API boundaries. Because
of rule 1, a context holding a tracer must not be handed to another goroutine
as-is; put a goroutine tracer in a fresh context instead, or use
`WrapGoroutine()`, which does exactly that.

```go
// DO: a new context carrying this goroutine's own tracer
ctx := pinpoint.NewContext(context.Background(), tracer.NewGoroutineTracer())
```

`RequestWithTracerContext()` and `TracerFromRequestContext()` are the
`*http.Request` equivalents.

## 12. Agent Lifecycle

* The agent is a **process-global singleton**. A second `NewAgent()` call
  returns the existing agent together with an `agent is already created` error,
  and closes the `Config` you passed if it is a different one. Check the error;
  do not assume a fresh agent.
* `NewAgent()` returns `NoopAgent()` **and** an error when a required identity
  value is missing or invalid. The application keeps running untraced, so the
  returned error is the only signal — log it.
* With `Enable=false`, `NewAgent()` returns `NoopAgent()` and a `nil` error.
  That is not a failure.
* `Shutdown()` stops the agent's goroutines; that agent never traces again.
  Tracers already in flight keep working as no-ops. To resume tracing, build a
  new agent with `NewAgent()` — no process restart needed. See
  [Troubleshooting](troubleshooting.md#stopping-and-resuming-the-agent).
* `GetAgent()` never returns `nil`; before creation it returns `NoopAgent()`.
  That is what makes `pinpoint.GetAgent().NewSpanTracer(...)` safe in library
  code that cannot know whether the application started an agent.

## 13. Server Metadata

* `WithServerInfo(info)` sets `PServerMetaData.serverInfo`, the same value as
  the `ServerInfo` config key. Empty means "not set" and sends the default
  `"Go Application"`.
* `WithServiceInfo(name, libs...)` **appends** one `PServiceInfo` entry. Call it
  once per group. The agent's own entry — the Go runtime and the build's module
  list — is always sent first; host entries follow in call order. There is no
  way to drop the agent's entry.
* Both are **startup-only**. The values are re-read on every agent information
  send, but there is no call that triggers a send: a change reaches the
  collector with the next `Collector.AgentInfo.RefreshInterval` cycle (24 hours
  by default) or never, when the refresh is disabled. See
  [Java Parity](java_parity.md#server-metadata-injection--aligned-with-c).
* Host strings are sanitized to valid UTF-8 like every other string the agent
  sends; a value with invalid bytes is sent with those bytes replaced, not
  rejected.

---

## Related Documentation

* [Quick Start](quick_start.md)
* [Custom Instrumentation](instrument.md)
* [Configuration](config.md)
* [Plugin User Guide](plugin_guide.md)
* [Troubleshooting](troubleshooting.md)
