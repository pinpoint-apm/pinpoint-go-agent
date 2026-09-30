package ppconfluentkafka

import (
	"testing"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The injected headers land in a slice of the message's own: two messages
// built from one slice with spare capacity must not overwrite each other's.
func Test_headerWriter_DoesNotWriteIntoSharedCapacity(t *testing.T) {
	base := make([]kafka.Header, 0, 8)
	m1 := &kafka.Message{Headers: base}
	m2 := &kafka.Message{Headers: base}

	(&headerWriter{msg: m1}).Set(pinpoint.HeaderTraceId, "one")
	(&headerWriter{msg: m2}).Set(pinpoint.HeaderTraceId, "two")

	require.Len(t, m1.Headers, 1)
	assert.Equal(t, "one", string(m1.Headers[0].Value), "the second message's header overwrote the first's")
	assert.Empty(t, base, "the caller's slice is untouched")
}
