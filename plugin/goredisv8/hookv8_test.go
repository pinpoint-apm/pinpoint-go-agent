package ppgoredisv8

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/go-redis/redis/v8"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cmd(name string, err error) redis.Cmder {
	c := redis.NewCmd(context.Background(), name, "key")
	if err != nil {
		c.SetErr(err)
	}
	return c
}

// The endpoint is what puts the call on the right node of the server map. The
// hook is constructed from the same options the client is, and a caller that
// passes none must not produce an empty endpoint.
func TestNewHook_Endpoint(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

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
			_, err := tt.hook.BeforeProcess(ctx, cmd("get", nil))
			require.NoError(t, err)
			require.NoError(t, tt.hook.AfterProcess(ctx, cmd("get", nil)))

			assert.Equal(t, tt.want, tracer.Last().EndPoint)
		})
	}
}

// One command is one span event: opened before the call, closed after it with
// the command name and whatever the server said - no error for a command that
// succeeded, so a later failed one is not mistaken for it.
func TestHook_Process(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	for _, cmdErr := range []error{errors.New("WRONGTYPE"), nil} {
		tracer := pptest.NewRecordingTracer()
		ctx := pinpoint.NewContext(context.Background(), tracer)

		got, err := h.BeforeProcess(ctx, cmd("get", nil))
		require.NoError(t, err)
		assert.Equal(t, ctx, got, "BeforeProcess replaced the context")
		require.NoError(t, h.AfterProcess(ctx, cmd("get", cmdErr)))

		require.Len(t, tracer.Events, 1, "one command must produce exactly one span event")
		e := tracer.Events[0]
		assert.Equal(t, "go-redis/v8.Process()", e.Operation)
		assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.ServiceType)
		assert.Equal(t, "REDIS", e.Destination)
		assert.Equal(t, "redis1:6379", e.EndPoint)
		assert.Equal(t, "get", e.Strings[pinpoint.AnnotationArgs0])
		assert.Equal(t, cmdErr, e.Err, "the command's own error must reach the span event")
		assert.True(t, e.Ended, "the span event was left open")
	}
}

// A pipeline is one round trip, so it is one span event listing every command
// in it, failed by the first command that failed.
func TestHook_ProcessPipeline(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	cmdErr := errors.New("WRONGTYPE")
	cmds := []redis.Cmder{cmd("set", nil), cmd("get", cmdErr), cmd("del", errors.New("second"))}

	got, err := h.BeforeProcessPipeline(ctx, cmds)
	require.NoError(t, err)
	assert.Equal(t, ctx, got, "BeforeProcessPipeline replaced the context")
	require.NoError(t, h.AfterProcessPipeline(ctx, cmds))

	require.Len(t, tracer.Events, 1, "a pipeline is one round trip, so one span event")
	e := tracer.Events[0]
	assert.Equal(t, "go-redis/v8.ProcessPipeline()", e.Operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.ServiceType)
	assert.Equal(t, "set, get, del", e.Strings[pinpoint.AnnotationArgs0])
	assert.ErrorIs(t, e.Err, cmdErr, "the pipeline must be failed by its first failure")
	assert.True(t, e.Ended, "the span event was left open")
}

func Test_cmdName(t *testing.T) {
	assert.Equal(t, "", cmdName(nil))
	assert.Equal(t, "", cmdName([]redis.Cmder{}))
	assert.Equal(t, "get", cmdName([]redis.Cmder{cmd("get", nil)}))
	assert.Equal(t, "set, get", cmdName([]redis.Cmder{cmd("set", nil), cmd("get", nil)}))
	assert.Equal(t, "set, get, del",
		cmdName([]redis.Cmder{cmd("set", nil), cmd("get", nil), cmd("del", nil)}))
}

// A pipeline fails as a whole on its first failed command; reporting a later
// one would point at the wrong command in the trace.
func Test_pipeError(t *testing.T) {
	assert.NoError(t, pipeError(nil))
	assert.NoError(t, pipeError([]redis.Cmder{cmd("set", nil), cmd("get", nil)}))

	first := errors.New("first")
	assert.ErrorIs(t,
		pipeError([]redis.Cmder{cmd("set", nil), cmd("get", first), cmd("del", errors.New("second"))}),
		first)
	assert.ErrorIs(t, pipeError([]redis.Cmder{cmd("set", first)}), first)
}

// The hook is registered on the client, so it runs for every command the
// application makes - including those from code that never started a span.
// Recording those would unbalance the span-event stack of whatever ran next on
// that goroutine.
func TestHook_IgnoresUnsampledCommands(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})
	ctx := context.Background()

	got, err := h.BeforeProcess(ctx, cmd("get", nil))
	require.NoError(t, err)
	assert.Equal(t, ctx, got, "BeforeProcess replaced the context")
	require.NoError(t, h.AfterProcess(ctx, cmd("get", nil)))

	gotPipe, err := h.BeforeProcessPipeline(ctx, []redis.Cmder{cmd("get", nil)})
	require.NoError(t, err)
	assert.Equal(t, ctx, gotPipe, "BeforeProcessPipeline replaced the context")
	require.NoError(t, h.AfterProcessPipeline(ctx, []redis.Cmder{cmd("get", nil)}))
}

// One hook serves every connection of a shared client, so concurrent commands
// through it must stay race-free. Run under -race.
func TestHook_ConcurrentCommands(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine carries its own tracer, as each request would.
			tracer := pptest.NewRecordingTracer()
			ctx := pinpoint.NewContext(context.Background(), tracer)
			for j := 0; j < 25; j++ {
				_, err := h.BeforeProcess(ctx, cmd("get", nil))
				assert.NoError(t, err)
				assert.NoError(t, h.AfterProcess(ctx, cmd("get", nil)))
			}
			assert.Len(t, tracer.Events, 25)
		}()
	}
	wg.Wait()
}
