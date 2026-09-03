# Quick Start

Pinpoint Go Agent enables you to monitor Go applications using Pinpoint. This
guide takes you from `go get` to a traced request in the Pinpoint UI.

## Prerequisites

* **Go 1.25+**
* A running **Pinpoint 2.4.0+** collector, and its host address. Three ports
  must be reachable from your application: 9991 (agent), 9993 (span) and
  9992 (stat).
* Linux, macOS or Windows.

Go compiles to native machine code, so — unlike the Java agent — there is
nothing to attach at startup. Instrumentation is a source change, which is why
this guide is about code and not about a launcher.

## Install
### Go get
```
go get github.com/pinpoint-apm/pinpoint-go-agent
```

### import
``` go
import "github.com/pinpoint-apm/pinpoint-go-agent"
```

## Create an Agent
Go programs cannot be automatically instrumented because they are compiled into native machine code.
Therefore, developers must add the codes for instrumenting to the Go program they want to track.

First, you can create a pinpoint agent from the main function or http request handler.
After you set the application name, agent id, pinpoint collector's host address, and so on for ConfigOptions, 
you can call the NewAgent() function to create an agent. 
For more information on config option, refer the [Configuration](config.md) document.

``` go
func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("Your Application Name"),
		pinpoint.WithAgentId("Agent Id"),
		pinpoint.WithCollectorHost("pinpoint's collector host"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	
	...
```

## Instrument HTTP Request

As mentioned earlier, the Go application must be manually instrumented at the source code level. 
Pinpoint Go Agent provides plugins packages to help developers trace the popular frameworks and toolkits. 
These packages help you to make instruments with simple source code modifications.
For more information on plugins packages, refer the [Plugin User Guide](plugin_guide.md).

### Inbound Http Request

The pinpoint http plugin lets you trace Go's built-in http packages.
For example, if you want to trace the handler of the http server below,

``` go
http.HandleFunc("/", index)
```
you can write code for the instruments as shown below.
``` go
http.HandleFunc("/", pphttp.WrapHandlerFunc(index))
```

The complete example code for tracing the http server's handler is as follows:
``` go
package main

import (
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func index(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "hello world")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("TraceWebRequest"),
		pinpoint.WithAgentId("TraceWebRequestAgent"),
		pinpoint.WithCollectorHost("localhost"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/", pphttp.WrapHandlerFunc(index))
	...
}
```

### Outgoing Http Request 
If you are tracking outgoing HTTP requests, you must instrument the HTTP client. 
The WrapClient() function in the pinpoint http plugin allows you to trace http client calls.

``` go
func outgoing(w http.ResponseWriter, r *http.Request) {
	client := phttp.WrapClient(nil)

	request, _ := http.NewRequest("GET", "http://localhost:9000/hello", nil)
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
	
	http.HandleFunc("/outgoing", pphttp.WrapHandlerFunc(outgoing))
}
```

## Instrument Web Framework
Pinpoint Go Agent provides a plugin to track the Gin, Echo, Chi and Gorilla Web framework.
Below is an example of tracking the handler of the Gin framework.
You can simply register Gin plugin with the middleware of the Gin or wrap the Gin handler like http plugin.

``` go
router.Use(pgin.Middleware())
```
``` go
package main

import (
	"github.com/gin-gonic/gin"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin"
)

func hello(c *gin.Context) {
	c.Writer.WriteString("hello")
}

func main() {
	... //set up agent
	
	router := gin.Default()
	router.Use(ppgin.Middleware())

	router.GET("/hello", hello)
	router.Run(":8000")
}
```

## Instrument Database Query
Pinpoint mysql plugin is a 'database/sql' driver that instruments the go-sql-driver/mysql. 
When invoking the sql.Open() function, instead of the go-sql-driver/mysql driver,
``` go
import "database/sql"
import _ "github.com/go-sql-driver/mysql"

db, err := sql.Open("mysql", "user:password@/dbname")
```
you can use the pinpoint mysql plugin.
``` go
import _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"

db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
```

Below is a complete example of tracking MySQL calls.
``` go
package main

import (
	"database/sql"

	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
	github.com/pinpoint-apm/pinpoint-go-agent"
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

## Context propagation
In the example of trace database query above, looking at the query() function,
there is a code that invokes the pinpoint.FromContext() function to acquire the tracer.

``` go
tracer := pinpoint.FromContext(r.Context())
```

And, the query() function calls the pinpoint.NewContext() function to add the tracer to the go context.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) from tables")
```

