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
- **Pinpoint 3.1.0 or newer** is required, up from 2.4.0. Spans are sent in
  unary `SendSpanBatch` requests, which a collector implements from 3.1.0; the
  long-lived `SendSpan` stream of v1 is gone. Against an older collector every
  batch fails and its spans are dropped.
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
- **`pinpoint.NewTestAgent(config)` takes no `*testing.T`.** The parameter was
  never used, and it made every production binary link the `testing` package.
- **The deprecated v1 spellings are gone.** `pinpoint.WithSamplingRate` (use
  `WithSamplingCounterRate`); the `LogLevel` config key with its
  `--pinpoint-loglevel` flag and `PINPOINT_GO_LOGLEVEL` variable (use
  `Log.Level`, `--pinpoint-log-level`, `PINPOINT_GO_LOG_LEVEL`);
  `pplogrus.WithField` (use `NewField`); `Tracer.NewAsyncSpan` (use
  `NewGoroutineTracer`); `pphttp.WrapHandle` and `WrapHandleFunc` (use
  `WrapHandler` and `WrapHandlerFunc`); `ppbeego.Middleware` and `DoRequest`
  (use `ServerFilterChain` and `ClientFilterChain`); and in `ppsaramaibm` the
  consumer wrappers `ConsumeMessage`, `WrapConsumerMessage`,
  `ConsumerMessage`, `HandlerFunc`, `NewConsumer`, `Consumer`,
  `WrapPartitionConsumer` and `PartitionConsumer` (use `ConsumeMessageContext`
  on a raw sarama consumer) together with the three `WithContext` functions
  that bound a tracer to a producer without being thread-safe (send through
  `SendMessageContext`, `SendMessagesContext` and `InputContext`; the plain
  `SendMessage`, `SendMessages` and `Input` now produce without tracing).
- **`Tracer.Extract` is gone.** Called on a live span it re-read the
  transaction from a carrier after `Inject` may already have sent the old ids
  downstream. `Agent.NewSpanTracerWithReader` reads the carrier as it creates
  the span, which is all the agent itself ever used `Extract` for.
- **The config file is read by the agent itself, not viper.** YAML, JSON and
  properties files - the documented formats - are told apart by extension
  (`.yaml`/`.yml`, `.json`, `.properties`/`.props`/`.prop`), keys stay
  case-insensitive, and the command line flags and `PINPOINT_GO_*` variables
  are read the same way as before. What viper read on top of that is not:
  TOML, HCL, INI and `.env` files, and in a properties file the `${...}`
  references, escapes and line continuations. viper, pflag and the fourteen
  modules they required (fsnotify, hcl, go-toml, ini, properties,
  mapstructure, afero, ...) leave the module graph of every application
  importing the agent.
- **Four plugins for dead upstreams are gone: `plugin/sarama`, `plugin/goredis`,
  `plugin/goredisv7` and `plugin/mssql`.** Shopify/sarama is archived and IBM/sarama is the same
  module renamed, so `plugin/sarama-IBM` (`ppsaramaibm`) is the sarama plugin:
  the two were one file under two import paths. go-redis v6 (the
  pre-modules `github.com/go-redis/redis`, last released in 2020) and v7 are
  end-of-life; `plugin/goredisv8` and `plugin/goredisv9` serve the maintained
  majors.
  denisenkom/go-mssqldb is deprecated for its fork microsoft/go-mssqldb, so
  `plugin/mssql-microsoft` is the SQL Server plugin; its driver is registered
  as `mssql-microsoft-pinpoint`, where `ppmssql` used `sqlserver-pinpoint`.
- **`plugin/echo` is gone.** It instrumented labstack/echo v3, which is
  end-of-life upstream with an unpatched advisory (GHSA-vfp3-v2gw-7wfq) and no
  fixed v3 release to pin, and its code was a copy of `plugin/echov4`. Use
  `plugin/echov4` or `plugin/echov5`.
