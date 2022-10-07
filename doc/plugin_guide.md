# Pinpoint Go Agent Plug-ins

| plugin package | source directory                          | instrumented package                                                           |
|----------------|-------------------------------------------|--------------------------------------------------------------------------------|
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
| pphttp         | [plugin/http](/plugin/http)               | Go standard HTTP library                                                       |
| pplogrus       | [plugin/logrus](/plugin/logrus)           | sirupsen/logrus package (https://github.com/sirupsen/logrus)                   |
| ppmongo        | [plugin/mongodriver](/plugin/mongodriver) | mongodb/mongo-go-driver package (https://github.com/mongodb/mongo-go-driver)   |
| ppmysql        | [plugin/mysql](/plugin/mysql)             | go-sql-driver/mysql package (https://github.com/go-sql-driver/mysql)           |
| pppgsql        | [plugin/pgsql](/plugin/pgsql)             | lib/pq package (https://github.com/lib/pq)                                     |
| ppredigo       | [plugin/redigo](/plugin/redigo)           | gomodule/redigo package (https://github.com/gomodule/redigo)                   |
| ppsarama       | [plugin/sarama](/plugin/sarama)           | Shopify/sarama package (https://github.com/Shopify/sarama)                     |

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
	cluster := gocql.NewCluster("127.0.0.1")
	cluster.Keyspace = "example"
	cluster.Consistency = gocql.Quorum

	observer := ppgocql.NewObserver()
	cluster.QueryObserver = observer
	cluster.BatchObserver = observer

	session, _ := cluster.CreateSession()
	defer session.Close()

	ctx := r.Context()
	var id gocql.UUID
	var text string

	query := session.Query(`SELECT id, text FROM tweet WHERE timeline = ? LIMIT 1`, "me")
	if err := query.WithContext(ctx).Consistency(gocql.One).Scan(&id, &text); err != nil {
		log.Println(err)
	}
	io.WriteString(w, "Tweet:"+text)
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
		elasticsearch.Config{
			Transport: ppgoelastic.NewTransport(nil),
		})
	if err != nil {
		log.Fatalf("Error creating the client: %s", err)
	}

	var buf bytes.Buffer
	query := map[string]interface{}{
		"query": map[string]interface{}{
			"match": map[string]interface{}{
				"title": "test",
			},
		},
	}
	if err := json.NewEncoder(&buf).Encode(query); err != nil {
		log.Fatalf("Error encoding query: %s", err)
	}

	res, err = es.Search(
		es.Search.WithContext(ctx),
		es.Search.WithIndex("test"),
		es.Search.WithBody(&buf),
		es.Search.WithTrackTotalHits(true),
		es.Search.WithPretty(),
	)
	if err != nil {
		log.Fatalf("Error getting response: %s", err)
	}
	defer res.Body.Close()
}
```
[Full Example Source](/plugin/goelastic/example/goelastic.go)

## ppgohbase
This package instruments the gohbase calls. Use the NewClient as the gohbase.NewClient.

```go
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
	"github.com/tsuna/gohbase/filter"
	"github.com/tsuna/gohbase/hrpc"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/gohbase"
)

func doHbase(w http.ResponseWriter, r *http.Request) {
	client := ppgohbase.NewClient("localhost")
	ctx := r.Context()

	values := map[string]map[string][]byte{"cf": {"a": []byte{0}}}
	putRequest, err := hrpc.NewPutStr(ctx, "table", "key", values)
	if err != nil {
		log.Println(err)
	}
	_, err = client.Put(putRequest)
	if err != nil {
		log.Println(err)
	}
	
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

	_, _ = mc.Get("foo") // error
	
	...
}
```
[Full Example Source](/plugin/gomemcache/example/gomemcache_example.go)

## ppgoredis
This package instruments the go-redis calls. Use the NewClient as the redis.NewClient.
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

	http.ListenAndServe(":9000", nil)

	redisClient.Close()
	redisClusterClient.Close()
	agent.Shutdown()
}
```
[Full Example Source](/plugin/goredis/example/redisv6.go)

## ppgoredisv8
This package instruments the go-redis/v8 calls. Use the NewHook as the redis.Hook.
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

	pipe := client.Pipeline()
	incr := pipe.Incr(ctx, "foo")
	pipe.Expire(ctx, "foo", time.Hour)
	_, er := pipe.Exec(ctx)
	fmt.Println(incr.Val(), er)

	err := client.Set(ctx, "key", "value", 0).Err()
	if err != nil {
		fmt.Println(err)
	}

	val, err := client.Get(ctx, "key").Result()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("key", val)
}

var redisClusterClient *redis.ClusterClient

func redisv8Cluster(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client := redisClusterClient.WithContext(ctx)

	pipe := client.Pipeline()
	incr := pipe.Incr(ctx, "foo")
	pipe.Expire(ctx, "foo", time.Hour)
	_, er := pipe.Exec(ctx)
	fmt.Println(incr.Val(), er)

	err := client.Set(ctx, "key", "value", 0).Err()
	if err != nil {
		fmt.Println(err)
	}

	val, err := client.Get(ctx, "key").Result()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("key", val)
}

func main() {
	... //setup agent

	addrs := []string{"localhost:6379", "localhost:6380"}

	//redis client
	redisOpts := &redis.Options{
		Addr: addrs[0],
	}
	redisClient = redis.NewClient(redisOpts)
	redisClient.AddHook(ppgoredisv8.NewHook(redisOpts))

	//redis cluster client
	redisClusterOpts := &redis.ClusterOptions{
		Addrs: addrs,
	}
	redisClusterClient = redis.NewClusterClient(redisClusterOpts)
	redisClusterClient.AddHook(ppgoredisv8.NewClusterHook(redisClusterOpts))

	http.HandleFunc("/redis", pphttp.WrapHandlerFunc(redisv8))
	http.HandleFunc("/rediscluster", pphttp.WrapHandlerFunc(redisv8Cluster))

	http.ListenAndServe(":9000", nil)

	redisClient.Close()
	redisClusterClient.Close()
	agent.Shutdown()
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

type Product struct {
	gorm.Model
	Code  string
	Price uint
}

func gormQuery(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/testdb")
	if nil != err {
		panic(err)
	}

	gormdb, err := ppgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}

	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)
	gormdb = gormdb.WithContext(ctx)

	gormdb.AutoMigrate(&Product{})

	// Create
	gormdb.Create(&Product{Code: "D42", Price: 100})

	// Read
	var product Product
	gormdb.First(&product, 1)
	gormdb.First(&product, "code = ?", "D42")

	// Update - update product's price to 200
	gormdb.Model(&product).Update("Price", 200)

	// Delete - delete product
	gormdb.Delete(&product, 1)
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
		if err == io.EOF {
			return stream.SendAndClose(returnMsg)
		}
		if err != nil {
			return err
		}

		printGreeting(stream.Context(), in)
	}
}

