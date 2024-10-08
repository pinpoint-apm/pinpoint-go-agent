package pinpoint

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func Test_agentGrpc_sendAgentInfo(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpc(agent, t)
			b := agent.agentGrpc.registerAgentWithRetry()
			assert.Equal(t, true, b, "sendAgentInfo")
		})
	}
}

func Test_agentGrpc_sendApiMetadata(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpc(agent, t)
			b := agent.agentGrpc.sendApiMetadataWithRetry(asyncApiId, "Asynchronous Invocation", -1, apiTypeInvocation)
			assert.Equal(t, true, b, "sendApiMetadata")
		})
	}
}

func Test_agentGrpc_sendSqlMetadata(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpc(agent, t)
			b := agent.agentGrpc.sendSqlMetadataWithRetry(1, "SELECT 1")
			assert.Equal(t, true, b, "sendSqlMetadata")
		})
	}
}

func Test_agentGrpc_sendStringMetadata(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpc(agent, t)
			b := agent.agentGrpc.sendStringMetadataWithRetry(1, "string value")
			assert.Equal(t, true, b, "sendStringMetadata")
		})
	}
}

func Test_pingStream_sendPing(t *testing.T) {
	type args struct {
		agent *agent
	}
	opts := []ConfigOption{
		WithAppName("TestApp"),
	}
	cfg, _ := NewConfig(opts...)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(cfg)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.agentGrpc = newMockAgentGrpcPing(agent, t)
			stream := agent.agentGrpc.newPingStreamWithRetry()
			err := stream.sendPing()
			assert.NoError(t, err, "sendPing")
		})
	}
}

func Test_spanStream_sendSpan(t *testing.T) {
	type args struct {
		agent *agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(defaultConfig())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.spanGrpc = newMockSpanGrpc(agent, t)
			stream := agent.spanGrpc.newSpanStreamWithRetry()

			span := defaultSpan()
			span.agent = agent
			span.NewSpanEvent("t1")
			err := stream.sendSpan(span.newEventChunk(true))
			assert.NoError(t, err, "sendSpan")
		})
	}
}

func Test_statStream_sendStat(t *testing.T) {
	type args struct {
		agent *agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(defaultConfig())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.statGrpc = newMockStatGrpc(agent, t)
			stream := agent.statGrpc.newStatStreamWithRetry()

			stats := make([]*inspectorStats, 1)
			stats[0] = getStats()
			msg := makePAgentStatBatch(stats)
			err := stream.sendStats(msg)
			assert.NoError(t, err, "sendStats")
		})
	}
}

func Test_statStream_sendStatRetry(t *testing.T) {
	type args struct {
		agent *agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newTestAgent(defaultConfig())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			agent.statGrpc = newRetryMockStatGrpc(agent, t)
			stream := agent.statGrpc.newStatStreamWithRetry()

			stats := make([]*inspectorStats, 1)
			stats[0] = getStats()
			msg := makePAgentStatBatch(stats)
			err := stream.sendStats(msg)
			assert.NoError(t, err, "sendStats")
		})
	}
}
