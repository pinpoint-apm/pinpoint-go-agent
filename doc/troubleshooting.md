# Pinpoint Go Agent - Troubleshooting Guide

This guide covers verifying that the agent started, turning it off, and the
issues that come up most often. For the option names referenced throughout, see
[Configuration](config.md); for the API rules whose violation causes broken
traces, see [Tracer, Span, and Annotation Contracts](api_contracts.md).

---

## Verifying Agent Startup

The agent writes its own operational logs (config, gRPC, goroutines, ...) to
**stdout** by default (before this release it was stderr). Startup is the fastest thing to check, and it is worth
checking first — a missing trace is far more often a missing agent than a
missing instrument.

Raise the level and watch the startup sequence:

```bash
PINPOINT_GO_LOG_LEVEL=debug ./your-app
```

Lines look like this (logrus text format, with the source module tagged):

```text
INFO[2026-09-03 11:36:00.000000] new pinpoint agent          module=pinpoint src=agent
INFO[2026-09-03 11:36:00.010000] connect to collector: dns:///my-collector:9991 (ssl: false)  module=pinpoint src=grpc
INFO[2026-09-03 11:36:00.120000] agent registered            module=pinpoint src=agent
INFO[2026-09-03 11:36:00.121000] start ping goroutine        module=pinpoint src=agent
INFO[2026-09-03 11:36:00.121000] start span batch goroutine  module=pinpoint src=agent
INFO[2026-09-03 11:36:00.121000] start send stats goroutine  module=pinpoint src=agent
```

The workers start only after `agent registered` — registration is the
precondition for tracing here. Until the collector accepts the AgentInfo,
`NewSpanTracer()` returns a no-op tracer and no stats are collected, so a blocked agent
port (9991) alone is enough to make a fully instrumented application report
nothing at all. The retry never gives up, so a collector that comes up later is
picked up without an application restart, and every 30 seconds of waiting the
agent repeats what that wait costs:

```text
INFO[2026-09-03 11:36:30.010000] still waiting for agent registration after 30000ms (collector unreachable or the send failed): tracing stays disabled (NewSpan is a noop and no stats are collected) until the collector accepts AgentInfo  module=pinpoint src=agent
```

The parenthesis separates the two cases: `collector unreachable or the send
failed` means the call itself failed - no connection, a timeout, or the
collector refusing the call with an error status (such as `InvalidArgument
desc = apiKey`, below); the `send agent info - ...` line before it says which.
`collector rejected the registration, likely permanent` means the call went
through and the collector answered no. A refusal of either kind usually waits
forever unless the configuration changes. Tracing does not start until
registration succeeds, which is why a blocked agent port shows up as "no data at
all" rather than as partial data.

Three lines tell you almost everything:

| Line | Means |
|---|---|
| `new pinpoint agent` | config validated, agent object built |
| `connect to collector: <addr> (ssl: <bool>)` | the address actually used, after all config sources resolved, as a gRPC target (`dns:///host:port`, or `passthrough:///host:port` with `Collector.Grpc.DnsResolverEnable: false`) |
| `agent registered` | the collector accepted this agent's identity |

`new pinpoint agent` is followed by the resolved configuration, one
`key = value` line per option, with sensitive values (such as `ApiKey`)
printed as `****`. That dump is the authoritative answer to "which config did
it actually use" — it reflects the full precedence order (command flag >
environment variable > config file > config function > default), so it settles
questions a config file alone cannot.

`src=` names the subsystem, which is what makes the logs greppable:
`agent` (lifecycle and goroutines), `grpc` (transport), `config` (resolution
and reloads), `span` (span lifecycle warnings), `stats`, `cmd` (profiler
commands and goroutine dumps).

**Always check the error from `NewAgent()`.** On invalid or missing identity
values it returns a no-op agent *and* an error; the application then runs
perfectly, untraced.

```go
agent, err := pinpoint.NewAgent(cfg)
if err != nil {
    log.Printf("pinpoint agent start failed: %v", err)
}
```

