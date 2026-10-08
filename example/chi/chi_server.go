package main

import (
	"github.com/go-chi/chi/v5/middleware"
	"io"
	"log"
	"math/rand"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

func outgoing(w http.ResponseWriter, r *http.Request) {
	sleep()

	ctx := r.Context() // carries the request's tracer; no need to rewrap it
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost:9000/async_wrapper", nil)

	resp, err := pphttp.DoClient(http.DefaultClient.Do, req)
	if nil != err {
		io.WriteString(w, err.Error())
		return
	}

	defer resp.Body.Close()
	io.Copy(w, resp.Body)

	//seed := rand.NewSource(time.Now().UnixNano())
	//random := rand.New(seed)
	//time.Sleep(time.Duration(random.Intn(2000)+1) * time.Millisecond)
}

func sleep() {
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed).Intn(10000)
	time.Sleep(time.Duration(random+1) * time.Millisecond)
}

func main() {
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoChiTest"),
		pinpoint.WithAgentName("GoChiTestAgent"),
		pinpoint.WithHttpUrlStatEnable(true),
		//pinpoint.WithSamplingCounterRate(10),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}

	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()
	defer pinpoint.ShutdownOnSignal(agent)() // SIGTERM, SIGINT

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(ppchi.Middleware())

	r.Get("/outgoing", outgoing)

	r.Get("/user/{name}", func(w http.ResponseWriter, r *http.Request) {
		sleep()
		name := chi.URLParam(r, "name")
		message := name + " is very handsome!"
		w.Write([]byte("message: " + message))
	})

	r.Get("/user/{name}/age/{old}", func(w http.ResponseWriter, r *http.Request) {
		sleep()
		name := chi.URLParam(r, "name")
		age := chi.URLParam(r, "old")
		message := name + " is " + age + " years old."
		w.Write([]byte("message: " + message))
	})

	http.ListenAndServe(":8000", r)
}
