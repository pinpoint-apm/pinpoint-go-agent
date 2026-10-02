package ppgoredisv9

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cmd(name string) redis.Cmder {
	return redis.NewCmd(context.Background(), name, "key")
}

// The endpoint is what puts the call on the right node of the server map. The
// hook is constructed from the same options the client is, and a caller that
// passes none must not produce an empty endpoint.
func TestNewHook_Endpoint(t *testing.T) {
	for _, tt := range []struct {
		name string
		hook redis.Hook
		want string
	}{
		{"client options", NewHook(&redis.Options{Addr: "redis1:6379"}), "redis1:6379"},
		{"no client options", NewHook(nil), "unknown"},
		{"cluster options", NewClusterHook(&redis.ClusterOptions{Addrs: []string{"redis1:6379", "redis2:6379"}}), "redis1:6379,redis2:6379"},
		{"one cluster address", NewClusterHook(&redis.ClusterOptions{Addrs: []string{"redis1:6379"}}), "redis1:6379"},
		{"no cluster addresses", NewClusterHook(&redis.ClusterOptions{}), ""},
		{"no cluster options", NewClusterHook(nil), "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := pptest.NewRecordingTracer()
			ctx := pinpoint.NewContext(context.Background(), tracer)

			require.NoError(t, tt.hook.ProcessHook(func(context.Context, redis.Cmder) error { return nil })(ctx, cmd("get")))
			assert.Equal(t, tt.want, tracer.Last().EndPoint)
		})
	}
}

// One command is one span event, wrapped around the next hook in the chain.
// The command's error has to reach both the caller and the span; a command
// that succeeded records none, so a later failed one is not mistaken for it.
func TestProcessHook(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	for _, cmdErr := range []error{errors.New("WRONGTYPE"), nil} {
		tracer := pptest.NewRecordingTracer()
		ctx := pinpoint.NewContext(context.Background(), tracer)

		inner := false
		err := h.ProcessHook(func(context.Context, redis.Cmder) error {
			inner = true
			return cmdErr
		})(ctx, cmd("get"))

		require.True(t, inner, "the next hook did not run")
		assert.Equal(t, cmdErr, err, "the command's error must come back unchanged")

		require.Len(t, tracer.Events, 1, "one command must produce exactly one span event")
		e := tracer.Events[0]
		assert.Equal(t, "go-redis/v9.Process()", e.Operation)
		assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.ServiceType)
		assert.Equal(t, "REDIS", e.Destination)
		assert.Equal(t, "redis1:6379", e.EndPoint)
		assert.Equal(t, "get", e.Strings[pinpoint.AnnotationArgs0])
		assert.Equal(t, cmdErr, e.Err)
		assert.True(t, e.Ended, "the span event was left open")
	}
}

// The next hook panicking must not leave the span event open: the deferred
// close is what keeps the surrounding request's event stack balanced.
func TestProcessHook_PanicClosesTheSpanEvent(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	assert.PanicsWithValue(t, "boom", func() {
		_ = NewHook(&redis.Options{Addr: "redis1:6379"}).
			ProcessHook(func(context.Context, redis.Cmder) error { panic("boom") })(ctx, cmd("get"))
	})

	require.Len(t, tracer.Events, 1)
	assert.True(t, tracer.Events[0].Ended, "a panicking command left the span event open")
}

// A pipeline is one round trip, so it is one span event listing every command
// in it.
func TestProcessPipelineHook(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	cmds := []redis.Cmder{cmd("set"), cmd("get"), cmd("del")}
	require.NoError(t, h.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })(ctx, cmds))

	require.Len(t, tracer.Events, 1, "a pipeline is one round trip, so one span event")
	e := tracer.Events[0]
	assert.Equal(t, "go-redis/v9.ProcessPipeline()", e.Operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.ServiceType)
	assert.Equal(t, "set, get, del", e.Strings[pinpoint.AnnotationArgs0])
	assert.NoError(t, e.Err)
	assert.True(t, e.Ended, "the span event was left open")
}

// A failed pipeline records the error on its single event.
func TestProcessPipelineHook_Error(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	want := errors.New("connection reset")
	err := NewHook(&redis.Options{Addr: "redis1:6379"}).
		ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return want })(ctx, []redis.Cmder{cmd("get")})

	assert.ErrorIs(t, err, want)
	require.Len(t, tracer.Events, 1)
	assert.ErrorIs(t, tracer.Events[0].Err, want)
}

func Test_cmdName(t *testing.T) {
	assert.Equal(t, "", cmdName(nil))
	assert.Equal(t, "", cmdName([]redis.Cmder{}))
	assert.Equal(t, "get", cmdName([]redis.Cmder{cmd("get")}))
	assert.Equal(t, "set, get", cmdName([]redis.Cmder{cmd("set"), cmd("get")}))
	assert.Equal(t, "set, get, del", cmdName([]redis.Cmder{cmd("set"), cmd("get"), cmd("del")}))
}

// The hook is registered on the client, so it runs for every command the
// application makes - including those from code that never started a span.
// Recording those would unbalance the span-event stack of whatever ran next on
// that goroutine, so the hook has to step aside and still run the chain.
func TestHooks_IgnoreUnsampledCommands(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})
	ctx := context.Background()
	cmdErr := errors.New("WRONGTYPE")
	inner := 0

	assert.ErrorIs(t, h.ProcessHook(func(context.Context, redis.Cmder) error {
		inner++
		return cmdErr
	})(ctx, cmd("get")), cmdErr)

	assert.ErrorIs(t, h.ProcessPipelineHook(func(context.Context, []redis.Cmder) error {
		inner++
		return cmdErr
	})(ctx, []redis.Cmder{cmd("get")}), cmdErr)

	assert.Equal(t, 2, inner, "the next hook must still run for an untraced command")
}

// Dialing is not traced, but the hook still sits in the chain: it has to pass
// the connection and any dial error straight through.
func TestDialHook_PassesThrough(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})
	want := errors.New("connection refused")

	var gotNetwork, gotAddr string
	conn, err := h.DialHook(func(ctx context.Context, network, addr string) (net.Conn, error) {
		gotNetwork, gotAddr = network, addr
		return nil, want
	})(context.Background(), "tcp", "redis1:6379")

	assert.Nil(t, conn, "DialHook returned a connection along with an error")
	assert.ErrorIs(t, err, want)
	assert.Equal(t, "tcp", gotNetwork)
	assert.Equal(t, "redis1:6379", gotAddr)
}

// A successful dial has to come back to go-redis unchanged, or the client
// never gets its connection.
func TestDialHook_ReturnsTheConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	got, err := NewHook(&redis.Options{Addr: "redis1:6379"}).
		DialHook(func(context.Context, string, string) (net.Conn, error) { return client, nil })(
		context.Background(), "tcp", "redis1:6379")

	require.NoError(t, err)
	assert.Equal(t, client, got)
}

// One hook serves every connection of a shared client, so concurrent commands
// through it must stay race-free. Run under -race.
func TestHooks_ConcurrentCommands(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})
	process := h.ProcessHook(func(context.Context, redis.Cmder) error { return nil })
	pipeline := h.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine carries its own tracer, as each request would.
			tracer := pptest.NewRecordingTracer()
			ctx := pinpoint.NewContext(context.Background(), tracer)
			for j := 0; j < 25; j++ {
				assert.NoError(t, process(ctx, cmd("get")))
				assert.NoError(t, pipeline(ctx, []redis.Cmder{cmd("set"), cmd("get")}))
			}
			assert.Len(t, tracer.Events, 50)
		}()
	}
	wg.Wait()
}
