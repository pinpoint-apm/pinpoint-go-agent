package pppgxv5

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
)

// pgx's Conn.Config() deep-copies the ConnConfig, so an unsampled callback
// must return before touching it. A zero Conn has no config to copy: reaching
// Config() here would panic, which is what proves the gate comes first.
func Test_connSpanEventSkipsTheConfigCopyWhenUnsampled(t *testing.T) {
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				tracer := connSpanEvent(tt.ctx, &pgx.Conn{}, "pgx.Query")
				assert.False(t, tracer.IsSampled())
			})
		})
	}
}
