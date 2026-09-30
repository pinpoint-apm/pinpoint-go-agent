package ppconfluentkafka

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A nil producer stays nil, as the sarama wrappers keep it: a wrapper around
// a nil producer nil-derefs inside the bindings on its first call.
func TestWrapProducer_NilStaysNil(t *testing.T) {
	assert.Nil(t, WrapProducer(nil, nil))
}
