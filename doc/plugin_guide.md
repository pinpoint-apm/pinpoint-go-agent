# Pinpoint Go Agent Plug-ins

| plugin package | source directory                          | instrumented package                                                           |
|----------------|-------------------------------------------|--------------------------------------------------------------------------------|
| pphttp         | [plugin/http](/plugin/http)               | Go standard HTTP package                                                       |
| ppchi          | [plugin/chi](/plugin/chi)                 | go-chi/chi package (https://github.com/go-chi/chi)                             |
| ppecho         | [plugin/echo](/plugin/echo)               | labstack/echo package (https://github.com/labstack/echo)                       |
| ppgin          | [plugin/gin](/plugin/gin)                 | gin-gonic/gin package (https://github.com/gin-gonic/gin)                       |
| ppgocql        | [plugin/gocql](/plugin/gocql)             | gocql package (https://github.com/gocql/gocql).                                |
| ppgoelastic    | [plugin/goelastic](/plugin/goelastic)     | elastic/go-elasticsearch package (https://github.com/elastic/go-elasticsearch) |
| ppgohbase      | [plugin/gohbase](/plugin/gohbase)         | tsuna/gohbase package (https://github.com/tsuna/gohbase).                      |
| ppgomemcache   | [plugin/gomemcache](/plugin/gomemcache)   | bradfitz/gomemcache package (https://github.com/bradfitz/gomemcache)           |
| ppgoredis      | [plugin/goredis](/plugin/goredis)         | go-redis/redis package (https://github.com/go-redis/redis)                     |
| ppgoredisv8    | [plugin/goredisv8](/plugin/goredisv8)     | go-redis/redis/v8 package (https://github.com/go-redis/redis)                  |
| ppgorilla      | [plugin/gorilla](/plugin/gorilla)         | gorilla/mux package (https://github.com/gorilla/mux).                          |
| ppgorm         | [plugin/gorm](/plugin/gorm)               | go-gorm/gorm package (https://github.com/go-gorm/gorm)                         |
| ppgrpc         | [plugin/grpc](/plugin/grpc)               | grpc/grpc-go package (https://github.com/grpc/grpc-go)                         |
| pplogrus       | [plugin/logrus](/plugin/logrus)           | sirupsen/logrus package (https://github.com/sirupsen/logrus)                   |
| ppmongo        | [plugin/mongodriver](/plugin/mongodriver) | mongodb/mongo-go-driver package (https://github.com/mongodb/mongo-go-driver)   |
| ppmysql        | [plugin/mysql](/plugin/mysql)             | go-sql-driver/mysql package (https://github.com/go-sql-driver/mysql)           |
| pppgsql        | [plugin/pgsql](/plugin/pgsql)             | lib/pq package (https://github.com/lib/pq)                                     |
| ppredigo       | [plugin/redigo](/plugin/redigo)           | gomodule/redigo package (https://github.com/gomodule/redigo)                   |
| ppsarama       | [plugin/sarama](/plugin/sarama)           | Shopify/sarama package (https://github.com/Shopify/sarama)                     |

## pphttp
### http server
This package instruments inbound requests handled by a http.ServeMux.
Use NewServeMux to trace all handlers:

``` go
mux := pphttp.NewServeMux()
mux.HandleFunc("/bar", outGoing)
```

Use WrapHandler or WrapHandlerFunc to select the handlers you want to track:

``` go
http.HandleFunc("/", pphttp.WrapHandlerFunc(index))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "net/http"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func outGoing(w http.ResponseWriter, r *http.Request) {
    tracer := pinpoint.FromContext(r.Context())
    ctx := pinpoint.NewContext(context.Background(), tracer)
    // or ctx := r.Context()
    
    client := pphttp.WrapClientWithContext(ctx, &http.Client{})
    resp, err := client.Get("http://localhost:9000/async_wrapper?foo=bar&say=goodbye")
    ...
}

func main() {
    //setup agent
    opts := []pinpoint.ConfigOption{
        pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
    }
    cfg, err := pinpoint.NewConfig(opts...)
    agent, err := pinpoint.NewAgent(cfg)
    defer agent.Shutdown()

    mux := pphttp.NewServeMux()
    mux.HandleFunc("/bar", outGoing)
    http.ListenAndServe("localhost:8000", mux)
}

```

#### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)

### http client
This package instruments outbound requests and add distributed tracing headers.
Use WrapClient, WrapClientWithContext or DoClient.

``` go
client := pphttp.WrapClient(&http.Client{})
client.Get(external_url)
```

``` go
req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
pphttp.DoClient(http.DefaultClient.Do, req)
```

It is necessary to pass the context containing the pinpoint.Tracer to the http.Request.

``` go
import (
    "net/http"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func wrapClient(w http.ResponseWriter, r *http.Request) {
    req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://localhost:9000/async", nil)
    client := pphttp.WrapClient(&http.Client{})
    resp, err := client.Do(req)
    ...
}

func main() {
    ... //setup agent

    http.HandleFunc("/wrapclient", pphttp.WrapHandlerFunc(wrapClient))
    http.ListenAndServe(":8000", nil)
}

```
[Full Example Source](/plugin/http/example/http_server.go)

#### Config Options
* [Http.Client.RecordRequestHeader](/doc/config.md#Http.Client.RecordRequestHeader)
* [Http.Client.RecordResponseHeader](/doc/config.md#Http.Client.RecordResponseHeader)
* [Http.Client.RecordRequestCookie](/doc/config.md#Http.Client.RecordRequestCookie)

## ppchi
This package instruments inbound requests handled by a chi.Router. 
Register the Middleware as the middleware of the router to trace all handlers:

``` go
r := chi.NewRouter()
r.Use(ppchi.Middleware())
```

Use WrapHandler or WrapHandlerFunc to select the handlers you want to track:

``` go
r.Get("/hello", ppchi.WrapHandlerFunc(hello))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
import (
    "net/http"

    "github.com/go-chi/chi"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi"
)

func hello(w http.ResponseWriter, r *http.Request) {
    io.WriteString(w, "hello world")
}

func main() {
    ... //setup agent
	
    r := chi.NewRouter()
    r.Use(ppchi.Middleware())
    
    r.Get("/hello", hello)
    http.ListenAndServe(":8000", r)
}
```

[Full Example Source](/plugin/chi/example/chi_server.go)

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)

## ppecho
This package instruments inbound requests handled by an echo.Router.
Register the Middleware as the middleware of the router to trace all handlers:

``` go
e := echo.New()
e.Use(ppecho.Middleware())
```

Use WrapHandler to select the handlers you want to track:

``` go
e.GET("/hello", ppecho.WrapHandler(hello))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/labstack/echo"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo"
)

func hello(c echo.Context) error {
    return c.String(200, "Hello World!!")
}

func main() {
    ... //setup agent
	
    e := echo.New()
    e.Use(ppecho.Middleware())

    e.GET("/hello", hello)
    e.Start(":9000")
}

```
[Full Example Source](/plugin/echo/example/echo_server.go)

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)

## ppgin
This package instruments inbound requests handled by a gin.Engine. 
Register the Middleware as the middleware of the router to trace all handlers:

``` go
r := gin.Default()
r.Use(ppgin.Middleware())
```

Use WrapHandler to select the handlers you want to track:

``` go
r.GET("/external", ppgin.WrapHandler(extCall))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/gin-gonic/gin"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin"
)

func endpoint(c *gin.Context) {
    c.Writer.WriteString("endpoint")
}

func main() {
    ... //setup agent
	
    router := gin.Default()
    router.Use(ppgin.Middleware())

    router.GET("/endpoint", endpoint)
    router.Run(":8000")
}

```
[Full Example Source](/plugin/gin/example/gin_server.go)

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)

## ppgocql
This package instruments all queries created from gocql session. 
Use the NewObserver as the gocql.QueryObserver or gocql.BatchObserver:

``` go
cluster := gocql.NewCluster("127.0.0.1")

observer := ppgocql.NewObserver()
cluster.QueryObserver = observer
cluster.BatchObserver = observer
```

It is necessary to pass the context containing the pinpoint.Tracer using the pinpoint.WithContext function.

``` go
import (
    "github.com/gocql/gocql"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocql"
)

func doCassandra(w http.ResponseWriter, r *http.Request) {
    observer := ppgocql.NewObserver()
    cluster := gocql.NewCluster("127.0.0.1")
    cluster.QueryObserver = observer
    cluster.BatchObserver = observer

    session, _ := cluster.CreateSession()
    query := session.Query(`SELECT id, text FROM tweet WHERE timeline = ? LIMIT 1`, "me")
    err := query.WithContext(r.Context()).Consistency(gocql.One).Scan(&id, &text)
    ...
}

```
[Full Example Source](/plugin/gocql/example/gocql_example.go)

## ppgoelastic
This package instruments the elasticsearch calls.
Use the NewTransport function as the elasticsearch.Client's Transport.

``` go
es, err := elasticsearch.NewClient(
    elasticsearch.Config{
        Transport: ppgoelastic.NewTransport(nil),
})
```

It is necessary to pass the context containing the pinpoint.Tracer to elasticsearch.Client.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
res, err = es.Search(
    es.Search.WithContext(ctx),
    es.Search.WithIndex("test"),
    ...
)
```

``` go
import (
    "github.com/elastic/go-elasticsearch/v8"
    "github.com/elastic/go-elasticsearch/v8/esapi"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelastic"
)

func goelastic(w http.ResponseWriter, req *http.Request) {
    ctx := req.Context()
    es, err := elasticsearch.NewClient(
        elasticsearch.Config{Transport: ppgoelastic.NewTransport(nil)}
    )

    ...
    
    res, err = es.Search(
        es.Search.WithContext(ctx),
        es.Search.WithIndex("test"),
        es.Search.WithBody(&buf),
    )

    ...
}
```
[Full Example Source](/plugin/goelastic/example/goelastic.go)

## ppgohbase
This package instruments the gohbase calls. Use the NewClient as the gohbase.NewClient.

``` go
client := ppgohbase.NewClient("localhost")
```

It is necessary to pass the context containing the pinpoint.Tracer to gohbase.Client.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
putRequest, _ := hrpc.NewPutStr(ctx, "table", "key", values)
client.Put(putRequest)
```

``` go
import (
    "github.com/tsuna/gohbase/hrpc"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gohbase"
)

func doHbase(w http.ResponseWriter, r *http.Request) {
    client := ppgohbase.NewClient("localhost")

    values := map[string]map[string][]byte{"cf": {"a": []byte{0}}}
    putRequest, err := hrpc.NewPutStr(r.Context(), "table", "key", values)
    _, err = client.Put(putRequest)
	
    ...
}
```
[Full Example Source](/plugin/gohbase/example/hbase_example.go)

## ppgomemcache
This package instruments the gomemcache calls. Use the NewClient as the memcache.New.

``` go
mc := ppgomemcache.NewClient(addr...)
```

It is necessary to pass the context containing the pinpoint.Tracer to Client using Client.WithContext.

``` go
mc.WithContext(pinpoint.NewContext(context.Background(), tracer))
mc.Get("foo")
```

``` go
import (
    "github.com/bradfitz/gomemcache/memcache"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache"
)

func doMemcache(w http.ResponseWriter, r *http.Request) {
    addr := []string{"localhost:11211"}
    mc := ppgomemcache.NewClient(addr...)
    mc.WithContext(r.Context())

    item, err = mc.Get("foo")
	
    ...
}
```
[Full Example Source](/plugin/gomemcache/example/gomemcache_example.go)

## ppgoredis
This package instruments the go-redis calls. 
Use the NewClient as the redis.NewClient and the NewClusterClient as redis.NewClusterClient, respectively.
It supports go-redis v6.10.0 and later.

``` go
rc = ppgoredis.NewClient(redisOpts)
```

It is necessary to pass the context containing the pinpoint.Tracer to Client using Client.WithContext.

``` go
rc = rc.WithContext(pinpoint.NewContext(context.Background(), tracer))
rc.Pipeline()
```

``` go
package main

import (
    "github.com/go-redis/redis"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredis"
)

var redisClient *ppgoredis.Client
var redisClusterClient *ppgoredis.ClusterClient

func redisv6(w http.ResponseWriter, r *http.Request) {
    c := redisClient.WithContext(r.Context())
    redisPipeIncr(c.Pipeline())
}

func redisv6Cluster(w http.ResponseWriter, r *http.Request) {
    c := redisClusterClient.WithContext(r.Context())
    redisPipeIncr(c.Pipeline())
}

func redisPipeIncr(pipe redis.Pipeliner) {
    incr := pipe.Incr("foo")
    pipe.Expire("foo", time.Hour)
    _, er := pipe.Exec()
    fmt.Println(incr.Val(), er)
}

func main() {
    ... //setup agent

    addrs := []string {"localhost:6379", "localhost:6380"}

    //redis client
    redisOpts := &redis.Options{
        Addr: addrs[0],
    }
    redisClient = ppgoredis.NewClient(redisOpts)

    //redis cluster client
    redisClusterOpts := &redis.ClusterOptions{
        Addrs: addrs,
    }
    redisClusterClient = ppgoredis.NewClusterClient(redisClusterOpts)

    http.HandleFunc("/redis", phttp.WrapHandlerFunc(redisv6))
    http.HandleFunc("/rediscluster", phttp.WrapHandlerFunc(redisv6Cluster))

    ...
}
```
[Full Example Source](/plugin/goredis/example/redisv6.go)

## ppgoredisv8
This package instruments the go-redis/v8 calls. Use the NewHook or NewClusterHook as the redis.Hook.
Only available in versions of go-redis with an AddHook() function.

``` go
rc = redis.NewClient(redisOpts)
client.AddHook(ppgoredisv8.NewHook(opts))
```

It is necessary to pass the context containing the pinpoint.Tracer to redis.Client.

``` go
rc = rc.WithContext(pinpoint.NewContext(context.Background(), tracer))
rc.Pipeline()
```

``` go
package main

import (
    "github.com/go-redis/redis/v8"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8"
)

var redisClient *redis.Client

func redisv8(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    client := redisClient.WithContext(ctx)

    err := client.Set(ctx, "key", "value", 0).Err()
    val, err := client.Get(ctx, "key").Result()
    fmt.Println("key", val)
}

func main() {
    ... //setup agent

    addrs := []string{"localhost:6379", "localhost:6380"}

    redisOpts := &redis.Options{
        Addr: addrs[0],
    }
    redisClient = redis.NewClient(redisOpts)
    redisClient.AddHook(ppgoredisv8.NewHook(redisOpts))

    http.HandleFunc("/redis", pphttp.WrapHandlerFunc(redisv8))

    ...
}
```
[Full Example Source](/plugin/goredisv8/example/redisv8.go)

## ppgorilla
This package instruments inbound requests handled by a gorilla mux.Router. 
Register the Middleware as the middleware of the router to trace all handlers:

``` go
r := mux.NewRouter()
r.Use(ppgorilla.Middleware())
```

Use WrapHandler or WrapHandlerFunc to select the handlers you want to track:

``` go
r.HandleFunc("/outgoing", ppgorilla.WrapHandlerFunc(outGoing))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/gorilla/mux"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorilla"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func hello(w http.ResponseWriter, r *http.Request) {
    io.WriteString(w, "hello world")
}

func main() {
    ... //setup agent
	
    r := mux.NewRouter()
    r.Use(ppgorilla.Middleware())

    //r.HandleFunc("/", ppgorilla.WrapHandlerFunc(hello)))
    http.ListenAndServe(":8000", r)
}

```
[Full Example Source](/plugin/gorilla/example/mux_server.go)

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)

## ppgorm
This package instruments the go-gorm/gorm calls. Use the Open as the gorm.Open.

``` go
g, err := ppgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
```

It is necessary to pass the context containing the pinpoint.Tracer to gorm.DB.

``` go
g = g.WithContext(pinpoint.NewContext(context.Background(), tracer))
g.Create(&Product{Code: "D42", Price: 100})
```

``` go
package main

import (
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
    "gorm.io/driver/mysql"
    "gorm.io/gorm"
)

func gormQuery(w http.ResponseWriter, r *http.Request) {
    db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/testdb")
    gormdb, err := ppgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
    gormdb = gormdb.WithContext(r.Context())

    gormdb.AutoMigrate(&Product{})
    gormdb.Create(&Product{Code: "D42", Price: 100})

    var product Product
    gormdb.First(&product, "code = ?", "D42")

    ...
}
```
[Full Example Source](/plugin/gorm/example/gorm_example.go)

## ppgrpc
This package instruments gRPC servers and clients. 

### server
To instrument a gRPC server, use UnaryServerInterceptor and StreamServerInterceptor.

``` go
grpcServer := grpc.NewServer(
    grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()),
    grpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
)
```

ppgrpc's server interceptor adds the pinpoint.Tracer to the gRPC server handler's context. 
By using the pinpoint.FromContext function, this tracer can be obtained.
Alternatively, the context of the handler may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
func (s *Server) UnaryCallUnaryReturn(ctx context.Context, msg *testapp.Greeting) (*testapp.Greeting, error) {
    tracer := pinpoint.FromContext(ctx)
    tracer.NewSpanEvent("gRPC Server Handler").EndSpanEvent()
    return "hi", nil
}
```

``` go
package main

import (
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
    "google.golang.org/grpc"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
)

type Server struct{}
var returnMsg = &testapp.Greeting{Msg: "Hello!!"}

func (s *Server) UnaryCallUnaryReturn(ctx context.Context, msg *testapp.Greeting) (*testapp.Greeting, error) {
    printGreeting(ctx, msg)
    return returnMsg, nil
}

func (s *Server) StreamCallUnaryReturn(stream testapp.Hello_StreamCallUnaryReturnServer) error {
    for {
        in, err := stream.Recv()
        printGreeting(stream.Context(), in)
    }
}

func printGreeting(ctx context.Context, in *testapp.Greeting) {
    defer pinpoint.FromContext(ctx).NewSpanEvent("printGreeting").EndSpanEvent()
    log.Println(in.Msg)
}

func main() {
    ... //setup agent
	
    grpcServer := grpc.NewServer(
        grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()),
        grpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
    )
    testapp.RegisterHelloServer(grpcServer, &Server{})
    grpcServer.Serve(listener)
}

```
[Full Example Source](/plugin/grpc/example/server.go)

### client
To instrument a gRPC client, use UnaryClientInterceptor and StreamClientInterceptor.

``` go
conn, err := grpc.Dial(
    "localhost:8080",
    grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
    grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
)
```

It is necessary to pass the context containing the pinpoint.Tracer to grpc.Client.

``` go
client := testapp.NewHelloClient(conn)
client.UnaryCallUnaryReturn(pinpoint.NewContext(context.Background(), tracer), greeting)
```

``` go
import (
    "google.golang.org/grpc"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
)

func unaryCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
    in, err := client.UnaryCallUnaryReturn(ctx, greeting)
    log.Println(in.Msg)
}

func streamCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
    stream, err := client.StreamCallUnaryReturn(ctx)

    for i := 0; i < numStreamSend; i++ {
        if err := stream.Send(greeting); err != nil {
            break
        }
    }

    ...
}

func doGrpc(w http.ResponseWriter, r *http.Request) {
    conn, err := grpc.Dial(
        "localhost:8080",
        grpc.WithInsecure(),
        grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
        grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
    )
    defer conn.Close()

    ctx := r.Context()
    unaryCallUnaryReturn(ctx, client)
    streamCallUnaryReturn(ctx, client)
}
```
[Full Example Source](/plugin/grpc/example/client.go)

## pplogrus
This package allows additional transaction id and span id of the pinpoint span to be printed in the log message. 
Use the NewField or NewEntry and pass the logrus field back to the logger.

``` go
tracer := pinpoint.FromContext(ctx)
logger.WithFields(pplogrus.NewField(tracer)).Fatal("oh, what a wonderful world")
```
``` go
entry := pplogrus.NewEntry(tracer).WithField("foo", "bar")
entry.Error("entry log message")
```

You can use NewHook as the logrus.Hook. 
It is necessary to pass the context containing the pinpoint.Tracer to logrus.Logger.

``` go
logger.AddHook(pplogrus.NewHook())
entry := logger.WithContext(pinpoint.NewContext(context.Background(), tracer)).WithField("foo", "bar")
entry.Error("hook log message")
```

``` go
import (
    "github.com/sirupsen/logrus"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus"
)

func logging(w http.ResponseWriter, r *http.Request) {
    logger := logrus.New()
    tracer := pinpoint.TracerFromRequestContext(r)
    logger.WithFields(pplogrus.NewField(tracer)).Fatal("ohhh, what a world")
}
```
[Full Example Source](/plugin/logrus/example/logrus_example.go)

## ppmongo
This package instruments the mongo-go-driver calls. 
Use the NewMonitor as Monitor field of mongo-go-driver's ClientOptions.

``` go
opts := options.Client()
opts.Monitor = ppmongo.NewMonitor()
client, err := mongo.Connect(ctx, opts)
```

It is necessary to pass the context containing the pinpoint.Tracer to mongo.Client.

``` go
collection := client.Database("testdb").Collection("example")
ctx := pinpoint.NewContext(context.Background(), tracer)
collection.InsertOne(ctx, bson.M{"foo": "bar", "apm": "pinpoint"})
```

``` go
import (
    "go.mongodb.org/mongo-driver/bson"
    "go.mongodb.org/mongo-driver/mongo"
    "go.mongodb.org/mongo-driver/mongo/options"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver"
)

func mongodb(w http.ResponseWriter, r *http.Request) {
    opts := options.Client()
    opts.ApplyURI("mongodb://localhost:27017")
    opts.Monitor = ppmongo.NewMonitor()
    client, err := mongo.Connect(context.Background(), opts)

    collection := client.Database("testdb").Collection("example")
    _, err = collection.InsertOne(r.Context(), bson.M{"foo": "bar", "apm": "pinpoint"})
    ...
}
```
[Full Example Source](/plugin/mongodriver/example/mongo_example.go)

## ppmysql
This package instruments the mysql driver calls. 
Use this package's driver in place of the mysql driver.

``` go
db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) from tables")
```

``` go
import (
    "database/sql"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
)

func query(w http.ResponseWriter, r *http.Request) {
    db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
    row := db.QueryRowContext(r.Context(), "SELECT count(*) from tables")

    var count int
    row.Scan(&count)
    fmt.Println("number of tables in information_schema", count)
}
```
[Full Example Source](/plugin/mysql/example/mysql_example.go)

## pppgsql
This package instruments the postgres driver calls. 
Use this package's driver in place of the postgres driver.

``` go
db, err := sql.Open("pq-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
Spans will be created for queries and other statement executions if the context methods are used, and the context includes a transaction.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
```

``` go
import (
    pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql"
)

func query(w http.ResponseWriter, r *http.Request) {
    db, err := sql.Open("pq-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
    row := db.QueryRowContext(r.Context(), "SELECT count(*) FROM pg_catalog.pg_tables")

    var count int
    err = row.Scan(&count)
    fmt.Println("number of entries in pg_catalog.pg_tables", count)
}
```
[Full Example Source](/plugin/pgsql/example/pgsql_example.go)

## ppredigo
This package instruments the gomodule/redigo calls.
Use the Dial, DialContext (or DialURL, DialURLContext) as the redis.Dial.

``` go
c, err := ppredigo.Dial("tcp", "127.0.0.1:6379")
```

It is necessary to propagate the context that contains the pinpoint.Tracer to redis.Conn.
You can call WithContext to propagate a context containing a pinpoint.Tracer to the operations:
``` go
ppredigo.WithContext(c, pinpoint.NewContext(context.Background(), tracer))
c.Do("SET", "vehicle", "truck")
```

Also, you can use function taking the context like redis.DoContext.

``` go
redis.DoContext(c, pinpoint.NewContext(context.Background(), tracer), "GET", "vehicle")
```

``` go
package main

import (
    "github.com/gomodule/redigo/redis"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/redigo"
)

func redigo_test(w http.ResponseWriter, r *http.Request) {
    c, err := ppredigo.Dial("tcp", "127.0.0.1:6379")
    ppredigo.WithContext(c, r.Context())

    c.Do("SET", "vehicle", "truck")
    redis.DoWithTimeout(c, 1000*time.Millisecond, "GET", "vehicle")
    
    //or 
    //redis.DoContext(c, r.Context(), "GET", "vehicle")
```
[Full Example Source](/plugin/redigo/example/redigo_example.go)

## ppsarama
This package instruments Kafka consumers and producers.

### Consumer
To instrument a Kafka consumer, use ConsumeMessage or ConsumeMessageContext.

``` go
pc, _ := consumer.ConsumePartition(topic, partition, offset)
for msg := range pc.Messages() {
    ppsarama.ConsumeMessage(processMessage, msg)
}
```

ConsumeMessage wraps a sarama.ConsumerMessage and passes a ConsumerMessage having pinpoint.Tracer to HandlerFunc. 
In HandlerFunc, this tracer can be obtained by using the ConsumerMessage.Tracer function.

``` go
func processMessage(msg *ppsarama.ConsumerMessage) error {
    tracer := msg.Tracer()
    defer tracer.NewSpanEvent("processMessage").EndSpanEvent()

    fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
    fmt.Println("retrieving message: ", string(msg.Value))
```

For sarama.ConsumerGroupHandler, ConsumeMessageContext is provided.

``` go
func (h exampleConsumerGroupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
    ctx := sess.Context()
    for msg := range claim.Messages() {
        _ = ppsarama.ConsumeMessageContext(process, ctx, msg)
    }
```

ConsumeMessageContext passes a context added pinpoint.Tracer to HandlerContextFunc. 
In HandlerContextFunc, this tracer can be obtained by using the pinpoint.FromContext function.
Alternatively, the context may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
func process(ctx context.Context, msg *sarama.ConsumerMessage) error {
    tracer := pinpoint.FromContext(ctx)
    defer tracer.NewSpanEvent("process").EndSpanEvent()

    fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
```

``` go
package main

import (
    "github.com/Shopify/sarama"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
)

func processMessage(msg *ppsarama.ConsumerMessage) error {
    tracer := msg.Tracer()
    defer tracer.NewSpanEvent("processMessage").EndSpanEvent()
    fmt.Println("retrieving message: ", string(msg.Value))
    ...
}

func subscribe(topic string, consumer sarama.Consumer) {
    partitionList, err := consumer.Partitions(topic)
    ...
    
    for _, partition := range partitionList {
        pc, _ := consumer.ConsumePartition(topic, partition, initialOffset)

        go func(pc sarama.PartitionConsumer) {
            for msg := range pc.Messages() {
                ppsarama.ConsumeMessage(processMessage, msg)
            }
            wg.Done()
        }(pc)
    ...
}

func main() {
    ... //setup agent

    config := sarama.NewConfig()
    config.Version = sarama.V2_3_0_0
    consumer, err := sarama.NewConsumer([]string{"127.0.0.1:9092"}, config)
    subscribe("topic", consumer)
}
```
[Full Example Source](/plugin/sarama/example/consumer.go)

### Producer
To instrument a Kafka producer, use NewSyncProducer or NewAsyncProducer.

``` go
config := sarama.NewConfig()
producer, err := ppsarama.NewSyncProducer(brokers, config)
```

It is necessary to pass the context containing the pinpoint.Tracer to sarama.SyncProducer (or sarama.AsyncProducer) using WithContext function.

``` go
ppsarama.WithContext(pinpoint.NewContext(context.Background(), tracer), producer)
partition, offset, err := producer.SendMessage(msg)
```

``` go
package main

import (
    "github.com/Shopify/sarama"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
)

func prepareMessage(topic, message string) *sarama.ProducerMessage {
    return &sarama.ProducerMessage{
        Topic:     topic,
        Partition: -1,
        Value:     sarama.StringEncoder(message),
    }
}

func save(w http.ResponseWriter, r *http.Request) {
    msg := prepareMessage("topic", "Hello, Kafka!!")
    ppsarama.WithContext(r.Context(), producer)
    partition, offset, err := producer.SendMessage(msg)
    ...
}

var producer sarama.SyncProducer

func main() {
    ... //setup agent
	
    config := sarama.NewConfig()
    ...

    producer, err := ppsarama.NewSyncProducer([]string{"127.0.0.1:9092"}, config)
    http.HandleFunc("/save", pphttp.WrapHandlerFunc(save))
}

```
[Full Example Source](/plugin/sarama/example/producer.go)

