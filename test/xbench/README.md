# Cross-version request-path benchmarks

`run.sh` compares what one instrumented request costs the goroutine that makes
it, in a released agent and in the working tree. It is the release-to-release
counterpart of the in-package benchmarks in `benchmark_test.go`, which time
internals that change from version to version and so cannot run against an
older release. The shapes follow the C++ agent's `span_lifecycle_benchmark`.

## Running

```bash
test/xbench/run.sh            # v1.4.7 against the working tree, 6 rounds
test/xbench/run.sh v1.4.7 10  # base ref and round count
test/xbench/run.sh v2.0.0     # any v1 or v2 tag, branch or commit
```

It needs only Go and the repository. `benchstat`
(`go install golang.org/x/perf/cmd/benchstat@latest`) prints the comparison at
the end; without it the raw results are still written. A run of 6 rounds takes
about 15 minutes.

What it does:

1. Extracts the base ref with `git archive` into a work directory.
2. Builds `bench_test.go.in` as two throwaway modules, one with a `replace`
   pointing at the extracted base and one at the working tree. **The working
   tree is used as is, uncommitted changes included.**
3. Builds and starts the e2e stub collector (`test/e2e/cmd/stubcollector`) on
   ports 9991-9993, so the agent registers, comes online and runs its sender
   goroutines as in production. Stop anything else on those ports first.
4. Runs both versions in alternating rounds, so drift in machine load hits both
   alike, at `-cpu 1,4,8` and `-benchtime 50000x`.
5. Writes `base.txt` and `head.txt` and benchstats them.

| environment variable | effect |
|---|---|
| `XBENCH_WORK` | work directory to use and keep, instead of a fresh `mktemp -d` |

Run on an otherwise idle machine.

## Request shapes

Every shape is one request: `NewSpanTracerWithReader`, its body, `EndSpan`.

| shape | body |
|---|---|
| `Unsampled` | new trace that the sampler rejects; the path most requests take whenever sampling is on |
| `UnsampledHeader` | upstream sent `Pinpoint-Sampled: s0` |
| `Sampled` | new sampled trace, two span events |
| `Continued` | inbound trace headers parsed and continued, two span events |
| `Nested10` | ten nested span events |
| `SQL` | one event with `SetSQL` (normalization and the SQL id cache) |
| `Error` | one event with `SetError` |
| `Inject` | one client event that injects the outbound headers |
| `UrlStat` | two events plus a URL statistics entry |

The test binary runs once with `XBENCH_RATE=1` (sample every trace) for the
sampled shapes and once with `XBENCH_RATE=0` for `Unsampled`, because the agent
is a process-global singleton with one sampler. `UnsampledHeader` runs in both,
so it has twice the samples.

Each shape is checked before it is timed: a request that comes back sampled
when it should not be, or as a noop span (span id 0), fails the run instead of
reporting a fast number.

## Reading the results

- `ns/op` is wall time per request across all goroutines, so at `-cpu=4` it is
  the throughput cost, not one request's latency. A value that stays flat or
  falls as `-cpu` rises means no cross-core contention; one that rises is a
  cost to explain.
- At `-cpu=1` the sender goroutines share the one P with the requests, so
  sending and serializing spans in the background shows up in `ns/op`. A
  version that does more work per span on the sender looks slower here than its
  request path alone would.
- The test binary holds a 64MB pointer-free `ballast` for the application's
  own heap, which is what sets the GC pace in production. Without it the heap
  is mostly the agent's, and an agent with a smaller footprint gets a lower
  heap goal and more GCs: v2's ~2MB live heap against v1.4.7's ~16MB meant
  133 GCs against 27 over the same 2,000,000 requests, each a stop-the-world
  the one P at `-cpu=1` waits out, and read as a +26% unsampled regression.
- The span queue and the url stat queue are both 65536, so 50000 requests never
  reach the head-drop path, which is cheaper and would flatter the number. v1
  sizes the url stat queue with `Span.QueueSize`, so `compat_v2.go.in` sets
  `Http.UrlStat.QueueSize` to match. If you raise `-benchtime`, raise both.
- On Apple silicon the `-cpu=8` rows can spill onto efficiency cores; the
  1-to-4 comparison is the clean one there.

## Adding a shape or a version difference

- A shape is one entry in the `shapes` table in `bench_test.go.in`. It may use
  only API both majors export with the same signature.
- What the majors spell differently goes in `compat_v1.go.in` and
  `compat_v2.go.in`. Today that is the module path (`run.sh` replaces the
  `PINPOINT` import), `DistributedTracingContextReader.Get`, which returns
  `(string, bool)` in v2, `WithAgentId`, which v2 removed, and the url stat queue
  size.
- `run.sh` picks the compat file from the base's `module` line: a path ending
  in `/v2` uses `compat_v2.go.in`, anything else `compat_v1.go.in`.
- The templates end in `.go.in` so the root module never compiles them: the
  `PINPOINT` placeholder and the v1 import would break `go vet ./...`.

## Results: v1.4.7 against v2 (Apple M1 Pro, 6 rounds)

Measured at 8bc1d4f with the 64MB ballast. Medians; the `-cpu=4` and
`-cpu=8` columns are the change against v1.4.7 at the same count.

| shape | -cpu=1 | -cpu=4 | -cpu=8 | allocs/op |
|---|---|---|---|---|
| Sampled | 4.70µs → 943ns (−80%) | −79% | −77% | 66 → 4 |
| Continued | 4.55µs → 1.19µs (−74%) | −75% | −75% | 71 → 4 |
| Nested10 | 16.6µs → 3.59µs (−78%) | −78% | −80% | 247 → 13 |
| SQL | 6.63µs → 870ns (−87%) | −86% | −83% | 69 → 4 |
| Error | 3.09µs → 840ns (−73%) | −72% | −77% | 44 → 3 |
| Inject | 4.44µs → 1.38µs (−69%) | −69% | −74% | 64 → 9 |
| UrlStat | 4.88µs → 1.27µs (−74%) | −76% | −57% | 69 → 5 |
| Unsampled | 214ns → 204ns (−5%) | −66% | −71% | 4 → 1 |
| UnsampledHeader | 208ns → 196ns (−6%) | −63% | −73% | 4 → 1 |

Geomean: −73% time and −91% allocations, with no shape slower at any count.

- The v1.4.7 `-cpu=1` rows vary by up to ±57%: its sender serializes spans on
  the one P the requests run on. Compare the `-cpu=4` and `-cpu=8` rows for
  stable numbers.
- `Continued` went from −37% to −75% at `-cpu=8` with 8bc1d4f. Before it, a
  continued span was registered under the span id from the upstream header,
  so this benchmark's identical headers put every request on one registry
  shard lock.
