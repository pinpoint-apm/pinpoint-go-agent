package pinpoint

import (
	"github.com/stretchr/testify/assert"
	"os"
	"testing"
)

func TestNewConfig_DefaultValue(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, "TestApp", c.String(cfgAppName), cfgAppName)
			assert.Equal(t, ServiceTypeGoApp, c.Int(cfgAppType), cfgAppType)
			assert.NotEmpty(t, c.String(cfgAgentID), cfgAgentID)
			assert.Empty(t, c.String(cfgAgentName), cfgAgentName)
			assert.Equal(t, "localhost", c.String(cfgCollectorHost), cfgCollectorHost)
			assert.Equal(t, 9991, c.Int(cfgCollectorAgentPort), cfgCollectorAgentPort)
			assert.Equal(t, 9993, c.Int(cfgCollectorSpanPort), cfgCollectorSpanPort)
			assert.Equal(t, 9992, c.Int(cfgCollectorStatPort), cfgCollectorStatPort)
			assert.Equal(t, "info", c.String(cfgLogLevel), cfgLogLevel)
			assert.Equal(t, SamplingTypeCounter, c.String(cfgSamplingType), cfgSamplingType)
			assert.Equal(t, 1, c.Int(cfgSamplingCounterRate), cfgSamplingCounterRate)
			assert.Equal(t, float64(100), c.Float(cfgSamplingPercentRate), cfgSamplingPercentRate)
			assert.Equal(t, 0, c.Int(cfgSamplingNewThroughput), cfgSamplingNewThroughput)
			assert.Equal(t, 0, c.Int(cfgSamplingContinueThroughput), cfgSamplingContinueThroughput)
			assert.Equal(t, 5000, c.Int(cfgStatCollectInterval), cfgStatCollectInterval)
			assert.Equal(t, 6, c.Int(cfgStatBatchCount), cfgStatBatchCount)
			assert.Equal(t, false, c.Bool(cfgIsContainerEnv), cfgIsContainerEnv)
			assert.Empty(t, c.String(cfgConfigFile), cfgConfigFile)
			assert.Empty(t, c.String(cfgUseProfile), cfgUseProfile)
			assert.Equal(t, true, c.Bool(cfgSQLTraceBindValue), cfgSQLTraceBindValue)
			assert.Equal(t, 1024, c.Int(cfgSQLMaxBindValueSize), cfgSQLMaxBindValueSize)
			assert.Equal(t, true, c.Bool(cfgSQLTraceCommit), cfgSQLTraceCommit)
			assert.Equal(t, true, c.Bool(cfgSQLTraceRollback), cfgSQLTraceRollback)
		})
	}
}