The tracer is the object that implements the Tracer interface of the Pinpoint Go Agent,
which generates and stores instrumentation information. When calling the go function, 
we use the context of the go language to propagate this tracer. 
Pinpoint Go Agent provides a function that adds a tracer to the context, 
and a function that imports a tracer from the context, respectively.

``` go
NewContext(ctx context.Context, tracer Tracer) context.Context 
FromContext(ctx context.Context) Tracer
```

## Configure the Agent

The example above configures the agent in code, which is the shortest way to a
first trace. For anything beyond that, a config file or the environment is
better — both work without recompiling, and a config file additionally lets you
change some options on a running process.

```yaml
# pinpoint-config.yaml
applicationName: "MyAppName"
collector:
  host: "collector.myhost.com"
sampling:
  type: "COUNTER"
  counterRate: 1
log:
  level: "info"
```

```go
cfg, _ := pinpoint.NewConfig(
    pinpoint.WithConfigFile("/etc/myapp/pinpoint-config.yaml"),
)
```

```bash
PINPOINT_GO_APPLICATIONNAME=MyAppName \
PINPOINT_GO_COLLECTOR_HOST=collector.myhost.com \
./myapp
```

Sources are merged in this precedence order, each overriding the one below:
command flag, environment variable, config file, config function, default. See
[Configuration](config.md) for every option, and the
[examples](config.md#configuration-examples) for development, production and
container setups.

## Verify

Start your application and watch its **stderr** — that is where the agent logs
by default:

```text
INFO[...] new pinpoint agent          module=pinpoint src=agent
INFO[...] connect to collector: collector.myhost.com:9991 (ssl: false)  module=pinpoint src=grpc
INFO[...] success to register agent   module=pinpoint src=agent
```

`success to register agent` means the collector accepted this agent. Then send
a request through an instrumented handler:

```bash
curl http://localhost:8000/
```

and find your `ApplicationName` in the Pinpoint UI's application list. The
transaction should appear within a few seconds.

Two things to get right from the start:

```go
agent, err := pinpoint.NewAgent(cfg)
if err != nil {
    log.Printf("pinpoint agent start failed: %v", err)  // always check this
}
defer agent.Shutdown()                                  // flush before exit
```

`NewAgent()` returns a **no-op agent plus an error** when the configuration is
invalid — the application then runs perfectly and reports nothing, so the error
is your only signal. And because spans are sent by a separate goroutine, a
process that exits immediately can drop whatever is still queued;
`defer agent.Shutdown()` is what flushes it.

## Runnable examples

The [example](/example) directory has complete programs you can build and run:

| File | Shows |
|---|---|
| [http_server.go](/example/http_server/http_server.go) | instrumented HTTP server and client |
| [async.go](/example/async/async.go) | goroutine tracing and distributed tracing |
| [custom.go](/example/custom/custom.go) | hand-written spans and span events |
| [stand_alone.go](/example/stand_alone/stand_alone.go) | a non-HTTP program, plus SQL and gorm |

```bash
cd example
PINPOINT_GO_COLLECTOR_HOST=collector.myhost.com go run http_server.go
```

Every plugin directory also carries its own `README.md` and `example/`.

## Next Steps

* [Plugin User Guide](plugin_guide.md) — find the plugin for your framework,
  database, cache or queue.
* [Custom Instrumentation](instrument.md) — trace what no plugin covers.
* [Tracer, Span, and Annotation Contracts](api_contracts.md) — the rules the
  API expects you to keep. Short, and worth reading before you write the
  second instrument.
* [Configuration](config.md) — every option, plus sampling and privacy
  settings for production.
* [Troubleshooting](troubleshooting.md) — when the trace does not show up.

## Troubleshooting

| Symptom | First thing to check |
|---|---|
| No `new pinpoint agent` line | `ApplicationName` is required; check the error from `NewAgent()` |
| No `success to register agent` | collector host and all three ports; TLS settings |
| Agent registered, but nothing in the UI | is anything actually instrumented? Go traces nothing by default |
| Nothing in the UI, sampling suspected | set `Sampling.CounterRate` to 1 while diagnosing |
| Short-lived program reports nothing | add `defer agent.Shutdown()` |
| Only the first hop appears | the client must be wrapped, and the request must carry the tracer's context |

Run once with `PINPOINT_GO_LOG_LEVEL=debug` before digging further: the agent
prints its fully resolved configuration at startup, and reports API misuse
(`src=span` warnings) that is silent at the default level. See
[Troubleshooting](troubleshooting.md) for the full guide.
