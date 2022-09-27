package pinpoint

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func Test_agent_NewAgentError(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := NewAgent(nil)
			assert.Equal(t, NoopAgent(), a, "noop agent")
			assert.Error(t, err, "error")
		})
	}
}

func Test_agent_NewAgent(t *testing.T) {
	type args struct {
		config *Config
	}

	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true

	tests := []struct {
		name string
		args args
	}{
		{"1", args{c}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.args.config
			a, err := NewAgent(c)
			agent := a.(*agent)
			assert.NoError(t, err, "NewAgent")
			assert.Equal(t, "test", agent.appName, "ApplicationName")
			assert.Equal(t, "testagent", agent.agentID, "AgentID")
			assert.Equal(t, int32(ServiceTypeGoApp), agent.appType, "ApplicationType")
			assert.Greater(t, agent.startTime, int64(0), "StartTime")
			assert.Equal(t, globalAgent, a, "global agent")

			agent.startTime = 12345
			agent.enable = true
			assert.Equal(t, "testagent^12345^1", agent.generateTransactionId().String(), "generateTransactionId")

			a.Shutdown()
			assert.Equal(t, NoopAgent(), globalAgent, "global agent")
			assert.Equal(t, false, a.Enable(), "Enable")

			span := agent.NewSpanTracer("test", "/")
			assert.Equal(t, NoopTracer(), span, "NewSpanTracer")
		})
	}
}

func Test_agent_GlobalAgent(t *testing.T) {
	type args struct {
		config *Config
	}

	opts := []ConfigOption{
		WithAppName("testGlobal"),
		WithAgentId("testGlobalAgent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable = true
	defer a.Shutdown()

	tests := []struct {
		name string
		args args
	}{
		{"1", args{c}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, globalAgent, a, "global agent")
			assert.NotEqual(t, globalAgent, NoopAgent(), "global agent")

			a, err := NewAgent(c)
			assert.Error(t, err, "NewAgent")
			assert.Equal(t, globalAgent, a, "global agent")
		})
	}
}

func Test_agent_NewSpanTracer(t *testing.T) {
	type args struct {
		agent Agent
	}

	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable = true
	defer a.Shutdown()

	tests := []struct {
		name string
		args args
	}{
		{"1", args{agent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			span := agent.NewSpanTracer("test", "/")

			txid := span.TransactionId()
			assert.Equal(t, "testagent", txid.AgentId, "AgentId")
			assert.Greater(t, txid.StartTime, int64(0), "StartTime")
			assert.Greater(t, txid.Sequence, int64(0), "Sequence")

			spanid := span.SpanId()
			assert.NotEqual(t, int64(0), spanid, "spanId")
		})
	}
}

func Test_agent_NewSpanTracerWithReader(t *testing.T) {
	type args struct {
		agent  Agent
		reader DistributedTracingContextReader
	}

	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	c.offGrpc = true
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.enable = true
	defer a.Shutdown()

	m := map[string]string{
		HttpTraceId:      "t123456^12345^1",
		HttpSpanId:       "67890",
		HttpParentSpanId: "123",
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{agent, &DistributedTracingContextMap{m}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			span := agent.NewSpanTracerWithReader("test", "/", tt.args.reader)

			txId := span.TransactionId()
			assert.Equal(t, "t123456", txId.AgentId, "AgentId")
			assert.Equal(t, int64(12345), txId.StartTime, "StartTime")
			assert.Equal(t, int64(1), txId.Sequence, "Sequence")
			assert.Equal(t, int64(67890), span.SpanId(), "SpanId")
		})
	}
}
