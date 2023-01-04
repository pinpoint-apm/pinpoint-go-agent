package main

import (
	ppgorilla "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorilla"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func hello(w http.ResponseWriter, r *http.Request) {
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed)

	time.Sleep(time.Duration(random.Intn(5000)+1) * time.Millisecond)
	io.WriteString(w, "hello world")
}

func outGoing(w http.ResponseWriter, r *http.Request) {
	client := pphttp.WrapClientWithContext(r.Context(), &http.Client{})
	resp, err := client.Get("http://localhost:9000/async_wrapper?foo=bar&say=goodbye")

	if nil != err {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}
	defer resp.Body.Close()

	w.Header().Set("foo", "bar")
	w.WriteHeader(http.StatusAccepted)
	io.WriteString(w, "wrapClient success")
}

func notrace(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "handler is not traced")
}

func sleep() {
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed).Intn(10000)
	time.Sleep(time.Duration(random+1) * time.Millisecond)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoGorillaTest"),
		pinpoint.WithAgentId("GoGorillaTestAgent"),
		pinpoint.WithHttpUrlStatEnable(true),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}

	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	r := mux.NewRouter()
	//r.Use(ppgorilla.Middleware())

	r.Handle("/", ppgorilla.WrapHandler(http.HandlerFunc(hello)))
	r.HandleFunc("/outgoing", ppgorilla.WrapHandlerFunc(outGoing))
	r.HandleFunc("/notrace", notrace)

	r.HandleFunc("/user/{name}", ppgorilla.WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sleep()
		params := mux.Vars(r)
		name := params["name"]
		message := name + " is very handsome!"
		w.Write([]byte("message: " + message))
	}))

	r.HandleFunc("/user/{name}/age/{old}", ppgorilla.WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sleep()
		params := mux.Vars(r)
		name := params["name"]
		age := params["old"]
		message := name + " is " + age + " years old."
		w.Write([]byte("message: " + message))
	}))

	http.ListenAndServe(":8000", r)
}
