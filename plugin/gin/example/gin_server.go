package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	pgin "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func endpoint(c *gin.Context) {
	//	c.Writer.WriteString("endpoint")
	c.Writer.WriteHeader(500)
}

func extCall(c *gin.Context) {
	tracer := pinpoint.FromContext(c.Request.Context())
	req, _ := http.NewRequest("GET", "http://localhost:9000/query", nil)

	tracer = phttp.NewHttpClientTracer(tracer, "http.DefaultClient.Do", req)
	resp, err := http.DefaultClient.Do(req)
	phttp.EndHttpClientTracer(tracer, resp, err)

	if nil != err {
		c.Writer.WriteString(err.Error())
		tracer.SpanEvent().SetError(err)
		return
	}

	defer resp.Body.Close()
	result, _ := ioutil.ReadAll(resp.Body)

	c.Writer.WriteString(string(result))
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoGinTest"),
		pinpoint.WithAgentId("GoGinTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	router := gin.Default()
	router.Use(pgin.Middleware(agent))

	router.GET("/endpoint", endpoint)
	router.GET("/external", extCall)
	router.Run(":8000")
}
