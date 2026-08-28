# Mock-collector integration tests

This suite starts a real in-process gRPC collector on three OS-assigned
ephemeral ports and drives a real agent against it. It needs no running
Pinpoint Collector and no external network access. It is the Go counterpart of
the C++ agent's `test/it`.

The mock uses the generated `protobuf` service types and exposes the same
topology as production:

- Agent, Metadata and ProfilerCommandService on the agent port
- Span on the span port
- Stat on the stat port

Every received protobuf and its client metadata are copied into a thread-safe
`Snapshot`. The tests cover AgentInfo and ping, all metadata request types,
batched root/chunk/async spans, propagation and annotations, agent/URL
statistics, and profiler echo/active-thread commands.

`test/it` is its own Go module so it can depend on the `plugin/http` module
without adding that dependency to the agent module. Run it from this directory.

```bash
go test ./...            # full suite, ~1 minute
go test -short ./...     # skips the URL-statistics test's 30s tick
go test -race ./...      # clean: the agent races this suite used to surface are fixed
go test -run TestSendsAllMetadataAndCompleteSpanShapes -v
```

## Agent feature coverage

The suite exercises the SDK-facing features through a running agent and asserts
their collector wire representation:

- span lifecycle, nested event sequence/depth, spans finalized with unclosed
  events, event chunking, async/goroutine spans, `WrapGoroutine` context
  propagation, distributed-trace propagation, SQL/errors and every annotation
  shape the public API can record
- v1/v3 and v4 agent identity metadata across the Agent, Metadata, Span, Stat,
  ping and command channels, including v4 service-name propagation and API-key
  payload redaction
- exception metadata for errors captured on async spans, including the literal
  `NULL` URI-template fallback
- periodic agent statistics: sampling decisions, response time, CPU, memory,
  thread count and the active-request histogram
- URL-statistics aggregation, method prefixes, latency and failed-request
  histograms, and URL stats recorded for unsampled spans
- counter, percent and zero-rate sampling, upstream sampling decisions,
  unsampled `s0` downstream propagation, and new/continuation throughput
  limits including their transaction-stat counters
- SQL metadata through the real `database/sql` driver wrapper: UID and id
  modes, normalization, bind-value serialization, the `SQL.TraceBindValue`
  privacy gate, bind-value truncation at `SQL.MaxBindValueSize`, and
  transaction span events
- the `plugin/http` server and client helpers: remote-address resolution,
  recorded request/response headers and cookies, HTTP status handling, client
  endpoint serialization, the wrapped-handler path, and proxy
  address/header handling for the Apache, Nginx and App proxy headers
  (priority order, out-of-range timestamp rejection)
- profiler commands over the real bidirectional stream: echo, active-thread
  count, and the light-dump-then-targeted-dump flow a collector uses to drill
  into one in-flight request
- config-file watcher reloads that change sampling live without re-registering
  the agent
- the noop agent produced by `Enable: false`

## Failure injection

`MockCollector` also provides deterministic transport failure controls:

- `FailNext()` returns a selected gRPC status, immediately or after N stream
  messages.
- `TimeoutNext()` withholds a response until the client's deadline or
  cancellation.
- `RejectNext()` returns `codes.OK` with `PResult.success=false`.
- `StopEndpoint()` and `StartEndpoint()` close and rebind the Agent, Span or
  Stat listener on its original ephemeral port, exercising a real connection
  outage rather than only an RPC-level error.
- `BeginOutage()` and `EndOutage()` simulate a sustained collector failure:
  every subsequent RPC on all three endpoints keeps failing (default
  `Unavailable`) until the outage ends. The ports stay open, so the agent sees
  an unhealthy collector rather than a dead host, and every rejected attempt
  stays visible in the records.

Each handler completion is appended to `Snapshot.RpcResults`, so a test can
assert both the received protobuf and the injected result. The failure tests
cover metadata retries and cache release, command deadlines, failed span
batches, ping/command/stat stream reconnection, endpoint recovery, and bounded
shutdown while a span or stat request is stalled.

## Collector-outage scenarios

Four scenarios assert that the host application never degrades with the
collector:

- An agent started while the collector is unavailable keeps retrying
  registration, stays disabled, starts no downstream worker, and hands inert
  tracers to application requests; once the collector recovers it enables
  itself and every channel carries fresh work.
- A mid-flight outage leaves the agent enabled: application requests keep
  completing with real sampled spans while the span sender drains its queue
  into failing batches (recycling its concurrency permits) and the stat stream
  keeps reopening — after recovery spans, statistics and profiler commands all
  flow again.
- With a small `Span.QueueSize`, a span-endpoint connection outage shows that
  the bounded queue never blocks the application and that tracing resumes once
  the endpoint returns.
- `Shutdown()` stops tracing in bounded time, after which the application keeps
  running against inert tracers and nothing new reaches the collector; a second
  shutdown is a no-op.

## Known limitations

The suite no longer reports data races. It used to surface agent races between
`Shutdown()` and the worker goroutines (`statTicker`/`statDone`,
`urlStatTicker`/`urlStatDone`, `pingTicker`/`pingDone`), on `gAtcStreamCount`,
and on the `stats.go` globals reinitialized by `initStats()`; those are fixed in
the agent.

**Process-global agent state constrains the suite.** A Pinpoint agent is a
process-global singleton, so the tests run sequentially and each shuts its
agent down in `t.Cleanup`. Three consequences:

- `Agent.Shutdown()` only clears the global agent when the agent had been
  enabled, so a test that deliberately shuts down a never-registered agent
  would break every later test in the process. Those tests call `isolate(t)`,
  which re-runs the single test in a child process.
- The `plugin/http` package publishes its own config snapshot once per process,
  so every test here shares the HTTP settings from `defaultAgentConfig`.
  Changing them per test would silently have no effect.
- `asyncApiId` (the "Goroutine Invocation" API id) is a package global, so only
  the first agent in the process publishes it. The async tests therefore do not
  assert on that API metadata.

**Differences from the C++ suite.** Some C++ tests have no Go counterpart
because the feature does not exist here: the C API and fork scenarios, the
`Stat.Enable: false` gate, the active-thread-count stream limit, URL-stat path
trimming (the Go API takes an already-normalized URL template), and the
destructor-driven cleanup of an abandoned span. Where behavior differs, the Go
test asserts the Go behavior and says so in a comment — a malformed inbound
trace id starts a new transaction instead of dropping the request, `EndSpan`
discards events the application left open, and a metadata publication rejected
with `PResult.success=false` is not retried.
