package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber"
)

// Handler
func hello(c *fiber.Ctx) error {
	sleep()
	tracer := pinpoint.FromContext(c.UserContext())

	defer tracer.NewSpanEvent("f1").EndSpanEvent()
	defer tracer.NewSpanEvent("f2").EndSpanEvent()

	return c.Status(fiber.StatusAccepted).SendString("Accepted")
}

func sleep() {
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed).Intn(10000)
	time.Sleep(time.Duration(random+1) * time.Millisecond)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoFiberTest"),
		pinpoint.WithAgentId("GoFiberTestAgent"),
		pinpoint.WithHttpUrlStatEnable(true),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	// Fiber instance
	app := fiber.New()
	app.Use(ppfiber.Middleware())

	// Routes
	app.Get("/hello", hello)
	//app.Get("/hello", ppfiber.WrapHandler(hello))
	//app.Get("/about", ppfiber.WrapHandler(func(c *fiber.Ctx) error {
	//	sleep()
	//	return c.SendString("about")
	//}))

	app.Get("/users/:name/age/:old", func(c *fiber.Ctx) error {
		sleep()
		fmt.Fprintf(c, "%s\n", c.Params("name"))
		fmt.Fprintf(c, "%s\n", c.Params("old"))
		return nil
	})

	// Start server
	log.Fatal(app.Listen(":9000"))
}
