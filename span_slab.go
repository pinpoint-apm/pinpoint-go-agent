package pinpoint

import (
	"sync"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	wrappers "google.golang.org/protobuf/types/known/wrapperspb"
)

// slab hands out pointers to zeroed T values carved from one reusable backing
// array, so a protobuf message graph costs one allocation per node type at
// steady state instead of one per node. Growth mid-build is safe: append
// initializes the new slot before its pointer is taken, and pointers handed
// out earlier keep referencing the orphaned old array, which stays valid.
type slab[T any] struct{ buf []T }

func (s *slab[T]) get() *T {
	var zero T
	s.buf = append(s.buf, zero)
	return &s.buf[len(s.buf)-1]
}

// reset recycles every value handed out since the last reset. The clear drops
// references (strings, sub-messages) the recycled values still hold, so a
// builder idling in the pool does not retain the last send's data.
func (s *slab[T]) reset() {
	clear(s.buf)
	s.buf = s.buf[:0]
}

// ptrSlab hands out fixed-size []*T lists carved from one reusable backing
// array. The returned slice is capped at n, zeroed, and owned by the caller
// until the next reset.
type ptrSlab[T any] struct{ buf []*T }

func (s *ptrSlab[T]) take(n int) []*T {
	if n == 0 {
		return nil
	}
	if len(s.buf)+n > cap(s.buf) {
		s.buf = make([]*T, 0, max(2*cap(s.buf), n, 32))
	}
	start := len(s.buf)
	s.buf = s.buf[:start+n]
	sub := s.buf[start : start+n : start+n]
	clear(sub)
	return sub
}

func (s *ptrSlab[T]) reset() {
	clear(s.buf)
	s.buf = s.buf[:0]
}

// spanMessageBuilder builds PSpanMessage graphs out of reusable slabs — the Go
// stand-in for the C++ agent's per-call protobuf Arena (grpc.cpp SendSpanBatch:
// build on the arena, send, arena_.Reset()).
//
// Ownership: every message a build method returns lives on the builder's slabs
// and dies at releaseSpanMessageBuilder. Release strictly after the collector
// send completes: stream.Send marshals synchronously before returning, and a
// unary SendSpanBatch call marshals during the call, so "after Send / the call
// returns" is the normal reuse boundary. grpc-go tracing retains requests
// beyond that boundary, so senders pass it a GC-owned clone instead. Releasing
// earlier recycles memory a send still references and mixes data between spans.
type spanMessageBuilder struct {
	messages      slab[pb.PSpanMessage]
	spanOneofs    slab[pb.PSpanMessage_Span]
	spans         slab[pb.PSpan]
	chunkOneofs   slab[pb.PSpanMessage_SpanChunk]
	chunks        slab[pb.PSpanChunk]
	txIds         slab[pb.PTransactionId]
	acceptEvents  slab[pb.PAcceptEvent]
	parentInfos   slab[pb.PParentInfo]
	localAsyncIds slab[pb.PLocalAsyncId]

	events          slab[pb.PSpanEvent]
	eventLists      ptrSlab[pb.PSpanEvent]
	messageLists    ptrSlab[pb.PSpanMessage]
	nextEvents      slab[pb.PNextEvent]
	nextEventOneofs slab[pb.PNextEvent_MessageEvent]
	messageEvents   slab[pb.PMessageEvent]
	intStringValues slab[pb.PIntStringValue]
	stringValues    slab[wrappers.StringValue]

	annotations                    slab[pb.PAnnotation]
	annotationLists                ptrSlab[pb.PAnnotation]
	annotationValues               slab[pb.PAnnotationValue]
	intOneofs                      slab[pb.PAnnotationValue_IntValue]
	longOneofs                     slab[pb.PAnnotationValue_LongValue]
	stringOneofs                   slab[pb.PAnnotationValue_StringValue]
	stringStringOneofs             slab[pb.PAnnotationValue_StringStringValue]
	stringStrings                  slab[pb.PStringStringValue]
	intStringStringOneofs          slab[pb.PAnnotationValue_IntStringStringValue]
	intStringStrings               slab[pb.PIntStringStringValue]
	bytesStringStringOneofs        slab[pb.PAnnotationValue_BytesStringStringValue]
	bytesStringStrings             slab[pb.PBytesStringStringValue]
	longIntIntByteByteStringOneofs slab[pb.PAnnotationValue_LongIntIntByteByteStringValue]
	longIntIntByteByteStrings      slab[pb.PLongIntIntByteByteStringValue]
}

func (b *spanMessageBuilder) reset() {
	b.messages.reset()
	b.spanOneofs.reset()
	b.spans.reset()
	b.chunkOneofs.reset()
	b.chunks.reset()
	b.txIds.reset()
	b.acceptEvents.reset()
	b.parentInfos.reset()
	b.localAsyncIds.reset()

	b.events.reset()
	b.eventLists.reset()
	b.messageLists.reset()
	b.nextEvents.reset()
	b.nextEventOneofs.reset()
	b.messageEvents.reset()
	b.intStringValues.reset()
	b.stringValues.reset()

	b.annotations.reset()
	b.annotationLists.reset()
	b.annotationValues.reset()
	b.intOneofs.reset()
	b.longOneofs.reset()
	b.stringOneofs.reset()
	b.stringStringOneofs.reset()
	b.stringStrings.reset()
	b.intStringStringOneofs.reset()
	b.intStringStrings.reset()
	b.bytesStringStringOneofs.reset()
	b.bytesStringStrings.reset()
	b.longIntIntByteByteStringOneofs.reset()
	b.longIntIntByteByteStrings.reset()
}

func (b *spanMessageBuilder) stringValue(s string) *wrappers.StringValue {
	v := b.stringValues.get()
	v.Value = validUTF8(s)
	return v
}

// Builders are kept on a fixed free list first and in a sync.Pool only as
// overflow. sync.Pool alone is emptied every second GC cycle, and a builder
// rebuilt from zero regrows all of its slabs by doubling: measured on the
// 50-span batch benchmark, that regrowth was ~70KB and 14 allocations per
// batch, more than the marshal itself. The batch sender bounds its live
// builders by Span.BatchMaxConcurrentRequests (default 10) plus the one the
// worker is filling, so a free list of that order holds the steady state.
//
// ponytail: fixed capacity; size it from config if the default permit count
// is raised well past it.
const spanMessageBuilderFreeListSize = 16

var (
	spanMessageBuilderFreeList = make(chan *spanMessageBuilder, spanMessageBuilderFreeListSize)
	spanMessageBuilderPool     = sync.Pool{New: func() any { return &spanMessageBuilder{} }}
)

func acquireSpanMessageBuilder() *spanMessageBuilder {
	select {
	case b := <-spanMessageBuilderFreeList:
		return b
	default:
		return spanMessageBuilderPool.Get().(*spanMessageBuilder)
	}
}

// releaseSpanMessageBuilder recycles every message the builder produced.
// Call only after the collector send completed (see the type comment).
func releaseSpanMessageBuilder(b *spanMessageBuilder) {
	b.reset()
	select {
	case spanMessageBuilderFreeList <- b:
	default:
		spanMessageBuilderPool.Put(b)
	}
}
