# Pinpoint Go Agent - Troubleshooting Guide

This guide covers verifying that the agent started, turning it off, and the
issues that come up most often. For the option names referenced throughout, see
[Configuration](config.md); for the API rules whose violation causes broken
traces, see [Tracer, Span, and Annotation Contracts](api_contracts.md).

---

## Verifying Agent Startup

The agent writes its own operational logs (config, gRPC, goroutines, ...) to
**stderr** by default. Startup is the fastest thing to check, and it is worth
checking first — a missing trace is far more often a missing agent than a
missing instrument.

Raise the level and watch the startup sequence:

```bash
PINPOINT_GO_LOG_LEVEL=debug ./your-app
```

Lines look like this (logrus text format, with the source module tagged):

```text
INFO[2026-09-03 11:36:00.000000] new pinpoint agent          module=pinpoint src=agent
INFO[2026-09-03 11:36:00.010000] connect to collector: my-collector:9991 (ssl: false)  module=pinpoint src=grpc
INFO[2026-09-03 11:36:00.011000] start ping goroutine        module=pinpoint src=agent
INFO[2026-09-03 11:36:00.011000] start span goroutine        module=pinpoint src=agent
INFO[2026-09-03 11:36:00.011000] start send stats goroutine  module=pinpoint src=agent
INFO[2026-09-03 11:36:00.120000] success to register agent   module=pinpoint src=agent
```

