package pinpoint

import (
	"github.com/stretchr/testify/assert"
	"math"
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
			assert.Equal(t, "TestApp", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, ServiceTypeGoApp, c.Int(CfgAppType), CfgAppType)
			assert.NotEmpty(t, c.String(CfgAgentID), CfgAgentID)
			assert.Empty(t, c.String(CfgAgentName), CfgAgentName)
			assert.Equal(t, "localhost", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 9991, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 9993, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 9992, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "info", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, "stderr", c.String(CfgLogOutput), CfgLogOutput)
			assert.Equal(t, 10, c.Int(CfgLogMaxSize), CfgLogMaxSize)
			assert.Equal(t, samplingTypeCounter, c.String(CfgSamplingType), CfgSamplingType)
			assert.Equal(t, 1, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, float64(100), c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 0, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 0, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, defaultQueueSize, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, defaultEventDepth, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, defaultEventSequence, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 5000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 6, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, false, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Empty(t, c.String(CfgConfigFile), CfgConfigFile)
			assert.Empty(t, c.String(CfgActiveProfile), CfgActiveProfile)
			assert.Equal(t, true, c.Bool(CfgSQLTraceBindValue), CfgSQLTraceBindValue)
			assert.Equal(t, 1024, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
			assert.Equal(t, true, c.Bool(CfgSQLTraceCommit), CfgSQLTraceCommit)
			assert.Equal(t, true, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
			assert.Equal(t, true, c.Bool(CfgEnable), CfgEnable)
			assert.Equal(t, false, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 1024, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
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
		WithLogOutput("stdout"),
		WithLogMaxSize(100),
		WithSamplingType("percent"),
		WithSamplingPercentRate(90),
		WithSamplingCounterRate(200),
		WithSamplingNewThroughput(20),
		WithSamplingContinueThroughput(30),
		WithSpanQueueSize(2048),
		WithSpanMaxCallStackDepth(100),
		WithSpanMaxCallStackSequence(1000),
		WithStatCollectInterval(10000),
		WithStatBatchCount(3),
		WithIsContainerEnv(true),
		WithSQLTraceBindValue(false),
		WithSQLMaxBindValueSize(512),
		WithSQLTraceCommit(false),
		WithSQLTraceRollback(false),
		WithEnable(false),
		WithHttpUrlStatEnable(true),
		WithHttpUrlStatLimitSize(2048),
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
			assert.Equal(t, "TestApp", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, 1234, c.Int(CfgAppType), CfgAppType)
			assert.Equal(t, "TestAgent", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "TestAgentName", c.String(CfgAgentName), CfgAgentName)
			assert.Equal(t, "func.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 7777, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 8888, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 9999, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "error", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, "stdout", c.String(CfgLogOutput), CfgLogOutput)
			assert.Equal(t, 100, c.Int(CfgLogMaxSize), CfgLogMaxSize)
			assert.Equal(t, "percent", c.String(CfgSamplingType), CfgSamplingType)
			assert.Equal(t, 200, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, float64(90), c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 20, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 30, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 2048, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, 100, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 1000, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 10000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 3, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, false, c.Bool(CfgSQLTraceBindValue), CfgSQLTraceBindValue)
			assert.Equal(t, 512, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
			assert.Equal(t, false, c.Bool(CfgSQLTraceCommit), CfgSQLTraceCommit)
			assert.Equal(t, false, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
			assert.Equal(t, false, c.Bool(CfgEnable), CfgEnable)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 2048, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
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
			assert.Equal(t, c.String(CfgAppName), "TestApp", CfgAppName)
			assert.NotNil(t, c.String(CfgAgentID), CfgAgentID)
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
			assert.Equal(t, "MyAppName", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, 1900, c.Int(CfgAppType), CfgAppType)
			assert.Equal(t, "MyAgentID", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "my.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 9000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 9001, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 9002, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "debug", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, "PERCENT", c.String(CfgSamplingType), CfgSamplingType)
			assert.Equal(t, 20, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, 0.1, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 512, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, 32, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 512, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 7000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 1234, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
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
			assert.Equal(t, "JsonAppName", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, 1901, c.Int(CfgAppType), CfgAppType)
			assert.Equal(t, "JsonAgentID", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "real.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 9000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 9001, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 9002, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "debug", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, "percent", c.String(CfgSamplingType), CfgSamplingType)
			assert.Equal(t, 20, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, 5.5, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 1024, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, 10, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 50, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 7000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 10, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
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
			assert.Equal(t, "PropAppName", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, 1902, c.Int(CfgAppType), CfgAppType)
			assert.Equal(t, "PropAgentID", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "real.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 7000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 7001, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 7002, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "debug", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, "percent", c.String(CfgSamplingType), CfgSamplingType)
			assert.Equal(t, 20, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, 5.5, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 1024, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, 0, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 1, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, int32(2), maxEventDepth, "maxEventDepth")
			assert.Equal(t, int32(4), maxEventSequence, "maxEventSequence")
			assert.Equal(t, 7000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 10240, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
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
		WithActiveProfile("real"),
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
	os.Setenv("PINPOINT_GO_ACTIVEPROFILE", "dev")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, "MyAppName", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, "MyAgentID", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "dev.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, "COUNTER", c.String(CfgSamplingType), CfgSamplingType)
			assert.Equal(t, 1, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, 0.1, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 7000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
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
	os.Setenv("PINPOINT_GO_SPAN_QUEUESIZE", "1000")
	os.Setenv("PINPOINT_GO_SPAN_MAXCALLSTACKDEPTH", "128")
	os.Setenv("PINPOINT_GO_SPAN_MAXCALLSTACKSEQUENCE", "2000")
	os.Setenv("PINPOINT_GO_STAT_COLLECTINTERVAL", "3000")
	os.Setenv("PINPOINT_GO_STAT_BATCHCOUNT", "11")
	os.Setenv("PINPOINT_GO_LOG_LEVEL", "trace")
	os.Setenv("PINPOINT_GO_LOG_OUTPUT", "stdout")
	os.Setenv("PINPOINT_GO_LOG_MAXSIZE", "50")
	os.Setenv("PINPOINT_GO_ISCONTAINERENV", "false")
	os.Setenv("PINPOINT_GO_CONFIGFILE", "example/pinpoint-config.yaml")
	os.Setenv("PINPOINT_GO_ENABLE", "false")
	os.Setenv("PINPOINT_GO_HTTP_URLSTAT_ENABLE", "true")
	os.Setenv("PINPOINT_GO_HTTP_URLSTAT_LIMITSIZE", "100")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, "EnvVarArgTest", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, 2000, c.Int(CfgAppType), CfgAppType)
			assert.Equal(t, "envagentid", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "envagentname", c.String(CfgAgentName), CfgAgentName)
			assert.Equal(t, "env.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 8000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 8100, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 8200, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "trace", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, "stdout", c.String(CfgLogOutput), CfgLogOutput)
			assert.Equal(t, 50, c.Int(CfgLogMaxSize), CfgLogMaxSize)
			assert.Equal(t, "Percent", c.String(CfgSamplingType), CfgSamplingType)
			assert.Equal(t, 100, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, float64(120), c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 100, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 200, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 1000, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, 128, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 2000, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 3000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 11, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, false, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, "example/pinpoint-config.yaml", c.String(CfgConfigFile), CfgConfigFile)
			assert.Equal(t, "dev", c.String(CfgActiveProfile), CfgActiveProfile)
			assert.Equal(t, false, c.Bool(CfgEnable), CfgEnable)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 100, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
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
		"--pinpoint-span-queuesize=10",
		"--pinpoint-span-maxcallstackdepth=-1",
		"--pinpoint-span-maxcallstacksequence=-1",
		"--pinpoint-stat-collectinterval=6000",
		"--pinpoint-stat-batchcount=5",
		"--pinpoint-log-level=error",
		"--pinpoint-log-output=stdout",
		"--pinpoint-log-maxsize=20",
		"-app-arg4",
		"--pinpoint-iscontainerenv=true",
		"--pinpoint-configfile=example/pinpoint-config.yaml",
		"--pinpoint-activeprofile=real",
		"--pinpoint-enable=false",
		"--pinpoint-http-urlstat-enable=true",
		"--pinpoint-http-urlstat-limitsize=200",
		"--app-arg5=5",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)

			assert.Equal(t, "CmdLineArgTest", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, 2100, c.Int(CfgAppType), CfgAppType)
			assert.Equal(t, "cmdAgentID", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "cmdAgentName", c.String(CfgAgentName), CfgAgentName)
			assert.Equal(t, "cmd.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 7000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 7100, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 7200, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "error", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, "stdout", c.String(CfgLogOutput), CfgLogOutput)
			assert.Equal(t, 20, c.Int(CfgLogMaxSize), CfgLogMaxSize)
			assert.Equal(t, "percent", c.String(CfgSamplingType), CfgSamplingType)
			assert.Equal(t, 10, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, 0.0001, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 500, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 600, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 10, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, -1, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, -1, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, int32(math.MaxInt32), maxEventDepth, "maxEventDepth")
			assert.Equal(t, int32(math.MaxInt32), maxEventSequence, "maxEventSequence")
			assert.Equal(t, 6000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 5, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, "example/pinpoint-config.yaml", c.String(CfgConfigFile), CfgConfigFile)
			assert.Equal(t, "real", c.String(CfgActiveProfile), CfgActiveProfile)
			assert.Equal(t, false, c.Bool(CfgEnable), CfgEnable)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 200, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
		})
	}
}
