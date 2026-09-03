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
go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin
```

Each package name is the directory prefixed with `pp` (`plugin/gin` is
`ppgin`), and every plugin directory carries a `README.md` and a runnable
`example/` — those are the authoritative reference for the plugin's options.

A plugin needs a running agent, and most of them need a **tracer in the
context** of the call they instrument. That is the one recurring mistake: an
outgoing call built on `context.Background()` records nothing, no matter how
well the client is wrapped. See
[Context propagation](instrument.md#context-propagation).

## Reading the tables

* **Entry point** — what you call. `Middleware()` registers with the
  framework's own middleware chain; `Wrap*` wraps one handler, client or
  connection; a driver name is passed to `sql.Open`.
* Plugins with a version suffix (`echov4`, `goredisv9`, ...) track that major
  version of the library. Pick the one matching your dependency; they are
  separate modules and cannot be mixed for one library.
* Every wrapper is safe to leave in place when the agent is disabled or the
  transaction is unsampled — it passes through and records nothing.

---

## HTTP servers and web frameworks

These open the root span of a transaction: they extract the incoming
distributed-tracing headers, put a tracer in the request context, and record
the URL, status and configured headers.

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/http](/plugin/http) | Go standard `net/http` | `WrapHandler`, `WrapHandlerFunc`, `NewServeMux` |
| [plugin/beego](/plugin/beego) | [beego/beego/v2](https://github.com/beego/beego) | `ServerFilterChain`, `Middleware` |
| [plugin/chi](/plugin/chi) | [go-chi/chi](https://github.com/go-chi/chi) | `Middleware`, `WrapHandler`, `WrapHandlerFunc` |
| [plugin/echo](/plugin/echo) | [labstack/echo](https://github.com/labstack/echo) — *deprecated* | `Middleware`, `WrapHandler` |
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

```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin"

router := gin.Default()
router.Use(ppgin.Middleware())
```

```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"

http.HandleFunc("/", pphttp.WrapHandlerFunc(index))
```

Prefer `Middleware()` over per-handler wrapping where the framework offers it:
the middleware sees the framework's routed pattern, so the span is named
`/users/{id}` rather than `/users/1234`, which is what keeps the Pinpoint UI's
URL list readable (see
[contract 8](api_contracts.md#8-keep-operation-and-error-names-low-cardinality)).

Which URLs, methods, headers and cookies are recorded is controlled by the
`Http.Server.*` options in [Configuration](config.md#httpserverstatuscodeerrors),
or programmatically by the `WithHttpServer*` config options in the
`plugin/http` package.

## HTTP and RPC clients

These record an outgoing call as a span event on the current transaction and
inject the tracing headers so the next node continues it.

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/http](/plugin/http) | Go standard `net/http` client | `WrapClient`, `WrapClientWithContext`, `DoClient` |
| [plugin/fasthttp](/plugin/fasthttp) | [valyala/fasthttp](https://github.com/valyala/fasthttp) client | `DoClient` |
| [plugin/beego](/plugin/beego) | beego `httplib` client | `DoRequest`, `ClientFilterChain` |
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
| [plugin/mssql](/plugin/mssql) | [denisenkom/go-mssqldb](https://github.com/denisenkom/go-mssqldb) | `sqlserver-pinpoint` |
| [plugin/mssql-microsoft](/plugin/mssql-microsoft) | [microsoft/go-mssqldb](https://github.com/microsoft/go-mssqldb) | `mssql-microsoft-pinpoint` |
| [plugin/gorm](/plugin/gorm) | [go-gorm/gorm](https://github.com/go-gorm/gorm) | `Open` (wraps `gorm.Open`) |

```go
import _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"