- **`plugin/goelastic` serves every go-elasticsearch major.** `ppgoelastic.NewTransport` is an `http.RoundTripper` that never imports
  the client, and `elasticsearch.Config.Transport` is the same field in v7, v8
  and v9, so the three were one file under three names. Import
  `plugin/goelastic/v2`; `NewTransport` is unchanged.

### Changed

- **`NewConfig` returns an error for a config file that exists but cannot be
  read** - a syntax error, an unsupported extension. The `Config` comes back
  complete on defaults and the other sources, so `cfg, _ := NewConfig(...)`
  behaves as before; a missing file is still optional and only logged.
- **`SQL.CacheLengthLimit` no longer bounds the SQL-UID cache.** The cache keys
  on the statement's hash, so a statement past the limit registers its UID once
  instead of re-sending the metadata on every execution; the limit applies to
  the raw SQL cache alone.
- `Log.Output` defaults to `stdout`; it was `stderr`.
- **The config file is polled, not watched.** The dynamic options reload when
  the file's modification time or size changes, checked every second, so a
  change applies within that. The direct fsnotify dependency and the platform
  watch code with its failure modes (an inotify queue overflow, a directory
  watch for unlink+rewrite savers) are gone, and with viper the module leaves
  the graph.
- **The plugin examples live in the root `example` module** (`example/<plugin>`)
  instead of inside each plugin module. A plugin's go.mod no longer requires
  `plugin/http` (or, for `plugin/gorm`, `plugin/mysql` and the mysql driver) for
  a program only its example ran, so those requirements leave the module graph
  of every application importing the plugin. The gRPC test service the grpc
  examples, `example/gokit` and the end-to-end suite talk over lives in the
  agent module as `test/testapp`, generated by `scripts/generate-protobuf.sh`
  with the collector protocol.
- The `net/http.Header` carriers no longer canonicalize the Pinpoint header
  names on every lookup. Seven of the ten names (`Pinpoint-TraceID`,
  `Pinpoint-pSpanID`, ...) are not in textproto canonical form, so
  `HttpHeaderReader` and `Header.Set` took `CanonicalMIMEHeaderKey`'s
  allocating path per header: 9 allocations to extract a continued trace and
  11 to inject one. Both now use a table built once; the new
  `pinpoint.HttpHeaderWriter` is the `Inject` carrier for an `http.Header`,
  which `pphttp`'s client uses. `HttpHeaderReader` and `Header.Get` read the
  same keys as before.
- Smaller request-path savings: a sampled span is one allocation instead of
  four (its event stack and first event chunk are inline), `NewSpanEvent` no
  longer takes `spanEventLock` on top of the stack's own lock, the SQL
  normalizer reserves its parameter buffer once instead of growing it byte by
  byte, the bind value list is built in a `strings.Builder` (no copy on
  `String()`; `pppgxv5` too), `pphttp`'s client and `ppgohbase` look the span
  event up once per call, `pphttp` loads its config once per request phase,
  and `pphttp.WrapResponseWriter` is one allocation instead of two.
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
- `Collector.Grpc.WriteBufferSize` defaults to `65536`; it was 1MB. grpc-go
  pools write buffers in a `sync.Pool` that every GC cycle empties, so each
  collector connection allocated a fresh 1MB buffer after most cycles. In the
  end-to-end mixed load at 1000 RPS that was 42% of the bytes the process
  allocated; at 64KB the same load allocated 40% less and peaked 46% lower in
  RSS with unchanged throughput.
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
- **`github.com/spf13/cast` is no longer a dependency**; the agent converts
  option values itself, through the text a value is written as, so every
  source reads alike. An int option reads `010` as 10, where cast read octal 8,
  and rejects `0x10`, `1_000` and `10.0` with the usual "is not a valid int"
  warning; a bool option takes the numbers 1 and 0 but no other, and a number
  option no longer takes a bool. A YAML or JSON number is read as before.
- `gopkg.in/natefinch/lumberjack.v2` is v2.2.1, up from v2.0.0 (2016). A log
  file it creates is `0600` instead of `0644` and a log directory `0755`
  instead of `0744`; an existing file keeps its mode, also across rotation.
  v2.0.0 had no go.mod, so its test dependencies `BurntSushi/toml` and
  `gopkg.in/yaml.v2` were in the module graph of every application importing
  the agent.

