package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
)

func outGoingRequest(w http.ResponseWriter, ctx context.Context) {
	client := phttp.WrapClient(nil)

	request, _ := http.NewRequest("GET", "http://localhost:9001/query", nil)
	request = request.WithContext(ctx)

	resp, err := client.Do(request)
	if nil != err {
		w.Header().Set("foo", "error")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}
	defer resp.Body.Close()

	w.Header().Set("foo", "success")
	io.WriteString(w, "success")
}

func asyncWithTracer(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	wg := &sync.WaitGroup{}
	wg.Add(1)

	go func(asyncTracer pinpoint.Tracer) {
		defer wg.Done()

		defer asyncTracer.EndSpan() //!!must be called
		defer asyncTracer.NewSpanEvent("asyncWithTracer_goroutine").EndSpanEvent()

		ctx := pinpoint.NewContext(context.Background(), asyncTracer)
		outGoingRequest(w, ctx)
	}(tracer.NewGoroutineTracer())

	wg.Wait()
}

func asyncWithChan(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	wg := &sync.WaitGroup{}
	wg.Add(1)

	ch := make(chan pinpoint.Tracer)

	go func() {
		defer wg.Done()

		asyncTracer := <-ch
		defer asyncTracer.EndSpan() //!!must be called
		defer asyncTracer.NewSpanEvent("asyncWithChan_goroutine").EndSpanEvent()

		ctx := pinpoint.NewContext(context.Background(), asyncTracer)
		outGoingRequest(w, ctx)
	}()

	ch <- tracer.NewGoroutineTracer()

	wg.Wait()
}

func asyncWithContext(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	wg := &sync.WaitGroup{}
	wg.Add(1)

	go func(asyncCtx context.Context) {
		defer wg.Done()

		asyncTracer := pinpoint.FromContext(asyncCtx)
		defer asyncTracer.EndSpan() //!!must be called
		defer asyncTracer.NewSpanEvent("asyncWithContext_goroutine").EndSpanEvent()

		ctx := pinpoint.NewContext(context.Background(), asyncTracer)
		outGoingRequest(w, ctx)
	}(pinpoint.NewContext(context.Background(), tracer.NewGoroutineTracer()))

	wg.Wait()
}

func asyncFunc(asyncCtx context.Context) {
	w := asyncCtx.Value("wr").(http.ResponseWriter)
	outGoingRequest(w, asyncCtx)
}

func asyncWithWrapper(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	ctx := context.WithValue(context.Background(), "wr", w)
	f := tracer.WrapGoroutine("asyncFunc", asyncFunc, ctx)
	go f()
	time.Sleep(100 * time.Millisecond)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoAsyncExample"),
		pinpoint.WithAgentId("GoAsyncExampleAgent"),
		//pinpoint.WithSamplingCounterRate(100),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
		//phttp.WithHttpServerRecordRequestHeader([]string{"HEADERS-ALL"}),
		//phttp.WithHttpServerRecordRespondHeader([]string{"HEADERS-ALL"}),
	}
	c, _ := pinpoint.NewConfig(opts...)
	agent, _ := pinpoint.NewAgent(c)
	defer agent.Shutdown()

	http.HandleFunc("/async_chan", phttp.WrapHandlerFunc(asyncWithChan))
	http.HandleFunc("/async_context", phttp.WrapHandlerFunc(asyncWithContext))
	http.HandleFunc("/async_tracer", phttp.WrapHandlerFunc(asyncWithTracer))
	http.HandleFunc("/async_wrapper", phttp.WrapHandlerFunc(asyncWithWrapper))

	http.ListenAndServe(":9000", nil)
}
