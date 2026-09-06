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

**What happens on misuse:** at `Log.Level` `debug` or `trace` the agent records
the goroutine id of the first `NewSpanEvent()` call and warns
`span is shared by more than two goroutines` on a call from a different
goroutine, skipping the event. At `info` and above the check is off — it costs
a goroutine-id read per event — so a shared tracer degrades silently in
production. Run a new instrumentation once at `debug` before shipping it.

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
tracer := agent.NewSpanTracerWithReader("HTTP Server", r.URL.Path, r.Header)
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
stack pops nothing.

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
| depth | `Span.MaxCallStackDepth` | 64 | max nesting of concurrently open events |
| sequence | `Span.MaxCallStackSequence` | 5000 | max total events in one span |

Both accept `-1` for unlimited; minimums are 2 and 4 respectively. Both are
[dynamic](config.md#dynamic-configuration).

Once either is exceeded the span **overflows**, and for the duration of the
overflow:

* `NewSpanEvent()` records nothing; it only counts the nesting so that the
  matching `EndSpanEvent()` unwinds correctly.
* `SpanEvent()` returns a no-op recorder, so annotations, SQL and errors on the
  overflowed events are dropped. `SetDestination()` is the one exception: the
  value is kept for `Inject()` (see below) and nothing else.
* `SpanEventRecorder.SetError()` records nothing on the event - no exception
  info, no annotation, no exception chain - but still marks the span failed
  (`PSpan.err`, URL stat, scatter), subject to `Error.IgnoreErrors` as usual.
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
* An error recorded on a goroutine or async tracer (`SetError`, `SetFailure`,
  an event `SetError`, the `SQL.ErrorCount` limit) fails the **root** span:
  an async span goes out as a `PSpanChunk`, which has no `err` field, so the
  flag is stored on the root the way Java's `ChildTrace` shares its parent's
  `TraceRoot`. Only the flag moves; the error message and exception chain stay
  on the tracer that recorded them. This differs from Java in one respect:
  Java holds the root's span back until its last async child has ended, while
  this agent sends the root's final chunk at the root's own `EndSpan()`, so a
  child that fails **after** the root ended is not reflected in `PSpan.err`
  or the URL stat.
* A `nil` error is ignored by both, so the common
  `tracer.SpanEvent().SetError(err)` after a call needs no guard.
* `SetFailure()` marks failure without an error message — the right call for an
  HTTP status that counts as an error but carries no Go `error`.
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

### Statistics

Response times and URL statistics are collected for unsampled spans, which is
why `Http.UrlStat.Enable` gives useful numbers at low sampling rates. No-op
tracers collect nothing, so an excluded URL is absent from these statistics as
well as from traces.

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

---

## Related Documentation

* [Quick Start](quick_start.md)
* [Custom Instrumentation](instrument.md)
* [Configuration](config.md)
* [Plugin User Guide](plugin_guide.md)
* [Troubleshooting](troubleshooting.md)
