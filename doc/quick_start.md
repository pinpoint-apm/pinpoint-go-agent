# Quick Start

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