func printGreeting(ctx context.Context, in *testapp.Greeting) {
	defer pinpoint.FromContext(ctx).NewSpanEvent("printGreeting").EndSpanEvent()
	log.Println(in.Msg)
}

func main() {
	... //setup agent
	
	listener, err := net.Listen("tcp", "localhost:8080")
	if err != nil {
		panic(err)
	}

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
	grpc.WithInsecure(),
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

const numStreamSend = 2
var greeting = &testapp.Greeting{Msg: "Hello!"}

func unaryCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	in, err := client.UnaryCallUnaryReturn(ctx, greeting)
	if err != nil {
		log.Fatalf("unaryCallUnaryReturn got error %v", err)
	}
	log.Println(in.Msg)
}

func streamCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := client.StreamCallUnaryReturn(ctx)
	if err != nil {
		log.Fatalf("streamCallUnaryReturn got error %v", err)
	}

	for i := 0; i < numStreamSend; i++ {
		if err := stream.Send(greeting); err != nil {
			if err == io.EOF {
				break
			}
			log.Fatalf("streamCallUnaryReturn got error %v", err)
		}
	}

	msg, err := stream.CloseAndRecv()
	if err != nil {
		log.Fatalf("streamCallUnaryReturn got error %v", err)
	}
	log.Println(msg.Msg)
}

