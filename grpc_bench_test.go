package pinpoint

// Span transport conversion benchmarks.
//
// These measure the sender-side protobuf work that runs once per span send:
// building the PSpanMessage object graph (makePSpan / makePSpanChunk /
// makePSpanMessageBatch) and, for scale, the wire serialization gRPC performs
// on top of it. They quantify the per-send allocation cost the report's §2.9
// attributes to "a fresh protobuf struct per send".
//
// Run:
//
//	go test -run=^$ -bench=Benchmark_spanTransport -benchmem
//	go test -run=^$ -bench=Benchmark_spanTransport -benchmem -memprofile=alloc.out

import (
	"context"
	"testing"
	"time"

	empty "github.com/golang/protobuf/ptypes/empty"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// buildBenchChunk builds a finished, realistic span chunk: a sampled web
// request with nEvents traced calls, each carrying one SQL-shaped annotation,
// plus an HTTP status annotation on the span itself. The chunk is what
// sendSpanWorker dequeues and hands to the conversion functions.
func buildBenchChunk(a *agent, nEvents int) *spanChunk {
	s := newSampledSpan(a, "GET /bench", "/bench/rpc")
	s.annotations.AppendInt(AnnotationHttpStatusCode, 200)
	s.endPoint = "localhost:8080"
	s.remoteAddr = "10.0.0.1"

	for i := 0; i < nEvents; i++ {
		se := newSpanEvent(s, "example.com/pkg.query")
		se.annotations.AppendIntStringString(AnnotationSqlUid, 1,
			"SELECT id, name, email FROM users WHERE id = ?", "42")
		se.endElapsed = 1
		s.eventDepth.Add(1) // keep depths distinct, as real nesting would
		s.eventSequence.Add(1)
		s.spanEvents = append(s.spanEvents, se)
	}

	chunk := s.newEventChunk(true)
	chunk.optimizeSpanEvents()
	return chunk
}

// Conversion only: the object graph built for every stream Send.
func Benchmark_spanTransport_makePSpan(b *testing.B) {
	a := benchAgent()
	chunk := buildBenchChunk(a, 10)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		builder := acquireSpanMessageBuilder()
		_ = builder.makePSpan(chunk)
		releaseSpanMessageBuilder(builder)
	}
}

// Conversion + wire marshal: the full protobuf cost of one stream Send.
func Benchmark_spanTransport_makePSpanMarshal(b *testing.B) {
	a := benchAgent()
	chunk := buildBenchChunk(a, 10)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		builder := acquireSpanMessageBuilder()
		msg := builder.makePSpan(chunk)
		if _, err := proto.Marshal(msg); err != nil {
			b.Fatal(err)
		}
		releaseSpanMessageBuilder(builder)
	}
}

// The batch path: one SendSpanBatch request at the default batch size.
func Benchmark_spanTransport_makePSpanMessageBatch(b *testing.B) {
	a := benchAgent()
	chunks := make([]*spanChunk, defaultSpanBatchSize)
	for i := range chunks {
		chunks[i] = buildBenchChunk(a, 10)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		builder := acquireSpanMessageBuilder()
		_ = builder.makePSpanMessageBatch(chunks)
		releaseSpanMessageBuilder(builder)
	}
}

func Benchmark_spanTransport_makePSpanMessageBatchMarshal(b *testing.B) {
	a := benchAgent()
	chunks := make([]*spanChunk, defaultSpanBatchSize)
	for i := range chunks {
		chunks[i] = buildBenchChunk(a, 10)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		builder := acquireSpanMessageBuilder()
		msg := builder.makePSpanMessageBatch(chunks)
		if _, err := proto.Marshal(msg); err != nil {
			b.Fatal(err)
		}
		releaseSpanMessageBuilder(builder)
	}
}

// Event-count scaling: where the per-send allocations come from.
func Benchmark_spanTransport_makePSpanEvents1(b *testing.B)  { benchMakePSpanN(b, 1) }
func Benchmark_spanTransport_makePSpanEvents50(b *testing.B) { benchMakePSpanN(b, 50) }

func benchMakePSpanN(b *testing.B, nEvents int) {
	a := benchAgent()
	chunk := buildBenchChunk(a, nEvents)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		builder := acquireSpanMessageBuilder()
		_ = builder.makePSpan(chunk)
		releaseSpanMessageBuilder(builder)
	}
}

// --- stream vs batch, agent-side cost over a stub transport ---
//
// The stubs perform the wire marshal a real grpc-go Send / unary call does,
// but no network I/O, so the numbers compare the two transports' agent-side
// machinery: per-span timer + marshal on the stream path vs per-batch
// goroutine + permit + context on the batch path.

type stubSpanSendClient struct {
	grpc.ClientStream
}

func (s *stubSpanSendClient) Send(msg *pb.PSpanMessage) error {
	_, err := proto.Marshal(msg)
	return err
}

func (s *stubSpanSendClient) CloseAndRecv() (*empty.Empty, error) { return &empty.Empty{}, nil }

type stubSpanBatchClient struct{}

func (stubSpanBatchClient) SendSpan(ctx context.Context) (pb.Span_SendSpanClient, error) {
	panic("not used")
}

func (stubSpanBatchClient) SendSpanBatch(ctx context.Context, in *pb.PSpanMessageBatch) (*pb.PSpanResultBatch, error) {
	_, err := proto.Marshal(in)
	return &pb.PSpanResultBatch{}, err
}

func Benchmark_spanTransport_streamSendPerSpan(b *testing.B) {
	a := benchAgent()
	chunk := buildBenchChunk(a, 10)
	stream := &spanStream{stream: &stubSpanSendClient{}, cancel: func() {}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := stream.sendSpan(chunk); err != nil {
			b.Fatal(err)
		}
	}
}

// One iteration sends one 50-span batch; divide ns/op, B/op, allocs/op by 50
// for the per-span cost.
func Benchmark_spanTransport_batchSendPerBatch50(b *testing.B) {
	a := benchAgent()
	spanGrpc := &spanGrpc{
		spanClient:              stubSpanBatchClient{},
		agent:                   a,
		batchSize:               defaultSpanBatchSize,
		batchFlushTimeout:       time.Second,
		batchCollectDeadline:    time.Duration(defaultSpanBatchCollectDeadline) * time.Millisecond,
		maxConcurrentRequests:   8,
		concurrentRequestPermit: make(chan struct{}, 8),
	}
	chunks := make([]*spanChunk, defaultSpanBatchSize)
	for i := range chunks {
		chunks[i] = buildBenchChunk(a, 10)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		spanGrpc.sendSpanBatchAsync(chunks)
	}
	b.StopTimer()
	spanGrpc.inFlight.Wait()
}
