# Changelog

## v2.0.0 (unreleased)

Every module moves to a `/v2` module path, so an application moves to v2 on
purpose: `go get -u` keeps it on v1. What to change, in order, is in
[Migrating from v1 to v2](doc/migration_v2.md).

### Breaking

- **Every module path carries `/v2`.** The agent is
  `github.com/pinpoint-apm/pinpoint-go-agent/v2` and each plugin
  `github.com/pinpoint-apm/pinpoint-go-agent/plugin/<name>/v2`; the package
  names do not change. Move every pinpoint import at once: v1 and v2 register
  the same protobuf files, so a binary that links a module of each panics at
  startup with `proto: file "v1/Annotation.proto" is already registered`.
- **Go 1.25 or newer** is required, up from 1.21.
- **Pinpoint 3.1.0 or newer** is required, up from 2.4.0. `Span.Batch.Enable`
  (default `true`) sends spans in unary `SendSpanBatch` requests instead of the
  long-lived `SendSpan` stream, and a collector implements `SendSpanBatch` from
  3.1.0. Against an older collector every batch fails and its spans are
  dropped; with `Span.Batch.Enable: false` the agent keeps the stream, which
  collectors from 2.4.0 take.
- **The agent id is not configurable.** `WithAgentId`, `CfgAgentID`, the
  `AgentId` key, `PINPOINT_GO_AGENTID` and `--pinpoint-agentid` are gone:
  every process generates its own id, so an instance shows up under a new id
  after each restart, and `AgentName` is the stable label. The config file key,
  the environment variable and a leftover `--pinpoint-agentid` are ignored.
- **`DistributedTracingContextReader.Get` returns `(string, bool)`**: the value
  and whether the carrier holds the key, since a header held with an empty
  value and a header not held at all decide trace continuation differently. A
  reader of your own has to report both; `pinpoint.HttpHeaderReader` adapts an
  `http.Header`.
- **`SpanRecorder.SetError` and `SetFailure` take optional arguments**, an
  error group name and an `ErrorCategory`. Calls compile as before; an
  implementation of the interface, such as a mock, has to declare them.
- **`protobuf` and `asm` are internal packages.** The generated gRPC code, whose
  exported API every regeneration changed, and the goroutine pointer helper
  were importable although no application needs them. They are
  `internal/protobuf` and `internal/asm` now, which leaves the agent's root
  package as its only public one.
- `pphttp.WrapResponseWriter` returns an `http.ResponseWriter` instead of the
  unexported `*responseWriter`.
- `ppgomemcache.(*Client).WithContext` returns the copy of the client bound to
  the context's tracer; the request's calls should use that copy.
- **The HTTP client URL annotation strips the query by default.** `pphttp`
  and `ppfasthttp` record `HTTP.URL` up to the `?` unless the new
  `Http.Client.RecordUrlQuery` (default `false`) is on, matching the C++ agent;
  Java's `profiler.<plugin>.param` defaults on. Query strings carry tokens and
  ids. Endpoint and destination are unchanged.

### Changed

- `Log.Output` defaults to `stdout`; it was `stderr`.
- **Default behavior change.** The collector channel now uses the gRPC `dns`
  resolver (`dns:///host:port`) instead of the `passthrough` scheme. A collector
  host with several A records is resolved into the channel's full address list,
  so the agent gains client-side spreading across collector instances and
  failover to another address, and the list is re-resolved as records change;
  `passthrough` gave the channel a single address, which left the
  `Collector.Grpc.ConnectionMaxAge` load balancing policy (ported from Java's
  `SubconnectionExpiringLoadBalancer`) nothing to spread over and its
  re-resolution request nothing to re-resolve. IP literals (IPv4 and IPv6) and
  names in `/etc/hosts`, including the default `localhost`, are unchanged, as is
  TLS verification, which keeps deriving the server name from the channel
  authority - the collector hostname. The new `Collector.Grpc.DnsResolverEnable`
  (default true) restores the `passthrough` scheme when set to false, as a
  rollback lever that needs no redeploy.
