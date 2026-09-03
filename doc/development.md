# Development Guide

How to build, test and extend the agent itself. If you only want to instrument
an application, start with [Quick Start](quick_start.md) instead.

## Prerequisites

* **Go 1.25+** — the floor in `go.mod`. CI verifies 1.25 and 1.26.
* Nothing else for a normal build. Protobuf regeneration is the one exception
  and has [its own section](#regenerating-the-protobuf-code); the generated
  code is committed, so you do not need `protoc` to build or test.

## Repository layout

The repository is a set of **independent Go modules**, which is the single most
important thing to know before running anything:

| Path | Module | Why separate |
|---|---|---|
| `/` | `pinpoint-go-agent` | the agent; depends on nothing but gRPC and its own support libraries |
| `plugin/<name>/` | one module each | so instrumenting Gin does not pull in Kafka, Mongo and Elasticsearch |
| `example/` | own module | depends on the agent and two plugins, via `replace` |
| `test/it/` | own module | depends on `plugin/http`, which the agent module must not |
| `test/e2e/` | own module | depends on `plugin/http` and `plugin/grpc` |

`go test ./...` from the repository root therefore does **not** run the plugin
or integration tests — each module has to be entered. That is what the loops
below are for.

Inside the agent module, the pieces worth knowing:

| File | Responsibility |
|---|---|
| `agent.go` | lifecycle, the goroutines, the global singleton |
| `config.go` | option registry, five-source precedence, file watcher and reload |
| `span.go`, `span_event.go` | the call-stack model behind `Tracer` |
| `span_queue.go`, `span_slab.go` | span buffering between the request path and the sender |
| `grpc.go`, `grpc_balancer.go` | the four collector channels and their streams |
| `sampler.go` | counter, percent and throughput samplers |
| `sql_driver.go`, `sql_util.go` | the `database/sql` wrapper and SQL normalization |
| `url_stat.go`, `stats.go` | URL and agent statistics aggregation |
| `noop.go` | the no-op agent and tracer — the reason instrumentation needs no guards |
| `command.go`, `goroutine.go` | profiler commands, active-thread and goroutine dumps |
| `uid.go`, `objectname.go` | agent identity, v1/v3/v4 |

## Build and test the agent

```bash
go build -v
```
```bash
go test -v
```
```bash
go test -race ./...
```

The agent's tracers are concurrency-sensitive, so `-race` is part of the
contract rather than an occasional extra. Run it before sending a change that
touches spans, the queue or the config snapshot.

## Test the plugins

Each plugin is its own module, so they run one at a time. This is the loop CI
uses, and the one to run locally before touching a plugin:

```bash
for dir in plugin/*/; do (cd "$dir" && go test -race ./) || echo "FAILED: $dir"; done
```

Only the module's own package is tested — the `example/` directories are
standalone `main` programs and do not build as part of the package.

## Integration tests (mock collector)

`test/it` starts a real in-process gRPC collector on three ephemeral ports and
drives a real agent against it. It needs **no Pinpoint collector and no network
access**, which makes it the right place to assert what the agent actually puts
on the wire.

```bash
cd test/it
go test ./...            # full suite, ~1 minute
go test -short ./...     # skips the URL-statistics test's 30s tick
go test -race ./...
go test -run TestSendsAllMetadataAndCompleteSpanShapes -v
```

Every received protobuf and its client metadata are copied into a thread-safe
snapshot, so a test asserts on the real message rather than on an internal
call. Coverage spans span lifecycle and chunking, async/goroutine spans,
propagation, v1/v3/v4 identity metadata, SQL metadata through the real driver,
agent and URL statistics, sampling and throughput limits, the `plugin/http`
helpers, and profiler commands over the real bidirectional stream. See
[test/it/README.md](/test/it/README.md).

### Goroutine leak profile

Go 1.26's experimental goroutine leak profile catches a worker that outlived
`Shutdown()` — the shape that `runtime.NumGoroutine()` comparisons can only
guess at. The check lives in `test/it`'s `TestMain` and is a no-op without the
experiment, so it costs the ordinary runs nothing:

```bash
cd test/it
GOEXPERIMENT=goroutineleakprofile go test -v -timeout 15m ./...
```

## End-to-end tests (live collector)

`test/e2e` runs the agent against a real collector, across separate processes —
separate because a Pinpoint agent is a process-global singleton, so one process
cannot play both upstream and downstream.

```bash
cd test/e2e
export PINPOINT_GO_COLLECTOR_HOST="your-collector-host"
./run_e2e.sh
```

`pinpoint-config.yaml` deliberately omits `Collector.Host`, making the
collector an explicit runtime decision. To exercise the harness without a
collector, `--local-collector` starts a bundled stub that accepts everything
and records nothing — a self-test of the harness, not of the protocol.

`load_test.py` is the load generator: with `--rps` a constant-arrival-rate test
that reports latency and scheduling lag, without it an unthrottled saturation
test. See [test/e2e/README.md](/test/e2e/README.md).

## Benchmarks

Performance-sensitive paths carry benchmarks next to their unit tests
(`benchmark_test.go`, `grpc_bench_test.go`, `stats_bench_test.go`,
`sql_util_bench_test.go`):

```bash
go test -run '^$' -bench . -benchmem
```
```bash
go test -run '^$' -bench BenchmarkSpan -benchmem -count 10
```

Use `-count 10` and compare with `benchstat`; a single run of a
nanosecond-scale benchmark tells you very little. The agent runs inside the
request path, so an allocation added per span event is a real regression —
`-benchmem` is not optional here.

## Regenerating the protobuf code

The generated code in `protobuf/` is committed, so this is only needed when the
IDL changes:

```bash
./scripts/generate-protobuf.sh
```

The script downloads pinned versions of `protoc`, `protoc-gen-go`,
`protoc-gen-go-grpc` and the gRPC mock generator into `.tools/`, so it does not
depend on what happens to be installed. Sources come from
`pinpoint-grpc-idl/proto`; `Log.proto` is excluded on purpose — it describes a
log-shipping service this agent does not implement, and generating it would
ship a client and a mock nothing calls.

## Adding a plugin

Follow an existing plugin of the same shape — a middleware plugin like
`plugin/gin`, a driver like `plugin/mysql`, a hook like `plugin/goredisv9`.
Each new plugin needs:

1. **Its own module.** `plugin/<name>/go.mod`, with the agent as a dependency.
   Never add the instrumented library to the agent's own `go.mod`.
2. **Package name `pp<name>`.** `plugin/gin` is `ppgin`.
3. **A thin entry point.** Prefer the library's own seam — middleware, hook,
   observer, monitor, `RoundTripper` — over wrapping every call site.
4. **A `README.md`** with install, usage and a link to the full example, and an
   **`example/`** that builds and runs.
5. **Tests, run with `-race`.** The convention in the existing plugins is to
   assert against a real agent, and to cover the cases that break in
   production: an unsampled transaction, a disabled agent, a panic through the
   wrapper, an unmatched route, and concurrent requests.

The behavioral requirements are the same as for any instrumentation, and they
are worth re-reading before writing one:
[Tracer, Span, and Annotation Contracts](api_contracts.md), plus the
[checklist](instrument.md#checklist-for-a-new-instrumentation).

Two that plugin authors hit most often:

* **Pass through cleanly when there is nothing to trace.** A disabled agent or
  an unsampled transaction hands you a no-op tracer; the wrapper must still
  call the wrapped code and change nothing observable.
* **Never swallow a panic.** Close the span event (a `defer` does this) and let
  the panic propagate, so the framework's own recovery middleware behaves as
  the user configured it.

## Continuous integration

[`.github/workflows/ci.yml`](/.github/workflows/ci.yml) runs on every push and
pull request to `main`, in four jobs:

| Job | What it runs |
|---|---|
| `build` | `go build` and `go test` on the agent, plus the `test/it` suite, on Go 1.25 and 1.26 |
| `plugins` | `go test -race` in every `plugin/*` module, on Go 1.25 and 1.26 |
| `goroutine-leak` | `test/it` under `GOEXPERIMENT=goroutineleakprofile`, Go 1.26 only |
| — | `test/e2e` is **not** in CI: it needs a live collector |

`fail-fast` is off in the matrices on purpose: a break on one Go version still
reports the other, which is what distinguishes a version-specific regression
from a real one. The plugin loop likewise runs every module before failing, so
one broken plugin does not hide the rest.

Before opening a pull request, the short version of CI:

```bash
go test -race ./... && (cd test/it && go test ./...) && for dir in plugin/*/; do (cd "$dir" && go test -race ./) || echo "FAILED: $dir"; done
```

## Contributing

See [CONTRIBUTING.md](/CONTRIBUTING.md). Pull requests need a signed
Contributor License Agreement, and should not break the build or any test.

---

## Related Documentation

* [Quick Start](quick_start.md)
* [Custom Instrumentation](instrument.md)
* [Tracer, Span, and Annotation Contracts](api_contracts.md)
* [Plugin User Guide](plugin_guide.md)
* [Configuration](config.md)
* [Troubleshooting](troubleshooting.md)
