package pprueidis

import (
	"context"
	"errors"
	"testing"
	"time"

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

// fakeClient is a rueidis client whose command methods return canned results,
// so the hook can be driven without a server.
type fakeClient struct {
	rueidis.Client
	result  rueidis.RedisResult
	results []rueidis.RedisResult
	calls   int
}

func (c *fakeClient) Do(context.Context, rueidis.Completed) rueidis.RedisResult {
	c.calls++
	return c.result
}

func (c *fakeClient) DoCache(context.Context, rueidis.Cacheable, time.Duration) rueidis.RedisResult {
	c.calls++
	return c.result
}

func (c *fakeClient) DoMulti(context.Context, ...rueidis.Completed) []rueidis.RedisResult {
	c.calls++
	return c.results
}

func (c *fakeClient) DoMultiCache(context.Context, ...rueidis.CacheableTTL) []rueidis.RedisResult {
	c.calls++
	return c.results
}

func (c *fakeClient) DoStream(context.Context, rueidis.Completed) rueidis.RedisResultStream {
	c.calls++
	return rueidis.NewErrorResultStream(errStream)
}

func (c *fakeClient) DoMultiStream(context.Context, ...rueidis.Completed) rueidis.MultiRedisResultStream {
	c.calls++
	return rueidis.NewErrorResultStream(errStream)
}

var errStream = errors.New("stream")

var redisNil = rueidis.Nil

// slot0Key returns a key in cluster slot 0. A command is built here from the
// zero rueidis.Builder, the only one constructible without a live client, and
// that builder panics on a key outside slot 0; the hash tag pins the slot.
func slot0Key(name string) string { return "{key3444}" + name }

// Do and DoCache record only the verb, end their span event, hand the client's
// result back unchanged, and record a failure but not a cache miss.
func TestHook_SingleCommand(t *testing.T) {
	failed := errors.New("WRONGTYPE")
	get := rueidis.Builder{}.Get().Key(slot0Key("secret-key")).Build()
	cached := rueidis.Builder{}.Get().Key(slot0Key("secret-key")).Cache()

	for _, call := range []struct {
		name      string
		operation string
		do        func(*Hook, rueidis.Client, context.Context) rueidis.RedisResult
	}{
		{"Do", "rueidis.Do()", func(h *Hook, c rueidis.Client, ctx context.Context) rueidis.RedisResult {
			return h.Do(c, ctx, get)
		}},
		{"DoCache", "rueidis.DoCache()", func(h *Hook, c rueidis.Client, ctx context.Context) rueidis.RedisResult {
			return h.DoCache(c, ctx, cached, time.Minute)
		}},
	} {
		for _, tt := range []struct {
			name    string
			result  rueidis.RedisResult
			wantErr error
		}{
			{"success", rueidis.RedisResult{}, nil},
			{"failure", rueidis.NewErrorResult(failed), failed},
			{"cache miss", rueidis.NewErrorResult(redisNil), nil},
		} {
			t.Run(call.name+"/"+tt.name, func(t *testing.T) {
				tracer := pptest.NewRecordingTracer()
				client := &fakeClient{result: tt.result}

				resp := call.do(testHook(), client, pinpoint.NewContext(context.Background(), tracer))

				assert.Equal(t, 1, client.calls)
				assert.Equal(t, tt.result.Error(), resp.Error(), "the client's result must reach the caller")
				require.Len(t, tracer.Events, 1)
				e := tracer.Events[0]
				assert.Equal(t, call.operation, e.Operation)
				assert.Equal(t, "GET", e.Strings[pinpoint.AnnotationArgs0], "only the verb may be recorded")
				assert.Equal(t, tt.wantErr, e.Err)
				assert.True(t, e.Ended)
			})
		}
	}
}

// DoMulti and DoMultiCache annotate every verb of the batch and fail on its
// first failed command, skipping misses.
func TestHook_MultiCommand(t *testing.T) {
	failed := errors.New("WRONGTYPE")
	set := rueidis.Builder{}.Set().Key(slot0Key("k")).Value("v").Build()
	get := rueidis.Builder{}.Get().Key(slot0Key("k")).Build()
	cachedGet := rueidis.CT(rueidis.Builder{}.Get().Key(slot0Key("k")).Cache(), time.Minute)
	cachedHget := rueidis.CT(rueidis.Builder{}.Hget().Key(slot0Key("h")).Field("f").Cache(), time.Minute)

	for _, call := range []struct {
		name      string
		operation string
		verbs     string
		do        func(*Hook, rueidis.Client, context.Context) []rueidis.RedisResult
	}{
		{"DoMulti", "rueidis.DoMulti()", pinpoint.AnnotationList(2, func(i int) string { return []string{"SET", "GET"}[i] }),
			func(h *Hook, c rueidis.Client, ctx context.Context) []rueidis.RedisResult {
				return h.DoMulti(c, ctx, set, get)
			}},
		{"DoMultiCache", "rueidis.DoMultiCache()", pinpoint.AnnotationList(2, func(i int) string { return []string{"GET", "HGET"}[i] }),
			func(h *Hook, c rueidis.Client, ctx context.Context) []rueidis.RedisResult {
				return h.DoMultiCache(c, ctx, cachedGet, cachedHget)
			}},
	} {
		for _, tt := range []struct {
			name    string
			results []rueidis.RedisResult
			wantErr error
		}{
			{"success", []rueidis.RedisResult{{}, {}}, nil},
			{"miss then failure", []rueidis.RedisResult{rueidis.NewErrorResult(redisNil), rueidis.NewErrorResult(failed)}, failed},
			{"misses only", []rueidis.RedisResult{rueidis.NewErrorResult(redisNil), rueidis.NewErrorResult(redisNil)}, nil},
		} {
			t.Run(call.name+"/"+tt.name, func(t *testing.T) {
				tracer := pptest.NewRecordingTracer()
				client := &fakeClient{results: tt.results}

				resps := call.do(testHook(), client, pinpoint.NewContext(context.Background(), tracer))

				assert.Equal(t, 1, client.calls)
				assert.Len(t, resps, len(tt.results), "the client's results must reach the caller")
				require.Len(t, tracer.Events, 1)
				e := tracer.Events[0]
				assert.Equal(t, call.operation, e.Operation)
				assert.Equal(t, call.verbs, e.Strings[pinpoint.AnnotationArgs0])
				assert.Equal(t, tt.wantErr, e.Err)
				assert.True(t, e.Ended)
			})
		}
	}
}

// An untraced call still reaches the client and records nothing.
func TestHook_UnsampledCallsPassThrough(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{result: rueidis.NewErrorResult(errors.New("x")), results: []rueidis.RedisResult{{}}}
	h := testHook()
	get := rueidis.Builder{}.Get().Key(slot0Key("k")).Build()
	cached := rueidis.Builder{}.Get().Key(slot0Key("k")).Cache()

	assert.Error(t, h.Do(client, ctx, get).Error())
	assert.Error(t, h.DoCache(client, ctx, cached, time.Minute).Error())
	assert.Len(t, h.DoMulti(client, ctx, get), 1)
	assert.Len(t, h.DoMultiCache(client, ctx, rueidis.CT(cached, time.Minute)), 1)
	assert.NoError(t, h.Receive(receiveClient{}, ctx, rueidis.Builder{}.Subscribe().Channel("c").Build(), nil))
	assert.Equal(t, 4, client.calls)
}

// The stream calls are pass-through: they reach the client and open no span
// event, since the response is read after the call returns.
func TestHook_StreamsArePassThrough(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	client := &fakeClient{}
	get := rueidis.Builder{}.Get().Key(slot0Key("k")).Build()

	s := testHook().DoStream(client, ctx, get)
	assert.ErrorIs(t, s.Error(), errStream)
	m := testHook().DoMultiStream(client, ctx, get, get)
	assert.ErrorIs(t, m.Error(), errStream)

	assert.Equal(t, 2, client.calls)
	assert.Empty(t, tracer.Events, "a stream call must record nothing")
}
