package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

func externalRequest(tracer pinpoint.Tracer) int {
	req, err := http.NewRequest("GET", "http://localhost:9000/async_wrapper", nil)
	client := &http.Client{}

	tracer.NewSpanEvent("externalRequest")
	defer tracer.EndSpanEvent()

	se := tracer.SpanEvent()
	se.SetEndPoint(req.Host)
	se.SetDestination(req.Host)
	se.SetServiceType(pinpoint.ServiceTypeGoHttpClient)
	se.Annotations().AppendString(pinpoint.AnnotationHttpUrl, req.URL.String())
	tracer.Inject(req.Header)

	resp, err := client.Do(req)
	defer resp.Body.Close()

	tracer.SpanEvent().SetError(err)
	return resp.StatusCode
}

func doHandle(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.GetAgent().NewSpanTracerWithReader("HTTP Server", r.URL.Path, r.Header)
	defer tracer.EndSpan()

	span := tracer.Span()
	span.SetEndPoint(r.Host)
	defer tracer.NewSpanEvent("doHandle").EndSpanEvent()

	func() {
		defer tracer.NewSpanEvent("func_1").EndSpanEvent()

		func() {
			defer tracer.NewSpanEvent("func_2").EndSpanEvent()
			externalRequest(tracer)
		}()
		time.Sleep(1 * time.Second)
	}()

	io.WriteString(w, "hello world")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoCustomTest"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}

	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/foo", doHandle)
	log.Fatal(http.ListenAndServe("localhost:8000", nil))
}
