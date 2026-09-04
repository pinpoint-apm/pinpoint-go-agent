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
se.SetError(err)               // too late; may land on nothing
```

```go
// DO: fetch the recorder where it is used
tracer.NewSpanEvent("query")
tracer.SpanEvent().SetSQL(sql, args)
tracer.SpanEvent().SetError(err)
tracer.EndSpanEvent()
```

The same holds for `Annotation` handles from `Annotations()`.

**What happens on misuse:** with no active event, `SpanEvent()` warns
`abnormal span - has no event` and returns a no-op recorder, so calls on it are
silently dropped rather than crashing.

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
  overflowed events are dropped.
* `SpanRecorder.SetError()` is ignored.
* `NewGoroutineTracer()` returns `NoopTracer()`.
* `Inject()` **still writes** the distributed tracing headers. Overflow limits
  profiling detail; it is not a sampling decision. Dropping the headers would
  make the downstream node start a fresh transaction and cut the call chain, so
  the transaction stays intact and only the caller-side event link is lost.

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
* Annotate before the span or event ends. Rule 3 applies.

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

* `SpanRecorder.SetError(err)` marks the transaction failed.
  `SpanEventRecorder.SetError(err, errorName...)` marks one event failed **and
  the transaction with it** (`PSpan.err`, the URL stat failed histogram and the
  scatter failure point), as the Java agent does; the optional name groups
  errors in the UI and is subject to rule 8.
* A `nil` error is ignored by both, so the common
  `tracer.SpanEvent().SetError(err)` after a call needs no guard.
* `SetFailure()` marks failure without an error message — the right call for an
  HTTP status that counts as an error but carries no Go `error`.
* Call-stack capture (`Error.TraceCallStack`) works on errors that carry their
  own stack, i.e. those implementing `StackTrace() errors.StackTrace` such as
  `github.com/pkg/errors` errors. A plain `errors.New` error has no stack to
  record, and the option only costs the depth check for it.
* `Cause()` and `Unwrap()` (`fmt.Errorf("%w")`) chains are walked to build
  the exception chain, bounded at 64 links so a self-referential or cyclic
  user error cannot hang the request goroutine. As in the Java agent, every
  link is sent under one exception id with `exceptionDepth` 0 for the recorded
  error and 1..n down the chain, `exceptionClassName` set to the `SetError`
  name or the error's Go type name (e.g. `errors.withStack`), and `startTime`
  set to the failed span event's start time.

## 10. No-op and Unsampled Tracers Are Deliberately Silent

Three situations hand back a tracer that records nothing:

| Situation | Result |
|---|---|
| the transaction was not sampled | unsampled span; `IsSampled()` is false |
| no tracer in the context | `FromContext()` returns `NoopTracer()` |
| agent disabled, not yet created, or startup failed | `GetAgent()` returns `NoopAgent()`, whose tracers are no-ops |

Every method on these is safe to call and does nothing, which is the point:
instrumentation code needs no `nil` checks and no sampling branches.

```go
// This is correct and complete; no guard is needed.
tracer := pinpoint.FromContext(ctx)
defer tracer.NewSpanEvent("query").EndSpanEvent()
```

Two consequences worth knowing:

* An unsampled span still propagates the sampling decision on `Inject()`, so
  the downstream node does not sample the transaction back into existence.
* URL statistics are still collected for unsampled spans, which is why
  `Http.UrlStat.Enable` gives useful numbers at low sampling rates.

`IsSampled()` exists for the rare case where the instrumentation itself is
expensive — serializing a payload to annotate, for example. Use it to skip that
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
