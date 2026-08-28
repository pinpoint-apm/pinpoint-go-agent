# Pinpoint Go Agent live end-to-end tests

This directory contains two complementary suites, the Go counterparts of the
C++ agent's `test/e2e`:

- `smoke_test.sh` is a deterministic correctness suite. It checks agent
  registration, the public tracing API, HTTP/gRPC propagation, all four gRPC
  RPC shapes, annotations, SQL metadata, call-stack errors, goroutine spans,
  URL/method filters, span limits, sampling reload, the config-file watcher, and
  lifecycle shutdown/restart.
- `load_test.py` is the load generator. With `--rps` it is a
  constant-arrival-rate test that schedules request starts at monotonic-clock
  deadlines, reports latency and scheduling lag, and fails when errors or
  dropped arrivals exceed the configured thresholds. Without `--rps` it is an
  unthrottled saturation test whose workers reuse HTTP connections and issue the
  next request immediately after each response. `--rss-pid` additionally samples
  the server's memory across the run (useful for leak/stress passes).

The correctness stack uses separate processes because a Pinpoint agent is a
process-global singleton:

```text
curl -> upstream (HTTP)
          |-> downstream  (real HTTP trace headers)
          `-> grpcserver  (unary + three streaming shapes)
```

`test/e2e` is its own Go module so it can depend on the `plugin/http` and
`plugin/grpc` modules without adding those dependencies to the agent module.

## Collector configuration

Set the collector explicitly before running the suite:

```bash
export PINPOINT_GO_COLLECTOR_HOST="your-collector-host"
./run_e2e.sh
```

`pinpoint-config.yaml` intentionally has no `Collector.Host`, which makes the
collector an explicit runtime setting. Each process gets a unique
`PINPOINT_GO_AGENTNAME`/`PINPOINT_GO_AGENTID`, so concurrent runs are
distinguishable under the stable applications `go-e2e-http-upstream`,
`go-e2e-http-downstream` and `go-e2e-grpc-downstream`.

To exercise the stack without a collector, `--local-collector` starts a bundled
stub that accepts everything and records nothing:

```bash
./run_e2e.sh --local-collector
```

That is a self-test of the harness, not of the collector protocol. For
assertions on what the agent actually put on the wire, use the recording
collector in [test/it](../it/README.md).

## Build and run

`run_e2e.sh` builds the binaries into `./bin` before starting them:

```bash
./run_e2e.sh
./run_e2e.sh --skip-build            # reuse ./bin
go build -o bin/ ./cmd/...           # build by hand
```

## Correctness assertions

The suite fails unless all of the following are observed:

- every long-running app reports `agent_enabled: true` on its readiness
  endpoint, which happens only after AgentInfo registration succeeds;
- upstream and downstream HTTP trace IDs match, the injected child span ID is
  accepted by the downstream span, and the downstream parent span ID equals the
  upstream span ID;
- gRPC unary, server-streaming, client-streaming and bidirectional-streaming
  calls retain the distributed trace ID;
- inbound `Pinpoint-Sampled: s0` creates an unsampled span, and a hop that
  receives it forwards the same decision to the next hop;
- all public annotation value types, the logging context, SQL metadata,
  call-stack exception metadata, and a joined goroutine span are exercised;
- reduced depth/sequence/chunk limits are crossed without breaking requests, and
  a call made from beyond the depth limit still injects a complete trace
  context;
- one probe behind every `Http.Server.ExcludeUrl` pattern kind is untraced while
  an unmatched control still traces, and `ExcludeMethod` is honored;
- an agent restart changes counter sampling and both sampled and unsampled
  decisions are observed;
- the config-file watcher applies a new sampling rate to the running agent with
  no restart;
- shutdown disables the global agent and a subsequent cold agent registers and
  records a trace;
- every process log contains a successful AgentInfo registration, the upstream
  log shows span batches leaving for the collector, and none of them was
  rejected.

The local response assertions prove propagation and public API behavior. The
transport-log assertions prove that data reached the configured collector. If a
Pinpoint Web/API endpoint becomes available, a future test can additionally
query the unique agent IDs and validate the stored payload fields.

## Optional load passes

Append a load mode to the orchestrated run:

```bash
./run_e2e.sh --load-mode full --load-duration 120 --load-concurrency 20
```

For maximum throughput without an RPS limit, run the generator against an
already-started stack. `--concurrency` is the number of workers continuously
kept busy:

```bash
python3 ./load_test.py \
  --base-url http://127.0.0.1:8090 \
  --mode mixed --duration 60 --concurrency 100
