package pinpoint

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A plugin registers its options from its own init function, after this
// package built the noop agent's Config, and reads them through GetConfig()
// before an agent exists: the registration has to reach that Config, or the
// plugin sees zero values instead of its defaults.
func Test_AddConfigReachesTheNoopAgentConfig(t *testing.T) {
	AddConfig("Test.LateBool", CfgBool, true, true)
	AddConfig("Test.LateInt", CfgInt, 42, true)
	assert.True(t, NoopAgent().Config().Bool("Test.LateBool"))
	assert.Equal(t, 42, NoopAgent().Config().Int("Test.LateInt"))

	cfg, err := NewConfig(WithAppName("late"))
	require.NoError(t, err)
	defer cfg.Close()
	assert.Equal(t, 42, cfg.Int("Test.LateInt"), "a Config built afterwards carries it as before")
}

func TestNewConfig_DefaultValue(t *testing.T) {
	c, _ := NewConfig(WithAppName("TestApp"))
	assert.Equal(t, "TestApp", c.String(CfgAppName), CfgAppName)
	assert.Equal(t, ServiceTypeGoApp, c.Int(CfgAppType), CfgAppType)
	assert.Empty(t, c.String(CfgAgentName), CfgAgentName)
	assert.Equal(t, "localhost", c.String(CfgCollectorHost), CfgCollectorHost)
	assert.Equal(t, 9991, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
	assert.Equal(t, 9993, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
	assert.Equal(t, 9992, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
	assert.Equal(t, "info", c.String(CfgLogLevel), CfgLogLevel)
	assert.Equal(t, "stdout", c.String(CfgLogOutput), CfgLogOutput)
	assert.Equal(t, 10, c.Int(CfgLogMaxSize), CfgLogMaxSize)
	assert.Equal(t, 1, c.Int(CfgLogMaxBackups), CfgLogMaxBackups)
	assert.Equal(t, samplingTypeCounter, c.String(CfgSamplingType), CfgSamplingType)
	assert.Equal(t, 1, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
	assert.Equal(t, float64(100), c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
	assert.Equal(t, 0, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
	assert.Equal(t, 0, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
	assert.Equal(t, defaultQueueSize, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
	assert.Equal(t, defaultSpanBatchSize, c.Int(CfgCollectorGrpcSpanBatchSize), CfgCollectorGrpcSpanBatchSize)
	assert.Equal(t, defaultSpanBatchFlushInterval, c.Int(CfgCollectorGrpcSpanBatchFlushInterval), CfgCollectorGrpcSpanBatchFlushInterval)
	assert.Equal(t, defaultSpanBatchCollectDeadline, c.Int(CfgCollectorGrpcSpanBatchCollectDeadline), CfgCollectorGrpcSpanBatchCollectDeadline)
	assert.Equal(t, defaultSpanBatchMaxConcurrentRequests, c.Int(CfgCollectorGrpcSpanBatchMaxConcurrentRequests), CfgCollectorGrpcSpanBatchMaxConcurrentRequests)
	assert.Equal(t, defaultEventChunkSize, c.Int(CfgSpanEventChunkSize), CfgSpanEventChunkSize)
	assert.Equal(t, defaultEventDepth, c.Int(CfgSpanMaxCallStackDepth), CfgSpanMaxCallStackDepth)
	assert.Equal(t, defaultEventSequence, c.Int(CfgSpanMaxCallStackSequence), CfgSpanMaxCallStackSequence)
	assert.Equal(t, defaultQueueSize, c.Int(CfgStatQueueSize), CfgStatQueueSize)
	assert.Equal(t, defaultMetaQueueSize, c.Int(CfgCollectorGrpcSenderQueueSize), CfgCollectorGrpcSenderQueueSize)
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
	assert.Equal(t, 1000, c.Int(CfgHttpUrlStatLimitSize), CfgHttpUrlStatLimitSize)
	assert.Equal(t, 1024, c.Int(CfgHttpUrlStatQueueSize), CfgHttpUrlStatQueueSize)
	assert.Equal(t, false, c.Bool(CfgErrorTraceCallStack), CfgErrorTraceCallStack)
	assert.Equal(t, 32, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
	assert.Equal(t, 24*60*60*1000, c.Int(CfgCollectorAgentInfoRefreshInterval), CfgCollectorAgentInfoRefreshInterval)
}

func TestNewConfig_WithFunc(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithAppType(1234),
		WithAgentName("TestAgentName"),
		WithCollectorHost("func.collector.host"),
		WithCollectorAgentPort(7777),
		WithCollectorSpanPort(8888),
		WithCollectorStatPort(9999),
		WithLogLevel("error"),
		WithLogOutput("stdout"),
		WithLogMaxSize(100),
		WithLogMaxBackups(5),
		WithSamplingType("percent"),
		WithSamplingPercentRate(90),
		WithSamplingCounterRate(200),
		WithSamplingNewThroughput(20),
		WithSamplingContinueThroughput(30),
		WithSpanQueueSize(2048),
		WithCollectorGrpcSpanBatchSize(25),
		WithCollectorGrpcSpanBatchFlushInterval(2000),
		WithCollectorGrpcSpanBatchCollectDeadline(250),
		WithCollectorGrpcSpanBatchMaxConcurrentRequests(4),
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

	c, _ := NewConfig(opts...)
	assert.Equal(t, "TestApp", c.String(CfgAppName), CfgAppName)
	assert.Equal(t, 1234, c.Int(CfgAppType), CfgAppType)
	assert.Equal(t, "TestAgentName", c.String(CfgAgentName), CfgAgentName)
	assert.Equal(t, "func.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
	assert.Equal(t, 7777, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
	assert.Equal(t, 8888, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
	assert.Equal(t, 9999, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
	assert.Equal(t, "error", c.String(CfgLogLevel), CfgLogLevel)
	assert.Equal(t, "stdout", c.String(CfgLogOutput), CfgLogOutput)
	assert.Equal(t, 100, c.Int(CfgLogMaxSize), CfgLogMaxSize)
	assert.Equal(t, 5, c.Int(CfgLogMaxBackups), CfgLogMaxBackups)
	assert.Equal(t, samplingTypePercent, c.String(CfgSamplingType), CfgSamplingType) // normalized
	assert.Equal(t, 200, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
	assert.Equal(t, float64(90), c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
	assert.Equal(t, 20, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
	assert.Equal(t, 30, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
	assert.Equal(t, 2048, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
	assert.Equal(t, 25, c.Int(CfgCollectorGrpcSpanBatchSize), CfgCollectorGrpcSpanBatchSize)
	assert.Equal(t, 2000, c.Int(CfgCollectorGrpcSpanBatchFlushInterval), CfgCollectorGrpcSpanBatchFlushInterval)
	assert.Equal(t, 250, c.Int(CfgCollectorGrpcSpanBatchCollectDeadline), CfgCollectorGrpcSpanBatchCollectDeadline)
	assert.Equal(t, 4, c.Int(CfgCollectorGrpcSpanBatchMaxConcurrentRequests), CfgCollectorGrpcSpanBatchMaxConcurrentRequests)
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
}

func TestNewConfig_ConfigFileYaml(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithConfigFile("example/pinpoint-config.yaml"),
	}

	c, _ := NewConfig(opts...)
	defer c.Close()
	assert.Equal(t, "MyAppName", c.String(CfgAppName), CfgAppName)
	assert.Equal(t, 1900, c.Int(CfgAppType), CfgAppType)
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
}

func TestNewConfig_ConfigFileJson(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithConfigFile("example/pinpoint-config.json"),
	}

	c, _ := NewConfig(opts...)
	defer c.Close()
	assert.Equal(t, "JsonAppName", c.String(CfgAppName), CfgAppName)
	assert.Equal(t, 1901, c.Int(CfgAppType), CfgAppType)
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
}

func TestNewConfig_ConfigFileProp(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithConfigFile("example/pinpoint-config.prop"),
	}

	c, _ := NewConfig(opts...)
	defer c.Close()
	assert.Equal(t, "PropAppName", c.String(CfgAppName), CfgAppName)
	assert.Equal(t, 1902, c.Int(CfgAppType), CfgAppType)
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
}

func TestNewConfig_ConfigFileProfile(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithConfigFile("example/test-config.yaml"),
		WithActiveProfile("real"),
	}

	swapForTest(t, &os.Args, []string{
		"pinpoint_go_agent",
		"--pinpoint-configfile=example/pinpoint-config.yaml",
	})
	t.Setenv("PINPOINT_GO_ACTIVEPROFILE", "dev")

	c, _ := NewConfig(opts...)
	defer c.Close()
	assert.Equal(t, "MyAppName", c.String(CfgAppName), CfgAppName)
	assert.Equal(t, "dev.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
	assert.Equal(t, samplingTypeCounter, c.String(CfgSamplingType), CfgSamplingType)
	assert.Equal(t, 1, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
	assert.Equal(t, 0.1, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
	assert.Equal(t, 50, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
	assert.Equal(t, 60, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
	assert.Equal(t, 7000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
	assert.Equal(t, 10, c.Int(CfgStatBatchCount), CfgStatBatchCount)
	assert.Equal(t, true, c.Bool(CfgIsContainerEnv), CfgIsContainerEnv)
}

func TestNewConfig_EnvVarArg(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithConfigFile("example/test.yaml"),
	}

	t.Setenv("PINPOINT_GO_ACTIVEPROFILE", "dev")
	t.Setenv("PINPOINT_GO_APPLICATIONNAME", "EnvVarArgTest")
	t.Setenv("PINPOINT_GO_APPLICATIONTYPE", "2000")
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
	t.Setenv("PINPOINT_GO_COLLECTOR_GRPC_SENDERQUEUESIZE", "700")
	t.Setenv("PINPOINT_GO_COLLECTOR_GRPC_SPANBATCHSIZE", "40")
	t.Setenv("PINPOINT_GO_COLLECTOR_GRPC_SPANBATCHFLUSHINTERVAL", "1500")
	t.Setenv("PINPOINT_GO_COLLECTOR_GRPC_SPANBATCHCOLLECTDEADLINE", "300")
	t.Setenv("PINPOINT_GO_COLLECTOR_GRPC_SPANBATCHMAXCONCURRENTREQUESTS", "3")
	t.Setenv("PINPOINT_GO_SPAN_EVENTCHUNKSIZE", "88")
	t.Setenv("PINPOINT_GO_SPAN_MAXCALLSTACKDEPTH", "128")
	t.Setenv("PINPOINT_GO_SPAN_MAXCALLSTACKSEQUENCE", "2000")
	t.Setenv("PINPOINT_GO_STAT_COLLECTINTERVAL", "3000")
	t.Setenv("PINPOINT_GO_STAT_BATCHCOUNT", "11")
	t.Setenv("PINPOINT_GO_LOG_LEVEL", "trace")
	t.Setenv("PINPOINT_GO_LOG_OUTPUT", "stdout")
	t.Setenv("PINPOINT_GO_LOG_MAXSIZE", "50")
	t.Setenv("PINPOINT_GO_LOG_MAXBACKUPS", "4")
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

	c, _ := NewConfig(opts...)
	defer c.Close()
	assert.Equal(t, "EnvVarArgTest", c.String(CfgAppName), CfgAppName)
	assert.Equal(t, 2000, c.Int(CfgAppType), CfgAppType)
	assert.Equal(t, "envagentname", c.String(CfgAgentName), CfgAgentName)
	assert.Equal(t, "env.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
	assert.Equal(t, 8000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
	assert.Equal(t, 8100, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
	assert.Equal(t, 8200, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
	assert.Equal(t, "trace", c.String(CfgLogLevel), CfgLogLevel)
	assert.Equal(t, "stdout", c.String(CfgLogOutput), CfgLogOutput)
	assert.Equal(t, 50, c.Int(CfgLogMaxSize), CfgLogMaxSize)
	assert.Equal(t, 4, c.Int(CfgLogMaxBackups), CfgLogMaxBackups)
	assert.Equal(t, samplingTypePercent, c.String(CfgSamplingType), CfgSamplingType) // normalized
	assert.Equal(t, 100, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
	assert.Equal(t, float64(120), c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
	assert.Equal(t, 100, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
	assert.Equal(t, 200, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
	assert.Equal(t, 1000, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
	assert.Equal(t, 700, c.Int(CfgCollectorGrpcSenderQueueSize), CfgCollectorGrpcSenderQueueSize)
	assert.Equal(t, 40, c.Int(CfgCollectorGrpcSpanBatchSize), CfgCollectorGrpcSpanBatchSize)
	assert.Equal(t, 1500, c.Int(CfgCollectorGrpcSpanBatchFlushInterval), CfgCollectorGrpcSpanBatchFlushInterval)
	assert.Equal(t, 300, c.Int(CfgCollectorGrpcSpanBatchCollectDeadline), CfgCollectorGrpcSpanBatchCollectDeadline)
	assert.Equal(t, 3, c.Int(CfgCollectorGrpcSpanBatchMaxConcurrentRequests), CfgCollectorGrpcSpanBatchMaxConcurrentRequests)
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
}

// Flags that take a value accept "--pinpoint-key value" as well as
// "--pinpoint-key=value"; a boolean flag takes no value token; an unknown
// --pinpoint-* flag is ignored without stopping the parse; a value-taking
// flag without a value is dropped with a warning. The application's own
// arguments are never consumed.
func TestNewConfig_CmdLineArg_ValueForms(t *testing.T) {
	swapForTest(t, &os.Args, []string{
		"app",
		"--pinpoint-applicationname", "SpacedApp",
		"--pinpoint-collector-agentport", "7001",
		"--pinpoint-sql-tracequerystat", "positional", // boolean: "positional" is the application's
		"--pinpoint-unknown-option=1",
		"--pinpoint-unknown-flag", "value",
		"--pinpoint-agentname=EqAgent",
		"-x",
		"--pinpoint-log-level", "--pinpoint-sampling-counterrate=3", // no value: dropped, the next flag still applies
		"--pinpoint-span-maxcallstackdepth", "-1", // a value starting with "-" needs the "=" form
	})

	c, err := NewConfig()
	require.NoError(t, err)
	defer c.Close()

	assert.Equal(t, "SpacedApp", c.String(CfgAppName))
	assert.Equal(t, 7001, c.Int(CfgCollectorAgentPort))
	assert.True(t, c.Bool(CfgSQLTraceQueryStat))
	assert.Equal(t, "EqAgent", c.String(CfgAgentName))
	assert.Equal(t, 3, c.Int(CfgSamplingCounterRate), "the flags after an unknown or valueless one are still parsed")
	assert.Equal(t, defaultEventDepth, c.Int(CfgSpanMaxCallStackDepth), "a value starting with - is not taken")
	assert.Equal(t, "info", c.String(CfgLogLevel), "a valueless string flag is dropped")
}

func TestNewConfig_CmdLineArg(t *testing.T) {
	opts := []ConfigOption{
		WithAppName("TestApp"),
		WithConfigFile("example/test-config.yaml"),
	}

	swapForTest(t, &os.Args, []string{
		"pinpoint_go_agent",
		"--app-arg1=1",
		"--app-arg2=2",
		"--pinpoint-applicationname=CmdLineArgTest",
		"--pinpoint-applicationtype=2100",
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
		"--pinpoint-collector-grpc-senderqueuesize=20",
		"--pinpoint-collector-grpc-spanbatchsize=30",
		"--pinpoint-collector-grpc-spanbatchflushinterval=2500",
		"--pinpoint-collector-grpc-spanbatchcollectdeadline=350",
		"--pinpoint-collector-grpc-spanbatchmaxconcurrentrequests=2",
		"--pinpoint-span-eventchunksize=30",
		"--pinpoint-span-maxcallstackdepth=-1",
		"--pinpoint-span-maxcallstacksequence=-1",
		"--pinpoint-stat-collectinterval=6000",
		"--pinpoint-stat-batchcount=5",
		"--pinpoint-log-level=error",
		"--pinpoint-log-output=stdout",
		"--pinpoint-log-maxsize=20",
		"--pinpoint-log-maxbackups=3",
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
	})

	c, _ := NewConfig(opts...)
	defer c.Close()

	assert.Equal(t, "CmdLineArgTest", c.String(CfgAppName), CfgAppName)
	assert.Equal(t, 2100, c.Int(CfgAppType), CfgAppType)
	assert.Equal(t, "cmdAgentName", c.String(CfgAgentName), CfgAgentName)
	assert.Equal(t, "cmd.collector.host", c.String(CfgCollectorHost), CfgCollectorHost)
	assert.Equal(t, 7000, c.Int(CfgCollectorAgentPort), CfgCollectorAgentPort)
	assert.Equal(t, 7100, c.Int(CfgCollectorSpanPort), CfgCollectorSpanPort)
	assert.Equal(t, 7200, c.Int(CfgCollectorStatPort), CfgCollectorStatPort)
	assert.Equal(t, "error", c.String(CfgLogLevel), CfgLogLevel)
	assert.Equal(t, "stdout", c.String(CfgLogOutput), CfgLogOutput)
	assert.Equal(t, 20, c.Int(CfgLogMaxSize), CfgLogMaxSize)
	assert.Equal(t, 3, c.Int(CfgLogMaxBackups), CfgLogMaxBackups)
	assert.Equal(t, samplingTypePercent, c.String(CfgSamplingType), CfgSamplingType) // normalized
	assert.Equal(t, 10, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
	assert.Equal(t, 0.0001, c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
	assert.Equal(t, 500, c.Int(CfgSamplingNewThroughput), CfgSamplingNewThroughput)
	assert.Equal(t, 600, c.Int(CfgSamplingContinueThroughput), CfgSamplingContinueThroughput)
	assert.Equal(t, 10, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
	assert.Equal(t, 20, c.Int(CfgCollectorGrpcSenderQueueSize), CfgCollectorGrpcSenderQueueSize)
	assert.Equal(t, 30, c.Int(CfgCollectorGrpcSpanBatchSize), CfgCollectorGrpcSpanBatchSize)
	assert.Equal(t, 2500, c.Int(CfgCollectorGrpcSpanBatchFlushInterval), CfgCollectorGrpcSpanBatchFlushInterval)
	assert.Equal(t, 350, c.Int(CfgCollectorGrpcSpanBatchCollectDeadline), CfgCollectorGrpcSpanBatchCollectDeadline)
	assert.Equal(t, 2, c.Int(CfgCollectorGrpcSpanBatchMaxConcurrentRequests), CfgCollectorGrpcSpanBatchMaxConcurrentRequests)
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

	cfgFileViper := newConfigFile(cfgFile)
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

// An invalid reloaded Log.Level keeps the published level.
func Test_reloadConfig_unknownLogLevelKeepsCurrentLevel(t *testing.T) {
	oldLevel := logger.defaultLogger.GetLevel()
	t.Cleanup(func() { logger.defaultLogger.SetLevel(oldLevel) })

	config, err := NewConfig(WithAppName("reloadApp"), WithLogLevel("error"))
	require.NoError(t, err)
	logger.setup(config)
	config.AddReloadCallback([]string{CfgLogLevel}, func() { logger.reloadLevel(config) })
	require.Equal(t, logrus.ErrorLevel, logger.defaultLogger.GetLevel())

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	cfgFileViper := newConfigFile(cfgFile)
	for _, body := range []string{"Log:\n  Level: eror\n"} {
		var buf bytes.Buffer
		restore := captureLogAt(&buf, logrus.ErrorLevel)
		require.NoError(t, os.WriteFile(cfgFile, []byte(body), 0o600))
		config.reloadConfig(cfgFileViper)
		restore()

		assert.Equal(t, "error", config.String(CfgLogLevel), body)
		assert.Equal(t, logrus.ErrorLevel, logger.defaultLogger.GetLevel(), body)
		assert.Contains(t, buf.String(), "keeping error", body)
	}

	// A valid level still reloads.
	require.NoError(t, os.WriteFile(cfgFile, []byte("Log:\n  Level: debug\n"), 0o600))
	config.reloadConfig(cfgFileViper)
	assert.Equal(t, logrus.DebugLevel, logger.defaultLogger.GetLevel())
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
	cfgFileViper := newConfigFile(cfgFile)

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
	cfgFileViper := newConfigFile(cfgFile)

	sampler := config.load().sampler
	config.reloadConfig(cfgFileViper)
	assert.Same(t, sampler, config.load().sampler, "unrelated reload rebuilt the sampler")

	assert.NoError(t, os.WriteFile(cfgFile, []byte("Sampling:\n  NewThroughput: 20\n"), 0o600))
	config.reloadConfig(cfgFileViper)
	assert.NotSame(t, sampler, config.load().sampler, "sampling change did not rebuild the sampler")
}

// A percent rate reloaded to 0 stops sampling new transactions entirely, and
// reloading it back restores the previous behaviour.
func Test_reloadConfig_percentRateZeroStopsSampling(t *testing.T) {
	config, err := NewConfig(WithAppName("reloadApp"), WithSamplingType("PERCENT"), WithSamplingPercentRate(50))
	assert.NoError(t, err)

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	cfgFileViper := newConfigFile(cfgFile)

	sampledOf := func(n int) int {
		sampler := config.load().sampler
		stats := newAgentStats()
		sampled := 0
		for i := 0; i < n; i++ {
			if sampler.isNewSampled(stats) {
				sampled++
			}
		}
		return sampled
	}
	reload := func(percent string) {
		assert.NoError(t, os.WriteFile(cfgFile, []byte("Sampling:\n  Type: PERCENT\n  PercentRate: "+percent+"\n"), 0o600))
		config.reloadConfig(cfgFileViper)
	}

	assert.Equal(t, 50, sampledOf(100), "50%")
	reload("0")
	assert.Equal(t, 0, sampledOf(100), "0%")
	reload("50")
	assert.Equal(t, 50, sampledOf(100), "back to 50%")
}

func Test_reloadConfig_keepsExceptionLimiterWhenThroughputUnchanged(t *testing.T) {
	config, err := NewConfig(WithAppName("reloadApp"), WithErrorNewThroughput(10))
	assert.NoError(t, err)

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	assert.NoError(t, os.WriteFile(cfgFile, []byte("Span:\n  MaxCallStackDepth: 12\n"), 0o600))
	cfgFileViper := newConfigFile(cfgFile)

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
		// A limit of 0 or less would drop every url stat entry.
		{CfgHttpUrlStatLimitSize, 0, 1000},
		{CfgHttpUrlStatLimitSize, -1, 1000},
		{CfgHttpUrlStatLimitSize, maxQueueSize + 1, 1000},
		{CfgCollectorGrpcSenderQueueSize, 0, defaultMetaQueueSize},
		{CfgCollectorGrpcSenderQueueSize, maxQueueSize + 1, defaultMetaQueueSize},
		// Every sampled request sized its chunk buffer by it, and every batch
		// its slice: 1e7 allocated 76MiB a request.
		{CfgSpanEventChunkSize, 0, defaultEventChunkSize},
		{CfgSpanEventChunkSize, 1e7, defaultEventChunkSize},
		{CfgCollectorGrpcSpanBatchSize, 0, defaultSpanBatchSize},
		{CfgCollectorGrpcSpanBatchSize, 1e9, defaultSpanBatchSize},
		{CfgStatCollectInterval, 0, 5000},
		{CfgStatCollectInterval, 100, 5000},
		{CfgStatCollectInterval, 999, 5000},
		{CfgStatCollectInterval, 10001, 5000},
		// A value above the supported maximum falls back to the default.
		{CfgStatCollectInterval, 60000, 5000},
		{CfgStatBatchCount, -1, 6},
		{CfgStatBatchCount, 101, 6},
		// 0 is "unlimited" to lumberjack; refused so the disk stays bounded.
		{CfgLogMaxBackups, 0, defaultLogMaxBackups},
		{CfgLogMaxBackups, -1, defaultLogMaxBackups},
		// A 0 message size fails every send.
		{CfgCollectorGrpcMaxSendMessageSize, 0, grpcMaxMessageSize},
		{CfgCollectorGrpcMaxReceiveMessageSize, -1, grpcMaxMessageSize},
		{CfgCollectorGrpcKeepAliveTime, 0, grpcKeepAliveTime},
		{CfgCollectorGrpcKeepAliveTimeout, -1, grpcKeepAliveTimeout},
		{CfgCollectorGrpcFlowControlWindow, 0, grpcFlowControlWindow},
		{CfgCollectorGrpcWriteBufferSize, -1, grpcWriteBufferSize},
		{CfgCollectorGrpcMaxHeaderListSize, 0, grpcMaxHeaderListSize},
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
	c, err := NewConfig(WithAppName("TestApp"), WithSpanQueueSize(maxQueueSize), WithStatCollectInterval(10000), WithStatBatchCount(100))
	assert.NoError(t, err)
	assert.Equal(t, maxQueueSize, c.Int(CfgSpanQueueSize), CfgSpanQueueSize)
	assert.Equal(t, 10000, c.Int(CfgStatCollectInterval), CfgStatCollectInterval)
	assert.Equal(t, 100, c.Int(CfgStatBatchCount), CfgStatBatchCount)
	assert.NotContains(t, buf.String(), "out of range")
}

// A float reaches an int option from a YAML, JSON or TOML file, or from Set.
// int(f) is implementation-defined for a value int cannot hold, so .inf or
// 1e20 took a different value on each CPU; only a float holding an int
// converts.
func Test_convertCfgValue_IntTakesOnlyIntegralFloats(t *testing.T) {
	for _, v := range []interface{}{20.0, float32(20)} {
		got, err := convertCfgValue(CfgInt, v)
		assert.NoError(t, err)
		assert.Equal(t, 20, got)
	}
	for _, v := range []interface{}{2.5, math.Inf(1), math.Inf(-1), math.NaN(), 1e20, -1e20} {
		_, err := convertCfgValue(CfgInt, v)
		assert.Error(t, err, "%v", v)
	}
}

// A flag, an environment variable or a properties file gives an int option a
// string, which is read in base 10: "010" is ten, not octal eight.
func Test_convertCfgValue_IntStringIsDecimal(t *testing.T) {
	got, err := convertCfgValue(CfgInt, "010")
	assert.NoError(t, err)
	assert.Equal(t, 10, got)
	_, err = convertCfgValue(CfgInt, "0x10")
	assert.Error(t, err)
}

// The snapshot holds the call stack limits as int32, so a value above
// MaxInt32 must mean unlimited like -1 rather than wrap to 0 and drop every
// span event.
func TestNewConfig_CallStackLimitsAboveMaxInt32AreUnlimited(t *testing.T) {
	if math.MaxInt < 1<<32 {
		t.Skip("int cannot hold a value above MaxInt32")
	}
	c, err := NewConfig(WithAppName("TestApp"))
	assert.NoError(t, err)
	c.Set(CfgSpanMaxCallStackDepth, int64(1)<<32)
	c.Set(CfgSpanMaxCallStackSequence, int64(1)<<32+1)

	assert.Equal(t, math.MaxInt32, c.Int(CfgSpanMaxCallStackDepth))
	assert.Equal(t, math.MaxInt32, c.Int(CfgSpanMaxCallStackSequence))
	assert.Equal(t, int32(math.MaxInt32), c.load().spanMaxEventDepth)
	assert.Equal(t, int32(math.MaxInt32), c.load().spanMaxEventSequence)
}

// Collector.Grpc.IdleTimeout defaults to 0, which is grpc-go's documented
// "idling disabled" value (WithIdleTimeout, v1.82.1; the unset default would be
// 30 minutes). A negative value normalizes to the default like the other
// Collector.Grpc.*MaxAge keys; a positive value is kept and re-enables idling.
func TestNewConfig_CollectorGrpcIdleTimeout(t *testing.T) {
	c, err := NewConfig(WithAppName("TestApp"))
	require.NoError(t, err)
	assert.Equal(t, CfgInt, c.cfgMap[CfgCollectorGrpcIdleTimeout].valueType, "type")
	assert.Equal(t, 0, c.Int(CfgCollectorGrpcIdleTimeout), "default")
	assert.Equal(t, 0, grpcIdleTimeout, "0 disables idling in grpc-go")

	tests := []struct {
		name  string
		value int
		want  int
	}{
		{"positive is kept", 600000, 600000},
		{"zero disables", 0, 0},
		{"negative normalizes to the default", -1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewConfig(WithAppName("TestApp"), WithCollectorGrpcIdleTimeout(tt.value))
			require.NoError(t, err)
			assert.Equal(t, tt.want, c.Int(CfgCollectorGrpcIdleTimeout))

			// Set normalizes too.
			c.Set(CfgCollectorGrpcIdleTimeout, tt.value)
			assert.Equal(t, tt.want, c.Int(CfgCollectorGrpcIdleTimeout))
		})
	}
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
	cfgFileViper := newConfigFile(cfgFile)
	c.reloadConfig(cfgFileViper)
	assert.Equal(t, maxErrorCallStackDepth, c.Int(CfgErrorCallStackDepth), CfgErrorCallStackDepth)
}

// event's bind values from filling a whole gRPC message applies. Both clamps
// warn - a silent one leaves the config file saying 4096 while the agent runs
// at 1024.
func TestNewConfig_ClampSqlMaxBindValueSize(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	c, err := NewConfig(WithAppName("TestApp"), WithSQLMaxBindValueSize(4096))
	assert.NoError(t, err)
	defer c.Close()
	assert.Equal(t, 4096, c.Int(CfgSQLMaxBindValueSize), "a value above the default is no longer clamped")
	assert.Empty(t, buf.String(), "nothing was clamped, so nothing to warn about")

	// Dynamic key, so the ceiling must hold on the publish path a Set or a
	// reload goes through too.
	c.Set(CfgSQLMaxBindValueSize, maxSqlBindValueSize+1)
	assert.Equal(t, maxSqlBindValueSize, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
	assert.Equal(t, 1, strings.Count(buf.String(), "\n"), "one warning line")
	assert.Contains(t, buf.String(), CfgSQLMaxBindValueSize)

	buf.Reset()
	c.Set(CfgSQLMaxBindValueSize, -1)
	assert.Equal(t, 0, c.Int(CfgSQLMaxBindValueSize), CfgSQLMaxBindValueSize)
	assert.False(t, c.Bool(CfgSQLTraceBindValue), "a negative size turns bind value tracing off")
	assert.Equal(t, 1, strings.Count(buf.String(), "\n"), "one warning line")
	assert.Contains(t, buf.String(), CfgSQLTraceBindValue, "the warning hides that tracing was turned off")
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

// sampler it asks for instead of falling through to the percent sampler.
func TestNewConfig_SamplingTypeAliases(t *testing.T) {
	for _, given := range []string{"counter", " Counting ", samplingTypeCounting} {
		c, err := NewConfig(WithAppName("TestApp"), WithSamplingType(given), WithSamplingCounterRate(100))
		assert.NoError(t, err)
		assert.Equal(t, samplingTypeCounter, c.String(CfgSamplingType), given)
		_, isRate := c.load().sampler.baseSampler.(*rateSampler)
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

// A negative SQL.CacheExpireHours reached metaCache's ttl > 0 gate and turned
// expiry off as if it were 0, so a lapsed collector row left an empty SQL in
// the web UI until a restart. It now recovers the default with a warning, while
// 0 keeps meaning "never expires".
func TestNewConfig_SQLCacheExpireHours(t *testing.T) {
	var buf bytes.Buffer
	restore := captureWarnLog(&buf)
	c, err := NewConfig(WithAppName("TestApp"), WithSQLCacheExpireHours(-1))
	assert.NoError(t, err)
	assert.Equal(t, defaultSqlCacheExpireHours, c.Int(CfgSQLCacheExpireHours), "negative recovers the default")
	assert.Contains(t, buf.String(), "SQL.CacheExpireHours = -1 is out of range")
	c.Close()
	restore()

	for _, ok := range []int{0, 1} {
		buf.Reset()
		restore = captureWarnLog(&buf)
		c, err = NewConfig(WithAppName("TestApp"), WithSQLCacheExpireHours(ok))
		assert.NoError(t, err)
		assert.Equal(t, ok, c.Int(CfgSQLCacheExpireHours), "%d is kept", ok)
		assert.NotContains(t, buf.String(), CfgSQLCacheExpireHours)
		c.Close()
		restore()
	}
}

// the default with a warning outside [1, maxSqlCacheSize]: 0 or a negative
// would leave the SQL caches with no usable capacity and the upper bound keeps
// a typo from committing gigabytes at startup.
func TestNewConfig_SQLCacheSize(t *testing.T) {
	c, err := NewConfig(WithAppName("TestApp"))
	assert.NoError(t, err)
	assert.Equal(t, defaultSqlCacheSize, c.Int(CfgSQLCacheSize), "default")
	assert.Equal(t, 1024, defaultSqlCacheSize, "Java profiler.jdbc.sqlcachesize")
	c.Close()

	c, err = NewConfig(WithAppName("TestApp"), WithSQLCacheSize(4096))
	assert.NoError(t, err)
	assert.Equal(t, 4096, c.Int(CfgSQLCacheSize), "a valid value is kept")
	c.Close()

	for _, bad := range []int{0, -1, maxSqlCacheSize + 1} {
		var buf bytes.Buffer
		restore := captureWarnLog(&buf)
		c, err = NewConfig(WithAppName("TestApp"), WithSQLCacheSize(bad))
		assert.NoError(t, err)
		assert.Equal(t, defaultSqlCacheSize, c.Int(CfgSQLCacheSize), "out of range %d", bad)
		assert.Contains(t, buf.String(), "SQL.CacheSize = "+fmt.Sprint(bad)+" is out of range")
		c.Close()
		restore()
	}
}

// A value that does not convert to its option's registered type is dropped
// with a warning, so malformed input cannot silently become a zero value.
func TestNewConfig_MalformedValueKeepsCurrentValue(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		cfgName  string
		raw      string
		typeName string
		check    func(t *testing.T, c *Config)
	}{
		{
			name:     "int option given a string",
			body:     "Sampling:\n  CounterRate: abc\n",
			cfgName:  CfgSamplingCounterRate,
			raw:      "abc",
			typeName: "int",
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, 1, c.Int(CfgSamplingCounterRate))
				assert.True(t, c.load().sampler.isNewSampled(newAgentStats()),
					"a typo in the rate turned sampling off")
			},
		},
		{
			name:     "bool option given a non-bool string",
			body:     "SQL:\n  TraceCommit: maybe\n",
			cfgName:  CfgSQLTraceCommit,
			raw:      "maybe",
			typeName: "bool",
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, true, c.Bool(CfgSQLTraceCommit))
			},
		},
		{
			name:     "float option given a string",
			body:     "Sampling:\n  PercentRate: xyz\n",
			cfgName:  CfgSamplingPercentRate,
			raw:      "xyz",
			typeName: "float",
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, float64(100), c.Float(CfgSamplingPercentRate))
			},
		},
		{
			// convertCfgValue rejects anything that is not a sequence rather
			// than wrap a scalar into a one-element slice - a comma separated
			// string excepted, which is how a list is spelled in an
			// environment variable.
			name:     "string slice option given a scalar",
			body:     "Span:\n  IgnoreErrors: 42\n",
			cfgName:  CfgSpanIgnoreErrors,
			raw:      "42",
			typeName: "string slice",
			check: func(t *testing.T, c *Config) {
				assert.Empty(t, c.StringSlice(CfgSpanIgnoreErrors))
				assert.Empty(t, c.load().errorIgnoreRules)
			},
		},
		{
			name:     "string option given a mapping",
			body:     "Log:\n  Level:\n    a: b\n",
			cfgName:  CfgLogLevel,
			raw:      "map[a:b]",
			typeName: "string",
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, "info", c.String(CfgLogLevel))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			defer captureWarnLog(&buf)()

			cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
			require.NoError(t, os.WriteFile(cfgFile, []byte(tt.body), 0o600))

			c, err := NewConfig(WithAppName("TestApp"), WithConfigFile(cfgFile))
			require.NoError(t, err)
			defer c.Close()

			tt.check(t, c)
			assert.Contains(t, buf.String(),
				fmt.Sprintf("%s = %s is not a valid %s, keeping ", tt.cfgName, tt.raw, tt.typeName))
		})
	}
}

// Load warnings use the configured log output, including an output named in
// the configuration file itself.
func TestNewConfig_LoadWarningsReachTheConfiguredLogFile(t *testing.T) {
	t.Cleanup(func() { logger.setOutput("stderr", 10, 1) })

	t.Run("output from the config file", func(t *testing.T) {
		logFile := filepath.Join(t.TempDir(), "pinpoint.log")
		cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
		body := fmt.Sprintf("Log:\n  Output: %s\nSampling:\n  Type: bogus\n  CounterRate: abc\n", logFile)
		require.NoError(t, os.WriteFile(cfgFile, []byte(body), 0o600))

		c, err := NewConfig(WithAppName("TestApp"), WithConfigFile(cfgFile))
		require.NoError(t, err)
		defer c.Close()

		b, err := os.ReadFile(logFile)
		require.NoError(t, err)
		assert.Contains(t, string(b), "Sampling.CounterRate = abc is not a valid int")
		// logrus quotes a msg that contains quotes and escapes the inner ones.
		assert.Contains(t, string(b), `Sampling.Type = \"bogus\" is not supported`)
	})

	// With the output given by the environment, even an unreadable config
	// file is reported in the file.
	t.Run("output from the environment", func(t *testing.T) {
		logFile := filepath.Join(t.TempDir(), "pinpoint.log")
		t.Setenv("PINPOINT_GO_LOG_OUTPUT", logFile)

		c, err := NewConfig(WithAppName("TestApp"), WithConfigFile(filepath.Join(t.TempDir(), "missing.yaml")))
		require.NoError(t, err)
		defer c.Close()

		b, err := os.ReadFile(logFile)
		require.NoError(t, err)
		assert.Contains(t, string(b), "config file loading error")
	})
}

// The same policy has to hold for every source the value can arrive from, not
// just the config file: an environment variable is always a string, so it is
// the source a wrong type is most likely to come from.
func TestNewConfig_MalformedEnvVarKeepsCurrentValue(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	t.Setenv("PINPOINT_GO_SAMPLING_COUNTERRATE", "one")
	t.Setenv("PINPOINT_GO_SAMPLING_PERCENTRATE", "half")
	t.Setenv("PINPOINT_GO_SQL_TRACEROLLBACK", "sure")

	c, err := NewConfig(WithAppName("TestApp"), WithSamplingCounterRate(7))
	require.NoError(t, err)
	defer c.Close()

	assert.Equal(t, 7, c.Int(CfgSamplingCounterRate), "the config function value was overwritten by a bad env var")
	assert.Equal(t, float64(100), c.Float(CfgSamplingPercentRate), CfgSamplingPercentRate)
	assert.Equal(t, true, c.Bool(CfgSQLTraceRollback), CfgSQLTraceRollback)
	assert.Contains(t, buf.String(), "Sampling.CounterRate = one is not a valid int, keeping 7")
	assert.Contains(t, buf.String(), "Sampling.PercentRate = half is not a valid float, keeping 100")
	assert.Contains(t, buf.String(), "SQL.TraceRollback = sure is not a valid bool, keeping true")
}

// Set() is the only entry point that takes an interface{} from the caller, so
// it needs the same guard as the config file and environment variable paths.
func Test_SetMalformedValueKeepsCurrentValue(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	c, err := NewConfig(WithAppName("TestApp"), WithSamplingCounterRate(7))
	require.NoError(t, err)
	defer c.Close()

	c.Set(CfgSamplingCounterRate, "abc")
	assert.Equal(t, 7, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
	assert.Contains(t, buf.String(), "Sampling.CounterRate = abc is not a valid int, keeping 7")

	// A good value still goes through, converted to the registered type.
	c.Set(CfgSamplingCounterRate, "9")
	assert.Equal(t, 9, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
}

// A malformed value arriving by reload must not report the option as changed
// either: the value did not change, so no reload callback has anything to do.
func Test_reloadConfig_malformedValueKeepsCurrentValue(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()

	c, err := NewConfig(WithAppName("reloadApp"))
	require.NoError(t, err)
	defer c.Close()

	var reloaded int
	c.AddReloadCallback([]string{CfgSamplingCounterRate}, func() { reloaded++ })

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("Sampling:\n  CounterRate: abc\n"), 0o600))
	cfgFileViper := newConfigFile(cfgFile)
	c.reloadConfig(cfgFileViper)

	assert.Equal(t, 1, c.Int(CfgSamplingCounterRate), CfgSamplingCounterRate)
	assert.Equal(t, 0, reloaded, "a rejected value fired the reload callback")
	assert.Contains(t, buf.String(), "Sampling.CounterRate = abc is not a valid int, keeping 1")
}

// Converted values use their registered type so reload change detection
// compares like with like.
func TestNewConfig_StoresTheRegisteredType(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("Sampling:\n  Type: PERCENT\n  PercentRate: 100\n"), 0o600))

	c, err := NewConfig(WithAppName("TestApp"), WithConfigFile(cfgFile))
	require.NoError(t, err)
	defer c.Close()

	values := c.load().values
	assert.IsType(t, float64(0), values[CfgSamplingPercentRate], CfgSamplingPercentRate)
	assert.IsType(t, int(0), values[CfgSamplingCounterRate], CfgSamplingCounterRate)
	assert.IsType(t, false, values[CfgSQLTraceCommit], CfgSQLTraceCommit)
	assert.IsType(t, "", values[CfgLogLevel], CfgLogLevel)
	assert.IsType(t, int(0), values[CfgLogMaxBackups], CfgLogMaxBackups)
	assert.IsType(t, []string{}, values[CfgSpanIgnoreErrors], CfgSpanIgnoreErrors)

	var reloaded int
	c.AddReloadCallback([]string{CfgSamplingPercentRate}, func() { reloaded++ })
	cfgFileViper := newConfigFile(cfgFile)
	c.reloadConfig(cfgFileViper)
	assert.Equal(t, 0, reloaded, "an unchanged rate was reported as changed")
}

// The stat queue was sized from Span.QueueSize, so shrinking the span queue
// silently shrank the stat queue with it.
func Test_StatQueueSizeIsIndependentOfSpanQueueSize(t *testing.T) {
	c, err := NewConfig(WithSpanQueueSize(16))
	require.NoError(t, err)

	assert.Equal(t, 16, c.Int(CfgSpanQueueSize))
	assert.Equal(t, defaultQueueSize, c.Int(CfgStatQueueSize), "Span.QueueSize must not move the stat queue")

	a, err := NewTestAgent(c)
	require.NoError(t, err)
	defer a.Shutdown()
	assert.Equal(t, defaultQueueSize, cap(a.(*agent).statChan), "statChan must be sized from Stat.QueueSize")
}

// The metadata queue was sized from Span.QueueSize, so tuning the span queue
// silently resized the metadata queue with it.
func Test_MetaQueueSizeIsIndependentOfSpanQueueSize(t *testing.T) {
	c, err := NewConfig(WithSpanQueueSize(16), WithCollectorGrpcSenderQueueSize(32))
	require.NoError(t, err)

	assert.Equal(t, 16, c.Int(CfgSpanQueueSize))
	assert.Equal(t, 32, c.Int(CfgCollectorGrpcSenderQueueSize))

	a, err := NewTestAgent(c)
	require.NoError(t, err)
	defer a.Shutdown()
	assert.Equal(t, 32, cap(a.(*agent).metaChan), "metaChan must be sized from Collector.Grpc.SenderQueueSize")
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
	c, err := NewConfig(WithAppName("ignoreErrApp"), WithSpanIgnoreErrors("*pinpoint.nomatch:"))
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
	c, err := NewConfig(WithAppName("ignoreErrApp"), WithSpanIgnoreErrors(":inner boom"))
	require.NoError(t, err)
	snapshot := c.load()

	assert.True(t, snapshot.ignoreError(&causeOnlyErr{msg: "outer", cause: fmt.Errorf("inner boom")}, ""))
	assert.False(t, snapshot.ignoreError(&causeOnlyErr{msg: "outer", cause: fmt.Errorf("other")}, ""))
}

// With neither Span.ErrorMark nor Span.ErrorMarkExclude configured, every
// category must be enabled.
func TestNewConfig_ErrorMarkDefaultsToEveryCategory(t *testing.T) {
	c, err := NewConfig(WithAppName("errorMarkApp"))
	require.NoError(t, err)
	defer c.Close()

	assert.Empty(t, c.StringSlice(CfgSpanErrorMark), CfgSpanErrorMark)
	assert.Empty(t, c.StringSlice(CfgSpanErrorMarkExclude), CfgSpanErrorMarkExclude)
	assert.Equal(t, allErrorCategories, c.load().errorMarkMask)
	assert.Equal(t, ErrorCategory(15), allErrorCategories, "unknown|exception|http-status|sql")
}

// The bit values are a wire contract with the collector and the other agents.
func TestErrorCategory_BitValuesMatchJava(t *testing.T) {
	assert.Equal(t, ErrorCategory(1), ErrorCategoryUnknown, "Java ErrorCategory.UNKNOWN")
	assert.Equal(t, ErrorCategory(2), ErrorCategoryException, "Java ErrorCategory.EXCEPTION")
	assert.Equal(t, ErrorCategory(4), ErrorCategoryHttpStatus, "Java ErrorCategory.HTTP_STATUS")
	assert.Equal(t, ErrorCategory(8), ErrorCategorySql, "Java ErrorCategory.SQL")
}

// The resolution rules themselves are in Test_ErrorMarkMaskResolution; this
// checks the two options reach parseErrorMarkMask.
func TestNewConfig_ErrorMarkAndExclude(t *testing.T) {
	c, err := NewConfig(WithAppName("errorMarkApp"),
		WithSpanErrorMark("exception", "http-status", "sql"), WithSpanErrorMarkExclude("sql"))
	require.NoError(t, err)
	defer c.Close()

	assert.Equal(t, ErrorCategoryUnknown|ErrorCategoryException|ErrorCategoryHttpStatus, c.load().errorMarkMask)
}

// An unrecognised name is warned about and ignored - it must not silently
// widen the mask back to every cause, which is what treating the whole list as
// unparseable would do.
func TestNewConfig_ErrorMarkWarnsOnAnUnknownCategory(t *testing.T) {
	var buf bytes.Buffer
	restore := captureWarnLog(&buf)

	c, err := NewConfig(WithAppName("errorMarkApp"), WithSpanErrorMark("exception", "typo-here"))
	require.NoError(t, err)
	defer c.Close()
	mask := c.load().errorMarkMask
	restore()

	assert.Equal(t, ErrorCategoryUnknown|ErrorCategoryException, mask)
	assert.Contains(t, buf.String(), "typo-here")
}

// A snapshot built by hand instead of by NewConfig has no mask at all, and a
// zero mask reads as every category enabled.
func Test_marksError_ZeroMaskReadsAsEveryCategory(t *testing.T) {
	snapshot := &configSnapshot{}

	for _, category := range []ErrorCategory{ErrorCategoryUnknown, ErrorCategoryException,
		ErrorCategoryHttpStatus, ErrorCategorySql} {
		assert.True(t, snapshot.marksError(category), category)
	}
	assert.True(t, emptyConfigSnapshot.marksError(ErrorCategoryException))
}

// which is also how a config file spells a string slice on one line.
func TestNewConfig_ErrorMarkFromEnv(t *testing.T) {
	t.Setenv("PINPOINT_GO_SPAN_ERRORMARK", "exception, sql")
	t.Setenv("PINPOINT_GO_SPAN_ERRORMARKEXCLUDE", "sql")

	c, err := NewConfig(WithAppName("errorMarkApp"))
	require.NoError(t, err)
	defer c.Close()

	assert.Equal(t, ErrorCategoryUnknown|ErrorCategoryException, c.load().errorMarkMask)
}

// A ConfigOption is a startup default whichever way it writes its value. The
// plugin options go through Set(), which stamps the source a Set() after
// startup gets, and that source is what a reload skips: an option given as a
// config function was never overridden by the file while the core options,
// which assign the staging map directly, were. Both are the default source
// after NewConfig; only a Set() made afterwards keeps its value across a
// reload, as doc/config.md says.
func Test_NewConfig_optionsThroughSetAreOverriddenByTheFile(t *testing.T) {
	viaSet := func(c *Config) { c.Set(CfgSpanMaxCallStackDepth, 5) }
	config, err := NewConfig(WithAppName("reloadApp"), viaSet, WithSamplingCounterRate(3))
	require.NoError(t, err)
	require.Equal(t, 5, config.Int(CfgSpanMaxCallStackDepth))
	config.Set(CfgSamplingCounterRate, 7)

	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	body := `
Span:
  MaxCallStackDepth: 12
Sampling:
  CounterRate: 9
`
	require.NoError(t, os.WriteFile(cfgFile, []byte(body), 0o600))
	cfgFileViper := newConfigFile(cfgFile)
	config.reloadConfig(cfgFileViper)

	assert.Equal(t, 12, config.Int(CfgSpanMaxCallStackDepth), "an option given through Set() is a default the file overrides")
	assert.Equal(t, 7, config.Int(CfgSamplingCounterRate), "a Set() after NewConfig keeps its precedence")
}

// The noop agent's Config is a process singleton no watcher reloads, and the
// global agent falls back to it after Shutdown. A plugin that re-binds its
// reload callback to the global agent on every NewAgent/Shutdown cycle grew
// its callback list without bound; the static Config keeps none.
func Test_NoopAgentConfig_keepsNoReloadCallbacks(t *testing.T) {
	config := NoopAgent().Config()
	before := len(config.callback)
	for i := 0; i < 3; i++ {
		config.AddReloadCallback([]string{CfgSpanMaxCallStackDepth}, func() {})
	}
	assert.Equal(t, before, len(config.callback))

	regular, err := NewConfig(WithAppName("cb"))
	require.NoError(t, err)
	n := len(regular.callback)
	regular.AddReloadCallback([]string{CfgSpanMaxCallStackDepth}, func() {})
	assert.Equal(t, n+1, len(regular.callback), "a reloadable Config keeps its callbacks")
}

// ===========================================================================
// Locked invariants - behaviour pinned against the Java and C++ agents. The
// cross-agent rationale and references live in doc/development.md.
// ===========================================================================

// Test_ErrorMarkMaskResolution locks how Span.ErrorMark and
// Span.ErrorMarkExclude resolve into the mask of categories allowed to fail a
// transaction (parseErrorMarkMask). An unset mark enables
// every category, exclude is subtracted from it, and UNKNOWN is added back
// last. Tokens are trimmed and lower-cased; an unrecognised token is warned
// about and ignored.
func Test_ErrorMarkMaskResolution(t *testing.T) {
	tests := []struct {
		name    string
		mark    []string
		exclude []string
		want    ErrorCategory
	}{
		{name: "an unset mark enables every category",
			want: allErrorCategories},
		{name: "a mark is the whole allow list, plus unknown",
			mark: []string{"exception"}, want: ErrorCategoryUnknown | ErrorCategoryException},
		{name: "exclude subtracts from the default everything",
			exclude: []string{"http-status"}, want: allErrorCategories &^ ErrorCategoryHttpStatus},
		{name: "exclude wins over mark",
			mark: []string{"exception", "sql"}, exclude: []string{"sql"},
			want: ErrorCategoryUnknown | ErrorCategoryException},
		{name: "unknown is re-added after the subtraction",
			mark: []string{"exception"}, exclude: []string{"exception"}, want: ErrorCategoryUnknown},
		{name: "unknown is not selectable and cannot be excluded",
			exclude: []string{"unknown"}, want: allErrorCategories},
		{name: "unknown has no spelling of its own in a mark",
			mark: []string{"unknown"}, want: ErrorCategoryUnknown},
		{name: "excluding every named category still leaves unknown",
			exclude: []string{"exception", "http-status", "sql"}, want: ErrorCategoryUnknown},
		{name: "tokens match case-insensitively",
			mark: []string{"EXCEPTION", "Http-Status", "sQl"}, want: allErrorCategories},
		{name: "surrounding space is trimmed",
			mark: []string{"  exception  "}, want: ErrorCategoryUnknown | ErrorCategoryException},
		{name: "one entry may carry a comma separated list",
			mark: []string{"exception,sql"},
			want: ErrorCategoryUnknown | ErrorCategoryException | ErrorCategorySql},
		{name: "an empty token is skipped, leaving an empty rather than a default set",
			mark: []string{""}, want: ErrorCategoryUnknown},
		{name: "an unrecognised token is ignored and the rest still resolves",
			mark: []string{"exception", "nonsense"},
			want: ErrorCategoryUnknown | ErrorCategoryException},
		{name: "an unrecognised token in exclude subtracts nothing",
			exclude: []string{"nonsense"}, want: allErrorCategories},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parseErrorMarkMask(tc.mark, tc.exclude))
		})
	}
}

// Test_ConfigRejectsAnUnsupportedLogLevel locks the same rule
// one layer up, where an operator actually meets it: a value that is not one of
// trace, debug, info, warn or error keeps the level
// already published, or the default on the first publish. logrus itself would
// also take fatal and panic, which would silence warn and error while looking
// like a valid setting, so the agent refuses them too.
func Test_ConfigRejectsAnUnsupportedLogLevel(t *testing.T) {
	t.Cleanup(func() { logger.setLevel("info") })

	tests := []struct {
		set  string
		want string
	}{
		{"trace", "trace"},
		{"debug", "debug"},
		{"info", "info"},
		{"warn", "warn"},
		{"warning", "warning"},
		{"error", "error"},
		{"verbose", "info"},
		{"fatal", "info"},
		{"panic", "info"},
	}
	for _, tc := range tests {
		t.Run(tc.set, func(t *testing.T) {
			c, err := NewConfig(WithAppName("logLevelApp"), WithLogLevel(tc.set))
			assert.NoError(t, err)
			assert.Equal(t, tc.want, c.String(CfgLogLevel),
				"Log.Level = %q resolves to %q", tc.set, tc.want)
		})
	}
}

// A Log.MaxSize below 1 is restored to the 10 MB default.
func Test_LogMaxSizeFloor(t *testing.T) {
	c, err := NewConfig(WithAppName("logRotationApp"), WithLogMaxSize(0))
	assert.NoError(t, err)
	assert.Equal(t, 10, c.Int(CfgLogMaxSize), "Log.MaxSize below 1 is restored to the default")
}

// A process exec'd with no argv at all has an empty os.Args; slicing it from
// 1 panicked NewConfig and the registration goroutine.
func TestEmptyArgsDoNotPanic(t *testing.T) {
	swapForTest(t, &os.Args, nil)

	config := defaultConfig()
	assert.NotPanics(t, func() { assert.Empty(t, config.parseCmdArgs()) })
	assert.NotPanics(t, func() { assert.Empty(t, makeServerMetaData(config).VmArg) })
}
