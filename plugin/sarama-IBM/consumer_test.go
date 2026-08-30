package ppsaramaibm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// An empty or mistyped broker-address value must fall back to Unknown instead
// of panicking out of ConsumeClaim and killing the consumer.
func Test_newConsumerTracer_EmptyBrokerAddress(t *testing.T) {
	msg := &sarama.ConsumerMessage{Topic: "topic"}

	for _, ctx := range []context.Context{
		NewContext(context.Background(), []string{}),
		context.WithValue(context.Background(), contextKey, "not-a-slice"),
	} {
		tracer := newConsumerTracer(ctx, msg)
		if tracer == nil {
			t.Fatal("no tracer returned")
		}
		tracer.EndSpan()
	}
}

func startAgent(t *testing.T) {
	t.Helper()
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentId("testAgent"))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := pinpoint.NewTestAgent(config, t)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := json.Unmarshal(tracer.JsonString(), &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// The RPC name is the consumer span's title on the Pinpoint screen, and it is
// what distinguishes one partition's consumption from another's.
func Test_makeRpcName(t *testing.T) {
	msg := &sarama.ConsumerMessage{Topic: "widgets", Partition: 3, Offset: 42}
	if got, want := makeRpcName(msg), "kafka://topic=widgets?partition=3&offset=42"; got != want {
		t.Errorf("makeRpcName() = %q, want %q", got, want)
	}

	// The first message of a fresh partition is offset 0, not an absent one.
	msg = &sarama.ConsumerMessage{Topic: "widgets"}
	if got, want := makeRpcName(msg), "kafka://topic=widgets?partition=0&offset=0"; got != want {
		t.Errorf("makeRpcName() = %q, want %q", got, want)
	}
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

	if got := r.Get(pinpoint.HeaderTraceId); got != "txid^1^1" {
		t.Errorf("Get(%s) = %q, want %q", pinpoint.HeaderTraceId, got, "txid^1^1")
	}
	if got := r.Get("absent"); got != "" {
		t.Errorf("Get(absent) = %q, want empty", got)
	}
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

			if got.EndPoint != tt.want {
				t.Errorf("endpoint = %q, want %q", got.EndPoint, tt.want)
			}
			if got.RemoteAddr != tt.want {
				t.Errorf("remote address = %q, want %q", got.RemoteAddr, tt.want)
			}
			if want := makeRpcName(tt.msg); got.RpcName != want {
				t.Errorf("rpc name = %q, want %q", got.RpcName, want)
			}
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

	if got := tracer.TransactionId().String(); got != callerTxId {
		t.Errorf("consumer transaction id = %q, want the producer's %q", got, callerTxId)
	}
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

	if !sampled {
		t.Error("the handler received an unsampled tracer")
	}
	if gotMsg != msg {
		t.Error("the handler received a different message")
	}
	if !errors.Is(err, want) {
		t.Errorf("ConsumeMessageContext() = %v, want %v", err, want)
	}
}

// The deprecated form hands the tracer to the handler on the wrapper rather
// than in a context; it still has to work.
func TestConsumeMessage(t *testing.T) {
	startAgent(t)

	want := errors.New("handler failed")
	msg := &sarama.ConsumerMessage{Topic: "widgets"}

	err := ConsumeMessage(func(m *ConsumerMessage) error {
		if m.ConsumerMessage != msg {
			t.Error("the handler received a different message")
		}
		if m.Tracer() == nil || !m.Tracer().IsSampled() {
			t.Error("the handler received an unsampled tracer")
		}
		if m.SpanTracer() != m.Tracer() {
			t.Error("SpanTracer and Tracer returned different tracers")
		}
		return want
	}, msg)

	if !errors.Is(err, want) {
		t.Errorf("ConsumeMessage() = %v, want %v", err, want)
	}
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
		if !msg.Tracer().IsSampled() {
			t.Error("a forwarded message carries an unsampled tracer")
		}
		offsets = append(offsets, msg.Offset)
		msg.Tracer().EndSpan()
	}

	if len(offsets) != 2 || offsets[0] != 1 || offsets[1] != 2 {
		t.Errorf("forwarded offsets = %v, want [1 2]", offsets)
	}
}

type stubPartitionConsumer struct {
	sarama.PartitionConsumer
	messages chan *sarama.ConsumerMessage
}

func (s *stubPartitionConsumer) Messages() <-chan *sarama.ConsumerMessage { return s.messages }