func TestNewConfig_WithFunc(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithAppType(1234),
		WithAgentId("TestAgent"),
		WithAgentName("TestAgentName"),
		WithCollectorHost("func.collector.host"),
		WithCollectorAgentPort(7777),
		WithCollectorSpanPort(8888),
		WithCollectorStatPort(9999),
		WithLogLevel("error"),
		WithSamplingType("percent"),
		WithSamplingPercentRate(90),
		WithSamplingCounterRate(200),
		WithSamplingNewThroughput(20),
		WithSamplingContinueThroughput(30),
		WithStatCollectInterval(10000),
		WithStatBatchCount(3),
		WithIsContainerEnv(true),
		WithSQLTraceBindValue(false),
		WithSQLMaxBindValueSize(512),
		WithSQLTraceCommit(false),
		WithSQLTraceRollback(false),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, "TestApp", c.String(cfgAppName), cfgAppName)
			assert.Equal(t, 1234, c.Int(cfgAppType), cfgAppType)
			assert.Equal(t, "TestAgent", c.String(cfgAgentID), cfgAgentID)
			assert.Equal(t, "TestAgentName", c.String(cfgAgentName), cfgAgentName)
			assert.Equal(t, "func.collector.host", c.String(cfgCollectorHost), cfgCollectorHost)
			assert.Equal(t, 7777, c.Int(cfgCollectorAgentPort), cfgCollectorAgentPort)
			assert.Equal(t, 8888, c.Int(cfgCollectorSpanPort), cfgCollectorSpanPort)
			assert.Equal(t, 9999, c.Int(cfgCollectorStatPort), cfgCollectorStatPort)
			assert.Equal(t, "error", c.String(cfgLogLevel), cfgLogLevel)
			assert.Equal(t, "percent", c.String(cfgSamplingType), cfgSamplingType)
			assert.Equal(t, 200, c.Int(cfgSamplingCounterRate), cfgSamplingCounterRate)
			assert.Equal(t, float64(90), c.Float(cfgSamplingPercentRate), cfgSamplingPercentRate)
			assert.Equal(t, 20, c.Int(cfgSamplingNewThroughput), cfgSamplingNewThroughput)
			assert.Equal(t, 30, c.Int(cfgSamplingContinueThroughput), cfgSamplingContinueThroughput)
			assert.Equal(t, 10000, c.Int(cfgStatCollectInterval), cfgStatCollectInterval)
			assert.Equal(t, 3, c.Int(cfgStatBatchCount), cfgStatBatchCount)
			assert.Equal(t, true, c.Bool(cfgIsContainerEnv), cfgIsContainerEnv)
			assert.Equal(t, false, c.Bool(cfgSQLTraceBindValue), cfgSQLTraceBindValue)
			assert.Equal(t, 512, c.Int(cfgSQLMaxBindValueSize), cfgSQLMaxBindValueSize)
			assert.Equal(t, false, c.Bool(cfgSQLTraceCommit), cfgSQLTraceCommit)
			assert.Equal(t, false, c.Bool(cfgSQLTraceRollback), cfgSQLTraceRollback)
		})
	}
}

func TestNewConfig_AppNameMissing(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAgentId("TestAgent"),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewConfig(tt.args.opts...)
			assert.Error(t, err, "error")
		})
	}
}

func TestNewConfig_GenerateAgentId(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, c.String(cfgAppName), "TestApp", cfgAppName)
			assert.NotNil(t, c.String(cfgAgentID), cfgAgentID)
		})
	}
}

func TestNewConfig_ConfigFileYaml(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithAgentId("TestAgent"),
		WithConfigFile("example/pinpoint-config.yaml"),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, "MyAppName", c.String(cfgAppName), cfgAppName)
			assert.Equal(t, 1900, c.Int(cfgAppType), cfgAppType)
			assert.Equal(t, "MyAgentID", c.String(cfgAgentID), cfgAgentID)
			assert.Equal(t, "my.collector.host", c.String(cfgCollectorHost), cfgCollectorHost)
			assert.Equal(t, 9000, c.Int(cfgCollectorAgentPort), cfgCollectorAgentPort)
			assert.Equal(t, 9001, c.Int(cfgCollectorSpanPort), cfgCollectorSpanPort)
			assert.Equal(t, 9002, c.Int(cfgCollectorStatPort), cfgCollectorStatPort)
			assert.Equal(t, "debug", c.String(cfgLogLevel), cfgLogLevel)
			assert.Equal(t, "PERCENT", c.String(cfgSamplingType), cfgSamplingType)
			assert.Equal(t, 20, c.Int(cfgSamplingCounterRate), cfgSamplingCounterRate)
			assert.Equal(t, 0.1, c.Float(cfgSamplingPercentRate), cfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(cfgSamplingNewThroughput), cfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(cfgSamplingContinueThroughput), cfgSamplingContinueThroughput)
			assert.Equal(t, 7000, c.Int(cfgStatCollectInterval), cfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(cfgStatBatchCount), cfgStatBatchCount)
			assert.Equal(t, true, c.Bool(cfgIsContainerEnv), cfgIsContainerEnv)
		})
	}
}

