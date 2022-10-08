# Pinpoint Go Agent Configuration

## Overview
Pinpoint Go Agent creates a Config populated with default settings, command line flags, environment variables, config file 
and config functions are prefixed with 'With', such as WithAppName.
Config uses the following precedence order.
Each item takes precedence over the item below it:

1. command line flag
2. environment variable
3. config file
4. config function
5. default

For example, if a configuration item is specified simultaneously in the environment variable and in the configuration file,
the value set in the environment variable is finally used.

It is supported JSON, YAML and Properties config files
and configuration keys used in config files are case-insensitive.

## Config Option
The titles below are used as configuration keys in config files.

### ApplicationName
ApplicationName option sets the application name.
If this option is not provided, the agent can't be started.

| command line flag           | environment variable        | config function | default |
|-----------------------------|-----------------------------|-----------------|---------|
| --pinpoint-applicationname  | PINPOINT_GO_APPLICATIONNAME | WithAppName()   |         |

### ApplicationType
ApplicationType option sets the application type.

| command line flag           | environment variable        | config function | default |
|-----------------------------|-----------------------------|-----------------|---------|
| --pinpoint-applicationtype  | PINPOINT_GO_APPLICATIONTYPE | WithAppType()   | 1800    |

### AgentId
AgentId option set id to distinguish agent.
We recommend that you enable hostname to be included.
If agent id is not set, automatically generated id is given.

| command line flag  | environment variable | config function | default |
|--------------------|----------------------|-----------------|---------|
| --pinpoint-agentid | PINPOINT_GO_AGENTID  | WithAgentId()   |         |

### AgentName
AgentName option sets the agent name.

| command line flag    | environment variable  | config function | default |
|----------------------|-----------------------|-----------------|---------|
| --pinpoint-agentname | PINPOINT_GO_AGENTNAME | WithAgentName() |         |

### Collector.Host
Collector.Host option sets the host address of Pinpoint collector.

| command line flag         | environment variable       | config function     | default     |
|---------------------------|----------------------------|---------------------|-------------|
| --pinpoint-collector-host | PINPOINT_GO_COLLECTOR_HOST | WithCollectorHost() | "localhost" |

### Collector.AgentPort
Collector.AgentPort option sets the agent port of Pinpoint collector.

| command line flag              | environment variable            | config function          | default |
|--------------------------------|---------------------------------|--------------------------|---------|
| --pinpoint-collector-agentport | PINPOINT_GO_COLLECTOR_AGENTPORT | WithCollectorAgentPort() | 9991    |

### Collector.SpanPort
Collector.SpanPort option sets the span port of Pinpoint collector.

| command line flag             | environment variable           | config function         | default |
|-------------------------------|--------------------------------|-------------------------|---------|
| --pinpoint-collector-spanport | PINPOINT_GO_COLLECTOR_SPANPORT | WithCollectorSpanPort() | 9993    |

### Collector.StatPort
Collector.StatPort option sets the stat port of Pinpoint collector.

| command line flag             | environment variable           | config function         | default |
|-------------------------------|--------------------------------|-------------------------|---------|
| --pinpoint-collector-statport | PINPOINT_GO_COLLECTOR_STATPORT | WithCollectorStatPort() | 9992    |

### Sampling.Type
Sampling.Type option sets the type of agent sampler.
Either "COUNTER" or "PERCENT" must be specified.

| command line flag        | environment variable      | config function    | default   |
|--------------------------|---------------------------|--------------------|-----------|
| --pinpoint-sampling-type | PINPOINT_GO_SAMPLING_TYPE | WithSamplingType() | "COUNTER" |

### Sampling.CounterRate
Sampling.CounterRate option sets the counter sampling rate.
Sample 1/rate. In other words, if the rate is 1, then it will be 100% and if it is 100, it will be 1% sampling.

| command line flag               | environment variable             | config function           | default |
|---------------------------------|----------------------------------|---------------------------|---------|
| --pinpoint-sampling-counterrate | PINPOINT_GO_SAMPLING_COUNTERRATE | WithSamplingCounterRate() | 1       |

### Sampling.PercentRate
Sampling.PercentRate option sets the sampling rate for a 'percent sampler'.

| command line flag               | environment variable             | config function           | default |
|---------------------------------|----------------------------------|---------------------------|---------|
| --pinpoint-sampling-percentrate | PINPOINT_GO_SAMPLING_PERCENTRATE | WithSamplingPercentRate() | 100     |

### Sampling.NewThroughput
Sampling.NewThroughput option sets the new TPS for a 'throughput sampler'.

| command line flag                 | environment variable               | config function             | default |
|-----------------------------------|------------------------------------|-----------------------------|---------|
| --pinpoint-sampling-newthroughput | PINPOINT_GO_SAMPLING_NEWTHROUGHPUT | WithSamplingNewThroughput() | 0       |

### Sampling.ContinueThroughput
Sampling.ContinueThroughput option sets the cont TPS for a 'throughput sampler'.

