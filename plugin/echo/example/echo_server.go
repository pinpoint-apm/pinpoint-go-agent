package main

import (
	"errors"
	"log"

	"github.com/labstack/echo"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	pecho "github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo"
)

func hello(c echo.Context) error {
	return c.String(200, "Hello World!!")
}

func myError(c echo.Context) error {
	tracer := pinpoint.TracerFromRequestContext(c.Request())
	tracer.SpanEvent().SetError(errors.New("my error message"))

	return c.String(500, "Error!!")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("testEcho"),
		pinpoint.WithAgentId("testEchoAgent"),
		pinpoint.WithCollectorHost("localhost"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	e := echo.New()
	e.Use(pecho.Middleware(agent))

	e.GET("/hello", hello)
	e.GET("/error", myError)
	e.Start(":9000")
}
