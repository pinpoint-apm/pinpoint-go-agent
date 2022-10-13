# Troubleshooting

## Logging

## Disable the Agent
If the agent causes disruptions or problems to a production application, you can disable the agent.

### Config option
You can disable the agent by setting **Enable** config option to false.
Restart your application with command flag '--pinpoint-enable=false' or environment variable 'PINPOINT_GO_ENABLE=false'.
Refer the [Configuration](config.md#Enable) document.

### Shutdown function
You can stop the agent by calling Agent.Shutdown() function, there’s no need to restart your application.

``` go
func shutdown(w http.ResponseWriter, r *http.Request) {
    pinpoint.GetAgent().Shutdown()
    io.WriteString(w, "Shutdown Pinpoint Go Agent")
}

func main() {
    ...
    
    http.HandleFunc("/shutdown", shutdown)
    http.ListenAndServe(":8000", r)
}
```

After Agent.Shutdown() is called, the agent will never collect tracing data again.
If you want to restart a agent, you can just create a new agent using the NewAgent() function,
there’s no need to restart your application as well.

``` go
func newAgent(w http.ResponseWriter, r *http.Request) {
    opts := []pinpoint.ConfigOption{
        pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
    }
    c, _ := pinpoint.NewConfig(opts...)
    agent, err := pinpoint.NewAgent(c)
    if err == nil {
        io.WriteString(w, "New Pinpoint Go Agent - success")
    } else {
        io.WriteString(w, "New Pinpoint Go Agent - fail")
    } 	
}

func main() {
    ...
    
    http.HandleFunc("/newagent", newAgent)
    http.ListenAndServe(":8000", r)
}
```
