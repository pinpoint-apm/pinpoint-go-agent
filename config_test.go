package pinpoint

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			assert.Empty(t, c.String(CfgAgentID), CfgAgentID)
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
			assert.Equal(t, true, c.Bool(CfgSpanBatchEnable), CfgSpanBatchEnable)
			assert.Equal(t, defaultSpanBatchSize, c.Int(CfgSpanBatchSize), CfgSpanBatchSize)
			assert.Equal(t, defaultSpanBatchFlushInterval, c.Int(CfgSpanBatchFlushInterval), CfgSpanBatchFlushInterval)
			assert.Equal(t, defaultSpanBatchCollectDeadline, c.Int(CfgSpanBatchCollectDeadline), CfgSpanBatchCollectDeadline)
			assert.Equal(t, defaultSpanBatchMaxConcurrentRequests, c.Int(CfgSpanBatchMaxConcurrentRequests), CfgSpanBatchMaxConcurrentRequests)
			assert.Equal(t, defaultEventChunkSize, c.Int(CfgSpanEventChunkSize), CfgSpanEventChunkSize)
			assert.Equal(t, defaultEventDepth, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, defaultEventSequence, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, defaultQueueSize, c.Int(CfgStatQueueSize), CfgStatQueueSize)
			assert.Equal(t, 5000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 6, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, false, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Empty(t, c.String(CfgConfigFile), CfgConfigFile)
			assert.Empty(t, c.String(CfgActiveProfile), CfgActiveProfile)
			assert.Equal(t, true, c.Bool(CfgSQLTraceBindValue), CfgSQLTraceBindValue)
			assert.Equal(t, 1024, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
			assert.Equal(t, true, c.Bool(CfgSQLTraceCommit), CfgSQLTraceCommit)
			assert.Equal(t, true, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
			assert.Equal(t, false, c.Bool(CfgSQLTraceQueryStat), CfgSQLTraceQueryStat)
			assert.Equal(t, true, c.Bool(CfgEnable), CfgEnable)
			assert.Equal(t, false, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 1024, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
			assert.Equal(t, 1024, c.Int(CfgHttpUrlStatQueueSize), CfgHttpUrlStatQueueSize)
			assert.Equal(t, false, c.Bool(CfgErrorTraceCallStack), CfgErrorTraceCallStack)
			assert.Equal(t, 32, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
			assert.Equal(t, 24*60*60*1000, c.Int(CfgCollectorAgentInfoRefreshInterval), CfgCollectorAgentInfoRefreshInterval)
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
		WithSpanBatchEnable(false),
		WithSpanBatchSize(25),
		WithSpanBatchFlushInterval(2000),
		WithSpanBatchCollectDeadline(250),
		WithSpanBatchMaxConcurrentRequests(4),
		WithSpanEventChunkSize(100),
		WithSpanMaxCallStackDepth(100),
		WithSpanMaxCallStackSequence(1000),
		WithStatCollectInterval(10000),
		WithStatBatchCount(3),
		WithIsContainerEnv(true),
		WithSQLTraceBindValue(false),
		WithSQLMaxBindValueSize(512),
		WithSQLTraceCommit(false),
		WithSQLTraceRollback(false),
		WithSQLTraceQueryStat(true),
		WithEnable(false),
		WithHttpUrlStatEnable(true),
		WithHttpUrlStatLimitSize(2048),
		WithErrorTraceCallStack(true),
		WithErrorCallStackDepth(64),
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
			assert.Equal(t, samplingTypePercent, c.String(CfgSamplingType), CfgSamplingType) // normalized
			assert.Equal(t, 200, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, float64(90), c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 20, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 30, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 2048, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, false, c.Bool(CfgSpanBatchEnable), CfgSpanBatchEnable)
			assert.Equal(t, 25, c.Int(CfgSpanBatchSize), CfgSpanBatchSize)
			assert.Equal(t, 2000, c.Int(CfgSpanBatchFlushInterval), CfgSpanBatchFlushInterval)
			assert.Equal(t, 250, c.Int(CfgSpanBatchCollectDeadline), CfgSpanBatchCollectDeadline)
			assert.Equal(t, 4, c.Int(CfgSpanBatchMaxConcurrentRequests), CfgSpanBatchMaxConcurrentRequests)
			assert.Equal(t, 100, c.Int(CfgSpanEventChunkSize), CfgSpanEventChunkSize)
			assert.Equal(t, 100, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 1000, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 10000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 3, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, false, c.Bool(CfgSQLTraceBindValue), CfgSQLTraceBindValue)
			assert.Equal(t, 512, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
			assert.Equal(t, false, c.Bool(CfgSQLTraceCommit), CfgSQLTraceCommit)
			assert.Equal(t, false, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
			assert.Equal(t, true, c.Bool(CfgSQLTraceQueryStat), CfgSQLTraceQueryStat)
			assert.Equal(t, false, c.Bool(CfgEnable), CfgEnable)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 2048, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
			assert.Equal(t, true, c.Bool(CfgErrorTraceCallStack), CfgErrorTraceCallStack)
			assert.Equal(t, 64, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
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
			c, err := NewConfig(tt.args.opts...)
			err = c.checkNameAndID()
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
			c.checkNameAndID()
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
			defer c.Close()
			assert.Equal(t, "MyAppName", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, 1900, c.Int(CfgAppType), CfgAppType)
			assert.Equal(t, "MyAgentID", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "my.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 9000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 9001, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 9002, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "debug", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, samplingTypePercent, c.String(CfgSamplingType), CfgSamplingType) // normalized
			assert.Equal(t, 20, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, 0.1, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 512, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, 50, c.Int(CfgSpanEventChunkSize), CfgSpanEventChunkSize)
			assert.Equal(t, 32, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 512, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 7000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, false, c.Bool(CfgSQLTraceBindValue), CfgSQLTraceBindValue)
			assert.Equal(t, 512, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
			assert.Equal(t, false, c.Bool(CfgSQLTraceCommit), CfgSQLTraceCommit)
			assert.Equal(t, false, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
			assert.Equal(t, true, c.Bool(CfgSQLTraceQueryStat), CfgSQLTraceQueryStat)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 1234, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
			assert.Equal(t, true, c.Bool(CfgErrorTraceCallStack), CfgErrorTraceCallStack)
			assert.Equal(t, 20, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
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
			defer c.Close()
			assert.Equal(t, "JsonAppName", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, 1901, c.Int(CfgAppType), CfgAppType)
			assert.Equal(t, "JsonAgentID", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "real.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 9000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 9001, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 9002, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "debug", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, samplingTypePercent, c.String(CfgSamplingType), CfgSamplingType) // normalized
			assert.Equal(t, 20, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, 5.5, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 1024, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, 10, c.Int(CfgSpanEventChunkSize), CfgSpanEventChunkSize)
			assert.Equal(t, 10, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 50, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 7000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, false, c.Bool(CfgSQLTraceBindValue), CfgSQLTraceBindValue)
			assert.Equal(t, 256, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
			assert.Equal(t, false, c.Bool(CfgSQLTraceCommit), CfgSQLTraceCommit)
			assert.Equal(t, false, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
			assert.Equal(t, true, c.Bool(CfgSQLTraceQueryStat), CfgSQLTraceQueryStat)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 10, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
			assert.Equal(t, true, c.Bool(CfgErrorTraceCallStack), CfgErrorTraceCallStack)
			assert.Equal(t, 30, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
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
			defer c.Close()
			assert.Equal(t, "PropAppName", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, 1902, c.Int(CfgAppType), CfgAppType)
			assert.Equal(t, "PropAgentID", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "real.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, 7000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
			assert.Equal(t, 7001, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
			assert.Equal(t, 7002, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
			assert.Equal(t, "debug", c.String(CfgLogLevel), CfgLogLevel)
			assert.Equal(t, samplingTypePercent, c.String(CfgSamplingType), CfgSamplingType) // normalized
			assert.Equal(t, 20, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, 5.5, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 50, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 60, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, defaultQueueSize, c.Int(CfgSpanQueueSize), CfgSpanQueueSize) // span.queueSize=-1 falls back to the default
			assert.Equal(t, 20, c.Int(CfgSpanEventChunkSize), CfgSpanEventChunkSize)
			assert.Equal(t, 2, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 4, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 7000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 10, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, false, c.Bool(CfgSQLTraceBindValue), CfgSQLTraceBindValue)
			assert.Equal(t, 128, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
			assert.Equal(t, false, c.Bool(CfgSQLTraceCommit), CfgSQLTraceCommit)
			assert.Equal(t, false, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
			assert.Equal(t, true, c.Bool(CfgSQLTraceQueryStat), CfgSQLTraceQueryStat)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 10240, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
			assert.Equal(t, true, c.Bool(CfgErrorTraceCallStack), CfgErrorTraceCallStack)
			assert.Equal(t, 40, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
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
	t.Setenv("PINPOINT_GO_ACTIVEPROFILE", "dev")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			defer c.Close()
			assert.Equal(t, "MyAppName", c.String(CfgAppName), CfgAppName)
			assert.Equal(t, "MyAgentID", c.String(CfgAgentID), CfgAgentID)
			assert.Equal(t, "dev.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
			assert.Equal(t, samplingTypeCounter, c.String(CfgSamplingType), CfgSamplingType)
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

	t.Setenv("PINPOINT_GO_ACTIVEPROFILE", "dev")
	t.Setenv("PINPOINT_GO_APPLICATIONNAME", "EnvVarArgTest")
	t.Setenv("PINPOINT_GO_APPLICATIONTYPE", "2000")
	t.Setenv("PINPOINT_GO_AGENTID", "envagentid")
	t.Setenv("PINPOINT_GO_AGENTNAME", "envagentname")
	t.Setenv("PINPOINT_GO_COLLECTOR_HOST", "env.collector.host")
	t.Setenv("PINPOINT_GO_COLLECTOR_AGENTPORT", "8000")
	t.Setenv("PINPOINT_GO_COLLECTOR_SPANPORT", "8100")
	t.Setenv("PINPOINT_GO_COLLECTOR_STATPORT", "8200")
	t.Setenv("PINPOINT_GO_SAMPLING_TYPE", "Percent")
	t.Setenv("PINPOINT_GO_SAMPLING_PERCENTRATE", "120")
	t.Setenv("PINPOINT_GO_SAMPLING_COUNTERRATE", "100")
	t.Setenv("PINPOINT_GO_SAMPLING_NEWTHROUGHPUT", "100")
	t.Setenv("PINPOINT_GO_SAMPLING_CONTINUETHROUGHPUT", "200")
	t.Setenv("PINPOINT_GO_SPAN_QUEUESIZE", "1000")
	t.Setenv("PINPOINT_GO_SPAN_BATCH_ENABLE", "true")
	t.Setenv("PINPOINT_GO_SPAN_BATCHSIZE", "40")
	t.Setenv("PINPOINT_GO_SPAN_BATCHFLUSHINTERVAL", "1500")
	t.Setenv("PINPOINT_GO_SPAN_BATCHCOLLECTDEADLINE", "300")
	t.Setenv("PINPOINT_GO_SPAN_BATCHMAXCONCURRENTREQUESTS", "3")
	t.Setenv("PINPOINT_GO_SPAN_EVENTCHUNKSIZE", "88")
	t.Setenv("PINPOINT_GO_SPAN_MAXCALLSTACKDEPTH", "128")
	t.Setenv("PINPOINT_GO_SPAN_MAXCALLSTACKSEQUENCE", "2000")
	t.Setenv("PINPOINT_GO_STAT_COLLECTINTERVAL", "3000")
	t.Setenv("PINPOINT_GO_STAT_BATCHCOUNT", "11")
	t.Setenv("PINPOINT_GO_LOG_LEVEL", "trace")
	t.Setenv("PINPOINT_GO_LOG_OUTPUT", "stdout")
	t.Setenv("PINPOINT_GO_LOG_MAXSIZE", "50")
	t.Setenv("PINPOINT_GO_ISCONTAINERENV", "false")
	t.Setenv("PINPOINT_GO_SQL_TRACEBINDVALUE", "true")
	t.Setenv("PINPOINT_GO_SQL_MAXBINDVALUESIZE", "100")
	t.Setenv("PINPOINT_GO_SQL_TRACECOMMIT", "false")
	t.Setenv("PINPOINT_GO_SQL_TRACEROLLBACK", "false")
	t.Setenv("PINPOINT_GO_SQL_TRACEQUERYSTAT", "true")
	t.Setenv("PINPOINT_GO_CONFIGFILE", "example/pinpoint-config.yaml")
	t.Setenv("PINPOINT_GO_ENABLE", "false")
	t.Setenv("PINPOINT_GO_HTTP_URLSTAT_ENABLE", "true")
	t.Setenv("PINPOINT_GO_HTTP_URLSTAT_LIMITSIZE", "100")
	t.Setenv("PINPOINT_GO_ERROR_TRACECALLSTACK", "true")
	t.Setenv("PINPOINT_GO_ERROR_CALLSTACKDEPTH", "50")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			defer c.Close()
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
			assert.Equal(t, samplingTypePercent, c.String(CfgSamplingType), CfgSamplingType) // normalized
			assert.Equal(t, 100, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, float64(120), c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 100, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 200, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 1000, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, true, c.Bool(CfgSpanBatchEnable), CfgSpanBatchEnable)
			assert.Equal(t, 40, c.Int(CfgSpanBatchSize), CfgSpanBatchSize)
			assert.Equal(t, 1500, c.Int(CfgSpanBatchFlushInterval), CfgSpanBatchFlushInterval)
			assert.Equal(t, 300, c.Int(CfgSpanBatchCollectDeadline), CfgSpanBatchCollectDeadline)
			assert.Equal(t, 3, c.Int(CfgSpanBatchMaxConcurrentRequests), CfgSpanBatchMaxConcurrentRequests)
			assert.Equal(t, 88, c.Int(CfgSpanEventChunkSize), CfgSpanEventChunkSize)
			assert.Equal(t, 128, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, 2000, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 3000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 11, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, false, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, true, c.Bool(CfgSQLTraceBindValue), CfgSQLTraceBindValue)
			assert.Equal(t, 100, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
			assert.Equal(t, false, c.Bool(CfgSQLTraceCommit), CfgSQLTraceCommit)
			assert.Equal(t, false, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
			assert.Equal(t, true, c.Bool(CfgSQLTraceQueryStat), CfgSQLTraceQueryStat)
			assert.Equal(t, "example/pinpoint-config.yaml", c.String(CfgConfigFile), CfgConfigFile)
			assert.Equal(t, "dev", c.String(CfgActiveProfile), CfgActiveProfile)
			assert.Equal(t, false, c.Bool(CfgEnable), CfgEnable)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 100, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
			assert.Equal(t, true, c.Bool(CfgErrorTraceCallStack), CfgErrorTraceCallStack)
			assert.Equal(t, 50, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
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
		"--pinpoint-span-batch-enable=true",
		"--pinpoint-span-batchsize=30",
		"--pinpoint-span-batchflushinterval=2500",
		"--pinpoint-span-batchcollectdeadline=350",
		"--pinpoint-span-batchmaxconcurrentrequests=2",
		"--pinpoint-span-eventchunksize=30",
		"--pinpoint-span-maxcallstackdepth=-1",
		"--pinpoint-span-maxcallstacksequence=-1",
		"--pinpoint-stat-collectinterval=6000",
		"--pinpoint-stat-batchcount=5",
		"--pinpoint-log-level=error",
		"--pinpoint-log-output=stdout",
		"--pinpoint-log-maxsize=20",
		"-app-arg4",
		"--pinpoint-iscontainerenv=true",
		"--pinpoint-sql-tracebindvalue=false",
		"--pinpoint-sql-maxbindvaluesize=500",
		"--pinpoint-sql-tracecommit=true",
		"--pinpoint-sql-tracerollback=false",
		"--pinpoint-sql-tracequerystat=true",
		"--pinpoint-configfile=example/pinpoint-config.yaml",
		"--pinpoint-activeprofile=real",
		"--pinpoint-enable=false",
		"--pinpoint-http-urlstat-enable=true",
		"--pinpoint-http-urlstat-limitsize=200",
		"--app-arg5=5",
		"--pinpoint-error-tracecallstack=true",
		"--pinpoint-error-callstackdepth=100",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewConfig(tt.args.opts...)
			defer c.Close()

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
			assert.Equal(t, samplingTypePercent, c.String(CfgSamplingType), CfgSamplingType) // normalized
			assert.Equal(t, 10, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
			assert.Equal(t, 0.0001, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
			assert.Equal(t, 500, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
			assert.Equal(t, 600, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
			assert.Equal(t, 10, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
			assert.Equal(t, true, c.Bool(CfgSpanBatchEnable), CfgSpanBatchEnable)
			assert.Equal(t, 30, c.Int(CfgSpanBatchSize), CfgSpanBatchSize)
			assert.Equal(t, 2500, c.Int(CfgSpanBatchFlushInterval), CfgSpanBatchFlushInterval)
			assert.Equal(t, 350, c.Int(CfgSpanBatchCollectDeadline), CfgSpanBatchCollectDeadline)
			assert.Equal(t, 2, c.Int(CfgSpanBatchMaxConcurrentRequests), CfgSpanBatchMaxConcurrentRequests)
			assert.Equal(t, 30, c.Int(CfgSpanEventChunkSize), CfgSpanEventChunkSize)
			assert.Equal(t, math.MaxInt32, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
			assert.Equal(t, math.MaxInt32, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
			assert.Equal(t, 6000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
			assert.Equal(t, 5, c.Int(CfgStatBatchCount), CfgStatBatchCount)
			assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
			assert.Equal(t, false, c.Bool(CfgSQLTraceBindValue), CfgSQLTraceBindValue)
			assert.Equal(t, 500, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
			assert.Equal(t, true, c.Bool(CfgSQLTraceCommit), CfgSQLTraceCommit)
			assert.Equal(t, false, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
			assert.Equal(t, true, c.Bool(CfgSQLTraceQueryStat), CfgSQLTraceQueryStat)
			assert.Equal(t, "example/pinpoint-config.yaml", c.String(CfgConfigFile), CfgConfigFile)
			assert.Equal(t, "real", c.String(CfgActiveProfile), CfgActiveProfile)
			assert.Equal(t, false, c.Bool(CfgEnable), CfgEnable)
			assert.Equal(t, true, c.Bool(CfgHttpUrlStatEnable), CfgHttpUrlStatEnable)
			assert.Equal(t, 200, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
			assert.Equal(t, true, c.Bool(CfgErrorTraceCallStack), CfgErrorTraceCallStack)
			assert.Equal(t, 100, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
		})
	}
}

func Test_reloadConfig(t *testing.T) {
	const sliceOpt = "Test.StringSlice"

	config, err := NewConfig(WithAppName("reloadApp"))
	assert.NoError(t, err)

	// The core registers no dynamic string slice option, but the http plugin
	// does; inject one so the reload path is exercised with a slice value.
	config.cfgMap[sliceOpt] = &cfgMapItem{
		value:        []string{"before"},
		defaultValue: []string{},
		valueType:    CfgStringSlice,
		dynamic:      true,
	}

	var reloadedSlice, reloadedDepth, reloadedUntouched int
	config.AddReloadCallback([]string{sliceOpt}, func() { reloadedSlice++ })
	config.AddReloadCallback([]string{CfgSpanMaxCallStackDepth}, func() { reloadedDepth++ })
	config.AddReloadCallback([]string{CfgSQLTraceCommit}, func() { reloadedUntouched++ })

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	body := `
Test:
  StringSlice:
    - after
    - /**/*.do
Span:
  MaxCallStackDepth: 12
`
	assert.NoError(t, os.WriteFile(cfgFile, []byte(body), 0o600))

	cfgFileViper := viper.New()
	cfgFileViper.SetConfigFile(cfgFile)
	config.reloadConfig(cfgFileViper)

	assert.Equal(t, []string{"after", "/**/*.do"}, config.StringSlice(sliceOpt))
	assert.Equal(t, 12, config.Int(CfgSpanMaxCallStackDepth))
	assert.Equal(t, int32(12), config.load().spanMaxEventDepth)
	assert.Equal(t, 1, reloadedSlice)
	assert.Equal(t, 1, reloadedDepth)
	assert.Equal(t, 0, reloadedUntouched, "callback fired for an option the file did not change")

	// Reloading the same file again changes nothing, so no callback fires.
	config.reloadConfig(cfgFileViper)
	assert.Equal(t, 1, reloadedSlice)
	assert.Equal(t, 1, reloadedDepth)
}

// A config file that still uses the deprecated LogLevel key must fire the
// Log.Level callback the logger is registered on. setFinalValue mirrors the old
// key onto the new one, but only the old name was reported as changed, so the
// snapshot picked the new level up while the logger kept the one it started
// with.
func Test_reloadConfig_deprecatedLogLevelFiresTheLogLevelCallback(t *testing.T) {
	config, err := NewConfig(WithAppName("reloadApp"))
	require.NoError(t, err)

	var reloadedLevel int
	config.AddReloadCallback([]string{CfgLogLevel}, func() { reloadedLevel++ })

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("LogLevel: debug\n"), 0o600))
	cfgFileViper := viper.New()
	cfgFileViper.SetConfigFile(cfgFile)
	config.reloadConfig(cfgFileViper)

	assert.Equal(t, "debug", config.String(CfgLogLevel), "the new key mirrors the old one")
	assert.Equal(t, 1, reloadedLevel, "the logger's callback must fire for the old key too")

	// A second reload of the same file changes nothing, so nothing fires.
	config.reloadConfig(cfgFileViper)
	assert.Equal(t, 1, reloadedLevel)
}

func Test_reloadConfig_recoversCallbackPanic(t *testing.T) {
	config, err := NewConfig(WithAppName("reloadApp"))
	assert.NoError(t, err)

	panickingCalls := 0
	followingCalls := 0
	config.AddReloadCallback([]string{CfgSamplingCounterRate}, func() {
		panickingCalls++
		panic("callback failure")
	})
	config.AddReloadCallback([]string{CfgSamplingCounterRate}, func() {
		followingCalls++
	})

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	assert.NoError(t, os.WriteFile(cfgFile, []byte("Sampling:\n  CounterRate: 2\n"), 0o600))
	cfgFileViper := viper.New()
	cfgFileViper.SetConfigFile(cfgFile)

	assert.NotPanics(t, func() { config.reloadConfig(cfgFileViper) })
	assert.Equal(t, 1, panickingCalls)
	assert.Equal(t, 1, followingCalls, "a panicking callback must not skip later callbacks")
	assert.Equal(t, 2, config.Int(CfgSamplingCounterRate))
}

// A delete-then-recreate save (unlink+rewrite editors, deploy tools) must not
// end the watcher: it watches the directory, so it survives the Remove and the
// following Create still reloads the recreated file.
func Test_configWatcher_SurvivesFileRemoval(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	assert.NoError(t, os.WriteFile(cfgFile, []byte("Span:\n  MaxCallStackDepth: 12\n"), 0o600))

	config, err := NewConfig(WithAppName("watchApp"), WithConfigFile(cfgFile))
	assert.NoError(t, err)
	defer config.Close()
	assert.Equal(t, 12, config.Int(CfgSpanMaxCallStackDepth))

	assert.NoError(t, os.Remove(cfgFile))
	assert.NoError(t, os.WriteFile(cfgFile, []byte("Span:\n  MaxCallStackDepth: 24\n"), 0o600))

	assert.Eventually(t, func() bool { return config.Int(CfgSpanMaxCallStackDepth) == 24 },
		5*time.Second, 10*time.Millisecond, "recreated config file must reload")
}

func Test_reloadConfig_keepsSamplerWhenSamplingUnchanged(t *testing.T) {
	config, err := NewConfig(WithAppName("reloadApp"), WithSamplingNewThroughput(10))
	assert.NoError(t, err)

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	assert.NoError(t, os.WriteFile(cfgFile, []byte("Span:\n  MaxCallStackDepth: 12\n"), 0o600))
	cfgFileViper := viper.New()
	cfgFileViper.SetConfigFile(cfgFile)

	sampler := config.load().sampler
	config.reloadConfig(cfgFileViper)
	assert.Same(t, sampler, config.load().sampler, "unrelated reload rebuilt the sampler")

	assert.NoError(t, os.WriteFile(cfgFile, []byte("Sampling:\n  NewThroughput: 20\n"), 0o600))
	config.reloadConfig(cfgFileViper)
	assert.NotSame(t, sampler, config.load().sampler, "sampling change did not rebuild the sampler")
}

func Test_reloadConfig_keepsExceptionLimiterWhenThroughputUnchanged(t *testing.T) {
	config, err := NewConfig(WithAppName("reloadApp"), WithErrorNewThroughput(10))
	assert.NoError(t, err)

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	assert.NoError(t, os.WriteFile(cfgFile, []byte("Span:\n  MaxCallStackDepth: 12\n"), 0o600))
	cfgFileViper := viper.New()
	cfgFileViper.SetConfigFile(cfgFile)

	limiter := config.load().newExceptionLimiter
	assert.NotNil(t, limiter)
	config.reloadConfig(cfgFileViper)
	assert.Same(t, limiter, config.load().newExceptionLimiter, "unrelated reload rebuilt the limiter")

	assert.NoError(t, os.WriteFile(cfgFile, []byte("Error:\n  NewThroughput: 20\n"), 0o600))
	config.reloadConfig(cfgFileViper)
	assert.NotSame(t, limiter, config.load().newExceptionLimiter, "throughput change did not rebuild the limiter")
}

func TestNewConfig_HttpUrlStatQueueSizeIsIndependentOfSpanQueueSize(t *testing.T) {
	c, err := NewConfig(WithAppName("TestApp"), WithSpanQueueSize(8192))
	assert.NoError(t, err)
	assert.Equal(t, 8192, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
	assert.Equal(t, defaultQueueSize, c.Int(CfgHttpUrlStatQueueSize), CfgHttpUrlStatQueueSize)

	c, err = NewConfig(WithAppName("TestApp"), WithHttpUrlStatQueueSize(64))
	assert.NoError(t, err)
	assert.Equal(t, 64, c.Int(CfgHttpUrlStatQueueSize), CfgHttpUrlStatQueueSize)
	assert.Equal(t, defaultQueueSize, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
}

// Out-of-range queue sizes and stat settings fall back to the default (Java and
// C++ agent behavior) with a warning. Left as configured, a non-positive value
// panics the stat worker (time.NewTicker(0), zero-length batch indexing) and a
// huge one allocates a giant channel buffer or stalls the stat collector.
func TestNewConfig_OutOfRangeQueueSizeAndStatOptions(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  int
	}{
		{CfgSpanQueueSize, 0, defaultQueueSize},
		{CfgSpanQueueSize, -1, defaultQueueSize},
		{CfgSpanQueueSize, 1e9, defaultQueueSize},
		{CfgHttpUrlStatQueueSize, -1, defaultQueueSize},
		{CfgHttpUrlStatQueueSize, maxQueueSize + 1, defaultQueueSize},
		{CfgStatCollectInterval, 0, 5000},
		{CfgStatCollectInterval, 100, 5000},
		{CfgStatCollectInterval, 999, 5000},
		{CfgStatCollectInterval, 60001, 5000},
		{CfgStatBatchCount, -1, 6},
		{CfgStatBatchCount, 101, 6},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s=%d", tt.name, tt.value), func(t *testing.T) {
			var buf bytes.Buffer
			defer captureWarnLog(&buf)()

			c, err := NewConfig(WithAppName("TestApp"), func(c *Config) { c.cfgMap[tt.name].value = tt.value })
			assert.NoError(t, err)
			assert.Equal(t, tt.want, c.Int(tt.name))
			assert.Contains(t, buf.String(), tt.name)
			assert.Contains(t, buf.String(), "out of range")

			// Set republishes too.
			buf.Reset()
			c.Set(tt.name, tt.value)
			assert.Equal(t, tt.want, c.Int(tt.name))
			assert.Contains(t, buf.String(), "out of range")
		})
	}

	// In-range values pass through silently.
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()
	c, err := NewConfig(WithAppName("TestApp"), WithSpanQueueSize(maxQueueSize), WithStatCollectInterval(60000), WithStatBatchCount(100))
	assert.NoError(t, err)
	assert.Equal(t, maxQueueSize, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
	assert.Equal(t, 60000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
	assert.Equal(t, 100, c.Int(CfgStatBatchCount), CfgStatBatchCount)
	assert.NotContains(t, buf.String(), "out of range")
}

func TestNewConfig_ClampErrorCallStackDepth(t *testing.T) {
	c, err := NewConfig(WithAppName("TestApp"), WithErrorCallStackDepth(0))
	assert.NoError(t, err)
	assert.Equal(t, defaultErrorCallStackDepth, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)

	// Dynamic option, so both bounds must also hold on the publish path a
	// reload or Set goes through.
	c.Set(CfgErrorCallStackDepth, -4)
	assert.Equal(t, defaultErrorCallStackDepth, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)

	c.Set(CfgErrorCallStackDepth, math.MaxInt)
	assert.Equal(t, maxErrorCallStackDepth, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)

	c, err = NewConfig(WithAppName("TestApp"), WithErrorCallStackDepth(math.MaxInt))
	assert.NoError(t, err)
	assert.Equal(t, maxErrorCallStackDepth, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	assert.NoError(t, os.WriteFile(cfgFile, []byte("Error:\n  CallStackDepth: 999999999\n"), 0o600))
	cfgFileViper := viper.New()
	cfgFileViper.SetConfigFile(cfgFile)
	c.reloadConfig(cfgFileViper)
	assert.Equal(t, maxErrorCallStackDepth, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
}

// A bad Sampling.Type must cost the type only. Overwriting Sampling.CounterRate
// with 0 made rateSampler drop every trace, so a typo switched tracing off.
func TestNewConfig_SamplingTypeFallbackKeepsRate(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	c, err := NewConfig(WithAppName("TestApp"), WithSamplingType("bogus"), WithSamplingCounterRate(5))
	assert.NoError(t, err)
	defer c.Close()
	assert.Equal(t, samplingTypeCounter, c.String(CfgSamplingType), CfgSamplingType)
	assert.Equal(t, 5, c.Int(CfgSamplingCounterRate), "the fallback wiped the rate")
	assert.True(t, c.load().sampler.isNewSampled(newAgentStats()), "the fallback stopped sampling")
	assert.Contains(t, buf.String(), samplingTypeCounter, "the warning hides the applied type")
	assert.Contains(t, buf.String(), "Sampling.CounterRate = 5", "the warning hides the applied rate")

	// Dynamic key: a typo arriving by reload must not wipe the rate either.
	c.Set(CfgSamplingType, "nonsense")
	assert.Equal(t, samplingTypeCounter, c.String(CfgSamplingType), CfgSamplingType)
	assert.Equal(t, 5, c.Int(CfgSamplingCounterRate), "the reload fallback wiped the rate")
}

// The type is stored normalized, so a lowercase or Java-named type reaches the
// sampler it asks for instead of falling through to the percent sampler.
func TestNewConfig_SamplingTypeAliases(t *testing.T) {
	for _, given := range []string{"counter", " Counting ", samplingTypeCounting} {
		c, err := NewConfig(WithAppName("TestApp"), WithSamplingType(given), WithSamplingCounterRate(100))
		assert.NoError(t, err)
		assert.Equal(t, samplingTypeCounter, c.String(CfgSamplingType), given)
		_, isRate := c.load().sampler.(*basicTraceSampler).baseSampler.(*rateSampler)
		assert.True(t, isRate, "%q did not build a counter sampler", given)
		c.Close()
	}
}

// Negative values are not an "enable with the default" sentinel: 0 already means
// off (unlimited, for a throughput) and a negative value means the same, warned.
// SQL.CacheLengthLimit keeps -1 alone as its documented unlimited escape hatch.
func TestNewConfig_NegativeValuesOfNewKeys(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	c, err := NewConfig(WithAppName("TestApp"), WithSQLErrorCount(-1), WithErrorNewThroughput(-1),
		WithSQLCacheLengthLimit(-5))
	assert.NoError(t, err)
	defer c.Close()
	assert.Equal(t, 0, c.Int(CfgSQLErrorCount), CfgSQLErrorCount)
	assert.Equal(t, 0, c.Int(CfgErrorNewThroughput), CfgErrorNewThroughput)
	assert.Nil(t, c.load().newExceptionLimiter, "a negative throughput must mean unlimited, not a limiter")
	assert.Equal(t, defaultSqlCacheLengthLimit, c.Int(CfgSQLCacheLengthLimit), CfgSQLCacheLengthLimit)
	assert.Contains(t, buf.String(), "SQL.ErrorCount = -1 is negative")
	assert.Contains(t, buf.String(), "Error.NewThroughput = -1 is negative")
	assert.Contains(t, buf.String(), "SQL.CacheLengthLimit = -5 is out of range")

	c.Set(CfgSQLCacheLengthLimit, -1)
	assert.Equal(t, math.MaxInt32, c.Int(CfgSQLCacheLengthLimit), "-1 must stay unlimited")
}

// The stat queue was sized from Span.QueueSize, so shrinking the span queue
// silently shrank the stat queue with it.
func Test_StatQueueSizeIsIndependentOfSpanQueueSize(t *testing.T) {
	c, err := NewConfig(WithSpanQueueSize(16))
	require.NoError(t, err)

	assert.Equal(t, 16, c.Int(CfgSpanQueueSize))
	assert.Equal(t, defaultQueueSize, c.Int(CfgStatQueueSize), "Span.QueueSize must not move the stat queue")

	a, err := NewTestAgent(c, t)
	require.NoError(t, err)
	defer a.Shutdown()
	assert.Equal(t, defaultQueueSize, cap(a.(*agent).statChan), "statChan must be sized from Stat.QueueSize")
}

// selfWrapErr is a user error whose Unwrap() returns itself: an unbounded
// cause walk over it never reaches nil.
type selfWrapErr struct{ msg string }

func (e *selfWrapErr) Error() string { return e.msg }
func (e *selfWrapErr) Unwrap() error { return e }

// cycleErr wraps another error; two of them pointing at each other form the
// cycle A -> B -> A.
type cycleErr struct {
	msg  string
	next error
}

func (e *cycleErr) Error() string { return e.msg }
func (e *cycleErr) Unwrap() error { return e.next }

// causeOnlyErr implements only pkg/errors' Cause(), not Unwrap().
type causeOnlyErr struct {
	msg   string
	cause error
}

func (e *causeOnlyErr) Error() string { return e.msg }
func (e *causeOnlyErr) Cause() error  { return e.cause }

// ignoreError walks a user-supplied chain on the request goroutine, so the walk
// is capped at maxCauserDepth (see errors.go) whatever Unwrap() returns.
func Test_ignoreError_BoundedCauseWalk(t *testing.T) {
	c, err := NewConfig(WithAppName("ignoreErrApp"), WithErrorIgnoreErrors("*pinpoint.nomatch:"))
	require.NoError(t, err)
	snapshot := c.load()

	a := &cycleErr{msg: "a"}
	b := &cycleErr{msg: "b", next: a}
	a.next = b

	for name, e := range map[string]error{
		"self":  &selfWrapErr{msg: "self"},
		"cycle": a,
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan bool, 1)
			go func() { done <- snapshot.ignoreError(e, "") }()
			select {
			case ignored := <-done:
				assert.False(t, ignored)
			case <-time.After(5 * time.Second):
				t.Fatal("ignoreError did not return: cause walk is unbounded")
			}
		})
	}
}

// A nested error reachable only through Cause() is matched, as the exception
// recorder walks the same chain.
func Test_ignoreError_CauseOnlyChain(t *testing.T) {
	c, err := NewConfig(WithAppName("ignoreErrApp"), WithErrorIgnoreErrors(":inner boom"))
	require.NoError(t, err)
	snapshot := c.load()

	assert.True(t, snapshot.ignoreError(&causeOnlyErr{msg: "outer", cause: fmt.Errorf("inner boom")}, ""))
	assert.False(t, snapshot.ignoreError(&causeOnlyErr{msg: "outer", cause: fmt.Errorf("other")}, ""))
}
