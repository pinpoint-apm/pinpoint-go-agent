package main

import (
	"github.com/sirupsen/logrus"
	"log"
	"net/http"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	plogrus "github.com/pinpoint-apm/pinpoint-go-agent/plugin/logrus"
)

func logging(w http.ResponseWriter, r *http.Request) {
	logger := logrus.New()
	tracer := pinpoint.TracerFromRequestContext(r)
	logger.WithFields(plogrus.WithField(tracer)).Fatal("ohhh, what a world")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoLogrusTest"),
		pinpoint.WithAgentId("GoLogrusTestAgent"),
		pinpoint.WithCollectorHost("localhost"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc(phttp.WrapHandleFunc("/logging", logging))

	http.ListenAndServe(":9000", nil)
}
