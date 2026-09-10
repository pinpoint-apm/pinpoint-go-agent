package main

import (
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/zap"
	"go.uber.org/zap"
)

func field(w http.ResponseWriter, r *http.Request) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	tracer := pinpoint.FromContext(r.Context())
	logger.Error("my error log message", ppzap.NewField(tracer)...)
}

func logger(w http.ResponseWriter, r *http.Request) {
	base, _ := zap.NewProduction()
	defer base.Sync()

	tracer := pinpoint.FromContext(r.Context())
	logger := ppzap.NewLogger(base, tracer).With(zap.String("foo", "bar"))
	logger.Error("logger log message")

	logger.Sugar().Errorw("sugared log message", "foo", "bar")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoZapTest"),
		pinpoint.WithAgentName("GoZapTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/field", pphttp.WrapHandlerFunc(field))
	http.HandleFunc("/logger", pphttp.WrapHandlerFunc(logger))

	http.ListenAndServe(":9000", nil)
}
