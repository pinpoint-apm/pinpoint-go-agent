package main

import (
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber"
)

// Handler
func hello(c *fiber.Ctx) error {
	tracer := pinpoint.FromContext(c.UserContext())

	defer tracer.NewSpanEvent("f1").EndSpanEvent()
	defer tracer.NewSpanEvent("f2").EndSpanEvent()

	return c.SendString("Hello, World !!")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoFiberTest"),
		pinpoint.WithAgentId("GoFiberTestAgent"),
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
	//app.Use(ppfiber.Middleware())

	// Routes
	app.Get("/hello", ppfiber.WrapHandler(hello))
	app.Get("/about", ppfiber.WrapHandler(func(c *fiber.Ctx) error {
		return c.SendString("about")
	}))

	// Start server
	log.Fatal(app.Listen(":9000"))
}