### Added

- **`Collector.AgentInfo.SendArgs`** (`WithCollectorAgentInfoSendArgs`) turns
  off sending the process's command line arguments with the agent information,
  for a command line that carries a password or a token. On by default, as
  before.
- **`ppfasthttp.NewServerTracer(ctx, method, serverName)`** and
  **`RecordServerResponse(tracer, ctx, status)`** start a server span from a
  `*fasthttp.RequestCtx` and record its response, for the plugin of a framework
  built on fasthttp; `ppfiber` and `ppfiberv3` use them.
- **`pphttp.TraceSpan(tracer, funcName, fn, after)`** is the server trace
  sequence every framework adapter runs - the handler's span event, 500 on a
  panic, then URL stat and response recording, then `EndSpan` - for an
  adapter over a framework that is not `http.Handler`-shaped. `TraceHandler`
  is it applied to an `http.Handler`; gin, echo, beego, httprouter, fasthttp
  and fiber are built on it instead of each carrying a copy.
- **`pinpoint.BindValuesString(args)`** renders a bind value list the way the
  `database/sql` wrapper records it, under `SQL.TraceBindValue` and
  `SQL.MaxBindValueSize`, for a plugin instrumenting a driver outside
  `database/sql`; `pppgxv5` uses it instead of its own copy.
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
  wrap a producer created elsewhere the way
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
  (go-kratos/kratos v3), `plugin/gocqlv2` (gocql v2),
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
  channel tuning (`Collector.Grpc.*`), the span batch sender (`Collector.Grpc.SpanBatch*`),
  error causes and exception chains (`Span.ErrorMark`, `Span.ErrorMarkExclude`,
  `Span.IgnoreErrors`, `Error.MaxChainDepth`, `Error.NewThroughput`), the SQL
  caches (`SQL.Cache*`, `SQL.EnableRawSqlCache`) and the agent identity
  (`Uid.Version`, `ServiceName`, `ApiKey`). Each is described in
  [Configuration](doc/config.md).

### Fixed

- **`Sampling.PercentRate` is rounded to hundredths of a percent, not
  truncated.** `0.29 * 100` is `28.999999999999996` in float64, so 0.29%
  sampled 0.28%, 2.3% sampled 2.29% and 4.35% sampled 4.34%. A rate below the
  0.01 minimum still samples nothing.
- **An empty `os.Args` no longer panics, and a panic while connecting no
  longer ends the host.** A process exec'd with no argv at all panicked in
  `NewConfig` and in the registration goroutine, which nothing recovered;
  the connect goroutine is now recovered like the workers and releases the
  agent as a failed connect.
- **`Collector.Grpc.*` sizes and keepalive times are range checked.** A 0 or
  negative message size failed every send with `ResourceExhausted`, and a
  negative keepalive timeout dropped the connection after every ping; a value
  out of range recovers the default, as the queue sizes do.
- **A nil carrier is safe on a sampled span.** `Inject(nil)` did nothing on
  the noop tracer and panicked once the request was sampled.
- **An unsampled span clamps a negative elapsed time**, as a sampled one does,
  so an NTP step no longer shrinks the response-time total.
- **The kafka consumers take a nil context** (`ppsaramaibm`,
  `ppconfluentkafka`) instead of panicking once the agent is enabled.
- **`ppconfluentkafka.WrapProducer` keeps a nil producer nil**, as the sarama
  wrappers do, and **injects headers into a slice of the message's own**
  rather than into spare capacity the caller's slice may share with another
  message.
- **`ppechov4` records the committed status.** A handler that
  wrote its response and then returned an error was recorded with the error's
  status while the wire kept the written one.
- **`pphttp` refuses a nil handler at registration**, as net/http does,
  instead of panicking on every request, and **masks a password in the
  recorded client URL**.
- **`ppgomemcache` tolerates a `Client` built as a struct literal** instead of
  nil-dereferencing on its first operation.