| command line flag                      | environment variable                    | config function                  | default |
|----------------------------------------|-----------------------------------------|----------------------------------|---------|
| --pinpoint-sampling-continuethroughput | PINPOINT_GO_SAMPLING_CONTINUETHROUGHPUT | WithSamplingContinueThroughput() | 0       |

### Stat.CollectInterval
Stat.CollectInterval option sets the statistics collection cycle (milliseconds) for the agent.

| command line flag               | environment variable             | config function           | default |
|---------------------------------|----------------------------------|---------------------------|---------|
| --pinpoint-stat-collectinterval | PINPOINT_GO_STAT_COLLECTINTERVAL | WithStatCollectInterval() | 5000    |

### Stat.BatchCount
Stat.BatchCount option sets batch delivery units for collected statistics.

| command line flag                | environment variable              | config function      | default |
|----------------------------------|-----------------------------------|----------------------|---------|
| --pinpoint-stat-batchcount       | PINPOINT_GO_STAT_BATCHCOUNT       | WithStatBatchCount() | 6       |

### SQL.TraceBindValue
SQL.TraceBindValue option enables bind value tracing for SQL Driver.

| command line flag             | environment variable           | config function         | default |
|-------------------------------|--------------------------------|-------------------------|---------|
| --pinpoint-sql-tracebindvalue | PINPOINT_GO_SQL_TRACEBINDVALUE | WithSQLTraceBindValue() | true    |

### SQL.MaxBindValueSize
SQL.MaxBindValueSize option sets the max length (bytes) of traced bind value for SQL Driver.

| command line flag               | environment variable             | config function           | default |
|---------------------------------|----------------------------------|---------------------------|---------|
| --pinpoint-sql-maxbindvaluesize | PINPOINT_GO_SQL_MAXBINDVALUESIZE | WithSQLMaxBindValueSize() | 1024    |

### SQL.TraceCommit
SQL.TraceCommit option enables commit tracing for SQL Driver.

| command line flag          | environment variable        | config function      | default |
|----------------------------|-----------------------------|----------------------|---------|
| --pinpoint-sql-tracecommit | PINPOINT_GO_SQL_TRACECOMMIT | WithSQLTraceCommit() | true    |

### SQL.TraceRollback
SQL.TraceRollback option enables rollback tracing for SQL Driver.

| command line flag            | environment variable          | config function        | default |
|------------------------------|-------------------------------|------------------------|---------|
| --pinpoint-sql-tracerollback | PINPOINT_GO_SQL_TRACEROLLBACK | WithSQLTraceRollback() | true    |

### LogLevel
LogLevel option sets the level of log generated by the pinpoint agent. 
Either debug, info, warn, or error must be set.

| command line flag   | environment variable | config function | default |
|---------------------|----------------------|-----------------|---------|
| --pinpoint-loglevel | PINPOINT_GO_LOGLEVEL | WithLogLevel()  | "info"  |

### IsContainerEnv
IsContainerEnv option sets whether the application is running in a container environment or not.
If this is not set, the agent automatically checks it.

| command line flag         | environment variable       | config function      | default |
|---------------------------|----------------------------|----------------------|---------|
| --pinpoint-iscontainerenv | PINPOINT_GO_ISCONTAINERENV | WithIsContainerEnv() |         |

### ConfigFile
The aforementioned settings can be saved to the config file is set by ConfigFile.
It is supported JSON, YAML and Properties config files
and configuration keys used in config files are case-insensitive.

| command line flag     | environment variable   | config function  | default |
|-----------------------|------------------------|------------------|---------|
| --pinpoint-configfile | PINPOINT_GO_CONFIGFILE | WithConfigFile() |         |

The format of the YAML config file is as follows:
``` yaml
applicationName: "MyAppName"
collector:
  host: "collector.myhost.com"
sampling:
  type: "percent"
  percentRate: 10
logLevel: "error"
```

* [YAML File Example](/example/pinpoint-config.yaml)
* [JSON File Example](/example/pinpoint-config.json)
* [Properties File Example](/example/pinpoint-config.prop)

### UseProfile
The configuration profile feature is supported.
You can set the profile in the config file and specify the profile to activate with the UseProfile option.

| command line flag     | environment variable   | config function  | default |
|-----------------------|------------------------|------------------|---------|
| --pinpoint-useprofile | PINPOINT_GO_USEPROFILE | WithUseProfile() |         |

```json
{
  "applicationName": "JsonAppName",
  "agentId": "JsonAgentID",
  "loglevel": "debug",
  "profile": {
    "dev": {
      "collector": {
        "host": "dev.collector.host"
      },
      "sampling": {
        "type": "COUNTER",
        "CounterRate": 1
      }
    },
    "real": {
      "collector": {
        "host": "real.collector.host"
      },
      "sampling": {
        "type": "percent",
        "percentRate": 5.5
      }
    }
  }
}
```
