package pinpoint

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// slabTestChunk builds a final span chunk whose every field is derived from
// seed, so a message that mixes data from two sends is detectable.
func slabTestChunk(a *agent, seed int, nEvents int) *spanChunk {
	s := newSampledSpan(a, fmt.Sprintf("op-%d", seed), fmt.Sprintf("/rpc/%d", seed))
	s.spanId = int64(seed)
	s.endPoint = fmt.Sprintf("ep-%d", seed)
	s.annotations.AppendString(AnnotationApi, fmt.Sprintf("v-%d", seed))

	for i := 0; i < nEvents; i++ {
		se := newSpanEvent(s, fmt.Sprintf("event-%d", seed))
		se.annotations.AppendStringString(AnnotationApi, fmt.Sprintf("e-%d", seed), fmt.Sprintf("e2-%d", seed))
		se.endElapsed = 1
		s.eventSequence.Add(1)
		s.spanEvents = append(s.spanEvents, se)
	}

	chunk := s.newEventChunk(true)
	chunk.optimizeSpanEvents()
	return chunk
}

// verifySeedMessage checks that every seed-derived field of one PSpan message
// carries the same seed.
func verifySeedMessage(msg *pb.PSpanMessage, seed int, nEvents int) error {
	span := msg.GetSpan()
	if span == nil {
		return fmt.Errorf("seed %d: message is not a PSpan", seed)
	}
	if span.GetSpanId() != int64(seed) {
		return fmt.Errorf("seed %d: spanId %d", seed, span.GetSpanId())
	}
	if want := fmt.Sprintf("/rpc/%d", seed); span.GetAcceptEvent().GetRpc() != want {
		return fmt.Errorf("seed %d: rpc %q", seed, span.GetAcceptEvent().GetRpc())
	}
	if want := fmt.Sprintf("ep-%d", seed); span.GetAcceptEvent().GetEndPoint() != want {
		return fmt.Errorf("seed %d: endPoint %q", seed, span.GetAcceptEvent().GetEndPoint())
	}
	if got := span.GetAnnotation()[0].GetValue().GetStringValue(); got != fmt.Sprintf("v-%d", seed) {
		return fmt.Errorf("seed %d: span annotation %q", seed, got)
	}
	if len(span.GetSpanEvent()) != nEvents {
		return fmt.Errorf("seed %d: %d events", seed, len(span.GetSpanEvent()))
	}
	for _, ev := range span.GetSpanEvent() {
		ss := ev.GetAnnotation()[0].GetValue().GetStringStringValue()
		if ss.GetStringValue1().GetValue() != fmt.Sprintf("e-%d", seed) ||
			ss.GetStringValue2().GetValue() != fmt.Sprintf("e2-%d", seed) {
			return fmt.Errorf("seed %d: event annotation %q/%q",
				seed, ss.GetStringValue1().GetValue(), ss.GetStringValue2().GetValue())
		}
	}
	return nil
}

// A reused builder must produce the same message a fresh one does — a reset
// that leaks state between sends would show up as a diff.
func Test_spanMessageBuilder_reuseMatchesFresh(t *testing.T) {
	a := newTestAgent(defaultConfig())
	chunk := slabTestChunk(a, 7, 3)

	fresh := (&spanMessageBuilder{}).makePSpan(chunk)

	reused := &spanMessageBuilder{}
	for seed := 0; seed < 5; seed++ {
		other := slabTestChunk(a, 100+seed, 5)
		reused.makePSpan(other)
		reused.reset()
	}
	got := reused.makePSpan(chunk)

	assert.True(t, proto.Equal(fresh, got), "reused builder diverged:\nfresh: %s\ngot:   %s", fresh, got)
	assert.NoError(t, verifySeedMessage(got, 7, 3))
}

