package pinpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gRPC sizes and keepalive times fall back to the default when out of
// range, like the queue sizes: a 0 message size fails every send.
func TestConfig_GrpcSizesOutOfRangeRecoverTheDefault(t *testing.T) {
	config, err := NewConfig(WithAppName("grpc-bounds"),
		WithCollectorGrpcMaxSendMessageSize(0),
		WithCollectorGrpcMaxReceiveMessageSize(-1),
		WithCollectorGrpcKeepAliveTime(0),
		WithCollectorGrpcKeepAliveTimeout(-1),
		WithCollectorGrpcFlowControlWindow(0),
		WithCollectorGrpcWriteBufferSize(-1),
		WithCollectorGrpcMaxHeaderListSize(0))
	require.NoError(t, err)

	assert.Equal(t, grpcMaxMessageSize, config.Int(CfgCollectorGrpcMaxSendMessageSize))
	assert.Equal(t, grpcMaxMessageSize, config.Int(CfgCollectorGrpcMaxReceiveMessageSize))
	assert.Equal(t, grpcKeepAliveTime, config.Int(CfgCollectorGrpcKeepAliveTime))
	assert.Equal(t, grpcKeepAliveTimeout, config.Int(CfgCollectorGrpcKeepAliveTimeout))
	assert.Equal(t, grpcFlowControlWindow, config.Int(CfgCollectorGrpcFlowControlWindow))
	assert.Equal(t, grpcWriteBufferSize, config.Int(CfgCollectorGrpcWriteBufferSize))
	assert.Equal(t, grpcMaxHeaderListSize, config.Int(CfgCollectorGrpcMaxHeaderListSize))
}
