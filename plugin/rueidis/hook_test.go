package pprueidis

import (
	"context"
	"errors"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHook() *Hook {
	return NewHook(rueidis.ClientOption{InitAddress: []string{"redis1:6379"}})
}

// The command name is built for the annotation only; an untraced call must not
// pay for building it.
func TestNewSpanEventSkipsCommandForUnsampledTracer(t *testing.T) {
	called := false
	tracer, _ := testHook().newSpanEvent(context.Background(), "test", func() string {
		called = true
		return "large command"
	})

	assert.False(t, tracer.IsSampled(), "an unsampled context produced a sampled tracer")
	assert.False(t, called, "the command was built for an unsampled tracer")
}

// The endpoint is what puts the call on the right node of the server map. The
// hook is constructed from the same options the client is, and a caller that
// passes none must not produce an empty endpoint.
func TestNewHook_Endpoint(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts rueidis.ClientOption
		want string
	}{
		{"single address", rueidis.ClientOption{InitAddress: []string{"redis1:6379"}}, "redis1:6379"},
		{"several addresses", rueidis.ClientOption{InitAddress: []string{"redis1:6379", "redis2:6379"}}, "redis1:6379,redis2:6379"},
		{"no address", rueidis.ClientOption{}, "unknown"},
		{"an empty address list", rueidis.ClientOption{InitAddress: []string{}}, "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := pptest.NewRecordingTracer()
			NewHook(tt.opts).newSpanEvent(pinpoint.NewContext(context.Background(), tracer),
				"rueidis.Do()", func() string { return "GET,key" })

			assert.Equal(t, tt.want, tracer.Last().EndPoint)
		})
	}
}

// Every rueidis operation opens its span event through this one function, so
// the service type, destination and command annotation it sets are what the
// whole plugin records.
func TestNewSpanEventRecordsTheCommand(t *testing.T) {
	tracer := pptest.NewRecordingTracer()

	built := 0
	testHook().newSpanEvent(pinpoint.NewContext(context.Background(), tracer),
		"rueidis.DoMulti()", func() string {
			built++
			return "SET,key,value, GET,key"
		})

	assert.Equal(t, 1, built, "the command name must be built exactly once")
	require.Len(t, tracer.Events, 1)
	e := tracer.Events[0]
	assert.Equal(t, "rueidis.DoMulti()", e.Operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.ServiceType)
	assert.Equal(t, "REDIS", e.Destination)
	assert.Equal(t, "redis1:6379", e.EndPoint)
	assert.Equal(t, "SET,key,value, GET,key", e.Strings[pinpoint.AnnotationArgs0])
}

// An empty command name would annotate the span event with an empty string,
// which reads as a command that ran with no name rather than one that could
// not be described.
func TestNewSpanEventSkipsAnEmptyCommandAnnotation(t *testing.T) {
	tracer := pptest.NewRecordingTracer()

	testHook().newSpanEvent(pinpoint.NewContext(context.Background(), tracer),
		"rueidis.Do()", func() string { return "" })

	assert.NotContains(t, tracer.Last().Strings, pinpoint.AnnotationArgs0,
		"an empty command was recorded as an annotation")
}

// A rueidis command can only be built from a live client's own builder, so the
// two name helpers are exercised on the empty batch each of them can be handed:
// a batch with nothing in it must annotate nothing rather than a bare
// separator.
func Test_cmdNames_EmptyBatch(t *testing.T) {
	assert.Equal(t, "", cmdCompletedName(nil))
	assert.Equal(t, "", cmdCompletedName([]rueidis.Completed{}))
	assert.Equal(t, "", cmdCacheableName(nil))
	assert.Equal(t, "", cmdCacheableName([]rueidis.CacheableTTL{}))
}

// Commands() carries the full command including keys and values; only the verb
// may reach the annotation.
func Test_cmdVerb(t *testing.T) {
	assert.Equal(t, "", cmdVerb(nil))
	assert.Equal(t, "", cmdVerb([]string{}))
	assert.Equal(t, "SET", cmdVerb([]string{"SET", "key", "value"}))
}

// A batch fails as a whole on its first failed command; reporting a later one
// would point at the wrong command in the trace.
func Test_multiResultError(t *testing.T) {
	assert.NoError(t, multiResultError(nil), "an empty batch has no failure to report")
	assert.NoError(t, multiResultError([]rueidis.RedisResult{}))

	first := errors.New("WRONGTYPE")
	assert.ErrorIs(t, multiResultError([]rueidis.RedisResult{
		rueidis.NewErrorResult(first),
		rueidis.NewErrorResult(errors.New("second")),
	}), first)

	assert.ErrorIs(t, multiResultError([]rueidis.RedisResult{
		rueidis.NewErrorResult(first),
	}), first)
}

// receiveClient is a rueidis client whose Receive returns err.
type receiveClient struct {
	rueidis.Client
	err error
}

func (c receiveClient) Receive(context.Context, rueidis.Completed, func(rueidis.PubSubMessage)) error {
	return c.err
}

// Receive returns once its ctx is done or the client is closed, which is how a
// subscription normally ends, so neither fails the transaction; a subscribe
// that failed does, and every error still reaches the caller.
func TestHook_ReceiveRecordsOnlyAFailedSubscribe(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	failed := errors.New("subscribe failed")

	for _, tt := range []struct {
		name string
		ctx  context.Context
		err  error
		want error
	}{
		{"ctx done", canceled, context.Canceled, nil},
		{"client closed", context.Background(), rueidis.ErrClosing, nil},
		{"subscribe failed", context.Background(), failed, failed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := pptest.NewRecordingTracer()
			err := testHook().Receive(receiveClient{err: tt.err}, pinpoint.NewContext(tt.ctx, tracer),
				rueidis.Builder{}.Subscribe().Channel("news").Build(), nil)

			assert.ErrorIs(t, err, tt.err, "the error must reach the caller")
			require.Len(t, tracer.Events, 1)
			assert.Equal(t, tt.want, tracer.Events[0].Err)
			assert.True(t, tracer.Events[0].Ended)
		})
	}
}