// Error info, next event, and the async-chunk shape ride the less-traveled
// slab paths; make sure they materialize with the right content.
func Test_spanMessageBuilder_errorNextEventAndAsyncChunk(t *testing.T) {
	a := newTestAgent(defaultConfig())

	s := newSampledSpan(a, "op", "/rpc")
	s.errorString = "span failed"
	s.errorFuncId = 11
	se := newSpanEvent(s, "client-call")
	se.errorString = "event failed"
	se.errorFuncId = 22
	se.destinationId = "db-1"
	se.nextSpanId = 999
	se.endPoint = "db-host:3306"
	s.spanEvents = append(s.spanEvents, se)
	chunk := s.newEventChunk(true)
	chunk.optimizeSpanEvents()

	builder := acquireSpanMessageBuilder()
	defer releaseSpanMessageBuilder(builder)

	span := builder.makePSpanMessage(chunk).GetSpan()
	assert.Equal(t, "span failed", span.GetExceptionInfo().GetStringValue().GetValue())
	assert.Equal(t, int32(11), span.GetExceptionInfo().GetIntValue())
	ev := span.GetSpanEvent()[0]
	assert.Equal(t, "event failed", ev.GetExceptionInfo().GetStringValue().GetValue())
	assert.Equal(t, int32(22), ev.GetExceptionInfo().GetIntValue())
	me := ev.GetNextEvent().GetMessageEvent()
	assert.Equal(t, int64(999), me.GetNextSpanId())
	assert.Equal(t, "db-1", me.GetDestinationId())
	assert.Equal(t, "db-host:3306", me.GetEndPoint())

	async := defaultSpan(a)
	async.asyncId = 5
	async.asyncSequence = 2
	async.spanEvents = append(async.spanEvents, newSpanEvent(async, "async-op"))
	asyncChunk := async.newEventChunk(false)
	asyncChunk.optimizeSpanEvents()
	pchunk := builder.makePSpanMessage(asyncChunk).GetSpanChunk()
	if assert.NotNil(t, pchunk, "async span must keep the PSpanChunk shape") {
		assert.Equal(t, int32(5), pchunk.GetLocalAsyncId().GetAsyncId())
		assert.Equal(t, int32(2), pchunk.GetLocalAsyncId().GetSequence())
	}
}

// What the collector gets for the addresses a span never recorded: "UNKNOWN"
// on the accept event (Java's SpanMessageMapper.toAcceptEvent defaults), and no
// next span id on a message event whose caller never injected a trace context.
func Test_spanMessageBuilder_unknownAddressAndUnsetNextSpanId(t *testing.T) {
	a := newTestAgent(defaultConfig())

	s := newSampledSpan(a, "op", "/rpc")
	se := newSpanEvent(s, "client-call")
	se.destinationId = "db-1"
	s.spanEvents = append(s.spanEvents, se)
	chunk := s.newEventChunk(true)
	chunk.optimizeSpanEvents()

	builder := acquireSpanMessageBuilder()
	defer releaseSpanMessageBuilder(builder)

	span := builder.makePSpanMessage(chunk).GetSpan()
	assert.Equal(t, unknownAddress, span.GetAcceptEvent().GetEndPoint(), "endPoint")
	assert.Equal(t, unknownAddress, span.GetAcceptEvent().GetRemoteAddr(), "remoteAddr")

	me := span.GetSpanEvent()[0].GetNextEvent().GetMessageEvent()
	assert.Equal(t, "db-1", me.GetDestinationId())
	assert.Zero(t, me.GetNextSpanId(), "nextSpanId must stay unset")
}

// The stream sender's contract: the message is marshaled by Send before the
// builder is released. Bytes captured at "Send" time must stay intact after
// the builder is recycled for later spans.
func Test_spanMessageBuilder_streamReuseKeepsSentBytes(t *testing.T) {
	a := newTestAgent(defaultConfig())

	type sent struct {
		seed  int
		bytes []byte
	}
	var sends []sent

	for seed := 1; seed <= 20; seed++ {
		builder := acquireSpanMessageBuilder()
		msg := builder.makePSpanMessage(slabTestChunk(a, seed, 4))
		wire, err := proto.Marshal(msg) // what stream.Send does before returning
		assert.NoError(t, err)
		sends = append(sends, sent{seed, wire})
		releaseSpanMessageBuilder(builder)
	}

	for _, s := range sends {
		var msg pb.PSpanMessage
		assert.NoError(t, proto.Unmarshal(s.bytes, &msg))
		assert.NoError(t, verifySeedMessage(&msg, s.seed, 4))
	}
}

type retainingSpanSendClient struct {
	pb.Span_SendSpanClient
	request *pb.PSpanMessage
}

func (c *retainingSpanSendClient) Send(in *pb.PSpanMessage) error {
	c.request = in
	return nil
}

type retainingSpanBatchClient struct {
	request *pb.PSpanMessageBatch
}

func (c *retainingSpanBatchClient) SendSpan(context.Context, ...grpc.CallOption) (pb.Span_SendSpanClient, error) {
	panic("not used")
}

