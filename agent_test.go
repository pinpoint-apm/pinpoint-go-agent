package pinpoint

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func Test_agent_NewSpanTracer(t *testing.T) {
	type args struct {
		agent Agent
	}

	opts := []ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
	}
	c, _ := NewConfig(opts...)
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.config.OffGrpc = true
	agent.enable = true

	tests := []struct {
		name string
		args args
	}{
		{"1", args{agent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.args.agent
			span := agent.NewSpanTracer("test")

			txid := span.TransactionId()
			assert.Equal(t, txid.AgentId, "testagent", "AgentId")
			assert.Greater(t, txid.StartTime, int64(0), "StartTime")
			assert.Greater(t, txid.Sequence, int64(0), "Sequence")

			spanid := span.SpanId()
			assert.NotEqual(t, spanid, int64(0), "spanId")
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
	a, _ := NewAgent(c)
	agent := a.(*agent)
	agent.config.OffGrpc = true
	agent.enable = true

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
			span := agent.NewSpanTracerWithReader("test", tt.args.reader)

			txid := span.TransactionId()
			assert.Equal(t, txid.AgentId, "t123456", "AgentId")
			assert.Equal(t, txid.StartTime, int64(12345), "StartTime")
			assert.Equal(t, txid.Sequence, int64(1), "Sequence")

			spanid := span.SpanId()
			assert.Equal(t, spanid, int64(67890), "spanId")
		})
	}
}