func TestNewConfig_ConfigFileJson(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithAgentId("TestAgent"),
		WithConfigFile("example/pinpoint-config.json"),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, "JsonAppName", c.String(cfgAppName), cfgAppName)
			assert.Equal(t, 1901, c.Int(cfgAppType), cfgAppType)
			assert.Equal(t, "JsonAgentID", c.String(cfgAgentID), cfgAgentID)
			assert.Equal(t, "real.collector.host", c.String(cfgCollectorHost), cfgCollectorHost)
			assert.Equal(t, 9000, c.Int(cfgCollectorAgentPort), cfgCollectorAgentPort)
			assert.Equal(t, 9001, c.Int(cfgCollectorSpanPort), cfgCollectorSpanPort)
			assert.Equal(t, 9002, c.Int(cfgCollectorStatPort), cfgCollectorStatPort)
			assert.Equal(t, "debug", c.String(cfgLogLevel), cfgLogLevel)
			assert.Equal(t, "percent", c.String(cfgSamplingType), cfgSamplingType)
			assert.Equal(t, 20, c.Int(cfgSamplingCounterRate), cfgSamplingCounterRate)
			assert.Equal(t, 5.5, c.Float(cfgSamplingPercentRate), cfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(cfgSamplingNewThroughput), cfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(cfgSamplingContinueThroughput), cfgSamplingContinueThroughput)
			assert.Equal(t, 7000, c.Int(cfgStatCollectInterval), cfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(cfgStatBatchCount), cfgStatBatchCount)
			assert.Equal(t, true, c.Bool(cfgIsContainerEnv), cfgIsContainerEnv)
		})
	}
}

func TestNewConfig_ConfigFileProp(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithAgentId("TestAgent"),
		WithConfigFile("example/pinpoint-config.prop"),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, "PropAppName", c.String(cfgAppName), cfgAppName)
			assert.Equal(t, 1902, c.Int(cfgAppType), cfgAppType)
			assert.Equal(t, "PropAgentID", c.String(cfgAgentID), cfgAgentID)
			assert.Equal(t, "real.collector.host", c.String(cfgCollectorHost), cfgCollectorHost)
			assert.Equal(t, 7000, c.Int(cfgCollectorAgentPort), cfgCollectorAgentPort)
			assert.Equal(t, 7001, c.Int(cfgCollectorSpanPort), cfgCollectorSpanPort)
			assert.Equal(t, 7002, c.Int(cfgCollectorStatPort), cfgCollectorStatPort)
			assert.Equal(t, "debug", c.String(cfgLogLevel), cfgLogLevel)
			assert.Equal(t, "percent", c.String(cfgSamplingType), cfgSamplingType)
			assert.Equal(t, 20, c.Int(cfgSamplingCounterRate), cfgSamplingCounterRate)
			assert.Equal(t, 5.5, c.Float(cfgSamplingPercentRate), cfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(cfgSamplingNewThroughput), cfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(cfgSamplingContinueThroughput), cfgSamplingContinueThroughput)
			assert.Equal(t, 7000, c.Int(cfgStatCollectInterval), cfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(cfgStatBatchCount), cfgStatBatchCount)
			assert.Equal(t, true, c.Bool(cfgIsContainerEnv), cfgIsContainerEnv)
		})
	}
}

func TestNewConfig_ConfigFileProfile(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithAgentId("TestAgent"),
		WithConfigFile("example/test-config.yaml"),
		WithUseProfile("real"),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{
		"pinpoint_go_agent",
		"--pinpoint-configfile=example/pinpoint-config.yaml",
	}
	os.Setenv("PINPOINT_GO_USEPROFILE", "dev")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, "MyAppName", c.String(cfgAppName), cfgAppName)
			assert.Equal(t, "MyAgentID", c.String(cfgAgentID), cfgAgentID)
			assert.Equal(t, "dev.collector.host", c.String(cfgCollectorHost), cfgCollectorHost)
			assert.Equal(t, "COUNTER", c.String(cfgSamplingType), cfgSamplingType)
			assert.Equal(t, 1, c.Int(cfgSamplingCounterRate), cfgSamplingCounterRate)
			assert.Equal(t, 0.1, c.Float(cfgSamplingPercentRate), cfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(cfgSamplingNewThroughput), cfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(cfgSamplingContinueThroughput), cfgSamplingContinueThroughput)
			assert.Equal(t, 7000, c.Int(cfgStatCollectInterval), cfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(cfgStatBatchCount), cfgStatBatchCount)
			assert.Equal(t, true, c.Bool(cfgIsContainerEnv), cfgIsContainerEnv)
		})
	}
}

