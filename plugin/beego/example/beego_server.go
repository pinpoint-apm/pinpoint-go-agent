package main

import (
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/beego/beego/v2/client/httplib"
	"github.com/beego/beego/v2/server/web"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego"
)

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoBeegoTest"),
		pinpoint.WithAgentId("GoBeegoTestAgent"),
		pinpoint.WithHttpUrlStatEnable(true),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	ctrl := &MainController{}
	web.Router("/hello", ctrl, "get:Hello")
	web.Router("/users/:id", ctrl, "get:User")
	web.Router("/path/*", ctrl, "get:Splat")
	web.Router("/regexp/:username:string", ctrl, "get:Regexp")
	web.InsertFilterChain("/*", ppbeego.ServerFilterChain())
	web.Run("localhost:9000")

	//	web.RunWithMiddleWares("localhost:9000", ppbeego.Middleware())
}

type MainController struct {
	web.Controller
}

func (ctrl *MainController) Hello() {
	sleep()

	tracer := pinpoint.FromContext(ctrl.Ctx.Request.Context())
	defer tracer.NewSpanEvent("f1").EndSpanEvent()
	defer tracer.NewSpanEvent("f2").EndSpanEvent()

	req := httplib.Get("http://localhost:9090/")
	//ppbeego.DoRequest(tracer, req)
	req.AddFilters(ppbeego.ClientFilterChain(tracer))

	_, err := req.Response()
	if err == nil {
		str, _ := req.String()
		ctrl.Ctx.WriteString(str)
	} else {
		ctrl.Ctx.Output.SetStatus(501)
		//ctrl.Ctx.Abort(501, err.Error())
	}
}

func (ctrl *MainController) User() {
	sleep()
	id := ctrl.Ctx.Input.Param(":id")
	json := map[string]string{
		"id": id,
	}
	ctrl.Ctx.Output.JSON(json, true, true)
}

func (ctrl *MainController) Splat() {
	sleep()
	splat := ctrl.Ctx.Input.Param(":splat")
	json := map[string]string{
		"splat": splat,
	}
	ctrl.Ctx.Output.JSON(json, true, true)
}

func (ctrl *MainController) Regexp() {
	sleep()
	name := ctrl.Ctx.Input.Param(":username")
	json := map[string]string{
		"username": name,
	}
	ctrl.Ctx.Output.JSON(json, true, true)
}

func sleep() {
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed).Intn(10000)
	time.Sleep(time.Duration(random+1) * time.Millisecond)
}
