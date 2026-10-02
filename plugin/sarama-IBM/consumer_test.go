package ppsaramaibm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An empty or mistyped broker-address value must fall back to Unknown instead
// of panicking out of ConsumeClaim and killing the consumer.
func Test_newConsumerTracer_EmptyBrokerAddress(t *testing.T) {
	pptest.StartAgent(t)
	msg := &sarama.ConsumerMessage{Topic: "topic"}

	for _, ctx := range []context.Context{
		NewContext(context.Background(), []string{}),
		context.WithValue(context.Background(), contextKey, "not-a-slice"),
		nil, // a message with nothing attached
	} {
		var tracer pinpoint.Tracer
		require.NotPanics(t, func() { tracer = newConsumerTracer(ctx, msg) },
			"a broker address the plugin cannot read must not kill the consumer")
		require.NotNil(t, tracer, "no tracer returned")
		tracer.EndSpan()
	}
}

// spanFields reads back what a span recorder was given. A real tracer's
// recorders are write-only, and the span must be read before it is ended.
type spanFields struct {
	RpcName    string
	EndPoint   string
	RemoteAddr string
}

func readSpan(t *testing.T, tracer pinpoint.Tracer) spanFields {
	t.Helper()
	var f spanFields
	require.NoError(t, json.Unmarshal(tracer.JsonString(), &f))
	return f
}

// The RPC name is the consumer span's title on the Pinpoint screen, and it is
// what distinguishes one partition's consumption from another's.
func Test_makeRpcName(t *testing.T) {
	assert.Equal(t, "kafka://topic=widgets?partition=3&offset=42",
		makeRpcName(&sarama.ConsumerMessage{Topic: "widgets", Partition: 3, Offset: 42}))

	// The first message of a fresh partition is offset 0, not an absent one.
	assert.Equal(t, "kafka://topic=widgets?partition=0&offset=0",
		makeRpcName(&sarama.ConsumerMessage{Topic: "widgets"}))

	// A topic name is whatever Kafka allowed; it goes in verbatim.
	assert.Equal(t, "kafka://topic=my.topic-1_x?partition=0&offset=0",
		makeRpcName(&sarama.ConsumerMessage{Topic: "my.topic-1_x"}))
}

// Kafka record headers come off the wire as a slice that can hold nil entries,
// so the reader has to skip those instead of dereferencing them.
func Test_distributedTracingContextReaderConsumer(t *testing.T) {
	r := &distributedTracingContextReaderConsumer{&sarama.ConsumerMessage{
		Headers: []*sarama.RecordHeader{
			nil,
			{Key: []byte(pinpoint.HeaderTraceId), Value: []byte("txid^1^1")},
			nil,
		},
	}}

	v, ok := r.Get(pinpoint.HeaderTraceId)
	assert.True(t, ok)
	assert.Equal(t, "txid^1^1", v)
	_, ok = r.Get("absent")
	assert.False(t, ok)

	// A message with no headers at all is what an untraced producer sends.
	bare := &distributedTracingContextReaderConsumer{&sarama.ConsumerMessage{}}
	_, ok = bare.Get(pinpoint.HeaderTraceId)
	assert.False(t, ok, "a message with no headers carries nothing")

	// A header carried with an empty value is present: the trace continues
	// through a producer or proxy that blanked it instead of dropping it.
	blank := &distributedTracingContextReaderConsumer{&sarama.ConsumerMessage{
		Headers: []*sarama.RecordHeader{{Key: []byte(pinpoint.HeaderSpanId)}},
	}}
	v, ok = blank.Get(pinpoint.HeaderSpanId)
	assert.True(t, ok, "a header with an empty value is present")
	assert.Equal(t, "", v)
}

// The broker is the consumer span's endpoint on the server map. It can come
// from the context the application built, or - failing that - from the host
// the producer stamped on the message; with neither, the span still needs an
// endpoint it can be filed under.
func Test_newConsumerTracer_BrokerAddress(t *testing.T) {
	pptest.StartAgent(t)

	for _, tt := range []struct {
		name string
		ctx  context.Context
		msg  *sarama.ConsumerMessage
		want string
	}{
		{
			name: "addresses from the context",
			ctx:  NewContext(context.Background(), []string{"broker1:9092", "broker2:9092"}),
			msg:  &sarama.ConsumerMessage{Topic: "widgets"},
			want: "broker1:9092",
		},
		{
			name: "host header from the producer",
			ctx:  context.Background(),
			msg: &sarama.ConsumerMessage{Topic: "widgets", Headers: []*sarama.RecordHeader{
				{Key: []byte(pinpoint.HeaderHost), Value: []byte("broker9:9092")},
			}},
			want: "broker9:9092",
		},
		{
			name: "neither",
			ctx:  context.Background(),
			msg:  &sarama.ConsumerMessage{Topic: "widgets"},
			want: "Unknown",
		},
		{
			// An empty slice is still a value, so the host header is not
			// consulted - the fallback has to hold.
			name: "empty addresses in the context",
			ctx:  NewContext(context.Background(), []string{}),
			msg:  &sarama.ConsumerMessage{Topic: "widgets"},
			want: "Unknown",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newConsumerTracer(tt.ctx, tt.msg)
			got := readSpan(t, tracer)
			tracer.EndSpan()

			assert.Equal(t, tt.want, got.EndPoint)
			assert.Equal(t, tt.want, got.RemoteAddr)
			assert.Equal(t, makeRpcName(tt.msg), got.RpcName,
				"the span is named after the topic, partition and offset")
		})
	}
}

