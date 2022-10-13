# Troubleshooting

## Disable the Agent
If the agent causes disruptions or problems to a production application, you can disable the agent.

### Config option
You can disable the agent by setting config option '**Enable**' to false.
Restart your application with command flag '--pinpoint-enable=false' or environment variable 'PINPOINT_GO_ENABLE=false'.
For more information, Refer the [Configuration](config.md#Enable) document.

### Shutdown function
You can stop the agent by calling Agent.Shutdown() function, and there’s no need to restart your application.
After Agent.Shutdown() is called, the agent will never collect tracing data again.
If you want to restart a agent, you can just create a new agent using the NewAgent() function,
there’s no need to restart your application as well.

As in the example below, you can write code to start and stop the agent whenever you want without.
``` go
func newAgent(w http.ResponseWriter, r *http.Request) {
    opts := []pinpoint.ConfigOption{
        pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
    }
    c, _ := pinpoint.NewConfig(opts...)
    _, err := pinpoint.NewAgent(c)
    if err == nil {
        io.WriteString(w, "New Pinpoint Go Agent - success")
    } else {
        io.WriteString(w, "New Pinpoint Go Agent - fail")
    } 	
}

func shutdown(w http.ResponseWriter, r *http.Request) {
    pinpoint.GetAgent().Shutdown()
    io.WriteString(w, "Shutdown Pinpoint Go Agent")
}

func main() {
    ...
    
    http.HandleFunc("/newagent", newAgent)
    http.HandleFunc("/shutdown", shutdown)
    http.HandleFunc("/handler", pphttp.WrapHandlerFunc(handler))
    http.ListenAndServe(":8000", r)
}
```

## Logging

Pinpoint Go Agent outputs logs related to agent operation (config, gRPC, goroutine, ...) to **stderr**.
This logs are helpful to the debugging process.

You can use config option **LogLevel** to increase the granularity of the agent’s logging.
For more information, Refer the [Configuration](config.md#LogLevel) document.