func TestNewConfig_EnvVarArg(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithAgentId("TestAgent"),
		WithConfigFile("example/test.yaml"),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}

	os.Setenv("PINPOINT_GO_APPLICATIONNAME", "EnvVarArgTest")
	os.Setenv("PINPOINT_GO_APPLICATIONTYPE", "2000")
	os.Setenv("PINPOINT_GO_AGENTID", "envagentid")
	os.Setenv("PINPOINT_GO_AGENTNAME", "envagentname")
	os.Setenv("PINPOINT_GO_COLLECTOR_HOST", "env.collector.host")
	os.Setenv("PINPOINT_GO_COLLECTOR_AGENTPORT", "8000")
	os.Setenv("PINPOINT_GO_COLLECTOR_SPANPORT", "8100")
	os.Setenv("PINPOINT_GO_COLLECTOR_STATPORT", "8200")
	os.Setenv("PINPOINT_GO_SAMPLING_TYPE", "Percent")
	os.Setenv("PINPOINT_GO_SAMPLING_PERCENTRATE", "120")
	os.Setenv("PINPOINT_GO_SAMPLING_COUNTERRATE", "100")
	os.Setenv("PINPOINT_GO_SAMPLING_NEWTHROUGHPUT", "100")
	os.Setenv("PINPOINT_GO_SAMPLING_CONTINUETHROUGHPUT", "200")
	os.Setenv("PINPOINT_GO_STAT_COLLECTINTERVAL", "3000")
	os.Setenv("PINPOINT_GO_STAT_BATCHCOUNT", "11")
	os.Setenv("PINPOINT_GO_LOGLEVEL", "trace")
	os.Setenv("PINPOINT_GO_ISCONTAINERENV", "false")
	os.Setenv("PINPOINT_GO_CONFIGFILE", "example/pinpoint-config.yaml")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, "EnvVarArgTest", c.String(cfgAppName), cfgAppName)
			assert.Equal(t, 2000, c.Int(cfgAppType), cfgAppType)
			assert.Equal(t, "envagentid", c.String(cfgAgentID), cfgAgentID)
			assert.Equal(t, "envagentname", c.String(cfgAgentName), cfgAgentName)
			assert.Equal(t, "env.collector.host", c.String(cfgCollectorHost), cfgCollectorHost)
			assert.Equal(t, 8000, c.Int(cfgCollectorAgentPort), cfgCollectorAgentPort)
			assert.Equal(t, 8100, c.Int(cfgCollectorSpanPort), cfgCollectorSpanPort)
			assert.Equal(t, 8200, c.Int(cfgCollectorStatPort), cfgCollectorStatPort)
			assert.Equal(t, "trace", c.String(cfgLogLevel), cfgLogLevel)
			assert.Equal(t, "Percent", c.String(cfgSamplingType), cfgSamplingType)
			assert.Equal(t, 100, c.Int(cfgSamplingCounterRate), cfgSamplingCounterRate)
			assert.Equal(t, float64(120), c.Float(cfgSamplingPercentRate), cfgSamplingPercentRate)
			assert.Equal(t, 100, c.Int(cfgSamplingNewThroughput), cfgSamplingNewThroughput)
			assert.Equal(t, 200, c.Int(cfgSamplingContinueThroughput), cfgSamplingContinueThroughput)
			assert.Equal(t, 3000, c.Int(cfgStatCollectInterval), cfgStatCollectInterval)
			assert.Equal(t, 11, c.Int(cfgStatBatchCount), cfgStatBatchCount)
			assert.Equal(t, false, c.Bool(cfgIsContainerEnv), cfgIsContainerEnv)
			assert.Equal(t, "example/pinpoint-config.yaml", c.String(cfgConfigFile), cfgConfigFile)
			assert.Equal(t, "dev", c.String(cfgUseProfile), cfgUseProfile)
		})
	}
}