func doGrpc(w http.ResponseWriter, r *http.Request) {
	conn, err := grpc.Dial(
		"localhost:8080",
		grpc.WithInsecure(),
		grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	client := testapp.NewHelloClient(conn)
	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)

	unaryCallUnaryReturn(ctx, client)
	streamCallUnaryReturn(ctx, client)
}
```
[Full Example Source](/plugin/grpc/example/client.go)

## http
You can instrument [go http package](https://google.golang.org/http) using the pinpoint http plugin.
If you want to track the http server's handler, you can use the WrapHandleFunc() function to wrap the handler function.

```go
http.HandleFunc(phttp.WrapHandleFunc(agent, "index", "/", index))
```

To track http client calls, use the NewHttpClientTracer() function to trigger the request.

```go
req, _ := http.NewRequest("GET", "http://localhost:9000/hello", nil)
tracer = phttp.NewHttpClientTracer(tracer, "http.DefaultClient", req)
```

Alternatively, you can use the WrapClient() function to wrap the http client.

```go
client := &http.Client{}
client = phttp.WrapClient(client)
```
 
``` go
import (
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func wrapRequest(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	req, _ := http.NewRequest("GET", "http://localhost:9000/hello", nil)

	tracer = phttp.NewHttpClientTracer(tracer, "http.DefaultClient", req)
	resp, err := http.DefaultClient.Do(req)
	phttp.EndHttpClientTracer(tracer, resp, err)

	if nil != err {
		io.WriteString(w, err.Error())
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

func wrapClient(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{}
	client = phttp.WrapClient(client)

	request, _ := http.NewRequest("GET", "http://localhost:9000/async", nil)
	request = request.WithContext(r.Context())

	resp, err := client.Do(request)
	if nil != err {
		io.WriteString(w, err.Error())
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

func main() {
	... //setup agent

	http.HandleFunc(phttp.WrapHandleFunc(agent, "external", "/wraprequest", wrapRequest))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "roundTripper", "/wrapclient", wrapClient))

	http.ListenAndServe(":8000", nil)
	agent.Shutdown()
}

```
[Full Example Source](/plugin/http/example/http_server.go)

## logrus
You can use the pinpoint logus plugin that has been wrapped from the [logrus](https://github.com/sirupsen/logrus) library to allow additional transaction id and span id of the pinpoint span to be printed in the log message. Call the WithField() function and pass the logus field back to the logger.
``` go
logger.WithFields(plogrus.WithField(tracer)).Fatal("ohhh, what a world")
```

``` go
import (
	"github.com/sirupsen/logrus"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	plogrus "github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus"
)

func logging(w http.ResponseWriter, r *http.Request) {
	logger := logrus.New()
	tracer := pinpoint.TracerFromRequestContext(r)
	logger.WithFields(plogrus.WithField(tracer)).Fatal("ohhh, what a world")
}
```
[Full Example Source](/plugin/logrus/example/logrus_example.go)

## mongodriver
You can instrument [mongo-driver](https://go.mongodb.org/mongo-driver) using the pinpoint mongodriver plugin.
Registering the pinpoint mongodriver plugin as the monitor of the mongo-driver will start tracking.

``` go
opts := options.Client()
opts.Monitor = pmongo.NewMonitor()
```

``` go
import (
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	pmongo "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver"
)

func mongodb(w http.ResponseWriter, r *http.Request) {
	opts := options.Client()
	opts.ApplyURI("mongodb://localhost:27017")
	opts.Monitor = pmongo.NewMonitor()
	ctx := context.Background()

	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		panic(err)
	}
	defer client.Disconnect(ctx)

	collection := client.Database("testdb").Collection("example")
	_, err = collection.InsertOne(r.Context(), bson.M{"foo": "bar", "apm": "pinpoint"})
	if err != nil {
		panic(err)
	}
}
```
[Full Example Source](/plugin/logrus/example/logrus_example.go)

## mysql
You can instrument [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) using the pinpoint mysql plugin.
When calling the sql.Open() function, pass the driver name of the pinpoint mysql plugin ('mysql-pinpoint').

``` go
db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
```

``` go
import (
	"database/sql"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
)

func query(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())

	db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
	if nil != err {
		panic(err)
	}

	ctx := pinpoint.NewContext(context.Background(), tracer)
	row := db.QueryRowContext(ctx, "SELECT count(*) from tables")
	var count int
	row.Scan(&count)

	fmt.Println("number of tables in information_schema", count)
}
```
[Full Example Source](/plugin/mysql/example/mysql_example.go)

## pgsql
You can instrument [pq](github.com/lib/pq) using the pinpoint pgsql plugin.
When calling the sql.Open() function, pass the driver name of the pinpoint pgsql plugin ('pq-pinpoint').

``` go
db, err := sql.Open("pq-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
```

``` go
import (
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql"
)

func query(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("pq-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
	if err != nil {
		panic(err)
	}

	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)
	row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
	var count int
	err = row.Scan(&count)
	if err != nil {
		log.Fatalf("sql error: %v", err)
	}

	fmt.Println("number of entries in pg_catalog.pg_tables", count)
}
```
[Full Example Source](/plugin/pgsql/example/pgsql_example.go)

## sarama
You can instrument [sarama](https://github.com/Shopify/sarama) using the pinpoint sarama plugin.

### Consumer
To track the sarama Consumer, use the NewConsumer() function of the pinpoint sarama plugin.
```go
config := sarama.NewConfig()
config.Version = sarama.V2_3_0_0
consumer, err := psarama.NewConsumer(cbrokers, config, agent)
```

``` go

import (
	"github.com/Shopify/sarama"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	psarama "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
)

const ctopic = "sample-topic"
var cbrokers = []string{"127.0.0.1:9092"}

func processMessage(msg *psarama.ConsumerMessage) {
	tracer := msg.SpanTracer()
	tracer.NewSpanEvent("processMessage")
	fmt.Println("Retrieving message: ", string(msg.Value))
	tracer.EndSpanEvent()
}

func subscribe(topic string, consumer *psarama.Consumer) {
	partitionList, err := consumer.Partitions(topic)
	if err != nil {
		fmt.Println("Error retrieving partitionList ", err)
	}
	initialOffset := sarama.OffsetOldest

	for _, partition := range partitionList {
		pc, _ := consumer.ConsumePartition(topic, partition, initialOffset)

		go func(pc *psarama.PartitionConsumer) {
			for message := range pc.Messages() {
				processMessage(message)
				message.SpanTracer().EndSpan()
			}
		}(pc)
	}
}

func main() {
	... //setup agent
	
	config := sarama.NewConfig()
	config.Version = sarama.V2_3_0_0
	consumer, err := psarama.NewConsumer(cbrokers, config, agent)
	if err != nil {
		log.Fatalf("Could not create consumer: %v", err)
	}

	subscribe(ctopic, consumer)
}

```
[Full Example Source](/plugin/sarama/example/consumer.go)

### Producer
To track the sarama Producer, use the NewSyncProducer() function of the pinpoint sarama plugin.
``` go
config := sarama.NewConfig()
config.Producer.Partitioner = sarama.NewRandomPartitioner
config.Version = sarama.V2_3_0_0

producer, err := psarama.NewSyncProducer(brokers, config)
```

``` go
package main

import (
	"github.com/Shopify/sarama"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	psarama "github.com/github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
)

var fakeDB string
const topic = "sample-topic"
var producer *psarama.SyncProducer
var brokers = []string{"127.0.0.1:9092"}

func newProducer() (*psarama.SyncProducer, error) {
	config := sarama.NewConfig()
	config.Producer.Partitioner = sarama.NewRandomPartitioner
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Return.Successes = true
	config.Version = sarama.V2_3_0_0

	producer, err := psarama.NewSyncProducer(brokers, config)

	return producer, err
}

func prepareMessage(topic, message string) *sarama.ProducerMessage {
	msg := &sarama.ProducerMessage{
		Topic:     topic,
		Partition: -1,
		Value:     sarama.StringEncoder(message),
	}

	return msg
}

func save(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	msg := prepareMessage(topic, "Hello, Kafka!!")
	producer.WithContext(r.Context())
	partition, offset, err := producer.SendMessage(msg)

	if err != nil {
		fmt.Fprintf(w, "%s error occured.", err.Error())
	} else {
		fmt.Fprintf(w, "Message was saved to partion: %d.\nMessage offset is: %d.\n", partition, offset)
	}
}

func main() {
	... //setup agent
	
	tmp, err := newProducer()
	if err != nil {
		log.Fatalf("Could not create producer: %v ", err)
	}
	producer = tmp

	http.HandleFunc(phttp.WrapHandleFunc(agent, "save", "/save", save))
	log.Fatal(http.ListenAndServe(":8081", nil))
}

```
[Full Example Source](/plugin/sarama/example/producer.go)

