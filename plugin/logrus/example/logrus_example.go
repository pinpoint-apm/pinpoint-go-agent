package main

import (
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	plogrus "github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus"
	"github.com/sirupsen/logrus"
)

func field(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())

	logrus.SetFormatter(&logrus.JSONFormatter{})
	entry := logrus.WithFields(plogrus.NewField(tracer))
	entry.Info("my error log message")
}

func entry(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())

	logrus.SetFormatter(&logrus.JSONFormatter{})
	entry := plogrus.NewEntry(tracer).WithField("foo", "bar")
	entry.Error("entry log message 1")

	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	entry = plogrus.NewLoggerEntry(logger, tracer).WithField("foo", "bar")
	entry.Error("entry log message 2")
}

func hook(w http.ResponseWriter, r *http.Request) {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.AddHook(plogrus.NewHook())

	entry := logger.WithContext(r.Context()).WithField("foo", "bar")
	entry.Error("hook log message")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoLogrusTest"),
		pinpoint.WithAgentId("GoLogrusTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/field", phttp.WrapHandlerFunc(field))
	http.HandleFunc("/entry", phttp.WrapHandlerFunc(entry))
	http.HandleFunc("/hook", phttp.WrapHandlerFunc(hook))

	http.ListenAndServe(":9000", nil)
}
