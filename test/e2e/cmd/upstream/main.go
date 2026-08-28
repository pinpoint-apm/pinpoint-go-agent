// upstream is the traced HTTP server at the top of the end-to-end stack. It
// exercises the public tracing API, calls the HTTP and gRPC downstreams, and
// exposes agent lifecycle endpoints the smoke test drives. See
// test/e2e/README.md for the endpoint list.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/test/e2e/internal/e2e"
)

// Environment variables the lifecycle endpoints override. A reload rides the
// environment rather than a ConfigOption: this suite points every process at a
// config file, whose values win over ConfigOption, so an inline option would be
// ignored. Environment variables are applied after the file, and a restarted
// agent is a first load.
const (
	envConfigFile          = "PINPOINT_GO_CONFIGFILE"
	envSamplingCounterRate = "PINPOINT_GO_SAMPLING_COUNTERRATE"
)

var (
	grpcTarget = "localhost:50051"
	httpTarget = "localhost:8091"

	// agentMu serializes the lifecycle endpoints: a Pinpoint agent is a
	// process-global singleton, so two concurrent restarts would race.
	agentMu sync.Mutex
	agent   pinpoint.Agent
)

func startAgent(opts ...pinpoint.ConfigOption) pinpoint.Agent {
	return e2e.StartAgent(append([]pinpoint.ConfigOption{e2e.ConfigFileOption()}, opts...)...)
}

// restartAgentAndWait replaces the running agent and waits for it to come
// online: NewAgent returns before registration completes.
func restartAgentAndWait(timeout time.Duration, opts ...pinpoint.ConfigOption) bool {
	if agent != nil {
		agent.Shutdown()
		agent = nil
	}
	agent = startAgent(opts...)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if agent.Enable() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return agent.Enable()
}

