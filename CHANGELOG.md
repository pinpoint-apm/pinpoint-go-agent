# Changelog

## v2.0.0 (2026-11-11)

Every module moves to a `/v2` module path, so an application moves to v2 on
purpose: `go get -u` keeps it on v1. What to change, in order, is in
[Migrating from v1 to v2](doc/migration_v2.md).

### Breaking

- **`Tracer.JsonString` and `Tracer.AsyncSpanId` are no longer on the `Tracer`
  interface.** Neither is tracing API: `JsonString` is a test and debugging
  rendering, now `pinpoint.SpanJSON(tracer)`, and `AsyncSpanId` is gone with
  its last user, the sarama async producer, which matches an ack to its span
  by the message itself. A tracer of your own no longer has to provide either.
- **`Tracer.AddMetric(metric, value)` is `Tracer.RecordUrlStat(UrlStatEntry)`.**
  URL statistics were its only metric: the string key, `pinpoint.MetricURLStat`,
  and the `any` value checked at run time are gone, so a wrong value is a
  compile error instead of a warning. An implementation of `Tracer` of your own,
  such as a mock, renames the method; one that embeds a `Tracer` needs nothing.
- **`ppsaramaibm.HeaderAsyncSpanId` and `pinpoint.UrlStatEntry.Status` are
  gone.** The header is the ack id v1 wrote on every async message; no v2
  producer writes or reads it. `Status` was never read: the failed histogram
  follows the span's failure mark, which an entry recorded by hand sets with
  `SetFailure(ErrorCategoryHttpStatus)`. `pphttp.CollectUrlStat(tracer, url,
  method)` drops its `status` parameter with it.
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
- **The agent identity defaults to `Uid.Version` v4**, as in the Java agent,
  and an unknown value falls back to v4. v4 requires `ServiceName`: without it
  `NewAgent` returns an error, `Enable: false` included. A 3.1.x collector
  accepts a v4 agent only with an `ApiKey`; against one, set `Uid.Version: v3`,
  which needs no `ServiceName`.
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
  as `mssql-microsoft-pinpoint`, where `ppmssqldb` used `sqlserver-pinpoint`.
- **`plugin/echo` is gone.** It instrumented labstack/echo v3, which is
  end-of-life upstream with an unpatched advisory (GHSA-vfp3-v2gw-7wfq) and no
  fixed v3 release to pin, and its code was a copy of `plugin/echov4`. Use
  `plugin/echov4` or `plugin/echov5`.
- **`plugin/goelastic` serves every go-elasticsearch major.** `ppgoelastic.NewTransport` is an `http.RoundTripper` that never imports
  the client, and `elasticsearch.Config.Transport` is the same field in v7, v8
  and v9, so the three were one file under three names. Import
  `plugin/goelastic/v2`; `NewTransport` is unchanged.

### Changed

- **`ppsaramaibm.ConsumeMessageContext` and
  `ppconfluentkafka.ConsumeMessageContext` record the handler call as a span
  event** named after the handler function, as the HTTP and gRPC server
  plugins record theirs. What the handler records goes under it, and a
  goroutine tracer forked during it - by the handler, or by the gocql,
  go-redis and pgx plugins off the consumer's goroutine - now records,
  where a consumer span with no event open forked a noop that recorded
  nothing. A handler that opened an event of its own for the same call can
  drop it.
- **The `ppsaramaibm` async producer sends straight to sarama's input.**
  `Input`, `CommitTxn` and `AbortTxn` are sarama's own, and `InputContext`
  sends on the same channel instead of through a wrapper buffer of
  `ChannelBufferSize` in front of it, so a send blocks as long as one on raw
  sarama does. sarama's unbuffered input already keeps one goroutine's
  messages in order, queues a transaction's end marker behind them and refuses
  a message sent during the shutdown with `sarama.ErrShuttingDown`. A message
  `InputContext` hands over once the shutdown has closed that input is still
  dropped, with its span recording the error. A send racing that close is a
  data race, as it is on raw sarama's `Input()`. `AsyncClose` and `Close` can
  still be called more than once.
- **`NewConfig` returns an error for a config file that exists but cannot be
  read** - a syntax error, an unsupported extension. The `Config` comes back
  complete on defaults and the other sources, so `cfg, _ := NewConfig(...)`
  behaves as before; a missing file is still optional and only logged.
- **An unset `AgentName` is `<hostname>-<pid>`**, such as `web-01-4321`,
  instead of the generated agent id, so the instances of an application can be
  told apart in the web UI without setting one. An invalid `AgentName` falls
  back to the same name, with the warning as before. The Java agent still
  falls back to the agent id.
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
- The new `SQL.RemoveComments` defaults to `true`: comments are dropped from the
  normalized SQL, which changes the text, and with it the SQL id, of every
  statement that carries one. `false` keeps them as v1 did.
