# Pinpoint Go Agent Plug-ins

Go compiles to native code, so nothing can be instrumented automatically. The
plugin packages are the next best thing: each one wraps a popular library so
that instrumenting it is a one- or two-line source change instead of hand-built
spans. Anything they do not cover, you write yourself with the
[custom instrumentation API](instrument.md).

## How to use them

Every plugin is its **own Go module**, so you only take on the dependencies of
the libraries you actually instrument:

```bash
go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin/v2
```

Each package name is usually the directory prefixed with `pp` (`plugin/gin` is
`ppgin`); the exceptions are `plugin/mongodriver` (`ppmongo`),
`plugin/mongodriverv2` (`ppmongov2`), `plugin/sarama-IBM` (`ppsaramaibm`) and
`plugin/mssql-microsoft` (`ppmssqlmicrosoft`). Every plugin directory carries a
`README.md` and, except the shared `plugin/mongomonitor` helper, a runnable
example under [example/](/example) (`example/<plugin>`) — those are the
authoritative reference for the plugin's options.

A plugin needs a running agent, and most of them need a **tracer in the
context** of the call they instrument. That is the one recurring mistake: an
outgoing call built on `context.Background()` records nothing, no matter how
well the client is wrapped. See
[Context propagation](instrument.md#context-propagation).

A plugin records on the tracer in the context it is given, from whichever
goroutine makes the call. Handing the request's context to several goroutines
at once - an `errgroup` running queries in parallel - puts them all on one
tracer, which interleaves their events
([contract 1](api_contracts.md#1-a-tracer-tracks-one-goroutine)). Give each
goroutine a tracer of its own:

```go
load := pinpoint.FromContext(ctx).WrapGoroutine("loadUser", func(ctx context.Context) {
    err = db.QueryRowContext(ctx, q, id).Scan(&name)
}, ctx)
g.Go(func() error { load(); return err })
```

The go-redis v9 hook and the gocql observers are the exception: they notice a
call made off the tracer's goroutine and record it on a goroutine tracer of
their own.

## Reading the tables

* **Entry point** — what you call. `Middleware()` registers with the
  framework's own middleware chain; `Wrap*` wraps one handler, client or
  connection; a driver name is passed to `sql.Open`.
* Plugins with a version suffix (`echov4`, `goredisv9`, ...) track that major
  version of the library. Pick the one matching your dependency; they are
  separate modules and cannot be mixed for one library.
* Every wrapper is safe to leave in place when the agent is disabled - it
  passes through and records nothing - or the transaction is unsampled: no
  span or event is sent, but the request still counts in the URL statistics
  and outgoing calls carry the unsampled flag.

---

## HTTP servers and web frameworks

These open the root span of a transaction: they extract the incoming
distributed-tracing headers, put a tracer where the handler can reach it
([Getting the tracer in a handler](#getting-the-tracer-in-a-handler)), and
record the URL, status and configured headers. gRPC and kratos are the
exceptions to the last part: they record no HTTP status, and none of the
`Http.Server.*` options apply to them. kratos also names the span after the
kratos operation (`/helloworld.Greeter/SayHello`), for HTTP requests too.

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/http](/plugin/http) | Go standard `net/http` | `WrapHandler`, `WrapHandlerFunc`, `NewServeMux` |
| [plugin/beego](/plugin/beego) | [beego/beego/v2](https://github.com/beego/beego) | `ServerFilterChain` |
| [plugin/chi](/plugin/chi) | [go-chi/chi](https://github.com/go-chi/chi) | `Middleware`, `WrapHandler`, `WrapHandlerFunc` |
| [plugin/echov4](/plugin/echov4) | [labstack/echo/v4](https://github.com/labstack/echo) | `Middleware`, `WrapHandler` |
| [plugin/echov5](/plugin/echov5) | [labstack/echo/v5](https://github.com/labstack/echo) | `Middleware`, `WrapHandler` |
| [plugin/fasthttp](/plugin/fasthttp) | [valyala/fasthttp](https://github.com/valyala/fasthttp) | `WrapHandler` |
| [plugin/fasthttprouter](/plugin/fasthttprouter) | [fasthttp/router](https://github.com/fasthttp/router) | `New` (wrapped router) |
| [plugin/fiber](/plugin/fiber) | [gofiber/fiber/v2](https://github.com/gofiber/fiber) | `Middleware`, `WrapHandler` |
| [plugin/fiberv3](/plugin/fiberv3) | [gofiber/fiber/v3](https://github.com/gofiber/fiber) | `Middleware`, `WrapHandler` |
| [plugin/gin](/plugin/gin) | [gin-gonic/gin](https://github.com/gin-gonic/gin) | `Middleware`, `WrapHandler` |
| [plugin/gorilla](/plugin/gorilla) | [gorilla/mux](https://github.com/gorilla/mux) | `Middleware`, `WrapHandler`, `WrapHandlerFunc` |
| [plugin/httprouter](/plugin/httprouter) | [julienschmidt/httprouter](https://github.com/julienschmidt/httprouter) | `New` (wrapped router), `WrapHandle` |
| [plugin/kratos](/plugin/kratos) | [go-kratos/kratos/v2](https://github.com/go-kratos/kratos) | `ServerMiddleware` |
| [plugin/kratosv3](/plugin/kratosv3) | [go-kratos/kratos/v3](https://github.com/go-kratos/kratos) | `ServerMiddleware` |
| [plugin/grpc](/plugin/grpc) | [grpc/grpc-go](https://github.com/grpc/grpc-go) | `UnaryServerInterceptor`, `StreamServerInterceptor` |

[go-kit/kit](https://github.com/go-kit/kit) needs no plugin of its own: its
HTTP and gRPC transports sit directly on `net/http` and grpc-go, so the
`plugin/http` and `plugin/grpc` entry points above trace them. See
[example/gokit](/example/gokit).

```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin/v2"

router := gin.Default()
router.Use(ppgin.Middleware())
```

```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"

http.HandleFunc("/", pphttp.WrapHandlerFunc(index))
```

Which URLs, methods, headers and cookies are recorded is controlled by the
`Http.Server.*` options in [Configuration](config.md#httpserverstatuscodeerrors),
or programmatically by the `WithHttpServer*` config options in the
`plugin/http` package. In gin, echo and fiber an error the handler returns (or
adds with `c.Error()`) fails the transaction whatever the response status, so a
4xx that comes as an error counts as a failure; see
[Http.Server.RecordHandlerError](config.md#httpserverrecordhandlererror) for
how to leave those to the status.

### Getting the tracer in a handler

A call joins the transaction only when it is given the context that carries the
tracer, and each framework keeps it somewhere else. In four of them a value
that is also a `context.Context` is the wrong one: it compiles and runs, and
the call records nothing.

| Framework | Pass to outgoing calls | Not this |
|---|---|---|
| `net/http`, chi, gorilla, httprouter | `r.Context()` | |
| gin | `c.Request.Context()` | `c`, unless the engine sets `ContextWithFallback = true` |
| echo v4, v5 | `c.Request().Context()` | |
| fiber v2 | `c.UserContext()` | `c.Context()`, which is the `*fasthttp.RequestCtx` |
| fiber v3 | `c.Context()` | `c` or `c.RequestCtx()` |
| fasthttp, fasthttprouter | `ctx.UserValue(ppfasthttp.CtxKey).(context.Context)` | `ctx` |
| beego | `ctx.Request.Context()`, `c.Ctx.Request.Context()` in a controller | |
| gRPC, kratos | the handler's `ctx`; `stream.Context()` in a gRPC stream handler | |

### Where the middleware goes

Register it before the routes, right after the recovery middleware and ahead of
everything else:

```go
router := gin.New()
router.Use(gin.Recovery(), ppgin.Middleware(), auth())
router.GET("/users/:id", getUser)
```

* gin and fiber apply a middleware only to the routes registered after its
  `Use`; a route added earlier is never traced. echo and gorilla apply it to
  every route, and chi panics on a late `Use`.
* A recovery middleware inside the pinpoint one writes the 500 itself and
  swallows the panic, so the trace keeps only the status. Outside it, the panic
  passes through the pinpoint layer, which records it as an error named
  `panic` and re-raises it for the recovery to handle
  ([Panics](instrument.md#panics)).
* A request that a middleware ahead of pinpoint rejects - auth, rate limiting,
  CORS - is neither traced nor counted. A status that an outer middleware
  writes after the handler returns is not seen either: the trace records the
  status the handler left, 200 when it wrote none.
* A timeout middleware that runs the handler on a goroutine of its own goes
  outside: `http.TimeoutHandler(pphttp.WrapHandler(h), d, msg)`. The other way
  round the span is used from two goroutines, which logs
  `span is shared by more than one goroutine`, and a handler that outlives the
  timeout records on an ended span.
* gRPC: list ppgrpc's interceptors first in `grpc.ChainUnaryInterceptor()`
  and `grpc.ChainStreamInterceptor()`.

### URL statistics

With [Http.UrlStat.Enable](config.md#httpurlstatenable) on, a request is
counted under its route pattern - `/users/{id}` rather than `/users/1234` - which
is what keeps the statistics list readable
([contract 8](api_contracts.md#8-keep-operation-and-error-names-low-cardinality)).
The span itself carries the request path, as the Java agent's does. Where the
pattern comes from depends on the entry point:

| Entry point | Counted under | A path no route matches |
|---|---|---|
| gin, echo and chi `Middleware()`, beego `ServerFilterChain` | the route pattern | traced, counted under `/NULL` |
| fiber `Middleware()` | the route pattern | traced, counted under the middleware's own path (`/` for `app.Use(...)`) |
| gorilla `Middleware()` | the path template | not traced: gorilla runs middleware only on a match |
| `pphttp.NewServeMux` | the pattern as registered | not traced |
| `pphttprouter.New`, `ppfasthttprouter.New` (its groups too) | the registered path | not traced |
| `ppfasthttp.WrapHandler(h, pattern)`, `pphttprouter.WrapHandle(h, path)` | the pattern argument; nothing without one | - |
| `pphttp.WrapHandler`, `pphttp.WrapHandlerFunc` | nothing | - |
| gRPC, kratos | nothing | - |

The `WrapHandler` of gin, echo, fiber, chi and gorilla reports the same pattern
as their `Middleware()`, but only for the routes it wraps; the middleware also
covers the paths no route matches. `/NULL` is where a request that reported no
URL is counted. With `Http.UrlStat.WithMethod` on, a `NewServeMux` pattern that
names its method shows it twice (`GET GET /users/{id}`).

Two layers on one request make one span: `Middleware()` plus `WrapHandler` on a
route, a middleware registered twice, or `pphttp.WrapHandler` around a router
with its own pinpoint middleware. The inner layer finds the tracer in the
context and adds only its span event, and the URL statistics keep the first
pattern either layer reports. That is how a plain `http.ServeMux` is traced as a
whole - 404s included - without losing its patterns:

```go
handler := pphttp.TraceHandler(mux, "HTTP Server", "ServeMux",
    func(r *http.Request) string { return r.Pattern })
http.ListenAndServe(":8000", handler)
```

### Long-lived connections

A transaction lasts as long as its handler, so each WebSocket, server-sent
event stream or gRPC stream is one transaction:

* It reaches the UI when the connection closes, with the connection's lifetime
  as its response time, in the URL statistics too. Exclude such endpoints with
  [Http.Server.ExcludeUrl](config.md#httpserverexcludeurl) when that skews the
  numbers.
* Events recorded per message pile up on that one span; past
  `Span.MaxCallStackSequence` (5000) the rest are dropped.
* A gorilla/websocket upgrade is recorded with status 200: the upgrader writes
  its 101 on the hijacked connection.
* A handler that sends and receives on two goroutines gives the second one a
  tracer of its own ([Instrument Goroutine](instrument.md#instrument-goroutine)).

The response writer the http plugin wraps keeps `http.Flusher`,
`http.Hijacker` and `http.CloseNotifier`, and `http.ResponseController` reaches
the original through `Unwrap`, so streaming and upgrades work through it.
`http.Pusher` (HTTP/2 server push) is not carried.

## HTTP and RPC clients

These record an outgoing call as a span event on the current transaction and
inject the tracing headers so the next node continues it.

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/http](/plugin/http) | Go standard `net/http` client | `WrapClient`, `WrapClientWithContext`, `DoClient` |
| [plugin/fasthttp](/plugin/fasthttp) | [valyala/fasthttp](https://github.com/valyala/fasthttp) client | `DoClient` |
| [plugin/beego](/plugin/beego) | beego `httplib` client | `ClientFilterChain` |
| [plugin/grpc](/plugin/grpc) | [grpc/grpc-go](https://github.com/grpc/grpc-go) | `UnaryClientInterceptor`, `StreamClientInterceptor` |
| [plugin/kratos](/plugin/kratos) | [go-kratos/kratos/v2](https://github.com/go-kratos/kratos) | `ClientMiddleware` |
| [plugin/kratosv3](/plugin/kratosv3) | [go-kratos/kratos/v3](https://github.com/go-kratos/kratos) | `ClientMiddleware` |

```go
client := pphttp.WrapClient(nil)

req, _ := http.NewRequest("GET", "http://backend:9000/hello", nil)
req = req.WithContext(r.Context())   // the tracer travels in the context

resp, err := client.Do(req)
```

The `req.WithContext(r.Context())` line is not optional. Without it the
wrapped client has no tracer to work with, and the call chain stops at this
node.

What the client event covers:

* With `WrapClient` the event ends when the response headers arrive; reading
  the body is not part of it. Every round trip is an event of its own, each
  redirect hop too. `DoClient` makes one event around the call it is given.
* A 4xx or 5xx response is recorded as the event's status code, not as an
  error: only a request that gets no response fails the event.
* The request's own context wins. The context given to
  `WrapClientWithContext` is used only for a request whose context carries no
  tracer.
* A library that takes an `http.RoundTripper` rather than a client gets the
  instrumented one from `pphttp.WrapClient(&http.Client{Transport: rt}).Transport`.

## SQL databases

These are `database/sql` drivers registered under a `-pinpoint` name. Change
the driver name in `sql.Open` and pass a context carrying the tracer to the
`...Context` query methods.

| Plugin package | Instrumented package | Driver name |
|---|---|---|
| [plugin/mysql](/plugin/mysql) | [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) | `mysql-pinpoint` |
| [plugin/pgsql](/plugin/pgsql) | [lib/pq](https://github.com/lib/pq) | `pq-pinpoint` |
| [plugin/pgxv5](/plugin/pgxv5) | [jackc/pgx/v5](https://github.com/jackc/pgx) | `pgxv5-pinpoint`, or `NewTracer()` for the native pgx API |
| [plugin/oracle](/plugin/oracle) | [sijms/go-ora/v2](https://github.com/sijms/go-ora) | `oracle-pinpoint` |
| [plugin/oraclev3](/plugin/oraclev3) | [sijms/go-ora/v3](https://github.com/sijms/go-ora) | `oraclev3-pinpoint` |
| [plugin/mssql-microsoft](/plugin/mssql-microsoft) | [microsoft/go-mssqldb](https://github.com/microsoft/go-mssqldb) | `mssql-microsoft-pinpoint` |
| [plugin/gorm](/plugin/gorm) | [go-gorm/gorm](https://github.com/go-gorm/gorm) | `Open` (wraps `gorm.Open`) |

```go
import _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql/v2"

db, _ := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")

ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) FROM tables")
```

The blank import is enough — the driver registers itself in `init()`. Use the
`Context` variants of the query methods; the plain `Query`/`Exec` methods have
nowhere to carry a tracer and produce no span event. A `sql.Conn.Raw` callback
is handed the instrumented connection: `pinpoint.UnwrapSQLConn` returns the
driver's own one for a type assertion such as pgx's `*stdlib.Conn`.

**Only a pool opened under the `-pinpoint` name is traced.** A library that
opens its own pool goes around the plugin and records nothing:

* `sql.OpenDB` over the driver's own connector - `mysql.NewConnector`,
  `mssql.NewConnector`, pgx's `stdlib.OpenDB` and `OpenDBFromPool`. Open by
  name instead: `sql.Open("mysql-pinpoint", cfg.FormatDSN())`.
* pgx used natively or through `pgxpool`. Put the tracer on the config the
  pool is built from, `cfg.ConnConfig.Tracer = pppgxv5.NewTracer()`; a
  `stdlib.OpenDB` pool over that config is then traced too. Do not also open
  those connections through `pgxv5-pinpoint`, or every query is recorded twice.
* GORM's own dialectors, `mysql.Open(dsn)` and `postgres.Open(dsn)`. `ppgorm`
  records the GORM call only; the SQL, its bind values and the database node
  come from the driver underneath, so give GORM a traced pool:
  `mysql.New(mysql.Config{DriverName: "mysql-pinpoint", DSN: dsn})`,
  `postgres.New(postgres.Config{DriverName: "pgxv5-pinpoint", DSN: dsn})`, or
  `Conn: db`.
* ent's `entsql.Open`, which takes the SQL dialect from the driver name. Open
  the pool yourself and name the dialect: `entsql.OpenDB(dialect.Postgres, db)`.

sqlx chooses its bind variables by driver name and does not know the
`-pinpoint` names, so `Rebind`, `In` and the `Named...` methods write `?`, which
PostgreSQL, SQL Server and Oracle reject. Wrap the pool under the real name,
`sqlx.NewDb(db, "postgres")`, or register the name once:
`sqlx.BindDriver("pq-pinpoint", sqlx.DOLLAR)`. sqlc needs nothing; its
generated code always takes a context.

**The database name in the DSN is the node.** Queries are drawn to a node named
after the DSN's database, with the host - without its port - as the endpoint. A
DSN that names no database (`root:pw@tcp(db:3306)/`, a PostgreSQL URL without a
path, a SQL Server DSN without `database=`) still records every query in the
call tree, but no database node appears on the server map: an event without a
destination is sent without its endpoint too.

**What one event covers.** Each `ExecContext` or `QueryContext` is one event,
timed around the driver call alone. Waiting for a free pooled connection,
opening a new one, the prepare round trip `database/sql` makes for a MySQL query
with arguments (unless `interpolateParams=true`), and reading the returned rows
all fall outside it, so an exhausted pool or a slow `rows.Next()` loop shows up
as time in the caller. `Prepare` is not recorded: a prepared statement is traced
through the context of its own `ExecContext`/`QueryContext`, not the one given
to `PrepareContext`. `Begin`, `Commit` and `Rollback` are recorded on the
context given to `BeginTx`, so `db.Begin()` records the statements of a
transaction but not its boundaries. A statement over 1 MiB is recorded without
its text.

SQL statements are normalized - literals replaced with placeholders - so the UI
groups repeated statements. The literals themselves are still sent alongside,
for the UI to show the original statement: `SQL.TraceBindValue` turns off bind
values only, so pass sensitive values as arguments, not in the SQL text. See
[Best Practices](config.md#best-practices) for what each plugin records.

A transaction that runs [SQL.ErrorCount](config.md#sqlerrorcount) statements
(100 by default) is marked failed with no error on any event, which is how an
N+1 loop stands out; statements recorded by pgx's native tracer and by gocql
count too. Raise the option for batch work, or keep the count without the
verdict with `Span.ErrorMarkExclude: sql`.

## Cache, document, and search stores

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/goredisv8](/plugin/goredisv8) | [go-redis/redis/v8](https://github.com/go-redis/redis) | `NewHook`, `NewClusterHook` |
| [plugin/goredisv9](/plugin/goredisv9) | [redis/go-redis/v9](https://github.com/redis/go-redis) | `NewHook`, `NewClusterHook` |
| [plugin/redigo](/plugin/redigo) | [gomodule/redigo](https://github.com/gomodule/redigo) | `Dial`, `DialContext`, `DialURL`, `WrapConn`; the context goes with each command through `redis.DoContext` |
| [plugin/rueidis](/plugin/rueidis) | [redis/rueidis](https://github.com/redis/rueidis) | `NewHook` |
| [plugin/gomemcache](/plugin/gomemcache) | [bradfitz/gomemcache](https://github.com/bradfitz/gomemcache) | `NewClient` |
| [plugin/mongodriver](/plugin/mongodriver) | [mongodb/mongo-go-driver](https://github.com/mongodb/mongo-go-driver) | `NewMonitor` |
| [plugin/mongodriverv2](/plugin/mongodriverv2) | [mongodb/mongo-go-driver/v2](https://github.com/mongodb/mongo-go-driver) | `NewMonitor` |
| [plugin/mongomonitor](/plugin/mongomonitor) | shared by the two MongoDB plugins; not imported directly | - |
| [plugin/gocql](/plugin/gocql) | [gocql](https://github.com/gocql/gocql) | `NewObserver` |
| [plugin/gocqlv2](/plugin/gocqlv2) | [gocql/v2](https://github.com/apache/cassandra-gocql-driver) | `NewObserver` |
| [plugin/gohbase](/plugin/gohbase) | [tsuna/gohbase](https://github.com/tsuna/gohbase) | `NewClient` |
| [plugin/goelastic](/plugin/goelastic) | [elastic/go-elasticsearch](https://github.com/elastic/go-elasticsearch) v7, v8 and v9 | `NewTransport` |

Most of these clients hook into the library's own observability seam — a hook,
monitor, observer or `RoundTripper` — so you register once at construction
(gomemcache, gohbase and redigo wrap the client or connection instead):

```go
// redis/go-redis/v9
opts := &redis.Options{Addr: "localhost:6379"}
client := redis.NewClient(opts)
client.AddHook(ppgoredisv9.NewHook(opts))

// mongo-go-driver
opts := options.Client().SetMonitor(ppmongo.NewMonitor())

// gocql
observer := ppgocql.NewObserver()
cluster.QueryObserver = observer
cluster.BatchObserver = observer
```

The tracer still has to reach each call in its context — `client.Get(ctx, key)`
with a context from `pinpoint.NewContext()`, or the request context in a
handler. gohbase takes it from the context of the hrpc request, and
gomemcache, whose calls take no context, from the copy `mc.WithContext(ctx)`
returns.

Each store is drawn on the server map as one node: `REDIS`, `MEMCACHED`,
`HBASE` and `ElasticSearch` by name, MongoDB by database and Cassandra by
keyspace, so a gocql session without `cluster.Keyspace` can lose its node. The
endpoint is the address the client was configured with - every address,
comma-joined, for a cluster or a server list, and the ZooKeeper quorum for
HBase - while the MongoDB, Cassandra, Elasticsearch and redigo plugins report
the host actually contacted. The go-redis hooks read it from
`Options.Addr` or `ClusterOptions.Addrs`; for a Sentinel, Ring or universal
client, pass `&redis.Options{Addr: "<label>"}`, since `NewHook(nil)` reports
`unknown`.

**What counts as a failure.** A miss is not one: `redis.Nil`, a rueidis nil
reply, `memcache.ErrCacheMiss` and GORM's `ErrRecordNotFound` leave the event
clean. Some real failures do not reach the event either:

* MongoDB returns write errors - a duplicate key, a failed validation - in a
  reply the driver reports as a successful command, so the event shows success
  while `InsertOne` returns the error.
* goelastic records transport errors only. An error status from Elasticsearch
  (400 for a bad query, 404 for a missing index, 429, 5xx) is a response, and
  its status is not recorded.
* A SQL event carries the error of the call that starts the query; one met
  while reading the rows (`rows.Err()`) is not on it.

Where such a failure matters, record it yourself, on the span
(`tracer.Span().SetError(err)`) or on an event of your own: the plugin's event
has ended by the time the error is known.

## Message queues

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/sarama-IBM](/plugin/sarama-IBM) | [IBM/sarama](https://github.com/IBM/sarama) | producers: `NewSyncProducer`, `NewAsyncProducer`, `WrapSyncProducer`, `WrapAsyncProducer`; consumers: `ConsumeMessageContext`, `NewContext` |
| [plugin/confluentkafka](/plugin/confluentkafka) | [confluentinc/confluent-kafka-go](https://github.com/confluentinc/confluent-kafka-go) | producers: `NewProducer`, `WrapProducer`, `ProduceContext`; consumers: `ConsumeMessageContext`, `NewContext` |

A queue is a trace boundary, so it has two halves. The producer records a span
event on the current transaction and writes the tracing context into the
message headers; the consumer opens a **new root span** per message that
continues that transaction.

Send through a `Context` method: `SendMessageContext`, `SendMessagesContext`
or `InputContext` with sarama, `ProduceContext` with confluent. The plain
`SendMessage`, `SendMessages`, `Input` and `Produce` produce without tracing,
since they carry no context. The consumer side is `ConsumeMessageContext` over
a context from `NewContext`, which carries the broker addresses so the UI can
show the Kafka node; each plugin README has the full producer and consumer
code.

**The consumer span lasts as long as the handler.** `ConsumeMessageContext`
ends it when the handler returns, so call it where the message is processed: in
a worker pool, hand the workers the message and call `ConsumeMessageContext` in
the worker, not in the receive loop. Calls made with the handler's context
after it returned record nothing and inject no headers, and fanning out from
inside the handler needs a tracer per goroutine
([Instrument Goroutine](instrument.md#instrument-goroutine)). There is no batch
span: every message is its own transaction, and a bulk step run after the
handlers return belongs to none of them - give it a span of its own
([Jobs, consumers and workers](instrument.md#jobs-consumers-and-workers)).

Each message is recorded as `kafka://topic=<t>?partition=<p>&offset=<o>`, with
the topic, partition and offset as annotations. Its broker is the first address
given to `NewContext` (`Unknown` if it was given none); without a `NewContext`
context, the broker the producer recorded, or `Unknown` for a message from an
uninstrumented producer. An error the handler
returns fails the transaction, and a panic is recorded and re-raised. A message
carrying a Pinpoint context follows the producer's sampling decision, capped by
`Sampling.ContinueThroughput`; one without starts a new transaction at this
agent's sampling rate. At the default rate a busy consumer sends a span per
message, which `Sampling.NewThroughput` and `Sampling.ContinueThroughput`
bound.

The context travels in record headers, which Kafka has since 0.11. A sarama
`Config.Version` set below `V0_11_0_0` - sarama's own default is newer - makes
the producer write no header (it still records its span event; sarama would
reject every message carrying one) and the consumer read none, so every message
starts a new transaction.

## Logging integration

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/slog](/plugin/slog) | [log/slog](https://pkg.go.dev/log/slog) | `NewHandler`, `NewAttrs` |
| [plugin/logrus](/plugin/logrus) | [sirupsen/logrus](https://github.com/sirupsen/logrus) | `NewHook`, `NewField`, `NewEntry`, `NewLoggerEntry` |
| [plugin/zap](/plugin/zap) | [uber-go/zap](https://github.com/uber-go/zap) | `NewField`, `NewLogger` |

These collect nothing on their own. They stamp the transaction and span id onto
your log lines, which is what lets the Pinpoint UI jump from a span to the
matching log entry:

```go
logger := slog.New(ppslog.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
logger.ErrorContext(ctx, "something failed")

// or per-call, where the tracer is at hand
logger.LogAttrs(ctx, slog.LevelError, "something failed", ppslog.NewAttrs(tracer)...)
```

The slog handler reads the tracer from the context the log call is given, so use
the `*Context` methods of `slog.Logger`; a plain `logger.Error` passes
`context.Background()` and gets no ids. logrus takes a hook (`NewHook`) the same
way; zap gets no context from `zapcore.Core`, so it is per-call (`NewField`) or
per-request (`NewLogger`). The logrus and zap READMEs have the code.

Only a **sampled** transaction gets the ids. A line logged under an unsampled
one, with no tracer in the context, or with the agent disabled is written
unchanged, so searching your logs by transaction id finds the transactions
Pinpoint shows, not every request. The span is marked as logged when the ids are
built: by `NewField`, `NewAttrs` or a per-request logger (`ppzap.NewLogger`,
`pplogrus.NewEntry`, `NewLoggerEntry`) as soon as it is created, whether or not
it logs anything; by the slog handler and the logrus hook only when they write a
line.

Another logging library needs no plugin: build the two fields yourself, as
described in [Correlating your logs](instrument.md#correlating-your-logs).

---

## What if there is no plugin?

Write the instrument by hand — it is a handful of lines, and the plugins
themselves are built on exactly the same public API:

* An entry point that starts a transaction (a queue consumer, a scheduled job,
  a custom protocol server) →
  [Span](instrument.md#span) and
  [Jobs, consumers and workers](instrument.md#jobs-consumers-and-workers).
* An outbound call or an internal operation you want on the call stack →
  [SpanEvent](instrument.md#spanevent).
* A call to another Pinpoint-traced node →
  [Distributed tracing context](instrument.md#distributed-tracing-context).
* A database client that reports each operation through a hook or observer,
  after it ran →
  [Recording a query after it ran](instrument.md#recording-a-query-after-it-ran).
* A Kafka consumer on another client (segmentio/kafka-go, franz-go, ...) →
  `pinpoint.NewKafkaConsumerTracer()`, which both Kafka plugins are built on. It
  continues the producer's transaction from the record headers and records the
  topic, partition, offset and broker the way the plugins do; you end the span.
  The reader must tell a missing header from an empty one
  ([Distributed tracing context](instrument.md#distributed-tracing-context)):

  ```go
  tracer := pinpoint.NewKafkaConsumerTracer("Kafka Consumer Invocation",
      pinpoint.KafkaConsumerMessage{Topic: m.Topic, Partition: int32(m.Partition), Offset: m.Offset, Broker: broker},
      headers) // a pinpoint.DistributedTracingContextReader over the record headers
  defer tracer.EndSpan()
  defer tracer.NewSpanEvent("process").EndSpanEvent()

  process(pinpoint.NewContext(ctx, tracer), m)
  ```

Read [Tracer, Span, and Annotation Contracts](api_contracts.md) first; it is
short, and it covers the rules that a broken hand-written instrument usually
violates.