func onAgentStart(w http.ResponseWriter, r *http.Request) {
	agentMu.Lock()
	defer agentMu.Unlock()
	if agent != nil && agent.Enable() {
		e2e.WriteJSON(w, http.StatusOK, map[string]string{"status": "already_running"})
		return
	}
	agent = startAgent()
	e2e.WriteJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func onAgentShutdown(w http.ResponseWriter, r *http.Request) {
	agentMu.Lock()
	defer agentMu.Unlock()
	if agent == nil {
		e2e.WriteJSON(w, http.StatusOK, map[string]string{"status": "not_running"})
		return
	}
	agent.Shutdown()
	agent = nil
	e2e.WriteJSON(w, http.StatusOK, map[string]string{"status": "shutdown"})
}

// onAgentReload restarts the agent with a new sampling rate. Production
// reconfiguration goes through the config-file watcher (see
// /agent/watch-reload); this endpoint is the coarse restart path, which the
// smoke test uses to get a deterministic sampler.
func onAgentReload(w http.ResponseWriter, r *http.Request) {
	counterRate := e2e.IntParam(r, "counter_rate", 1, 0, 1000)

	agentMu.Lock()
	defer agentMu.Unlock()
	os.Setenv(envSamplingCounterRate, strconv.Itoa(counterRate))
	started := restartAgentAndWait(30 * time.Second)
	e2e.WriteJSON(w, http.StatusOK, map[string]any{
		"status":        "reloaded",
		"counter_rate":  agent.Config().Int(pinpoint.CfgSamplingCounterRate),
		"agent_enabled": started,
	})
}

// restoreEnv puts an environment variable back the way it was.
func restoreEnv(key, value string, present bool) {
	if present {
		os.Setenv(key, value)
	} else {
		os.Unsetenv(key)
	}
}

// onAgentWatchReload drives the config-file watcher end to end: it points a
// fresh agent at a file this process alone owns, edits the sampling rate, and
// waits for the running agent to follow with no restart. Every other server in
// the run reads the shared pinpoint-config.yaml, hence the private file.
func onAgentWatchReload(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(os.TempDir(),
		"pinpoint-go-e2e-watch-"+e2e.EnvOr("PINPOINT_GO_AGENTNAME", "e2e")+".yaml")

	agentMu.Lock()
	defer agentMu.Unlock()

	// The private file carries the collector ports too: it replaces the shared
	// file wholesale, and the collector host arrives through the environment.
	config := agent.Config()
	writeConfig := func(rate int) error {
		body := fmt.Sprintf("Collector:\n  AgentPort: %d\n  SpanPort: %d\n  StatPort: %d\n"+
			"Sampling:\n  Type: COUNTER\n  CounterRate: %d\n",
			config.Int(pinpoint.CfgCollectorAgentPort),
			config.Int(pinpoint.CfgCollectorSpanPort),
			config.Int(pinpoint.CfgCollectorStatPort),
			rate)
		return os.WriteFile(path, []byte(body), 0o600)
	}
	if err := writeConfig(1); err != nil {
		e2e.WriteJSON(w, http.StatusInternalServerError, map[string]string{"status": "write_failed"})
		return
	}
	defer os.Remove(path)

	originalConfigFile, hadConfigFile := os.LookupEnv(envConfigFile)
	originalRate, hadRate := os.LookupEnv(envSamplingCounterRate)
	// The rate has to ride the file here. An environment override -- which
	// /agent/reload leaves behind -- is applied after the file and would mask
	// the reload this endpoint exists to observe.
	os.Unsetenv(envSamplingCounterRate)
	os.Setenv(envConfigFile, path)

	started := restartAgentAndWait(30 * time.Second)
	before := probeSampled(40, "/watch-probe/before/")

	after := before
	waitedMs := 0
	if err := writeConfig(2); err == nil {
		for waitedMs < 30000 {
			time.Sleep(250 * time.Millisecond)
			waitedMs += 250
			after = probeSampled(40, "/watch-probe/after/")
			if after < 40 {
				break
			}
		}
	}

	// Restore the shared configuration before returning.
	restoreEnv(envConfigFile, originalConfigFile, hadConfigFile)
	restoreEnv(envSamplingCounterRate, originalRate, hadRate)
	restartAgentAndWait(30 * time.Second)

	e2e.WriteJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"started":        started,
		"before_sampled": before,
		"after_sampled":  after,
		"waited_ms":      waitedMs,
		"reloaded":       before == 40 && after > 0 && after < 40,
	})
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	port := e2e.Port(os.Args[1:], 8090)

	if v := os.Getenv("GRPC_TARGET"); v != "" {
		grpcTarget = v
	}
	if v := os.Getenv("HTTP_TARGET"); v != "" {
		httpTarget = v
	}

	e2e.ConfigureAgentEnvironment("go-e2e-http-upstream", "go-e2e-http-up")
	agent = startAgent()
	initDB()
	if err := initGrpcClient(); err != nil {
		log.Fatalf("gRPC client: %v", err)
	}
	logGrpcTarget()

	mux := http.NewServeMux()

	// Agent lifecycle endpoints
	mux.HandleFunc("POST /agent/start", onAgentStart)
	mux.HandleFunc("POST /agent/shutdown", onAgentShutdown)
	mux.HandleFunc("POST /agent/reload", onAgentReload)
	mux.HandleFunc("POST /agent/watch-reload", onAgentWatchReload)

	// HTTP-only endpoints
	mux.HandleFunc("GET /simple", onSimple)
	mux.HandleFunc("GET /deep", onDeep)
	mux.HandleFunc("GET /wide", onWide)
	mux.HandleFunc("GET /annotated", onAnnotated)
	mux.HandleFunc("GET /features", onFeatures)
	mux.HandleFunc("GET /mixed", onMixed)
	mux.HandleFunc("GET /error", onError)
	mux.HandleFunc("GET /sampling-probe", onSamplingProbe)
	mux.HandleFunc("GET /stats", onStats)
	mux.HandleFunc("GET /ready", onReady)

	// One probe behind every Http.Server.ExcludeUrl pattern kind, plus an
	// unmatched control and an ExcludeMethod probe.
	mux.HandleFunc("GET /filter/exact", onFilterProbe)
	mux.HandleFunc("GET /filter/prefix/deep/leaf", onFilterProbe)
	mux.HandleFunc("GET /filter/seg/one", onFilterProbe)
	mux.HandleFunc("GET /filter/mid/ant/x/y", onFilterProbe)
	mux.HandleFunc("GET /filter/query", onFilterProbe)
	mux.HandleFunc("GET /filter/kept", onFilterProbe)
	mux.HandleFunc("OPTIONS /filter/method", onFilterProbe)

	// Client endpoints
	mux.HandleFunc("GET /http-client", onHttpClient)
	mux.HandleFunc("GET /grpc-unary", onGrpcUnary)
	mux.HandleFunc("GET /grpc-stream", onGrpcServerStream)
	mux.HandleFunc("GET /grpc-client-stream", onGrpcClientStream)
	mux.HandleFunc("GET /grpc-bidi", onGrpcBidi)
	mux.HandleFunc("GET /grpc-error", onGrpcError)
	mux.HandleFunc("GET /grpc-all", onGrpcAll)

	// SQL-traced endpoints (no database required)
	mux.HandleFunc("GET /db-batch", onDbBatch)
	mux.HandleFunc("GET /db-complex", onDbComplex)

	// Go's own profiler, which run_e2e.sh --profile samples during the load
	// phase. Untraced: it is diagnostics, not application traffic.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	server := &http.Server{Addr: e2e.Addr(port), Handler: mux}
	mux.HandleFunc("POST /server/shutdown", func(w http.ResponseWriter, r *http.Request) {
		e2e.WriteJSON(w, http.StatusOK, map[string]string{"status": "shutting_down"})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(ctx)
		}()
	})

	log.Printf("end-to-end test server starting on %s (see test/e2e/README.md for endpoints)", e2e.Addr(port))
	log.Printf("collector: %s", e2e.CollectorHost())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}

	agentMu.Lock()
	defer agentMu.Unlock()
	if agent != nil {
		agent.Shutdown()
	}
}