- `Stat.CollectInterval` takes 1000 to 10000 ms, the bounds of Java's
  `DefaultAgentStatMonitor`; a value outside them falls back to the 5000 ms
  default with a warning. v1 took any value, so an interval set above 10
  seconds now runs at 5.
- URL statistics recorded without a URI template are now keyed as `/NULL`
  (Java's `URITemplate.NULL_URI`, also used by the C++ agent) instead of
  `UNKNOWN_URL`. Server-side history under the old `UNKNOWN_URL` key does not
  carry over to the new key.
- **The log file rotation is `github.com/elastic/lumberjack`**, Elastic's
  maintained hard fork of `gopkg.in/natefinch/lumberjack.v2` (v2.0.0 before),
  at commit c5b60bbeaaab, which has no release tag. Its `Close` stops the
  backup cleanup goroutine, which the unmaintained original leaked on every
  `Log.Output` change. A log file it creates is `0600` instead of `0644` and a
  log directory `0755` instead of `0744`; an existing file keeps its mode,
  also across rotation. v2.0.0 had no go.mod, so its test dependencies
  `BurntSushi/toml` and `gopkg.in/yaml.v2` were in the module graph of every
  application importing the agent.

### Added

- **`ClusterNamespace`**, Java's `profiler.cluster.namespace`. When set, it is
  sent as `Pinpoint-pAppNamespace` on every outbound call, and an inbound call
  carrying a different value starts a new transaction instead of continuing
  the caller's, as Java's `DefaultNameSpaceChecker` decides it; a call without
  the header is continued. Until now the agent never sent the header and
  continued every trace, so two Pinpoint clusters on one network could not be
  kept apart. Not dynamic: a change takes effect after a restart. A value
  outside printable ASCII is ignored with a warning, since gRPC metadata
  refuses it and every traced gRPC call would fail.
- **`pinpoint.UnwrapSQLConn`** returns the driver's own connection under one
  `WrapSQLDriver` wrapped, for a `database/sql` `Conn.Raw` callback: it is
  handed the wrapper, so a type assertion to the driver's type - pgx's
  documented `driverConn.(*stdlib.Conn)` - failed and panicked.
- **Span batches and the stat stream are gzip-compressed**
  (`Collector.Grpc.Compression`, `WithCollectorGrpcCompression`: `gzip`, the
  default, or `none`), as in the Java agent, whose two keys this one option
  stands for. A collector accepts gzip without configuration; one that answers
  it cannot decompress gzip gets the following batches and streams
  uncompressed, with a warning in the log.
  Unlike the Java agent, every stat message is compressed, a lone small one
  included, since grpc-go cannot choose per message. A gzip writer grpc-go has
  to make again after a garbage collection allocates about 800 KB, so a
  process that collects often may prefer `none`
  ([Collector.Grpc.Compression](doc/config.md#collectorgrpccompression)).
- **`pinpoint.ReloadConfig(yaml)`** applies a complete configuration, in the
  config file's YAML form, to the running agent, for a process that manages
  the agent's configuration itself - a control plane, an admin endpoint, a
  `SIGHUP` handler - and so has no config file for the agent to watch. The
  dynamic options take the YAML's values and one the YAML omits goes back to
  its config function value or default; a flag, environment variable or
  `Config.Set()` value still wins. The returned `ConfigReloadResult` says
  whether the reload was applied, why it was refused - no running agent, a
  config file in use, invalid YAML, a value of the wrong type or a
  configuration `NewAgent` would refuse - and lists in `RestartRequired` the
  non-dynamic options the YAML would have changed. A refused reload changes
  nothing. See [Reloading from YAML](doc/config.md#reloading-from-yaml).
- **`Collector.AgentInfo.SendArgs`** (`WithCollectorAgentInfoSendArgs`) turns
  off sending the process's command line arguments with the agent information,
  for a command line that carries a password or a token. On by default, as
  before.
- **One request, one span when server instrumentation is layered.**
  `pphttp.NewHttpServerTracer` and the `ppgrpc` server interceptors reuse the
  tracer already in the request context — a middleware installed twice, or a
  framework middleware inside `WrapHandler` — instead of starting a
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
  is on (default `false`; Java defaults on). An adapter that holds the query
  outside a net/http request passes it to `RecordHttpServerRequestWithQuery`.
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

- **URL statistics no longer pile up while the collector is down.** Every
  closed 30 second tick became a message of its own in the stat queue, so with
  `Http.UrlStat.Enable` an outage grew the agent for hours, up to
  `Stat.QueueSize` (1024) messages of up to `Http.UrlStat.LimitSize` URLs each:
  some 200MB at the defaults, about 0.4MB per 1000 URLs. The 5 retained ticks
  the documentation promised were never the bound. The ticks now go to the
  sender one message at a time and otherwise stay among those 5, which is all
  an outage of any length costs.
  On recovery and shutdown the sender splits a retained snapshot at
  `Collector.Grpc.MaxSendMessageSize`, keeping each URL/tick histogram whole,
  so several individually sendable ticks cannot be lost as one oversized
  message. Only an entry too large to fit by itself is dropped, with a
  rate-limited warning.
  Delivery stays at most once: the messages of a split snapshot are sent back
  to back without an acknowledgement each, so a stream that breaks part-way
  can lose the ones sent before the message that failed, and on shutdown the
  remaining statistics are sent only on a stat stream that does not have to be
  reopened.
- **A goroutine tracer's `SetLogging()` and `Span().SetError()` reach the
  collector.** A goroutine tracer goes out as a `PSpanChunk`, which has
  neither field, so both were stored where nothing sent them: a log line
  written only from a goroutine - a Kafka handler's fan-out, a
  `WrapGoroutine` worker - gave its transaction no log link, and the error
  message was lost while the failure flag still reached the root.
  `SetLogging()` now marks the request's span, as Java keeps the flag on the
  shared `TraceRoot`, and `Span().SetError()` is recorded on the goroutine's
  Goroutine Invocation event.
- **`ppfasthttprouter` traces the routes of `r.Group(...)`.** fasthttp/router's
  `Group` registered on the router underneath, past the wrapping, so group
  routes ran untraced and without the `ppfasthttp.CtxKey` value, whose
  unchecked type assertion then panicked. `Router.Group` returns a
  `ppfasthttprouter.Group`, nested groups included, and the URL statistics get
  the group's prefix with the route's path.
- **A nil pointer error no longer panics in `SetError` or loses the exception
  metadata.** An error holding a nil pointer (`var e *MyErr; return e`)
  compares != nil. `SetError` called its `Error()` on the request goroutine,
  and a wrapper whose cause was a nil pointer carried it to the metadata
  sender, whose recovered panic dropped the span's whole exception metadata.
  Its message is now `<nil>`, and a nil pointer cause ends the chain.
- **`pphttp` refuses a nil handler at registration**, as net/http does,
  instead of panicking on every request, and **masks a password in the
  recorded client URL**.
- **The sarama async producer returns a message sent during its shutdown on
  `Errors()`** with `sarama.ErrShuttingDown`, as raw sarama's does. It was
  dropped, with only its span recording the error, and `Close()` left it out
  of the `ProducerErrors` it returns. `Input()` and `InputContext()` send on
  sarama's own input: two wrapper channels picked between at random reordered
  one goroutine's messages across the two, and a key's messages on its
  partition with them.
- **`ppbeego` records the status the response went out with.** `Output.Body`
  (behind `ServeJSON`, `Render` and the rest) resets `Output.Status` to 0 after
  writing the header, so nearly every response was recorded as status 0 and a
  5xx never failed the span. The writer's status is read first.
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
- **`pppgxv5` requires pgx v5.11.0**, whose `Exec` and `Query` no longer skip
  the query end when deallocating invalidated statements fails, and follows
  pgx's call paths more closely: the second batch end pgx reports for a batch
  that fails before it is sent no longer ends the caller's own event, a
  `CopyFrom` is recorded whole at its end, since pgx skips the end when it
  cannot describe the target, a connect is recorded on a goroutine tracer of
  its own, since pgxpool connects on its own goroutine, and the wrapped stdlib
  driver opens a name from `stdlib.RegisterConnConfig` instead of failing it
  as an unparsable DSN.
- **`pphttp`'s response writer keeps `http.CloseNotifier`.** gin's `c.Stream`
  asserts it unchecked, so every streamed gin response behind
  `pphttp.WrapHandler` (and the chi, gorilla, beego and httprouter plugins that
  use it) panicked with a 500. A writer without it gets a channel that never
  fires.
- **The sarama async producer keeps transactions intact.** `CommitTxn` and
  `AbortTxn` went straight to sarama while accepted messages could still sit in
  the wrapper's buffer, so they reached sarama after the end-of-transaction
  marker: rejected as outside a transaction while the commit reported success,
  or a `WaitGroup` panic in `CommitTxn`. No message waits in a wrapper buffer
  now: every one sent before `CommitTxn` or `AbortTxn` is on sarama's input,
  ahead of the marker.
- **`ppechov4` keeps one route table per Echo.** Each `Middleware()` value
  copied all of `e.Routes()` on its first sampled request, so route- or
  group-level middleware cost instances x routes, about 200 MB for 1000 routes
  with a `Middleware()` each. The values on one Echo now share its table,
  which is dropped once the Echo is collected.
- **`ppbeego` documents a client filter that is safe under concurrency.**
  httplib copies its default settings into every request, spare capacity
  included, so the documented `req.AddFilters(ppbeego.ClientFilterChain(tracer))`
  appended into one array the requests share: concurrent requests ran each
  other's filters and sent each other's trace ids. Register
  `ClientFilterChain(nil)` once with `httplib.AddDefaultFilter` and build
  requests with `httplib.NewBeegoRequestWithCtx`.