```

The default two-second warm-up is excluded from throughput and latency results.
Use `--warmup 0` to disable it, `--max-error-rate` to permit expected errors, or
`--min-rps` to enforce a performance-regression threshold. The agent must be
ready unless `--no-require-agent` is supplied.

For a fixed request rate, add `--rps`:

```bash
python3 ./load_test.py \
  --base-url http://127.0.0.1:8090 \
  --mode mixed --rps 50 --duration 60 --concurrency 100
```

It rotates deterministically through the endpoints in the selected mode. The
`/error` endpoint's intentional HTTP 500 is treated as success. Arrivals are
dropped rather than queued or emitted as catch-up bursts when the client falls
behind or reaches the `--concurrency` in-flight bound; the default pass criteria
allow up to 5% dropped arrivals and no unexpected response errors. Use
`--rps-tolerance` and `--max-error-rate` to change those thresholds.

With `--load-rps`, `--load-concurrency` is the maximum number of in-flight
requests; without it, the load phase is unthrottled and `--load-concurrency` is
the continuously busy worker count. `run_e2e.sh` always passes the upstream
server's PID as `--rss-pid`, so every orchestrated load pass reports its
first/max/last RSS.

`--max-error-rate PCT` forwards the generator's error budget, which otherwise
defaults to 0 and fails the run on a single bad request. Raise it when a few
tail-latency timeouts are expected rather than meaningful — a heavily
instrumented build or a contended host can push p95 far enough that the
occasional request reaches the generator's 30s client timeout.

## Performance profiling

The upstream server exposes Go's own profiler at `/debug/pprof`, so no external
profiler is needed. `--profile` captures a CPU profile through it for the
duration of the load phase:

```bash
./run_e2e.sh --load-mode mixed --load-rps 50 --load-duration 60 \
  --load-concurrency 100 --profile --keep-logs
```

The profile is written under the run's log directory (kept automatically) and
read with `go tool pprof <file>`. `--profile-output` selects another location
and `--profile-seconds` shortens the capture. Against an already-running stack:

```bash
go tool pprof http://127.0.0.1:8090/debug/pprof/profile?seconds=30
```

This replaces the C++ suite's `profile_load.sh`, which attaches `perf` or
`xctrace` to a PID; a Go binary profiles itself.

## DB fault injection

The SQL endpoints run real statements through the agent's `database/sql` driver
wrapper against a stub driver, so SQL normalization, metadata publication and
bind-value recording are exercised without a database.

`PINPOINT_E2E_DB_FAULTS` makes every traced statement sleep 1–100 ms and turns
~30% of them into error spans, which is useful for exercising error metadata and
slow-query traces:

```bash
PINPOINT_E2E_DB_FAULTS=1 ./bin/upstream 8090
```

It is off by default: the sleeps dominate DB-mode timings, so a load pass with
faults enabled measures the injected sleep rather than agent overhead. Any value
other than `0` or the empty string turns it on, and the server logs a notice at
startup when it is enabled.

## Differences from the C++ suite

- The C API and fork scenarios have no Go counterpart.
- `testapp.Greeting` (reused from `plugin/grpc/example`) carries only a message
  field, so the gRPC downstream appends its trace context to the reply message
  as `|trace_id=..|span_id=..|sampled=..` instead of using dedicated fields.
  Regenerating the proto is not required for the propagation assertions.
- Profiling uses Go's pprof endpoint rather than `perf`/`xctrace`.
- URL statistics are collected with the request path as the template: the Go
  API takes an already-normalized URL, so there is no path-trimming setting to
  exercise.