func TestNewConfig_CmdLineArg(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithAgentId("TestAgent"),
		WithConfigFile("example/test-config.yaml"),
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{opts}},
	}

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{
		"pinpoint_go_agent",
		"--app-arg1=1",
		"--app-arg2=2",
		"--pinpoint-applicationname=CmdLineArgTest",
		"--pinpoint-applicationtype=2100",
		"--pinpoint-agentid=cmdAgentID",
		"--pinpoint-agentname=cmdAgentName",
		"-app-arg3",
		"--pinpoint-collector-host=cmd.collector.host",
		"--pinpoint-collector-agentport=7000",
		"--pinpoint-collector-spanport=7100",
		"--pinpoint-collector-statport=7200",
		"--pinpoint-sampling-type=percent",
		"--pinpoint-sampling-percentrate=0.0001",
		"--pinpoint-sampling-counterrate=10",
		"--pinpoint-sampling-newthroughput=500",
		"--pinpoint-sampling-continuethroughput=600",
		"--pinpoint-stat-collectinterval=6000",
		"--pinpoint-stat-batchcount=5",
		"--pinpoint-loglevel=error",
		"-app-arg4",
		"--pinpoint-iscontainerenv=true",
		"--pinpoint-configfile=example/pinpoint-config.yaml",
		"--pinpoint-useprofile=real",
		"--app-arg5=5",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)

			assert.Equal(t, "CmdLineArgTest", c.String(cfgAppName), cfgAppName)
			assert.Equal(t, 2100, c.Int(cfgAppType), cfgAppType)
			assert.Equal(t, "cmdAgentID", c.String(cfgAgentID), cfgAgentID)
			assert.Equal(t, "cmdAgentName", c.String(cfgAgentName), cfgAgentName)
			assert.Equal(t, "cmd.collector.host", c.String(cfgCollectorHost), cfgCollectorHost)
			assert.Equal(t, 7000, c.Int(cfgCollectorAgentPort), cfgCollectorAgentPort)
			assert.Equal(t, 7100, c.Int(cfgCollectorSpanPort), cfgCollectorSpanPort)
			assert.Equal(t, 7200, c.Int(cfgCollectorStatPort), cfgCollectorStatPort)
			assert.Equal(t, "error", c.String(cfgLogLevel), cfgLogLevel)
			assert.Equal(t, "percent", c.String(cfgSamplingType), cfgSamplingType)
			assert.Equal(t, 10, c.Int(cfgSamplingCounterRate), cfgSamplingCounterRate)
			assert.Equal(t, 0.0001, c.Float(cfgSamplingPercentRate), cfgSamplingPercentRate)
			assert.Equal(t, 500, c.Int(cfgSamplingNewThroughput), cfgSamplingNewThroughput)
			assert.Equal(t, 600, c.Int(cfgSamplingContinueThroughput), cfgSamplingContinueThroughput)
			assert.Equal(t, 6000, c.Int(cfgStatCollectInterval), cfgStatCollectInterval)
			assert.Equal(t, 5, c.Int(cfgStatBatchCount), cfgStatBatchCount)
			assert.Equal(t, true, c.Bool(cfgIsContainerEnv), cfgIsContainerEnv)
			assert.Equal(t, "example/pinpoint-config.yaml", c.String(cfgConfigFile), cfgConfigFile)
			assert.Equal(t, "real", c.String(cfgUseProfile), cfgUseProfile)
		})
	}
}
