package pinpoint

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestDefaultSpan(t *testing.T) {
	span := defaultSpan()

	assert.Equal(t, span.parentSpanId, int64(-1), "parentSpanId")
	assert.Equal(t, span.parentAppType, -1, "parentAppType")
	assert.Equal(t, span.eventDepth, int32(1), "eventDepth")
	assert.Equal(t, span.sampled, true, "sampled")
	assert.Equal(t, span.serviceType, int32(ServiceTypeGoApp), "serviceType")
	assert.NotNil(t, span.stack, "stack")
}
