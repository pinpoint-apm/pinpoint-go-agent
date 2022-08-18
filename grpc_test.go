package pinpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_agentGrpc_sendAgentInfo(t *testing.T) {
	type args struct {
		agent Agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newMockAgent()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent.(*mockAgent)
			agent.setMockAgentGrpc(t)
			_, err := agent.agentGrpc.sendAgentInfo()
			assert.NoError(t, err, "sendAgentInfo")
		})
	}
}

func Test_agentGrpc_sendApiMetadata(t *testing.T) {
	type args struct {
		agent Agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newMockAgent()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent.(*mockAgent)
			agent.setMockAgentGrpc(t)
			err := agent.agentGrpc.sendApiMetadata(asyncApiId, "Asynchronous Invocation", -1, ApiTypeInvocation)
			assert.NoError(t, err, "sendApiMetadata")
		})
	}
}

func Test_agentGrpc_sendSqlMetadata(t *testing.T) {
	type args struct {
		agent Agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newMockAgent()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent.(*mockAgent)
			agent.setMockAgentGrpc(t)
			err := agent.agentGrpc.sendSqlMetadata(1, "SELECT 1")
			assert.NoError(t, err, "sendSqlMetadata")
		})
	}
}

func Test_agentGrpc_sendStringMetadata(t *testing.T) {
	type args struct {
		agent Agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newMockAgent()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent.(*mockAgent)
			agent.setMockAgentGrpc(t)
			err := agent.agentGrpc.sendStringMetadata(1, "string value")
			assert.NoError(t, err, "sendStringMetadata")
		})
	}
}

func Test_pingStream_sendPing(t *testing.T) {
	type args struct {
		agent Agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newMockAgent()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent.(*mockAgent)
			agent.setMockAgentGrpc(t)
			stream := agent.agentGrpc.newPingStreamWithRetry()
			stream.setStreamInvoker(agent.agentGrpc.stream)
			err := stream.sendPing()
			assert.NoError(t, err, "sendPing")
		})
	}
}

func Test_spanStream_sendSpan(t *testing.T) {
	type args struct {
		agent Agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newMockAgent()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent.(*mockAgent)
			agent.setMockSpanGrpc(t)
			stream := agent.spanGrpc.newSpanStreamWithRetry()
			stream.setStreamInvoker(agent.spanGrpc.stream)
			span := defaultSpan()
			span.agent = agent
			span.NewSpanEvent("t1")
			err := stream.sendSpan(span)
			assert.NoError(t, err, "sendSpan")
		})
	}
}

func Test_statStream_sendStat(t *testing.T) {
	type args struct {
		agent Agent
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{newMockAgent()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent.(*mockAgent)
			agent.setMockStatGrpc(t)
			stream := agent.statGrpc.newStatStreamWithRetry()
			stream.setStreamInvoker(agent.statGrpc.stream)

			stats := make([]*inspectorStats, 1)
			stats[0] = getStats()
			err := stream.sendStats(stats)
			assert.NoError(t, err, "sendStats")
		})
	}
}
