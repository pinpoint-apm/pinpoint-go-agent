package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/slog"
)

func handler(w http.ResponseWriter, r *http.Request) {
	logger := slog.New(ppslog.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))

	// the request context carries the pinpoint.Tracer
	logger.With("foo", "bar").ErrorContext(r.Context(), "handler log message")
}

func attrs(w http.ResponseWriter, r *http.Request) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	tracer := pinpoint.FromContext(r.Context())

	logger.LogAttrs(r.Context(), slog.LevelError, "attrs log message", ppslog.NewAttrs(tracer)...)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoSlogTest"),
		pinpoint.WithAgentName("GoSlogTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/handler", pphttp.WrapHandlerFunc(handler))
	http.HandleFunc("/attrs", pphttp.WrapHandlerFunc(attrs))

	http.ListenAndServe(":9000", nil)
}
