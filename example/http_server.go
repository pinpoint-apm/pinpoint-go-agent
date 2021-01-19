package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
)

func index(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "hello world")
}

func seterror(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	tracer.SpanEvent().SetError(errors.New("my error message"))
	w.WriteHeader(500)
}

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

func query(span pinpoint.Tracer) {
	db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
	if nil != err {
		panic(err)
	}

	ctx := pinpoint.NewContext(context.Background(), span)
	row := db.QueryRowContext(ctx, "SELECT count(*) from tables")
	var count int
	row.Scan(&count)

	fmt.Println("number of tables in information_schema", count)
}

func async(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	wg := &sync.WaitGroup{}
	wg.Add(1)

	go func(asyncSpan pinpoint.Tracer) {
		defer wg.Done()
		defer asyncSpan.EndSpan() //!!must be called
		defer asyncSpan.NewSpanEvent("async_go_routine").EndSpanEvent()

		query(asyncSpan)
		time.Sleep(10 * time.Millisecond)
	}(tracer.NewAsyncSpan())

	wg.Wait()
	w.Write([]byte("done!"))
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoExample"),
		pinpoint.WithAgentId("GoExampleAgent12345678901234567890"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	c, _ := pinpoint.NewConfig(opts...)
	t, _ := pinpoint.NewAgent(c)

	http.HandleFunc(phttp.WrapHandleFunc(t, "index", "/", index))
	http.HandleFunc(phttp.WrapHandleFunc(t, "error", "/error", seterror))
	http.HandleFunc(phttp.WrapHandleFunc(t, "outgoing", "/outgoing", outgoing))
	http.HandleFunc(phttp.WrapHandleFunc(t, "async", "/async", async))

	http.ListenAndServe(":9000", nil)
	t.Shutdown()
}