- **`ppgoredisv9`, `pprueidis` and `ppredigo` record the error on
  the event they opened**, not on whichever event is on top of the stack once
  the call returns, which under a fan-out on one request was another
  goroutine's.
- **`ppredigo.WithContext` says when it cannot bind a connection.** A
  connection from a `redis.Pool` is redigo's own type and is traced through
  `redis.DoContext`; the no-op binding is now logged at debug level and
  documented.
- **`ppgoelastic` pools its gzip readers** instead of allocating one per
  compressed request.
- **A full metadata queue no longer spends an id per use.** A span that missed
  the API, error or SQL cache while `Collector.Grpc.SenderQueueSize` items were
  waiting minted an id, was refused, and released the entry, so the next use
  minted another: through a collector outage the int32 sequence wrapped within
  hours on a busy service, after which no SQL was recorded until the process
  restarted. The queue is checked first; a refused use records no metadata and
  the next one registers it once there is room.
- **`SetLogging` is a plain store.** The logrus, slog and zap plugins call it
  from whichever goroutine logs with the request's tracer, and two at once were
  a data race.
- **`ppsaramaibm` passes a nil message through.** sarama logs and
  ignores a nil on `Input()`; the wrapper dereferenced it, and on `Input()` the
  input forwarder died and every later message was dropped in silence, while
  `InputContext` panicked in the caller.
- **`ppbeego` records the status the response went out with.** `Output.Body`
  (behind `ServeJSON`, `Render` and the rest) resets `Output.Status` to 0 after
  writing the header, so nearly every response was recorded as status 0 and a
  5xx never failed the span. The writer's status is read first.
- **`ppbeego`'s client filter ends its own event.** `ClientFilterChain`
  discarded the tracer `pphttp.NewHttpClientTracer` returns and ended the
  caller's instead, so a filter added twice, or a retried request,
  closed whatever event the caller had open.
- **`ppfiber`, `ppfiberv3` and `ppfasthttp` make one span per request.** A
  handler wrapped inside the middleware, or wrapped twice, started a second
  span with its own transaction id and double-counted the response time and URL
  statistics. The inner layer now records on the existing span, as
  `pphttp.NewHttpServerTracer` does. `ppkratos` and `ppkratosv3` do the same for
  a middleware registered twice or combined with `ppgrpc`.
- **`ppfasthttp` no longer fails every request completing during a graceful
  stop.** `RequestCtx.Err()` was recorded as a handler error; it reports
  `context.Canceled` only once `Server.Shutdown` has begun, and it panicked on a
  `RequestCtx` no server initialized. Nothing is recorded from it.
- **`pppgsql` reads the DSN with lib/pq's own `pq.NewConfig`.** Every DSN
  went through `pq.ParseURL`, which takes only URLs, so `host=... dbname=...`
  was rejected with an ERROR line per pooled connection and the spans carried
  no endpoint. Reading it as the driver does also records the first host of a
  multi-host DSN instead of the whole `h1,h2` list, and honors `PGHOSTADDR`.
- **`pppgxv5` copies the connection config only for a sampled query.** Each
  callback called `Conn.Config()` - a deep copy with a `tls.Config` clone -
  before the sampling check, on every query of an unsampled request.
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
  `V0_11_0_0` - `Config.Version`'s default in the sarama releases
  `ppsaramaibm` supports. Headers are now written only when injected, and not at all on such a
  producer.
- **The sarama async producer keeps transactions intact.** `CommitTxn` and
  `AbortTxn` went straight to sarama while accepted messages could still sit in
  the wrapper's buffer, so they reached sarama after the end-of-transaction
  marker: rejected as outside a transaction while the commit reported success,
  or a `WaitGroup` panic in `CommitTxn`. Both now hand sarama every accepted
  message first.
- **`Span.EventChunkSize` and `Collector.Grpc.SpanBatchSize` have an upper bound.** Only
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
- **`ppsaramaibm` `WrapSyncProducer(nil)` and `WrapAsyncProducer(nil)`
  return nil.** The async wrapper's delivery goroutine read a nil producer's
  channels and crashed the process.
