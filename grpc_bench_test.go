package pinpoint

// Span transport conversion benchmarks.
//
// These measure the sender-side protobuf work that runs once per span send:
// building the PSpanMessage object graph (makePSpan / makePSpanChunk /
// makePSpanMessageBatch) and, for scale, the wire serialization gRPC performs
// on top of it. They quantify the allocation cost of building a fresh protobuf
// struct per send.
//
// Run:
//
//	go test -run=^$ -bench=Benchmark_spanTransport -benchmem
//	go test -run=^$ -bench=Benchmark_spanTransport -benchmem -memprofile=alloc.out

import (
	"context"
	"fmt"
	"testing"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// buildBenchChunk builds a finished, realistic span chunk: a sampled web
// request with nEvents traced calls, each carrying one SQL-shaped annotation,
// plus an HTTP status annotation on the span itself. The chunk is what
// sendSpanBatchWorker dequeues and hands to the conversion functions.
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
		s.spanEvents = append(s.spanEvents, se)
	}

	chunk := s.newEventChunk(true)
	chunk.optimizeSpanEvents()
	return chunk
}

// The object graph built per span (makePSpan) and per SendSpanBatch request at
// the default batch size (makePSpanMessageBatch); marshal=true adds the wire
// serialization gRPC performs on top of it.
func Benchmark_spanTransport_makePSpan(b *testing.B) {
	for _, marshal := range []bool{false, true} {
		b.Run(fmt.Sprintf("marshal=%v", marshal), func(b *testing.B) { benchMakePSpanN(b, 10, marshal) })
	}
}

func Benchmark_spanTransport_makePSpanMessageBatch(b *testing.B) {
	a := benchAgent()
	chunks := make([]*spanChunk, defaultSpanBatchSize)
	for i := range chunks {
		chunks[i] = buildBenchChunk(a, 10)
	}

	for _, marshal := range []bool{false, true} {
		b.Run(fmt.Sprintf("marshal=%v", marshal), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				builder := acquireSpanMessageBuilder()
				benchMarshal(b, marshal, builder.makePSpanMessageBatch(chunks))
				releaseSpanMessageBuilder(builder)
			}
		})
	}
}

// Event-count scaling: where the per-send allocations come from.
func Benchmark_spanTransport_makePSpanEvents1(b *testing.B)  { benchMakePSpanN(b, 1, false) }
func Benchmark_spanTransport_makePSpanEvents50(b *testing.B) { benchMakePSpanN(b, 50, false) }

func benchMakePSpanN(b *testing.B, nEvents int, marshal bool) {
	a := benchAgent()
	chunk := buildBenchChunk(a, nEvents)

	b.ReportAllocs()
	for b.Loop() {
		builder := acquireSpanMessageBuilder()
		benchMarshal(b, marshal, builder.makePSpan(chunk))
		releaseSpanMessageBuilder(builder)
	}
}

// benchMarshal adds the wire serialization when marshal is set.
func benchMarshal(b *testing.B, marshal bool, msg proto.Message) {
	if marshal {
		if _, err := proto.Marshal(msg); err != nil {
			b.Fatal(err)
		}
	}
}

// --- stream vs batch, agent-side cost over a stub transport ---
//
// The stubs perform the wire marshal a real grpc-go Send / unary call does,
// but no network I/O, so the numbers compare the two transports' agent-side
// machinery: per-span timer + marshal on the stream path vs per-batch
// goroutine + permit + context on the batch path.

type stubSpanBatchClient struct{}

func (stubSpanBatchClient) SendSpan(ctx context.Context, _ ...grpc.CallOption) (pb.Span_SendSpanClient, error) {
	panic("not used")
}

func (stubSpanBatchClient) SendSpanBatch(ctx context.Context, in *pb.PSpanMessageBatch, _ ...grpc.CallOption) (*pb.PSpanResultBatch, error) {
	_, err := proto.Marshal(in)
	return &pb.PSpanResultBatch{}, err
}

// One iteration sends one 50-span batch; divide ns/op, B/op, allocs/op by 50
// for the per-span cost.
func Benchmark_spanTransport_batchSendPerBatch50(b *testing.B) {
	a := benchAgent()
	spanGrpc := newMockSpanGrpc(a)
	spanGrpc.spanClient = stubSpanBatchClient{}
	spanGrpc.maxConcurrentRequests = 8
	spanGrpc.concurrentRequestPermit = make(chan struct{}, 8)
	chunks := make([]*spanChunk, defaultSpanBatchSize)
	for i := range chunks {
		chunks[i] = buildBenchChunk(a, 10)
	}

	b.ReportAllocs()
	for b.Loop() {
		spanGrpc.sendSpanBatchAsync(chunks)
	}
	b.StopTimer()
	spanGrpc.inFlight.Wait()
}