func (c *retainingSpanBatchClient) SendSpanBatch(_ context.Context, in *pb.PSpanMessageBatch, _ ...grpc.CallOption) (*pb.PSpanResultBatch, error) {
	c.request = in
	return &pb.PSpanResultBatch{}, nil
}

// grpc-go tracing retains request pointers after Send returns. Those requests
// must be detached from the slabs before their builders are recycled.
func Test_spanGrpc_tracingRetainsStableMessages(t *testing.T) {
	previous := grpc.EnableTracing
	grpc.EnableTracing = true
	defer func() { grpc.EnableTracing = previous }()

	agent := newTestAgent(defaultConfig())
	streamClient := &retainingSpanSendClient{}
	stream := &spanStream{stream: streamClient, cancel: func() {}}
	assert.NoError(t, stream.sendSpan(slabTestChunk(agent, 7, 3)))
	assert.NoError(t, verifySeedMessage(streamClient.request, 7, 3))

	batchClient := &retainingSpanBatchClient{}
	spanGrpc := &spanGrpc{
		spanClient:              batchClient,
		agent:                   agent,
		batchFlushTimeout:       time.Second,
		maxConcurrentRequests:   1,
		concurrentRequestPermit: make(chan struct{}, 1),
	}
	spanGrpc.sendSpanBatchAsync([]*spanChunk{slabTestChunk(agent, 8, 3)})
	spanGrpc.inFlight.Wait()
	if assert.Len(t, batchClient.request.GetSpan(), 1) {
		assert.NoError(t, verifySeedMessage(batchClient.request.GetSpan()[0], 8, 3))
	}
}

// mixCheckSpanClient verifies every batch at call time — while the sender
// still owns the builder — and holds the call briefly so many builders stay
// in flight at once.
type mixCheckSpanClient struct {
	mu       sync.Mutex
	errs     []error
	received int
}

func (c *mixCheckSpanClient) SendSpan(ctx context.Context, _ ...grpc.CallOption) (pb.Span_SendSpanClient, error) {
	panic("not used")
}

func (c *mixCheckSpanClient) SendSpanBatch(ctx context.Context, in *pb.PSpanMessageBatch, _ ...grpc.CallOption) (*pb.PSpanResultBatch, error) {
	time.Sleep(time.Millisecond)
	for _, msg := range in.GetSpan() {
		rpc := msg.GetSpan().GetAcceptEvent().GetRpc()
		var seed int
		if _, err := fmt.Sscanf(rpc, "/rpc/%d", &seed); err != nil {
			seed = -1
		}
		err := verifySeedMessage(msg, seed, 4)
		c.mu.Lock()
		if err != nil {
			c.errs = append(c.errs, err)
		}
		c.received++
		c.mu.Unlock()
	}
	return &pb.PSpanResultBatch{}, nil
}

// Concurrent batch sends must never observe another send's data through a
// recycled builder. Run with -race.
func Test_spanGrpc_sendSpanBatchAsync_noDataMixing(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	client := &mixCheckSpanClient{}
	spanGrpc := &spanGrpc{
		spanClient:              client,
		agent:                   agent,
		batchSize:               defaultSpanBatchSize,
		batchFlushTimeout:       time.Second,
		batchCollectDeadline:    time.Duration(defaultSpanBatchCollectDeadline) * time.Millisecond,
		maxConcurrentRequests:   8,
		concurrentRequestPermit: make(chan struct{}, 8),
	}

	const senders, batchesPerSender, spansPerBatch = 4, 25, 5
	var wg sync.WaitGroup
	for g := 0; g < senders; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < batchesPerSender; i++ {
				chunks := make([]*spanChunk, spansPerBatch)
				for j := range chunks {
					seed := (g*batchesPerSender+i)*spansPerBatch + j
					chunks[j] = slabTestChunk(agent, seed, 4)
				}
				spanGrpc.sendSpanBatchAsync(chunks)
			}
		}(g)
	}
	wg.Wait()
	spanGrpc.inFlight.Wait()

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.errs) > 0 {
		msgs := make([]string, 0, len(client.errs))
		for _, err := range client.errs {
			msgs = append(msgs, err.Error())
		}
		t.Fatalf("mixed span data:\n%s", strings.Join(msgs, "\n"))
	}
	assert.Equal(t, senders*batchesPerSender*spansPerBatch, client.received,
		"every span must reach the collector (no permit timeouts expected)")
}