db, _ := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")

ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) FROM tables")
```

The blank import is enough — the driver registers itself in `init()`. Use the
`Context` variants of the query methods; the plain `Query`/`Exec` methods have
nowhere to carry a tracer and produce no span event.

SQL statements are normalized (literals replaced with placeholders) before
being sent, so the UI groups repeated statements. What is recorded is governed
by the `SQL.*` options — most importantly `SQL.TraceBindValue`, which is the
privacy gate on bind values. See [Configuration](config.md#sqltracebindvalue).

## Cache, document, and search stores

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/goredis](/plugin/goredis) | [go-redis/redis](https://github.com/go-redis/redis) | `NewClient`, `NewClusterClient` |
| [plugin/goredisv7](/plugin/goredisv7) | [go-redis/redis/v7](https://github.com/go-redis/redis) | `NewHook`, `NewClusterHook` |
| [plugin/goredisv8](/plugin/goredisv8) | [go-redis/redis/v8](https://github.com/go-redis/redis) | `NewHook`, `NewClusterHook` |
| [plugin/goredisv9](/plugin/goredisv9) | [redis/go-redis/v9](https://github.com/redis/go-redis) | `NewHook`, `NewClusterHook` |
| [plugin/redigo](/plugin/redigo) | [gomodule/redigo](https://github.com/gomodule/redigo) | `Dial`, `DialContext`, `DialURL`, `WithContext` |
| [plugin/rueidis](/plugin/rueidis) | [redis/rueidis](https://github.com/redis/rueidis) | `NewHook` |
| [plugin/gomemcache](/plugin/gomemcache) | [bradfitz/gomemcache](https://github.com/bradfitz/gomemcache) | `NewClient` |
| [plugin/mongodriver](/plugin/mongodriver) | [mongodb/mongo-go-driver](https://github.com/mongodb/mongo-go-driver) | `NewMonitor` |
| [plugin/mongodriverv2](/plugin/mongodriverv2) | [mongodb/mongo-go-driver/v2](https://github.com/mongodb/mongo-go-driver) | `NewMonitor` |
| [plugin/gocql](/plugin/gocql) | [gocql](https://github.com/gocql/gocql) | `NewObserver` |
| [plugin/gocqlv2](/plugin/gocqlv2) | [gocql/v2](https://github.com/apache/cassandra-gocql-driver) | `NewObserver` |
| [plugin/gohbase](/plugin/gohbase) | [tsuna/gohbase](https://github.com/tsuna/gohbase) | `NewClient` |
| [plugin/goelastic](/plugin/goelastic) | [elastic/go-elasticsearch](https://github.com/elastic/go-elasticsearch) | `NewTransport` |
| [plugin/goelasticv8](/plugin/goelasticv8) | [elastic/go-elasticsearch/v8](https://github.com/elastic/go-elasticsearch) | `NewTransport` |
| [plugin/goelasticv9](/plugin/goelasticv9) | [elastic/go-elasticsearch/v9](https://github.com/elastic/go-elasticsearch) | `NewTransport` |

These clients hook into the library's own observability seam — a hook, monitor,
observer or `RoundTripper` — so you register once at construction:

```go
// redis/go-redis/v9
opts := &redis.Options{Addr: "localhost:6379"}
client := redis.NewClient(opts)
client.AddHook(ppgoredisv9.NewHook(opts))

// mongo-go-driver
opts := options.Client().SetMonitor(ppmongo.NewMonitor())

// gocql
cluster.QueryObserver = ppgocql.NewObserver()
```

The tracer still has to reach each call in its context — `client.Get(ctx, key)`
with a context from `pinpoint.NewContext()`, or the request context in a
handler.

## Message queues

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/sarama](/plugin/sarama) | [Shopify/sarama](https://github.com/Shopify/sarama) | producers: `NewSyncProducer`, `NewAsyncProducer`; consumers: `ConsumeMessageContext`, `NewContext`, `WrapPartitionConsumer` |
| [plugin/sarama-IBM](/plugin/sarama-IBM) | [IBM/sarama](https://github.com/IBM/sarama) | same as above |

A queue is a trace boundary, so it has two halves. The producer records a span
event on the current transaction and writes the tracing context into the
message headers; the consumer opens a **new root span** per message that
continues that transaction:

```go
// producer, inside a traced request
producer, _ := ppsarama.NewSyncProducer(brokers, config)

ctx := pinpoint.NewContext(context.Background(), tracer)
partition, offset, err := producer.SendMessageContext(ctx, msg)
```

```go
// consumer
func process(ctx context.Context, msg *sarama.ConsumerMessage) error {
    tracer := pinpoint.FromContext(ctx)
    defer tracer.NewSpanEvent("process").EndSpanEvent()
    return handle(msg)
}

// NewContext carries the broker addresses so the UI can show the Kafka node
ctx := ppsarama.NewContext(context.Background(), brokers)
for msg := range pc.Messages() {
    ppsarama.ConsumeMessageContext(process, ctx, msg)
}
```

Use `SendMessageContext` rather than `WithContext` + `SendMessage`:
`WithContext` binds the tracer to the producer itself and is not thread-safe.

## Logging integration

| Plugin package | Instrumented package | Entry point |
|---|---|---|
| [plugin/logrus](/plugin/logrus) | [sirupsen/logrus](https://github.com/sirupsen/logrus) | `NewHook`, `NewField`, `WithField`, `NewEntry`, `NewLoggerEntry` |

This one collects nothing on its own. It stamps the transaction and span id
onto your log lines, which is what lets the Pinpoint UI jump from a span to the
matching log entry:

```go
logger.AddHook(pplogrus.NewHook())

// or per-entry
logger.WithFields(pplogrus.WithField(tracer)).Error("something failed")
```

---

## What if there is no plugin?

Write the instrument by hand — it is a handful of lines, and the plugins
themselves are built on exactly the same public API:

* An entry point that starts a transaction (a queue consumer, a scheduled job,
  a custom protocol server) →
  [Span](instrument.md#span).
* An outbound call or an internal operation you want on the call stack →
  [SpanEvent](instrument.md#spanevent).
* A call to another Pinpoint-traced node →
  [Distributed tracing context](instrument.md#distributed-tracing-context).

Read [Tracer, Span, and Annotation Contracts](api_contracts.md) first; it is
short, and it covers the rules that a broken hand-written instrument usually
violates.

---

## Related Documentation

* [Quick Start](quick_start.md)
* [Custom Instrumentation](instrument.md)
* [Tracer, Span, and Annotation Contracts](api_contracts.md)
* [Configuration](config.md)
* [Troubleshooting](troubleshooting.md)
