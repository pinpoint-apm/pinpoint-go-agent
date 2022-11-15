package main

import (
	"log"
	"os"

	"github.com/beego/beego/v2/client/httplib"
	"github.com/beego/beego/v2/server/web"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego"
)

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoBeegoTest"),
		pinpoint.WithAgentId("GoBeegoTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	web.Router("/", &MainController{})
	web.RunWithMiddleWares("localhost:9000", ppbeego.Middleware())
}

type MainController struct {
	web.Controller
}

func (m *MainController) Get() {
	tracer := pinpoint.FromContext(m.Ctx.Request.Context())
	defer tracer.NewSpanEvent("f1").EndSpanEvent()
	defer tracer.NewSpanEvent("f2").EndSpanEvent()

	req := httplib.Get("http://localhost:9090/")
	ppbeego.DoRequest(tracer, req)
	str, err := req.String()
	if err == nil {
		m.Ctx.WriteString(str)
	} else {
		m.Ctx.WriteString(err.Error())
	}
}