Note that `Enable=false` with a valid identity returns a no-op agent with a
`nil` error — that is a deliberate opt-out, not a failure. The identity is
checked first, so a missing `ApplicationName` or `ServiceName` is still
reported as an error.

### Routing the agent's own logs

`Log.Output` accepts `stderr`, `stdout` or a file path; a file path is rotated
at `Log.MaxSize` MB and `Log.MaxBackups` rotated files are kept. All three are
[dynamic](config.md#dynamic-configuration), so
you can turn debug logging on in a running process by editing the config file.

`Log.Output` and `Log.Level` are applied while the configuration is still being
loaded, in two passes: first from the command line and environment, then again
once the config file and profile have been read. The warnings the load itself
emits - a value of the wrong type, an unsupported `Sampling.Type`, an out of
range `SQL.CacheLengthLimit` - therefore reach the file you configured. Not
everything can:

* a command line flag that fails to parse (`command line flag <flag> needs a
  value ..., ignored`) and a `ConfigOption` value of the wrong type precede any
  logging configuration and go to stderr regardless;
* `config file loading error`, `config file <path> not found` and `config file
  doesn't have the profile` are logged before the file's own `Log.*` values
  are applied, so when
  `Log.Output` is set only in that config file they go to the output the
  command line and environment chose - `stdout` by default. Set it with
  `PINPOINT_GO_LOG_OUTPUT` or `--pinpoint-log-output` and these go to the file
  too.

A `NewConfig` call made while another agent is already running leaves that
agent's logging alone; its load messages go to the running agent's output.

To fold the agent's logs into your application's existing logrus setup, install
an extra logger — every agent log line is then also written there, with the
`module=pinpoint` and `src=` fields attached:

```go
pinpoint.SetExtraLogger(myLogrusLogger)
```

---

## Disabling the Agent

If the agent disrupts a production application, you can turn it off without
removing the instrumentation from your source.

### Config option

Set `Enable` to false and restart:

```bash
./your-app --pinpoint-enable=false
```
```bash
PINPOINT_GO_ENABLE=false ./your-app
```

`NewAgent()` then returns a no-op agent, and every instrument in your code
becomes a no-op call. See [Enable](config.md#enable).

### Sampling to zero

To keep the agent connected but stop starting new transactions, set the
sampling rate to zero instead. A request that continues a sampled trace from
an upstream agent is still traced; only `Sampling.ContinueThroughput` limits
those. Unlike `Enable`, `Sampling.PercentRate` is
[dynamic](config.md#dynamic-configuration), so this takes effect on a running
process as soon as the config file is saved:

```yaml
sampling:
  type: "PERCENT"
  percentRate: 0
```

URL statistics are still collected for unsampled requests when
[Http.UrlStat.Enable](config.md#httpurlstatenable) is on (it is off by
default), so basic throughput and latency numbers survive. This is the right first step when the concern is
trace volume rather than the agent itself.

---

## Stopping and Resuming the Agent

`Agent.Shutdown()` stops the agent's goroutines with no application restart.
After it returns, that agent never collects trace data again. To resume, build
a new agent with `NewAgent()` — again, no process restart.

```go
pinpoint.GetAgent().Shutdown()          // stop: that agent never traces again

c, _ := pinpoint.NewConfig(pinpoint.WithConfigFile("/etc/myapp/pinpoint-config.yaml"))
agent, err := pinpoint.NewAgent(c)      // resume: a fresh agent, same process
```

What to know about this pattern:

* The agent is a process-global singleton. Calling `NewAgent()` while one is
  live returns the **existing** agent plus an `agent is already created`
  error — you must `Shutdown()` first.
* A handle keeps the agent it was given. After a resume, a
  `defer agent.Shutdown()` and a `ShutdownOnSignal(agent)` watcher set up in
  `main()` still point at the stopped agent, so the new one is never flushed on
  exit. Shut down whichever agent is current, and re-arm the watcher:

  ```go
  defer func() { pinpoint.GetAgent().Shutdown() }()
  stop := pinpoint.ShutdownOnSignal(agent)
  defer func() { stop() }()

  // to resume:
  agent.Shutdown()
  stop()
  agent, err = pinpoint.NewAgent(c)
  stop = pinpoint.ShutdownOnSignal(agent)
  ```
* `Shutdown()` returns within milliseconds against a reachable collector and
  within about six seconds at worst: up to three seconds waiting for a config
  file reload callback in progress to finish, then up to three draining the
  worker goroutines. Spans and their queued metadata are drained within that
  same budget; metadata retries stop when shutdown begins. An exceeded worker
  deadline logs `shutdown timeout`, and any records left at that point may be lost.
* Call `defer agent.Shutdown()` in `main()` for normal exits. Spans are sent by
  a separate goroutine, so a process that exits immediately after the last
  request can drop the spans still in the queue. A `defer` does not run when
  the process is killed by a signal or calls `os.Exit()`; see
  [Spans Missing at Shutdown or on a Rollout](#spans-missing-at-shutdown-or-on-a-rollout).

---

## Common Issues

### Agent Not Starting

**Symptoms:** no `new pinpoint agent` line; `NewAgent()` returned an error; the
application is untraced.

| Cause | Check |
|---|---|
| `ApplicationName` not set | it is required; without it the agent cannot start |
| `ServiceName` not set | the default [Uid.Version](config.md#uidversion) v4 requires it (3 to 254 bytes); set it, or set `Uid.Version` to `v3` |
| Name/ID too long or has invalid characters | `ApplicationName` must match `[a-zA-Z0-9\._\-]+`; length limits depend on [Uid.Version](config.md#uidversion). An invalid `AgentName` does not stop the agent: it is replaced by `<host>-<pid>` with a warning |
| `Enable=false` left in a config file or the environment | there is no `new pinpoint agent` line and no config dump, and `NewAgent()` returns a `nil` error; check `PINPOINT_GO_ENABLE`, `--pinpoint-enable` and the config file's `enable` key |
| `ApiKey` has a control or non-ASCII character (v4) | it must be printable ASCII; surrounding whitespace is trimmed, as for the names |
| `Uid.Version` is `v4`, the default, against a 3.1.x collector | the agent starts, but the collector refuses every call unless `ApiKey` is set and matches `[a-zA-Z0-9\._\-]+`: `send agent info - rpc error: code = InvalidArgument desc = apiKey` repeats and registration waits forever; set `Uid.Version` to `v3` ([Uid.Version](config.md#uidversion)) |
| A second `NewAgent()` call | returns `agent is already created`; the first agent is still the live one |
| Config file not found or unparseable | look for the `src=config` lines: `config file <path> not found` (info) or `config file loading error` (error); the path is resolved as given, relative to the working directory |

The agent id is never configured; the agent always generates its own. An
invalid `ApplicationName` is fatal.

### No Data in the Pinpoint UI

Work down this list; it is ordered by how often each turns out to be the cause.

1. **Agent registered?** Without `agent registered` the problem is
   connectivity or identity, not instrumentation. See
   [Cannot Connect to Collector](#cannot-connect-to-collector).
2. **Collector older than 3.1.0?** `SendSpanBatch failed - N spans dropped:
   rpc error: code = Unimplemented ...` in the agent log means the collector
   does not implement the span sender. Upgrade the collector. The same line
   with another code is a failed send of another kind, such as `Unavailable`.
3. **Is anything instrumented?** Go is not auto-instrumented. A span only
   exists where your code created one — via a plugin wrapper or
   `NewSpanTracer()`. An agent can be perfectly healthy and report nothing.
4. **Sampling.** With `Sampling.Type: PERCENT`, a `Sampling.PercentRate` of 0
   starts no new transaction (one continued from a sampled upstream is still
   traced); with the default `COUNTER` type the rate is
   `Sampling.CounterRate` and `PercentRate` is ignored.
   `Sampling.NewThroughput` caps new transactions per second. Set
   `counterRate: 1` (or `percentRate: 100` with the `PERCENT` type) while
   diagnosing.
5. **Wrong application in the UI.** The resolved config dump shows the
   `ApplicationName` actually sent.
6. **Time skew.** Pinpoint indexes spans by timestamp. A host clock minutes off
   puts your traces outside the window you are looking at.
7. **Process exited too early, or started too early.** A short-lived program
   records nothing before the agent registers and loses what is queued without
   `Shutdown()`; see
   [Short-lived programs](getting_started.md#short-lived-programs). Only the
   last spans before a restart missing? See
   [Spans Missing at Shutdown or on a Rollout](#spans-missing-at-shutdown-or-on-a-rollout).

### Spans Missing at Shutdown or on a Rollout

**Symptoms:** the last requests before a deploy, scale-down or restart never
appear; the UI keeps listing an agent instance as alive long after its process
is gone; everything else is traced normally.

**Cause.** `Shutdown()` is the only path that sends what is still in the span
queue and reports the agent's end time to the collector. It runs only when
something calls it. `defer agent.Shutdown()` runs on a normal return from
`main()` and nowhere else:

* A **signal** with its default handling — `SIGTERM` from Kubernetes, Docker,
  systemd or `kill`, `SIGINT` from Ctrl-C — terminates the process at once. No
  deferred function runs. In a Kubernetes deployment this happens on every
  rollout, so the spans of the last few seconds of each pod are lost every
  time.
* **`os.Exit()`** (and `log.Fatal*`, which calls it) also skips every deferred
  function.

Go has no `atexit` and no runtime shutdown hook, so the agent cannot close
itself on the way out, and it deliberately does not install a signal handler by
default — see below for why.

**Fix, option 1 — opt in to the agent's signal watcher.** If your program does
not handle signals itself, let the agent do it:

```go
agent, err := pinpoint.NewAgent(cfg)
if err != nil {
    log.Printf("pinpoint agent start failed: %v", err)
}
defer agent.Shutdown()
stop := pinpoint.ShutdownOnSignal(agent) // SIGTERM and SIGINT; pass others to change
defer stop()
```

On one of the signals the watcher calls `Shutdown()`, which drains the queue
for at most three seconds (about six when a config file reload callback is in
flight, see above), then restores the default signal handling with
`signal.Stop` and re-raises the same signal. The process still dies of the
signal, with the same `128+signum` exit status it would have had, so
orchestrators and shell scripts see nothing different. The agent never calls
`os.Exit`. The returned `stop` function removes the watcher and ends its
goroutine; call it when the agent is no longer wanted. It is safe alongside
`defer agent.Shutdown()`: `Shutdown()` is idempotent. On Windows it watches
only SIGTERM by default: a process cannot re-raise a Ctrl-C there, so a
watched one would only stop the agent.

This is **off by default**, and must stay opt-in, because `signal.Notify`
changes process-wide state: it disables the default handling of every signal
it names. A library that did that silently would collide with a host that
already handles the same signals, and — if it consumed a `SIGTERM` without
re-raising it — would turn the program into one that ignores `SIGTERM` until
the orchestrator escalates to `SIGKILL`, at which point nothing runs at all.

**Fix, option 2 — handle the signal yourself.** If the program already has a
signal handler, just call `Shutdown()` from it; do not also use
`ShutdownOnSignal`, or the two will compete for the same signals:

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
defer stop()

srv := &http.Server{Addr: ":8000"}
go srv.ListenAndServe()

<-ctx.Done()                    // SIGTERM or Ctrl-C
srv.Shutdown(context.Background()) // finish in-flight requests first ...
agent.Shutdown()                   // ... then flush their spans
```

This is the better shape for an HTTP server: the server drains first, so the
spans of the requests that were in flight when the signal arrived are enqueued
before the agent flushes.

**What cannot be fixed: `os.Exit`.** Go has no `atexit`, so no library can run
anything when a program calls `os.Exit()` — directly, through `log.Fatal*`, or
through a framework that exits on error. If you opted in to `ShutdownOnSignal`
and still lose spans, look for an `os.Exit` on the exit path and call
`agent.Shutdown()` before it. The same applies to a `panic` that is not
recovered on another goroutine (a panic that unwinds `main` still runs its
deferred `Shutdown()`) and to `SIGKILL`, which no process can observe.

### Traces Are Incomplete or Look Wrong

Almost always an API contract violation. Run once with
`PINPOINT_GO_LOG_LEVEL=debug` and grep for `src=span` — the agent names each
case:

| Warning | Meaning | Fix |
|---|---|---|
| `abnormal span - EndSpan already called` | `EndSpan()` called twice | end it once, via `defer` |
| `abnormal span - N unclosed event(s) ended by EndSpan` | events still open at `EndSpan()` | pair each `NewSpanEvent` with `EndSpanEvent`, in one `defer` |
| `abnormal span - has no event` | recorder or goroutine tracer requested with no active event | open a span event first |
| `span is shared by more than one goroutine` | one tracer used from several goroutines | `NewGoroutineTracer()` or `WrapGoroutine()` per goroutine |
| `callStack maximum depth/sequence exceeded` | span overflowed | record fewer events (one per loop, not per iteration), or raise `Span.MaxCallStackDepth` / `Span.MaxCallStackSequence` |
| (no warning) transaction failed, but no event has an error | it ran `SQL.ErrorCount` statements (default 100); those recorded by pgx's native tracer and by gocql count too | an N+1 loop is what the mark surfaces; for batch work raise the option, or keep the count without the verdict with `Span.ErrorMarkExclude: sql` ([SQL.ErrorCount](config.md#sqlerrorcount)) |

The `called after EndSpan` warnings for setters are logged at `debug` level,
which is why this list is worth a debug run; the lifecycle ones (`NewSpanEvent`,
`EndSpanEvent`, `Inject`, `NewGoroutineTracer`) are WARN lines, throttled to one
a minute; the shared-tracer check runs at every level. See
[Contracts](api_contracts.md) for each rule in full.

### Missing Distributed Traces

A call chain that shows as separate transactions instead of one:

* **The caller must `Inject()`.** Outgoing requests only carry the trace if the
  client is instrumented — `pphttp.WrapClient()`, the gRPC plugin's
  interceptors, or a manual `tracer.Inject(req.Header)`.
* **A tracer must reach the client call.** The client wrapper pulls the tracer
  from the request context; a request built with `context.Background()` carries
  nothing. Use `req.WithContext(r.Context())` or
  `pinpoint.RequestWithTracerContext()`. In a framework handler, check which
  value carries the tracer:
  [Getting the tracer in a handler](plugin_guide.md#getting-the-tracer-in-a-handler).
* **The callee must extract.** Use `NewSpanTracerWithReader()`, or the server
  wrapper, which does it for you.
* **Both nodes must reach the same collector**, and both must appear in the UI
  individually before they can appear as one chain.
* **Proxies must preserve the headers.** All `Pinpoint-*` headers must survive;
  a gateway that strips unknown headers breaks the chain.
* **A goroutine boundary loses the tracer** unless it got its own goroutine
  tracer. This is the usual cause when the *first* hop traces fine and a
  background continuation does not.

### High Memory Usage

* **Span queue backing up.** If the collector is slow or unreachable, span
  chunks queue until `Span.QueueSize` (default 1024; a span is one chunk per
  `Span.EventChunkSize` events plus its final one); past that the oldest
  queued chunks are overwritten, and the agent logs `span drops: N in total, M
  overwritten by span queue overflow` at WARN, counting chunks. See
  [Collector Down or Slow](#collector-down-or-slow) for the whole bound. Lowering
  the queue bounds the memory; fixing the collector fixes the cause. The
  metadata queue is bounded separately by `Collector.Grpc.SenderQueueSize`
  (default 1000); while it is full, new metadata is left unregistered for
  that use (no id is spent) and the agent logs `meta queue overflow`.
* **High-cardinality names.** Operation and error names are interned in a
  cache of 1024 entries, so a name built from a request value keeps missing
  it: every miss mints a new id and sends the metadata again, flooding the
  collector's API list. See
  [contract 8](api_contracts.md#8-keep-operation-and-error-names-low-cardinality).
  The same applies to URL statistics: an un-templated URL per request is what
  `Http.UrlStat.LimitSize` exists to cap.
* **Bind values and raw SQL.** `SQL.TraceBindValue` with a large
  `SQL.MaxBindValueSize` keeps more per query event. `SQL.EnableRawSqlCache`
  caches normalized statements — useful for prepared statements, wasted memory
  if every query inlines distinct literals.
* **Leaked tracers.** A goroutine tracer whose `EndSpan()` never runs keeps its
  span alive. `WrapGoroutine()` cannot leak this way; hand-rolled goroutine
  tracers can. A span whose `EndSpan()` never runs also stays registered as an
  active request; once that registry is full (10240 entries) the agent logs
  `active span registry full ... spans may not be ended` (`src=stats`).

### High CPU Usage or Slow Responses

* **Sampling rate.** The default (`COUNTER`, `counterRate: 1`) traces every
  transaction. Raise `counterRate` (`100` is 1 in 100), or with
  `Sampling.Type: PERCENT` lower `percentRate` (`1` is 1%), or set `Sampling.NewThroughput` to cap
  transactions per second under load.
* **Debug logging.** `debug`/`trace` levels add per-event work. Never leave
  them on in production.
* **`Error.TraceCallStack`.** Call-stack capture and symbolization is the
  costliest thing the agent does per error. It is off by default; keep
  `Error.CallStackDepth` modest when you turn it on.
* **Over-instrumentation.** Events inside a hot loop cost more than they teach.
  Record one event around the loop.
* **`Collector.Grpc.SpanBatch*`.** For a very high span rate, tune `Collector.Grpc.SpanBatchSize` and
  `Collector.Grpc.SpanBatchMaxConcurrentRequests`.

### Collector Down or Slow

No request waits for the collector. `NewAgent()` returns before it connects,
and spans, metadata and statistics go into bounded queues that the agent's own
goroutines drain; a full queue drops instead of blocking. What an outage costs
the application:

* **Down from the start:** tracing waits for registration, so every tracer is
  a no-op - no cost, and no data.
* **Lost later:** requests are traced and queued as usual. The span queue holds
  `Span.QueueSize` chunks (1024), a span taking one per
  `Span.EventChunkSize` events (20) plus its final one, and then overwrites
  the oldest. Up to `Collector.Grpc.SpanBatchMaxConcurrentRequests` x
  `Collector.Grpc.SpanBatchSize` more chunks (10 x 50) are held by send
  requests, each for up to 5 seconds. That is the memory to plan for.
* **Slow:** the same, and a batch that finds no free request slot within
  `Collector.Grpc.SpanBatchFlushInterval` (1 s) is dropped with
  `SendSpanBatch skipped: N spans dropped`.
* `Shutdown()` stays within its bound whatever the collector does; see
  [Stopping and Resuming the Agent](#stopping-and-resuming-the-agent).

### Cannot Connect to Collector

**Symptoms:** no `agent registered`; repeated `src=grpc` errors.

* Verify `Collector.Host` and the **three** ports — agent (9991), span (9993)
  and stat (9992) are separate and all must be reachable. The
  `connect to collector: <addr>` line shows what the agent resolved.
* Test reachability from the application host, not from your workstation:

```bash
nc -vz your-collector-host 9991
```

* For TLS, `Collector.Grpc.SslEnable` must be on; the log line prints
  `(ssl: true)`. An empty `Collector.Grpc.TrustCertFilePath` falls back to the
  system root CAs, which is what you want for a publicly-signed certificate
  and not what you want for a private CA.
* Registration retries every `Collector.AgentInfo.SendRetryInterval` (3s by
  default, randomized +/-30%) until it succeeds or the agent shuts down, so a
  collector that comes up later is picked up without an application restart.
  While the collector is unreachable the wait for the connection to come back
  paces the loop instead, backing off to 30s. A collector that answers the
  registration with `success=false` (still initializing, briefly refusing) is
  retried the same way and logs `register agent - <message>, retrying`, at
  most once a minute; it is never treated as permanent.
* If the connection cannot even be set up (bad TLS material, unparsable
  address) the agent logs `failed to connect to collector, agent disabled` and
  releases itself, so `GetAgent()` returns the no-op agent and `NewAgent` can be
  called again after fixing the configuration. Releasing itself also hands back
  the config file watcher and closes whatever collector connections it had
  already opened, so a retry starts clean. Still call `Shutdown()` on the handle
  the failed `NewAgent` returned before retrying if you kept it: it is the
  supported way to finish with an agent, and it is safe here - the teardown runs
  once and repeats are no-ops.
* Check the collector's own logs. A version mismatch is rejected there, not
  here: the agent requires Pinpoint 3.1.0+.

### Collector Connection Dropped and Recovered

**Symptoms:** a gap in spans or stats that closed by itself; you need to know
when the connection was lost and when it came back.

Each collector channel that waits for a ready connection (`agent`, `ping`,
`stat`, `command`; the span sender does not) logs under
`src=grpc`. Each kind of line below is rate limited to one per channel per
minute, on its own window, so a flapping collector cannot flood the log; the
first READY, and the first recovery to READY in a window, are logged past the
limit, and a line that closes a quiet window carries the number of transitions
it folded:

| Line | Level | Means |
|---|---|---|
| `<channel> connection state <from> -> <to>` | INFO | an observed state change; `-> READY` is the moment the channel recovered |
| `<channel> connection ready again after <d>; lifetime: ...` | INFO | an outage closed, with running totals (`rotations` counts `Collector.Grpc.ConnectionMaxAge` rotations over every channel) |
| `<channel> connection not ready (state <s>): waited <d> so far` | WARN | the outage is ongoing and data is not being sent meanwhile |

The state is a sampled read, not a stream: `from` and `to` are consecutive
samples, and a first connect is not an outage and gets no summary. Besides
these, every connection attempt logs `wait <channel> connection ready - state:
<state>, timeout: <timeout>` at INFO without a limit, which shows the retry
loop is still running.

### Configuration Changes Not Taking Effect

* Only options marked **dynamic** reload from the config file; see the
  [dynamic options](config.md#dynamic-configuration).
  Everything else needs a restart.
* Reloads come from polling the config file once a second, so a change takes
  up to that long to apply, and they require the running process to have been
  given a config file (`ConfigFile`) — environment variables and command flags
  are read once at startup.
* Remember the precedence order. An environment variable overrides the config
  file, so editing the file will not change an option that is also set in the
  environment. The startup config dump shows the effective value.
* Watch for `src=config` lines on save; a parse error leaves the previous
  values in place.
* A process without a config file can still change the dynamic options at
  runtime: `pinpoint.ReloadConfig(yaml)` applies a complete configuration to
  the running agent and reports, in `RestartRequired`, the non-dynamic options
  it could not apply. See [Reloading from YAML](config.md#reloading-from-yaml).

---

## Getting Help

When reporting an issue, include:

1. Agent version (from `go.mod`), Go version, and OS.
2. Pinpoint collector/web version.
3. The startup log at `debug` level, including the resolved config dump — with
   `ApiKey` and hostnames redacted as needed.
4. A minimal instrumented handler that reproduces the problem.

Report bugs and ask questions on the
[GitHub repository](https://github.com/pinpoint-apm/pinpoint-go-agent/issues).
