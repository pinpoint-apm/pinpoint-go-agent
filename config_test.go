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
			assert.Equal(t, c.String(cfgAppName), "TestApp", "ApplicationName")
			assert.Equal(t, c.String(cfgAgentID), "TestAgent", "AgentId")
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
			assert.Equal(t, c.String(cfgAppName), "TestApp", "ApplicationName")
			assert.NotNil(t, c.String(cfgAgentID), "AgentId")
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
			assert.Equal(t, "MyAppName", c.String(cfgAppName), "ApplicationName")
			assert.Equal(t, "my.collector.host", c.String(cfgCollectorHost), "Collector.Host")
			assert.Equal(t, "PERCENT", c.String(cfgSamplingType), "Sampling.Type")
			assert.Equal(t, float64(10), c.Float(cfgSamplingPercentRate), "Sampling.PercentRate")
		})
	}
}
