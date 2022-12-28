package main

import (
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func endpoint(c *gin.Context) {
	tracer := pinpoint.FromContext(c.Request.Context())
	defer tracer.NewSpanEvent("f1").EndSpanEvent()
	defer tracer.NewSpanEvent("f2").EndSpanEvent()
	tracer.NewSpanEvent("f3").EndSpanEvent()

	var i http.ResponseWriter
	i.Header() //panic

	//	c.Writer.WriteString("endpoint")
	c.Writer.WriteHeader(500)
}

func extCall(c *gin.Context) {
	sleep()
	tracer := pinpoint.FromContext(c.Request.Context())
	req, _ := http.NewRequest("GET", "http://localhost:9000/query", nil)

	tracer = pphttp.NewHttpClientTracer(tracer, "http.DefaultClient.Do", req)
	resp, err := http.DefaultClient.Do(req)
	pphttp.EndHttpClientTracer(tracer, resp, err)

	if nil != err {
		c.Writer.WriteHeader(500)
		c.Writer.WriteString(err.Error())
		tracer.SpanEvent().SetError(err)
		return
	}

	defer resp.Body.Close()
	result, _ := ioutil.ReadAll(resp.Body)

	c.Writer.WriteString(string(result))
}

func sleep() {
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed).Intn(10000)
	time.Sleep(time.Duration(random+1) * time.Millisecond)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoGinTest"),
		pinpoint.WithAgentId("GoGinTestAgent"),
		pinpoint.WithSamplingRate(10),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	router := gin.Default()
	router.Use(gin.Recovery())
	router.Use(ppgin.Middleware())

	//router.GET("/endpoint", ppgin.WrapHandler(endpoint))
	//router.GET("/external", ppgin.WrapHandler(extCall))

	router.GET("/user/:name", func(c *gin.Context) {
		sleep()
		name := c.Param("name")
		message := name + " is very handsome!"
		c.JSON(200, gin.H{"message": message})
	})

	router.GET("/user/:name/age/:old", func(c *gin.Context) {
		sleep()
		name := c.Param("name")
		age := c.Param("old")
		message := name + " is " + age + " years old."
		c.JSON(200, gin.H{"message": message})
	})

	router.GET("/color/:color/*fruits", func(c *gin.Context) {
		sleep()
		color := c.Param("color")
		fruits := c.Param("fruits")
		fruitArray := strings.Split(fruits, "/")
		fruitArray = append(fruitArray[:0], fruitArray[1:]...)
		c.JSON(200, gin.H{"color": color, "fruits": fruitArray})
	})

	router.GET("/external", extCall)
	router.Run(":8000")
}
