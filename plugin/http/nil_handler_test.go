package pphttp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A nil handler is refused at registration, as net/http's ServeMux refuses
// it: wrapped into a live HandlerFunc it registered fine and panicked on
// every request instead.
func TestServeMux_NilHandlerPanicsAtRegistration(t *testing.T) {
	mux := NewServeMux()
	assert.PanicsWithValue(t, "http: nil handler", func() { mux.Handle("/x", nil) })
	assert.PanicsWithValue(t, "http: nil handler", func() { mux.HandleFunc("/y", nil) })
	assert.PanicsWithValue(t, "http: nil handler", func() { WrapHandler(nil) })
}
