package ppsaramaibm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An empty or mistyped broker-address value must fall back to Unknown instead
// of panicking out of ConsumeClaim and killing the consumer.
func Test_newConsumerTracer_EmptyBrokerAddress(t *testing.T) {
	msg := &sarama.ConsumerMessage{Topic: "topic"}

	for _, ctx := range []context.Context{
		NewContext(context.Background(), []string{}),
		context.WithValue(context.Background(), contextKey, "not-a-slice"),
	} {
		var tracer pinpoint.Tracer
		require.NotPanics(t, func() { tracer = newConsumerTracer(ctx, msg) },
			"a broker address the plugin cannot read must not kill the consumer")
		require.NotNil(t, tracer, "no tracer returned")
		tracer.EndSpan()
	}
}

func startAgent(t *testing.T) {
	t.Helper()
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentId("testAgent"))
	require.NoError(t, err)

	agent, err := pinpoint.NewTestAgent(config, t)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)
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

	assert.Equal(t, "txid^1^1", r.Get(pinpoint.HeaderTraceId))
	assert.Equal(t, "", r.Get("absent"))

	// A message with no headers at all is what an untraced producer sends.
	bare := &distributedTracingContextReaderConsumer{&sarama.ConsumerMessage{}}
	assert.Equal(t, "", bare.Get(pinpoint.HeaderTraceId))
}

// The broker is the consumer span's endpoint on the server map. It can come
// from the context the application built, or - failing that - from the host
// the producer stamped on the message; with neither, the span still needs an
// endpoint it can be filed under.
func Test_newConsumerTracer_BrokerAddress(t *testing.T) {
	startAgent(t)

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
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	produced := &sarama.ProducerMessage{Topic: "widgets"}
	newSyncProducerTracer(pinpoint.NewContext(context.Background(), caller), []string{"broker1:9092"}, produced).EndSpanEvent()
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
// context has to carry the tracer and its error has to reach the caller.
func TestConsumeMessageContext(t *testing.T) {
	startAgent(t)

	want := errors.New("handler failed")
	var (
		sampled bool
		gotMsg  *sarama.ConsumerMessage
	)
	msg := &sarama.ConsumerMessage{Topic: "widgets", Partition: 1, Offset: 7}

	err := ConsumeMessageContext(func(ctx context.Context, m *sarama.ConsumerMessage) error {
		sampled = pinpoint.FromContext(ctx).IsSampled()
		gotMsg = m
		return want
	}, NewContext(context.Background(), []string{"broker1:9092"}), msg)

	assert.True(t, sampled, "the handler received an unsampled tracer")
	assert.Same(t, msg, gotMsg, "the handler received a different message")
	assert.ErrorIs(t, err, want, "the handler's error must come back unchanged")
}

// A handler that succeeds must leave the consumer span unfailed.
func TestConsumeMessageContext_SuccessfulHandler(t *testing.T) {
	startAgent(t)

	var span spanFields
	err := ConsumeMessageContext(func(ctx context.Context, m *sarama.ConsumerMessage) error {
		span = readSpan(t, pinpoint.FromContext(ctx))
		return nil
	}, NewContext(context.Background(), []string{"broker1:9092"}), &sarama.ConsumerMessage{Topic: "widgets"})

	require.NoError(t, err)
	assert.Equal(t, "broker1:9092", span.EndPoint)
}

// A panicking handler must not be swallowed by the wrapper.
func TestConsumeMessageContext_PanicPropagates(t *testing.T) {
	startAgent(t)

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

// The deprecated form hands the tracer to the handler on the wrapper rather
// than in a context; it still has to work.
func TestConsumeMessage(t *testing.T) {
	startAgent(t)

	want := errors.New("handler failed")
	msg := &sarama.ConsumerMessage{Topic: "widgets"}

	err := ConsumeMessage(func(m *ConsumerMessage) error {
		assert.Same(t, msg, m.ConsumerMessage, "the handler received a different message")
		require.NotNil(t, m.Tracer())
		assert.True(t, m.Tracer().IsSampled(), "the handler received an unsampled tracer")
		assert.Equal(t, m.Tracer(), m.SpanTracer(), "SpanTracer and Tracer returned different tracers")
		return want
	}, msg)

	assert.ErrorIs(t, err, want, "the handler's error must come back unchanged")
}

// The deprecated partition-consumer wrapper forwards every message from
// sarama's channel with a tracer attached, and closes its own channel when
// sarama's closes.
func TestWrapPartitionConsumer(t *testing.T) {
	startAgent(t)

	stub := &stubPartitionConsumer{messages: make(chan *sarama.ConsumerMessage, 2)}
	stub.messages <- &sarama.ConsumerMessage{Topic: "widgets", Offset: 1}
	stub.messages <- &sarama.ConsumerMessage{Topic: "widgets", Offset: 2}
	close(stub.messages)

	pc := WrapPartitionConsumer(stub)

	var offsets []int64
	for msg := range pc.Messages() {
		assert.True(t, msg.Tracer().IsSampled(), "a forwarded message carries an unsampled tracer")
		offsets = append(offsets, msg.Offset)
		msg.Tracer().EndSpan()
	}

	assert.Equal(t, []int64{1, 2}, offsets,
		"every message must be forwarded, in order, and the channel closed after the last")
}

type stubPartitionConsumer struct {
	sarama.PartitionConsumer
	messages chan *sarama.ConsumerMessage
}

func (s *stubPartitionConsumer) Messages() <-chan *sarama.ConsumerMessage { return s.messages }

func (s *stubPartitionConsumer) Close() error {
	// Raw sarama's Close delivers what is in flight and closes the channel.
	close(s.messages)
	return nil
}

// A caller that stops receiving and just calls Close - sarama's documented
// shutdown - must not leave the forwarder parked on the wrapper's unbuffered
// channel holding a never-ended span.
func TestWrapPartitionConsumer_CloseUnblocksAbandonedForwarder(t *testing.T) {
	startAgent(t)

	stub := &stubPartitionConsumer{messages: make(chan *sarama.ConsumerMessage, 2)}
	stub.messages <- &sarama.ConsumerMessage{Topic: "widgets", Offset: 1}
	stub.messages <- &sarama.ConsumerMessage{Topic: "widgets", Offset: 2}

	pc := WrapPartitionConsumer(stub)

	msg := <-pc.Messages() // take one message, then abandon the channel
	msg.Tracer().EndSpan()

	require.NoError(t, pc.Close())

	// Close only returns once its drain saw the wrapper channel closed, i.e.
	// the forwarder exited; the channel must therefore read as closed here.
	_, ok := <-pc.Messages()
	assert.False(t, ok, "the wrapper channel must be closed after Close")
}

// A nil message must come back as an error, not as a panic that would
// propagate out of ConsumeClaim and kill the consumer-group session.
func TestConsumeMessage_NilMessage(t *testing.T) {
	assert.Error(t, ConsumeMessage(func(*ConsumerMessage) error { return nil }, nil))
	assert.Error(t, ConsumeMessageContext(func(context.Context, *sarama.ConsumerMessage) error { return nil },
		context.Background(), nil))
}
