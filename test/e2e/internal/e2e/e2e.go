// Package e2e holds the helpers shared by the end-to-end test servers.
package e2e

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// TraceHeaders are the response headers every server sets so the caller can
// verify propagation without reading the collector.
const (
	HeaderTraceID = "X-It-Trace-Id"
	HeaderSpanID  = "X-It-Span-Id"
)

// SetDefaultEnv sets key only when it is not already present, so the runner's
// environment always wins.
func SetDefaultEnv(key, value string) {
	if _, ok := os.LookupEnv(key); !ok {
		os.Setenv(key, value)
	}
}

// EnvOr returns the value of key, or fallback when it is unset.
func EnvOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

// CollectorHost reports the collector the run was pointed at. The agent reads
// the same variable.
func CollectorHost() string {
	return EnvOr("PINPOINT_GO_COLLECTOR_HOST", "")
}

// ConfigureAgentEnvironment applies the per-process identity and the feature
// switches the suite depends on, without overriding the runner.
func ConfigureAgentEnvironment(application, agentName string) {
	SetDefaultEnv("PINPOINT_GO_APPLICATIONNAME", application)
	SetDefaultEnv("PINPOINT_GO_AGENTNAME", agentName)
	SetDefaultEnv("PINPOINT_GO_AGENTID", agentName)
	SetDefaultEnv("PINPOINT_GO_HTTP_URLSTAT_ENABLE", "true")
	SetDefaultEnv("PINPOINT_GO_SQL_TRACEQUERYSTAT", "true")
	SetDefaultEnv("PINPOINT_GO_SQL_TRACEBINDVALUE", "true")
	SetDefaultEnv("PINPOINT_GO_ERROR_TRACECALLSTACK", "true")
}

// StartAgent builds a config from the shared config file plus the environment
// and installs a new global agent. It returns the agent even when startup
// failed, in which case the noop agent is returned and the caller keeps
// serving untraced requests.
func StartAgent(opts ...pinpoint.ConfigOption) pinpoint.Agent {
	config, err := pinpoint.NewConfig(opts...)
	if err != nil {
		log.Printf("pinpoint config error: %v", err)
		return pinpoint.NoopAgent()
	}
	agent, err := pinpoint.NewAgent(config)
	if err != nil {
		log.Printf("pinpoint agent start failed: %v; check the agent log", err)
	}
	return agent
}

// ConfigFileOption points the agent at the suite's shared configuration file.
func ConfigFileOption() pinpoint.ConfigOption {
	return pinpoint.WithConfigFile(EnvOr("PINPOINT_GO_CONFIGFILE", "pinpoint-config.yaml"))
}

// WriteJSON writes body as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response: %v", err)
	}
}

// IntParam reads a bounded integer query parameter.
func IntParam(r *http.Request, name string, def, min, max int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// SpanIDString renders a span id the way the smoke test expects to read it.
func SpanIDString(tracer pinpoint.Tracer) string {
	return strconv.FormatInt(tracer.SpanId(), 10)
}

// Port resolves the listen port from the first command-line argument.
func Port(args []string, def int) int {
	if len(args) > 0 {
		if p, err := strconv.Atoi(args[0]); err == nil {
			return p
		}
	}
	return def
}

// Addr formats a listen address for port.
func Addr(port int) string { return fmt.Sprintf(":%d", port) }