// The point of the tracing headers is that the consumer's span continues the
// producer's transaction rather than starting a new one. This is the whole
// round trip: the producer injects, the consumer extracts.
func TestProducerToConsumerContinuesTheTransaction(t *testing.T) {
	pptest.StartAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	produced := &sarama.ProducerMessage{Topic: "widgets"}
	newSyncProducerTracer(pinpoint.NewContext(context.Background(), caller), &syncProducer{addrs: []string{"broker1:9092"}}, produced).EndSpanEvent()
	callerTxId := caller.TransactionId().String()
	caller.EndSpan()

	consumed := &sarama.ConsumerMessage{Topic: "widgets"}
	for _, h := range produced.Headers {
		consumed.Headers = append(consumed.Headers, &sarama.RecordHeader{Key: h.Key, Value: h.Value})
	}

	tracer := newConsumerTracer(context.Background(), consumed)
	defer tracer.EndSpan()

	assert.Equal(t, callerTxId, tracer.TransactionId().String(),
		"the consumer span must continue the producer's transaction")
}

// ConsumeMessageContext wraps the application's handler, so the handler's
// context has to carry the tracer - built from the brokers in the caller's
// context - and its error has to reach the caller.
func TestConsumeMessageContext(t *testing.T) {
	pptest.StartAgent(t)

	want := errors.New("handler failed")
	var (
		sampled bool
		span    spanFields
		gotMsg  *sarama.ConsumerMessage
	)
	msg := &sarama.ConsumerMessage{Topic: "widgets", Partition: 1, Offset: 7}

	err := ConsumeMessageContext(func(ctx context.Context, m *sarama.ConsumerMessage) error {
		sampled = pinpoint.FromContext(ctx).IsSampled()
		span = readSpan(t, pinpoint.FromContext(ctx))
		gotMsg = m
		return want
	}, NewContext(context.Background(), []string{"broker1:9092"}), msg)

	assert.True(t, sampled, "the handler received an unsampled tracer")
	assert.Equal(t, "broker1:9092", span.EndPoint)
	assert.Same(t, msg, gotMsg, "the handler received a different message")
	assert.ErrorIs(t, err, want, "the handler's error must come back unchanged")
}

// A panicking handler must not be swallowed by the wrapper.
func TestConsumeMessageContext_PanicPropagates(t *testing.T) {
	pptest.StartAgent(t)

	assert.PanicsWithValue(t, "boom", func() {
		_ = ConsumeMessageContext(func(context.Context, *sarama.ConsumerMessage) error { panic("boom") },
			context.Background(), &sarama.ConsumerMessage{Topic: "widgets"})
	})
}

// NewContext is how an application tells the plugin which brokers it is
// consuming from; a nil context must not take the consumer down.
func TestNewContext(t *testing.T) {
	ctx := NewContext(nil, []string{"broker1:9092"})
	require.NotNil(t, ctx)
	assert.Equal(t, []string{"broker1:9092"}, ctx.Value(contextKey))

	nested := NewContext(ctx, []string{"broker2:9092"})
	assert.Equal(t, []string{"broker2:9092"}, nested.Value(contextKey),
		"the innermost NewContext wins")
}

// The private key type prevents external context values from shadowing broker
// addresses.
func TestNewContext_ForeignStringKeyDoesNotShadowTheAddresses(t *testing.T) {
	ctx := NewContext(context.Background(), []string{"broker1:9092"})
	ctx = context.WithValue(ctx, "ppsaramaibm.broker.address", "not a slice") //nolint:staticcheck // the point of the test

	assert.Equal(t, []string{"broker1:9092"}, ctx.Value(contextKey))
}

// A nil message must come back as an error, not as a panic that would
// propagate out of ConsumeClaim and kill the consumer-group session.
func TestConsumeMessageContext_NilMessage(t *testing.T) {
	assert.Error(t, ConsumeMessageContext(func(context.Context, *sarama.ConsumerMessage) error { return nil },
		context.Background(), nil))
}

// A nil context is a message with nothing attached, not a reason to panic.
func TestConsumeMessageContext_NilContext(t *testing.T) {
	pptest.StartAgent(t)
	require.NotPanics(t, func() {
		_ = ConsumeMessageContext(func(context.Context, *sarama.ConsumerMessage) error { return nil },
			nil, &sarama.ConsumerMessage{Topic: "topic"})
	})
}
