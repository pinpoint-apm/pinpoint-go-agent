package pinpoint

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestNewConfig(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		WithAppName("TestApp"),
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
			c, _ := NewConfig(tt.args.opts...)
			assert.Equal(t, c.ApplicationName, "TestApp", "ApplicationName")
			assert.Equal(t, c.AgentId, "TestAgent", "AgentId")
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
			assert.Equal(t, c.ApplicationName, "TestApp", "ApplicationName")
			assert.NotNil(t, c.AgentId, "AgentId")
		})
	}
}

func TestNewConfig_ReadConfigFile(t *testing.T) {
	type args struct {
		opts []ConfigOption
	}

	opts := []ConfigOption{
		//WithAppName("TestApp"),
		WithAgentId("TestAgent"),
		WithConfigFile("tmp/pinpoint-config.yaml"),
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
			assert.Equal(t, "MyAppName", c.ApplicationName, "ApplicationName")
			assert.Equal(t, "my.collector.host", c.Collector.Host, "Collector.Host")
			assert.Equal(t, "PERCENT", c.Sampling.Type, "Sampling.Type")
			assert.Equal(t, float32(10), c.Sampling.PercentRate, "Sampling.PercentRate")
		})
	}
}