- The new `Collector.Grpc.KeepAlivePermitWithoutCalls` defaults to `false`, so
  keepalive pings stop while no stream is open; v1 always sent them, which
  `true` restores.
- The new `SQL.RemoveComments` defaults to `true`: comments are dropped from the
  normalized SQL, which changes the text, and with it the SQL id, of every
  statement that carries one. `false` keeps them as v1 did.
- `Http.UrlStat.LimitSize` now defaults to `1000`, Java's
  `profiler.uri.stat.completed.data.limit.size`, instead of `1024`; the C++
  agent made the same move, so the two ports and Java drop a tick's excess
  URIs at the same point. `Http.UrlStat.QueueSize` stays `1024`: it bounds a
  different queue - the input queue between the request path and the
  aggregator, which Java sizes at 5192 on one queue and neither port
  reproduces.
- `Stat.CollectInterval` is capped at `10000` ms, Java's
  `DefaultAgentStatMonitor` maximum, instead of `60000`; a larger value falls
  back to the default as before. A six-minute stat batch is no longer
  reachable by misconfiguration.
- URL statistics recorded without a URI template are now keyed as `/NULL`
  (Java's `URITemplate.NULL_URI`, also used by the C++ agent) instead of
  `UNKNOWN_URL`. Server-side history under the old `UNKNOWN_URL` key does not
  carry over to the new key.
- SQL statements longer than 1 MiB are no longer normalized or recorded: no SQL
  annotation, no SQL metadata, and no `SQL.ErrorCount` increment. The 64KB
  metadata text cap is unchanged. The value matches the C++ agent; the
  drop policy is documented in `doc/development.md`.
- The active span registry behind the active-request histogram is now bounded
  at 10240 entries (320 per shard), the Java agent's `DefaultActiveTraceRepository`
  maximum. A span that is never ended used to leave its entry behind forever;
  now a full shard evicts an existing entry for the new span and logs a
  rate-limited warning naming the size and the cap. Only an application that
  leaks spans reaches the cap; the histogram it then reports covers the most
  recent 10240 spans rather than all of them.
- A span that drops exception entries at the `Error.MaxChainDepth` entry limit
  now logs how many it dropped when it ends. The existing warning latches after
  the first drop, so it said a span hit the limit but not by how much, and a
  retry loop that lost a handful of chain links read exactly like one that lost
  thousands. The limit itself is unchanged (10 to 64 entries a span, derived
  from `Error.MaxChainDepth`'s own clamp ceiling). It is not raised to the C++
  agent's 100 because the cap here is derived from the option rather than set
  independently, so one chain of the configured depth is always kept whole;
  Java's mid-span buffer flush is not ported because sending exception metadata
  before the span ends is a separate design, not a constant.
- The metadata retry budget and rejection policy are locked
  (`Test_MetadataRetryBudget`), mirrored in the C++ suite:
  both ports drop a `PResult.success=false` reply where Java retries it, so
  a change in either port is now a deliberate joint change.

### Added

- **`pinpoint.SetTracerHooks(TracerHooks)`** registers callbacks the core
  calls with the agent's `Tracer`: `FromContext` sees (and may replace) the
  tracer `FromContext` returns, `SpanStart` the tracer `NewSpanTracer`/
  `NewSpanTracerWithReader` return, `SpanEvent` and `SpanEnd` run first in
  `NewSpanEvent` and `EndSpan` of sampled and unsampled spans. The
  compile-time instrumentation tool's goroutine-local tracer registers them
  instead of instrumenting the core's functions by name (agent_changes 20).
  Unregistered, each call site costs one atomic load.
- **`ppconfluentkafka.WrapProducer(p, conf)`** wraps a `*kafka.Producer` created
  elsewhere the way `ppconfluentkafka.NewProducer` wraps the one it creates
  (the broker comes from the configuration's `bootstrap.servers`);
  `NewProducer` now calls it. The compile-time instrumentation tool uses it
  from its `kafka.NewProducer` hook (agent_changes 23).
- **`ppgomemcache.WrapClient(c, endpoint)`** wraps a `*memcache.Client` created
  elsewhere the way `ppgomemcache.NewClient` wraps the one it creates;
  `NewClient` now calls it. The compile-time instrumentation tool uses it from
  its `memcache.New` hook (agent_changes 22).
- **`ppsaramaibm.WrapSyncProducer(p, addrs, config)` / `WrapAsyncProducer(p, addrs, config)`**
  (and the same in `ppsarama`) wrap a producer created elsewhere the way
  `NewSyncProducer`/`NewAsyncProducer` wrap the one they create (idempotent);
  those constructors now call them. `config` is the producer's configuration
  (nil means sarama's default); its `Version` decides whether trace headers
  are written. A wrapped producer without an address records no destination
  instead of panicking. The compile-time instrumentation tool uses them from
  its `sarama.NewSyncProducer`/`NewAsyncProducer` hooks (agent_changes 21).
- **`ppgohbase.WrapClient(c, zkquorum)`** wraps a `gohbase.Client` created
  elsewhere the way `ppgohbase.NewClient` wraps the one it creates
  (idempotent). The compile-time instrumentation tool uses it from its
  `gohbase.NewClient` hook (agent_changes 19).
- **`ppredigo.WrapConn(c, address)`** wraps a `redis.Conn` dialed elsewhere
  the way `ppredigo.Dial` wraps the one it dials (idempotent). The compile-time
  instrumentation tool uses it from its `redis.DialContext` hook
  (agent_changes 18).
- **`pphttprouter.WrapHandle(handler, path ...string)`** takes the route
  pattern as an optional second argument and then collects URL statistics as
  `pphttprouter.New()` does; existing calls are unchanged. The compile-time
  instrumentation tool passes the pattern from the `(*Router).Handle` it hooks
  (agent_changes 15).
- **`ppgorm.Instrument(db)`** registers the gorm plugin's callbacks on a
  `*gorm.DB` opened elsewhere, idempotently; `ppgorm.Open` now calls it. The
  compile-time instrumentation tool uses it from its `gorm.Open` hook
  (agent_changes 14).
- **Compile-time instrumentation support.** For the Pinpoint Go compile-time
  instrumentation tool, and useful on their own:
  `pinpoint.ErrAgentAlreadyCreated` is the sentinel `NewAgent` returns with the
  existing agent (`errors.Is`); `pinpoint.IsInternalContext(ctx)` reports the
  agent's own collector RPCs so process-wide gRPC client instrumentation can
  skip them; `GetAgent`, `NoopAgent`, `GetConfig` and `Log` no longer panic
  when reached before the package initialized (a hook called from the init
  function of a package that does not import the agent);
  `pphttp.NewHttpClientTracer`/`EndHttpClientTracer` are supported entry
  points again (the exported halves of `WrapClient`).
- **SQL drivers can be wrapped as they register.** `pinpoint.IsWrappedSQLDriver`
  tells a driver `WrapSQLDriver` returned from one that has not been wrapped,
  and every SQL plugin exports `DBInfo()` (service types and DSN parser) as a
  constructor, so the compile-time instrumentation tool's `sql.Register` hook
  can wrap the driver the application registers (`sql.Open("mysql", dsn)` then
  traces like `sql.Open("mysql-pinpoint", dsn)`) and skip the plugin's own,
  already wrapped registration.
- **Command line flags in the `--pinpoint-key value` form.** Flags that take a
  value accept it as the next argument as well as after `=`; an unknown
  `--pinpoint-*` flag no longer stops the parse at the flags after it; a
  value-taking flag without a value is dropped with a warning instead of
  failing the whole command line.
- **One request, one span when server instrumentation is layered.**
  `pphttp.NewHttpServerTracer` and the `ppgrpc` server interceptors reuse the
  tracer already in the request context — a middleware installed twice, a
  framework middleware inside `WrapHandler`, or the compile-time
  instrumentation tool in front of the manual wrapper — instead of starting a
  second transaction: the inner layer records its span event on the existing
  span, records no second status, and its `EndSpan` is ignored
  (`pinpoint.NestedTracer`, `pinpoint.IsNestedTracer`). This is what the Java
  agent does (`DefaultTraceFactory.checkAndGet`, "already Trace Object
  exist"); the Go agent previously logged "installed twice" at debug level and
  created two spans with different transaction ids.
- **`plugin/confluentkafka`** instruments [confluentinc/confluent-kafka-go](https://github.com/confluentinc/confluent-kafka-go)
  v2: `NewProducer` + `ProduceContext` on the producer side, `ConsumeMessageContext`
  + `NewContext` on the consumer side, with the same headers and annotations as
  the sarama plugins ([#80](https://github.com/pinpoint-apm/pinpoint-go-agent/issues/80)).
- **Plugins for new libraries and new library majors**: `plugin/echov5`
  (labstack/echo v5), `plugin/fiberv3` (gofiber/fiber v3), `plugin/kratosv3`
  (go-kratos/kratos v3), `plugin/gocqlv2` (gocql v2), `plugin/goelasticv8` and
  `plugin/goelasticv9` (elastic/go-elasticsearch v8 and v9),
  `plugin/mongodriverv2` (mongo-go-driver v2), `plugin/oraclev3`
  (sijms/go-ora v3), `plugin/mssql-microsoft` (microsoft/go-mssqldb),
  `plugin/slog` (log/slog) and `plugin/zap` (uber-go/zap). See the
  [Plugin User Guide](doc/plugin_guide.md).
- **Configurable real-IP headers.** `Http.Server.RealIpHeader` (ordered list,
  default `["X-Forwarded-For", "X-Real-Ip"]` = today's behaviour, `[]` trusts
  none) and `Http.Server.RealIpEmptyValue` port Java's `RealIpHeaderResolver`:
  a `Forwarded` header is parsed for its `for=` token, other headers give
  their first hop, a value equal to the empty value is skipped. Both
  reloadable.
- **Request parameter recording, opt-in.** `pphttp.RecordHttpServerRequestWithQuery`
  (used by `RecordHttpServerRequest`, so every net/http based plugin gets it)
  records the query string as annotation 41 (`HTTP.PARAM`) in Java's
  `HttpServletParameterExtractor` format when `Http.Server.RecordRequestParam`
  is on (default `false`; Java defaults on). `pphttp.FormatRequestParams` is
  exported for adapters that hold the query outside a net/http request.
- New `pinpoint.ShutdownOnSignal(agent, sigs...) (stop func())` calls
  `Shutdown()` when the process receives one of the given signals (`SIGTERM`
  and `SIGINT` by default), then restores the default signal handling and
  re-raises the signal so the process still exits with `128+signum`. It is
  opt-in and off by default: the agent never calls `signal.Notify` on its
  own. Without it, a `defer agent.Shutdown()` does not run on `SIGTERM` - the
  signal every container orchestrator sends on a rollout - and the spans still
  queued are lost while the UI keeps listing the agent as alive. `os.Exit`
  cannot be covered by any means; see `doc/troubleshooting.md`. The C++ agent
  takes the same opt-in policy with a different mechanism (`std::atexit`, no
  signal handler); see `doc/development.md`.
- **47 new configuration options**, among them TLS on the collector channels
  (`Collector.Grpc.SslEnable`, `Collector.Grpc.TrustCertFilePath`), gRPC
  channel tuning (`Collector.Grpc.*`), the span batch sender (`Span.Batch*`),
  error causes and exception chains (`Span.ErrorMark`, `Span.ErrorMarkExclude`,
  `Span.IgnoreErrors`, `Error.MaxChainDepth`, `Error.NewThroughput`), the SQL
  caches (`SQL.Cache*`, `SQL.EnableRawSqlCache`) and the agent identity
  (`Uid.Version`, `ServiceName`, `ApiKey`). Each is described in
  [Configuration](doc/config.md).

### Fixed

- **A wrapped SQL driver no longer keeps a connection whose rollback failed.**
  The wrapper claimed `driver.SessionResetter` and `driver.Validator` for every
  driver, and database/sql decides from their presence alone whether a
  connection survives a rollback its context triggered. For a driver with
  neither (go-ora v2 in `plugin/oracle`, the sqlite drivers) or only one (pgx v5
  stdlib), a connection whose rollback had just failed went back to the pool
  with its transaction open on the server, and go-ora's next statement
  committed it. The wrapper now has exactly the ones the driver has.
- **`pphttp`'s response writer keeps `http.CloseNotifier`.** gin's `c.Stream`
  asserts it unchecked, so every streamed gin response behind
  `pphttp.WrapHandler` (and the chi, gorilla, beego and httprouter plugins that
  use it) panicked with a 500. A writer without it gets a channel that never
  fires.
- **`ppfasthttp`'s tracer context no longer derives from the `RequestCtx`.**
  fasthttp reuses the `RequestCtx` for the next request, so a goroutine that
  kept the context read that request's user values and raced their writes. The
  parent is `context.Background()` again; the `RequestCtx` has no deadline and
  its `Done` closes only at server shutdown.
- **The sarama producers no longer fail every message below Kafka 0.11.** The
  header writer replaced a nil `Headers` with an empty slice even when nothing
  was injected, and sarama rejects any message with non-nil headers below
  `V0_11_0_0` - `Config.Version`'s default in the sarama releases `ppsarama`
  supports. Headers are now written only when injected, and not at all on such a
  producer.
- **The sarama async producer keeps transactions intact.** `CommitTxn` and
  `AbortTxn` went straight to sarama while accepted messages could still sit in
  the wrapper's buffer, so they reached sarama after the end-of-transaction
  marker: rejected as outside a transaction while the commit reported success,
  or a `WaitGroup` panic in `CommitTxn`. Both now hand sarama every accepted
  message first.
- **`Span.EventChunkSize` and `Span.BatchSize` have an upper bound.** Only
  values below 1 were refused, and every sampled request sized its chunk
  buffer by the first (1e7 allocated 76MiB a request), every batch its slice
  by the second. Both now take 1 to 65536 like the queue sizes, and a chunk
  buffer is preallocated no larger than the default's 20 events.
- **A float for an int option must hold an int.** `.inf` or `1e20` from a
  config file converted through `int(f)`, which is implementation-defined: the
  same file became MaxInt64 on arm64 and MinInt64 on amd64. A non-finite,
  fractional or out-of-range float is now refused with the usual warning.
- **`Span.MaxCallStackDepth`/`Span.MaxCallStackSequence` above MaxInt32 are
  unlimited**, like -1, instead of wrapping to 0 or below and dropping every
  span event.
- **A collector outage no longer floods the log or churns the metadata.** The
  metadata send, span batch and per-span warnings are throttled to a line an
  interval with a suppressed count. A metadata item out of attempts is still
  released at once the first time since a delivery, but while give-ups follow
  one another - an outage - each keeps its cache entry for up to 30 retry
  delays instead of being re-registered with a new id every couple of
  seconds; any delivery ends the wait.
- **Recording a value-type error wrapper no longer panics.** With
  `Error.TraceCallStack` on, comparing two `struct{ Err error }` values whose
  `Err` held a slice-based error panicked on the request goroutine.
- **`ShutdownOnSignal` is documented as exclusive with a host signal handler**,
  which receives every signal twice, and on Windows, where a process cannot
  re-raise a Ctrl-C, watches only SIGTERM by default.
- **`ppgrpc`'s stream client interceptor no longer appends into gRPC's shared
  call options.** Without per-call options a stream is handed the
  `ClientConn`'s default option slice, and concurrent streams raced over its
  spare capacity and swapped each other's `OnFinish`.
- **`pphttp.DoClient` injects into a copy of the request.** It wrote into the
  caller's header map, which a template request shares with every
  `WithContext` copy: the writes raced, and the first transaction's headers
  made every later call look nested. `WrapClient` keeps a nil header nil too.
- **`ppconfluentkafka.ProduceContext` hands delivery reports over untouched.**
  A goroutine per message forwarded them, which reordered the reports on a
  shared channel and could send after `Flush` into a channel the application
  had closed. The span event now covers the enqueue with a delivery channel as
  without one, so a failed delivery is no longer recorded on it.