The workers start as soon as the channels are created; tracing does not wait
for registration. `success to register agent` may arrive later (or keep being
retried in the background if the collector's agent port is unreachable) while
spans and stats are already being sent.

Three lines tell you almost everything:

| Line | Means |
|---|---|
| `new pinpoint agent` | config validated, agent object built |
| `connect to collector: <addr> (ssl: <bool>)` | the address actually used, after all config sources resolved |
| `success to register agent` | the collector accepted this agent's identity |

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

Note that `Enable=false` returns a no-op agent with a `nil` error — that is a
deliberate opt-out, not a failure.

### Routing the agent's own logs

`Log.Output` accepts `stderr`, `stdout` or a file path; a file path is rotated
at `Log.MaxSize` MB. Both are [dynamic](config.md#dynamic-configuration), so
you can turn debug logging on in a running process by editing the config file.

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

To keep the agent connected but stop collecting transactions, set the sampling
rate to zero instead. Unlike `Enable`, `Sampling.CounterRate` is
[dynamic](config.md#dynamic-configuration), so this takes effect on a running
process as soon as the config file is saved:

```yaml
sampling:
  type: "COUNTER"
  counterRate: 0
```

URL statistics are still collected for unsampled requests, so basic throughput
and latency numbers survive. This is the right first step when the concern is
trace volume rather than the agent itself.

---

## Stopping and Resuming the Agent

`Agent.Shutdown()` stops the agent's goroutines with no application restart.
After it returns, that agent never collects trace data again. To resume, build
a new agent with `NewAgent()` — again, no process restart.

```go
func newAgent(w http.ResponseWriter, r *http.Request) {
    opts := []pinpoint.ConfigOption{
        pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
    }
    c, _ := pinpoint.NewConfig(opts...)
    _, err := pinpoint.NewAgent(c)
    if err == nil {
        io.WriteString(w, "New Pinpoint Go Agent - success")
    } else {
        io.WriteString(w, "New Pinpoint Go Agent - fail")
    }
}

func shutdown(w http.ResponseWriter, r *http.Request) {
    pinpoint.GetAgent().Shutdown()
    io.WriteString(w, "Shutdown Pinpoint Go Agent")
}

func main() {
    ...

    http.HandleFunc("/newagent", newAgent)
    http.HandleFunc("/shutdown", shutdown)
    http.HandleFunc("/handler", pphttp.WrapHandlerFunc(handler))
    http.ListenAndServe(":8000", nil)
}
```

Two things to know about this pattern:

* The agent is a process-global singleton. Calling `NewAgent()` while one is
  live returns the **existing** agent plus an `agent is already created`
  error — you must `Shutdown()` first.
* Call `defer agent.Shutdown()` in `main()` for normal exits. Spans are sent by
  a separate goroutine, so a process that exits immediately after the last
  request can drop the spans still in the queue.

---

## Common Issues

### Agent Not Starting

**Symptoms:** no `new pinpoint agent` line; `NewAgent()` returned an error; the
application is untraced.

| Cause | Check |
|---|---|
| `ApplicationName` not set | it is required; without it the agent cannot start |
| Name/ID too long or has invalid characters | `ApplicationName`, `AgentId`, `AgentName` must match `[a-zA-Z0-9\._\-]+`; length limits depend on [Uid.Version](config.md#uidversion) |
| `Enable=false` left in a config file or the environment | the resolved config dump shows `Enable = false` |
| `Uid.Version` is `v4` | v4 is not accepted by any released collector yet; use `v1` or `v3` |
| A second `NewAgent()` call | returns `agent is already created`; the first agent is still the live one |
| Config file not found or unparseable | look for `src=config` errors; the path is resolved as given, relative to the working directory |

An oversized or malformed `AgentId` is *not* fatal on its own — the agent
generates one instead. An invalid `ApplicationName` is.

### No Data in the Pinpoint UI

Work down this list; it is ordered by how often each turns out to be the cause.

1. **Agent registered?** Without `success to register agent` the problem is
   connectivity or identity, not instrumentation. See
   [Cannot Connect to Collector](#cannot-connect-to-collector).
2. **Is anything instrumented?** Go is not auto-instrumented. A span only
   exists where your code created one — via a plugin wrapper or
   `NewSpanTracer()`. An agent can be perfectly healthy and report nothing.
3. **Sampling.** `Sampling.CounterRate` of 0 collects nothing;
   `Sampling.NewThroughput` caps new transactions per second. Set
   `counterRate: 1` while diagnosing.
4. **Wrong application in the UI.** The resolved config dump shows the
   `ApplicationName` actually sent.
5. **Time skew.** Pinpoint indexes spans by timestamp. A host clock minutes off
   puts your traces outside the window you are looking at.
6. **Process exited too early.** Short-lived programs need
   `defer agent.Shutdown()`; see above.

### Traces Are Incomplete or Look Wrong

Almost always an API contract violation. Run once with
`PINPOINT_GO_LOG_LEVEL=debug` and grep for `src=span` — the agent names each
case:

| Warning | Meaning | Fix |
|---|---|---|
| `abnormal span - EndSpan already called` | `EndSpan()` called twice | end it once, via `defer` |
| `abnormal span - has unclosed event` | events still open at `EndSpan()` | pair each `NewSpanEvent` with `EndSpanEvent`, in one `defer` |
| `abnormal span - has no event` | recorder or goroutine tracer requested with no active event | open a span event first |
| `span is shared by more than two goroutines` | one tracer used from several goroutines | `NewGoroutineTracer()` or `WrapGoroutine()` per goroutine |
| `callStack maximum depth/sequence exceeded` | span overflowed | record fewer events (one per loop, not per iteration), or raise `Span.MaxCallStackDepth` / `Span.MaxCallStackSequence` |

Note that the shared-tracer check only runs at `debug` and `trace` levels, so a
concurrency mistake is invisible in a production log. See
[Contracts](api_contracts.md) for each rule in full.

### Missing Distributed Traces

A call chain that shows as separate transactions instead of one:

* **The caller must `Inject()`.** Outgoing requests only carry the trace if the
  client is instrumented — `pphttp.WrapClient()`, the gRPC plugin's
  interceptors, or a manual `tracer.Inject(req.Header)`.
* **A tracer must reach the client call.** The client wrapper pulls the tracer
  from the request context; a request built with `context.Background()` carries
  nothing. Use `req.WithContext(r.Context())` or
  `pinpoint.RequestWithTracerContext()`.
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

* **Span queue backing up.** If the collector is slow or unreachable, spans
  queue until `Span.QueueSize` (default 1024) and are then dropped. Look for
  `span channel - max capacity reached or closed` at `trace` level. Lowering
  the queue bounds the memory; fixing the collector fixes the cause.
* **High-cardinality names.** Operation and error names are interned per
  process, so a name built from a request value grows an unbounded cache. See
  [contract 8](api_contracts.md#8-keep-operation-and-error-names-low-cardinality).
  The same applies to URL statistics: an un-templated URL per request is what
  `Http.UrlStat.LimitSize` exists to cap.
* **Bind values and raw SQL.** `SQL.TraceBindValue` with a large
  `SQL.MaxBindValueSize` keeps more per query event. `SQL.EnableRawSqlCache`
  caches normalized statements — useful for prepared statements, wasted memory
  if every query inlines distinct literals.
* **Leaked tracers.** A goroutine tracer whose `EndSpan()` never runs keeps its
  span alive. `WrapGoroutine()` cannot leak this way; hand-rolled goroutine
  tracers can.

### High CPU Usage or Slow Responses

* **Sampling rate.** `counterRate: 1` traces every transaction. Raise the
  divisor (`counterRate: 10` is 10%) or set `Sampling.NewThroughput` to cap
  transactions per second under load.
* **Debug logging.** `debug`/`trace` levels add per-event work (including the
  goroutine-id read behind the shared-tracer check) and enable caller
  reporting. Never leave them on in production.
* **`Error.TraceCallStack`.** Call-stack capture and symbolization is the
  costliest thing the agent does per error. It is off by default; keep
  `Error.CallStackDepth` modest when you turn it on.
* **Over-instrumentation.** Events inside a hot loop cost more than they teach.
  Record one event around the loop.
* **`Span.Batch.Enable`.** For a very high span rate, batched unary sends can
  behave better than the long-lived stream; see the `Span.Batch*` options.

### Cannot Connect to Collector

**Symptoms:** no `success to register agent`; repeated `src=grpc` errors.

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
* Registration retries with backoff until it succeeds or the agent shuts down,
  so a collector that comes up later is picked up without an application
  restart. A collector that answers the registration with `success=false`
  (still initializing, briefly refusing) is retried the same way and logs
  `register agent - <message>, retrying`; it is never treated as permanent.
* If the connection cannot even be set up (bad TLS material, unparsable
  address) the agent logs `failed to connect to collector, agent disabled` and
  releases itself, so `GetAgent()` returns the no-op agent and `NewAgent` can be
  called again after fixing the configuration.
* Check the collector's own logs. A version mismatch is rejected there, not
  here: the agent requires Pinpoint 2.4.0+.

### Configuration Changes Not Taking Effect

* Only options marked **dynamic** reload from the config file; see the
  [reloadable options list](config.md#dynamic-configuration-reference).
  Everything else needs a restart.
* Reloads come from a file watcher, so they require the running process to have
  been given a config file (`ConfigFile`) — environment variables and command
  flags are read once at startup.
* Remember the precedence order. An environment variable overrides the config
  file, so editing the file will not change an option that is also set in the
  environment. The startup config dump shows the effective value.
* Watch for `src=config` lines on save; a parse error leaves the previous
  values in place.

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

---

## Related Documentation

* [Quick Start](quick_start.md)
* [Configuration](config.md)
* [Custom Instrumentation](instrument.md)
* [Tracer, Span, and Annotation Contracts](api_contracts.md)
* [Plugin User Guide](plugin_guide.md)
