package pinpoint

import (
	"cmp"
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// spanGrpc sends spans as unary SendSpanBatch requests.
type spanGrpc struct {
	spanConn              *grpc.ClientConn
	spanClient            pb.SpanClient
	agent                 *agent
	batchSize             int
	batchFlushTimeout     time.Duration
	batchCollectDeadline  time.Duration
	maxConcurrentRequests int

	// Buffered channel used as a semaphore that bounds the number of
	// in-flight SendSpanBatch requests. Acquire = send to it; release =
	// receive from it.
	concurrentRequestPermit chan struct{}
	inFlight                sync.WaitGroup

	// compress is Collector.Grpc.Compression=gzip until a collector
	// answers that it cannot decompress gzip; it then stays off for the life
	// of the sender, as in the Java agent.
	compress atomic.Bool
}

func newSpanGrpc(agent *agent) (*spanGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorSpanPort)
	if err != nil {
		return nil, err
	}

	s := &spanGrpc{
		spanConn:                conn,
		spanClient:              pb.NewSpanClient(conn),
		agent:                   agent,
		batchSize:               agent.config.Int(CfgCollectorGrpcSpanBatchSize),
		batchFlushTimeout:       time.Duration(agent.config.Int(CfgCollectorGrpcSpanBatchFlushInterval)) * time.Millisecond,
		batchCollectDeadline:    time.Duration(agent.config.Int(CfgCollectorGrpcSpanBatchCollectDeadline)) * time.Millisecond,
		maxConcurrentRequests:   agent.config.Int(CfgCollectorGrpcSpanBatchMaxConcurrentRequests),
		concurrentRequestPermit: make(chan struct{}, agent.config.Int(CfgCollectorGrpcSpanBatchMaxConcurrentRequests)),
	}
	s.compress.Store(agent.config.String(CfgCollectorGrpcCompression) == compressionGzip)
	return s, nil
}

// close releases the connection without waiting for in-flight batches: the
// batch worker awaits them itself on exit, and Shutdown only reaches this
// after that worker finished or was abandoned at shutdownTimeout. Waiting here
// too ran inFlight.Wait concurrently with the abandoned worker's inFlight.Add,
// which sync.WaitGroup forbids and punishes with a panic on the waiting
// goroutine, outside recoverPanic.
func (spanGrpc *spanGrpc) close() {
	if spanGrpc.spanConn != nil {
		spanGrpc.spanConn.Close()
	}
}

// collectSpanBatch gathers the first span plus queued spans until batch size or collect deadline is reached.
// The batch worker blocks for the first item, then uses a short collection window to improve
// batch density without delaying sparse traffic too long.
func (spanGrpc *spanGrpc) collectSpanBatch(first *spanChunk, queue *spanQueue) ([]*spanChunk, bool) {
	batch := make([]*spanChunk, 0, spanGrpc.batchSize)
	batch = append(batch, first)

	// The deadline timer is armed only once the queue runs dry: a batch that
	// fills straight from the queue never waits, so it never needs one.
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for len(batch) < spanGrpc.batchSize {
		if chunk, ok := queue.tryDequeue(); ok {
			batch = append(batch, chunk)
			continue
		}
		if timer == nil {
			timer = time.NewTimer(spanGrpc.batchCollectDeadline)
		}
		select {
		case <-queue.wake:
		case <-queue.done:
			// Drain what remains; report closed only once the queue is empty so
			// the worker comes back for a leftover larger than one batch.
			for len(batch) < spanGrpc.batchSize {
				chunk, ok := queue.tryDequeue()
				if !ok {
					return batch, true
				}
				batch = append(batch, chunk)
			}
			return batch, false
		case <-timer.C:
			return batch, false
		}
	}

	return batch, false
}

// sendSpanBatchAsync applies concurrent request limiting and sends a unary batch request.
// If no permit becomes available within the flush timeout, the whole batch is skipped rather than
// blocking the worker forever behind slow in-flight requests; completed calls always release their permit.
func (spanGrpc *spanGrpc) sendSpanBatchAsync(chunks []*spanChunk) {
	if !spanGrpc.acquireSpanBatchPermit() {
		// Counted with the queue's head-drops: these spans are lost the same
		// way, and reportSpanDrops would otherwise under-report the loss.
		spanGrpc.agent.spanDrops.record(int64(len(chunks)))
		spanBatchSkipLog.infof(
			"SendSpanBatch skipped: %d spans dropped, no available permits within %s concurrentRequests:%d/%d",
			len(chunks),
			spanGrpc.batchFlushTimeout.String(),
			len(spanGrpc.concurrentRequestPermit),
			spanGrpc.maxConcurrentRequests,
		)
		return
	}

	spanGrpc.inFlight.Add(1)
	go func() {
		defer spanGrpc.inFlight.Done()
		defer spanGrpc.releaseSpanBatchPermit()

		// The message is built here rather than on the worker so that the
		// defers above cover the build too. A panic in makePSpanMessageBatch
		// on the worker was recovered by superviseWorker, which restarted the
		// worker but could not return this permit: maxConcurrentRequests such
		// panics left every later batch waiting out the flush timeout and
		// being dropped for the life of the agent.
		builder := acquireSpanMessageBuilder()
		defer releaseSpanMessageBuilder(builder)

		// Recovered like every other agent goroutine: a panic here must not
		// take the host process down, and the defers above still release the
		// permit and the builder.
		recoverPanic("span batch send", func() {
			spanMessageBatch := builder.makePSpanMessageBatch(chunks)
			if len(spanMessageBatch.GetSpan()) == 0 {
				return
			}

			if grpc.EnableTracing {
				// Completed unary traces can also retain their request.
				spanMessageBatch = proto.Clone(spanMessageBatch).(*pb.PSpanMessageBatch)
			}

			// Measured against Collector.Grpc.MaxSendMessageSize before the
			// send: grpc-go refuses a request past it before writing a byte,
			// and a collector refuses one past its own inbound limit, either
			// way losing every span the request carried. One size pass is
			// what the common case costs; a batch past the limit goes out in
			// as many requests as it takes, and only a span that no request
			// can carry is dropped (splitSpanMessages).
			maxSize := spanGrpc.agent.config.Int(CfgCollectorGrpcMaxSendMessageSize)
			size := proto.Size(spanMessageBatch)
			if IsDebugLogLevelEnabled() {
				Log("grpc").Debugf("SendSpanBatch size=%d messageSize=%d", len(spanMessageBatch.GetSpan()), size)
			}
			if IsTraceLogLevelEnabled() {
				Log("grpc").Tracef("PSpanMessageBatch: %s", spanMessageBatch.String())
			}
			if size <= maxSize {
				spanGrpc.sendSpanMessageBatch(spanMessageBatch)
				return
			}
			for _, msgs := range splitSpanMessages(spanMessageBatch.GetSpan(), maxSize, &spanGrpc.agent.spanDrops) {
				spanGrpc.sendSpanMessageBatch(&pb.PSpanMessageBatch{Span: msgs})
			}
		})
	}()
}

// sendSpanMessageBatch makes one SendSpanBatch call under sendStreamTimeOut.
// The spans of a failed call are lost like a head-drop or a permit skip, and
// counted with them, so reportSpanDrops covers every cause.
func (spanGrpc *spanGrpc) sendSpanMessageBatch(batch *pb.PSpanMessageBatch) {
	ctx, cancel := context.WithTimeout(grpcMetadataContext(spanGrpc.agent, -1), sendStreamTimeOut)
	defer cancel()

	var opts []grpc.CallOption
	if spanGrpc.compress.Load() {
		opts = gzipCallOptions
	}
	response, err := spanGrpc.spanClient.SendSpanBatch(ctx, batch, opts...)
	if err != nil {
		disableGzipIfRejected(&spanGrpc.compress, err, "span batches")
		spanGrpc.agent.spanDrops.record(int64(len(batch.GetSpan())))
		spanBatchFailLog.infof("SendSpanBatch failed - %d spans dropped: %v", len(batch.GetSpan()), err)
		return
	}
	handleSpanBatchResponse(response)
}

// splitSpanMessages groups msgs, in order, into batches whose serialized size
// stays within maxSize, placing each message by its own framed size. A message
// over the limit on its own is dropped and counted on drops: no request could
// carry it. Called only for a batch already measured past the limit, so the
// size pass per message is paid on that path alone.
func splitSpanMessages(msgs []*pb.PSpanMessage, maxSize int, drops *dropReporter) [][]*pb.PSpanMessage {
	var batches [][]*pb.PSpanMessage
	start, size := 0, 0
	for i, m := range msgs {
		// The message as the batch frames it: the span field's tag and the
		// length prefix, then the message itself.
		n := protowire.SizeTag(1) + protowire.SizeBytes(proto.Size(m))
		if n > maxSize {
			if i > start {
				batches = append(batches, msgs[start:i])
			}
			start, size = i+1, 0
			drops.record(1)
			spanBatchOversizeLog.warnf("span dropped: %d bytes serialized exceeds %s %d, which no request can carry: %s",
				n, CfgCollectorGrpcMaxSendMessageSize, maxSize, describeSpanMessage(m))
			continue
		}
		if size+n > maxSize {
			batches = append(batches, msgs[start:i])
			start, size = i, 0
		}
		size += n
	}
	if start < len(msgs) {
		batches = append(batches, msgs[start:])
	}
	return batches
}

// describeSpanMessage names the span a message carries for a log line, by its
// transaction id and span id, whichever of the two shapes it has.
func describeSpanMessage(m *pb.PSpanMessage) string {
	tx, spanId := m.GetSpan().GetTransactionId(), m.GetSpan().GetSpanId()
	if c := m.GetSpanChunk(); c != nil {
		tx, spanId = c.GetTransactionId(), c.GetSpanId()
	}
	return TransactionId{tx.GetAgentId(), tx.GetAgentStartTime(), tx.GetSequence()}.String() +
		" span " + strconv.FormatInt(spanId, 10)
}

// acquireSpanBatchPermit waits up to the configured flush timeout for an async batch request slot.
// The buffered channel acts like the semaphore: its capacity is maxConcurrentRequests.
func (spanGrpc *spanGrpc) acquireSpanBatchPermit() bool {
	// A free permit is the normal case; take it without allocating a timer.
	select {
	case spanGrpc.concurrentRequestPermit <- struct{}{}:
		return true
	default:
	}

	timer := time.NewTimer(spanGrpc.batchFlushTimeout)
	defer timer.Stop()

	select {
	case spanGrpc.concurrentRequestPermit <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

func (spanGrpc *spanGrpc) releaseSpanBatchPermit() {
	<-spanGrpc.concurrentRequestPermit
}

// awaitInFlightSpanBatch waits briefly for async sends. Shutdown is best
// effort: wait up to shutdownTimeout for accepted requests, then continue
// closing - the same bound the worker drain has, so a test that shortens one
// shortens both.
func (spanGrpc *spanGrpc) awaitInFlightSpanBatch() {
	if !waitTimeout(&spanGrpc.inFlight, shutdownTimeout) {
		Log("grpc").Warnf("Timed out waiting for in-flight span requests to complete")
	}
}

// handleSpanBatchResponse logs collector-side partial success without failing the sender loop.
// A partial success means the collector accepted the request but rejected some spans, so the
// sender records the warning and continues with later batches.
func handleSpanBatchResponse(response *pb.PSpanResultBatch) {
	if response == nil || response.GetPartialSuccess() == nil {
		return
	}

	partialSuccess := response.GetPartialSuccess()
	rejectedSpans := partialSuccess.GetRejectedSpans()
	if rejectedSpans > 0 {
		spanBatchPartialLog.warnf(
			"SendSpanBatch partial success: rejectedSpans=%d, errorId=%d, errorMessage=%s",
			rejectedSpans,
			partialSuccess.GetErrorId(),
			partialSuccess.GetErrorMessage(),
		)
		return
	}

	if partialSuccess.GetErrorMessage() != "" {
		spanBatchPartialLog.infof(
			"SendSpanBatch warning: errorId=%d, %s",
			partialSuccess.GetErrorId(),
			partialSuccess.GetErrorMessage(),
		)
	}
}

func (b *spanMessageBuilder) makePSpanMessageBatch(chunks []*spanChunk) *pb.PSpanMessageBatch {
	spanMessages := b.messageLists.take(len(chunks))[:0]
	for _, chunk := range chunks {
		if chunk == nil || chunk.span == nil {
			continue
		}
		spanMessages = append(spanMessages, b.makePSpanMessage(chunk))
	}

	return &pb.PSpanMessageBatch{Span: spanMessages}
}

// makePSpanMessage converts one dequeued chunk: final synchronous spans become
// PSpan messages; non-final chunks and async spans keep the PSpanChunk shape.
func (b *spanMessageBuilder) makePSpanMessage(chunk *spanChunk) *pb.PSpanMessage {
	if !chunk.final || chunk.span.isAsyncSpan() {
		return b.makePSpanChunk(chunk)
	}
	return b.makePSpan(chunk)
}

func (b *spanMessageBuilder) makePSpan(chunk *spanChunk) *pb.PSpanMessage {
	span := chunk.span

	pspan := b.spans.get()
	pspan.Version = 1
	txId := b.txIds.get()
	txId.AgentId = span.txId.AgentId
	txId.AgentStartTime = span.txId.StartTime
	txId.Sequence = span.txId.Sequence
	pspan.TransactionId = txId
	pspan.SpanId = span.spanId
	pspan.ParentSpanId = span.parentSpanId
	pspan.StartTime = span.startTime.UnixMilli()
	pspan.Elapsed = int32(span.elapsed)
	pspan.ServiceType = span.serviceType

	acceptEvent := b.acceptEvents.get()
	acceptEvent.Rpc = validUTF8(span.rpcName)
	// A span with no address recorded reports unknownAddress: an empty string
	// leaves the web UI with a blank inbound node instead of one labelled
	// unknown.
	acceptEvent.EndPoint = validUTF8(cmp.Or(span.endPoint, unknownAddress))
	acceptEvent.RemoteAddr = validUTF8(cmp.Or(span.remoteAddr, unknownAddress))
	// A root span has no parent to describe, so it carries no PParentInfo naming
	// an empty parent. ParentApplicationType is -1 (UNDEFINED) when the name
	// came without a parseable type.
	if span.parentAppName != "" {
		parentInfo := b.parentInfos.get()
		parentInfo.ParentApplicationName = validUTF8(span.parentAppName)
		parentInfo.ParentApplicationType = int32(span.parentAppType)
		parentInfo.AcceptorHost = validUTF8(span.acceptorHost)
		parentInfo.ParentServiceName = validUTF8(span.parentServiceName)
		acceptEvent.ParentInfo = parentInfo
	}
	pspan.AcceptEvent = acceptEvent

	pspan.Annotation = b.annotationsWithApi(&span.annotations, span.apiId, span.operationName)
	pspan.ApiId = span.apiId
	pspan.Flag = int32(span.flags)
	pspan.SpanEvent = b.makePSpanEventList(chunk)
	pspan.Err = span.err.Load()
	pspan.ApplicationServiceType = span.agent.appType
	pspan.LoggingTransactionInfo = span.loggingInfo.Load()

	if span.errorString != "" {
		exceptionInfo := b.intStringValues.get()
		exceptionInfo.IntValue = span.errorFuncId
		exceptionInfo.StringValue = b.stringValue(span.errorString)
		pspan.ExceptionInfo = exceptionInfo
	}

	oneof := b.spanOneofs.get()
	oneof.Span = pspan
	gspan := b.messages.get()
	gspan.Field = oneof
	return gspan
}

func (b *spanMessageBuilder) makePSpanChunk(chunk *spanChunk) *pb.PSpanMessage {
	span := chunk.span

	pchunk := b.chunks.get()
	pchunk.Version = 1
	txId := b.txIds.get()
	txId.AgentId = span.txId.AgentId
	txId.AgentStartTime = span.txId.StartTime
	txId.Sequence = span.txId.Sequence
	pchunk.TransactionId = txId
	pchunk.SpanId = span.spanId
	pchunk.KeyTime = chunk.keyTime
	pchunk.EndPoint = validUTF8(chunk.endPoint)
	pchunk.SpanEvent = b.makePSpanEventList(chunk)
	pchunk.ApplicationServiceType = span.agent.appType

	if span.isAsyncSpan() {
		localAsyncId := b.localAsyncIds.get()
		localAsyncId.AsyncId = span.asyncId
		localAsyncId.Sequence = span.asyncSequence
		pchunk.LocalAsyncId = localAsyncId
	}

	oneof := b.chunkOneofs.get()
	oneof.SpanChunk = pchunk
	gspan := b.messages.get()
	gspan.Field = oneof
	return gspan
}

func (b *spanMessageBuilder) makePSpanEventList(chunk *spanChunk) []*pb.PSpanEvent {
	spanEventList := b.eventLists.take(len(chunk.eventChunk))
	for i, event := range chunk.eventChunk {
		spanEventList[i] = b.makePSpanEvent(event)
	}
	return spanEventList
}

// annotationsWithApi materializes a's list, appending an AnnotationApi
// fallback when no api id was cached. The fallback lives only in the builder
// output: mutating the span's own annotation here would race with the
// application goroutine and duplicate the entry on a re-serialization.
func (b *spanMessageBuilder) annotationsWithApi(a *annotation, apiId int32, operationName string) []*pb.PAnnotation {
	if apiId != 0 || operationName == "" {
		return a.getListInto(b)
	}
	a.annotationLock.Lock()
	defer a.annotationLock.Unlock()

	list := b.annotationLists.take(len(a.values) + 1)
	for i := range a.values {
		list[i] = a.values[i].toProtoInto(b)
	}
	api := annotationValue{key: AnnotationApi, typ: annotationTypeString, s1: operationName}
	list[len(a.values)] = api.toProtoInto(b)
	return list
}

func (b *spanMessageBuilder) makePSpanEvent(event *spanEvent) *pb.PSpanEvent {
	aSpanEvent := b.events.get()
	aSpanEvent.Sequence = event.sequence
	aSpanEvent.Depth = event.depth
	aSpanEvent.StartElapsed = int32(event.startElapsed)
	aSpanEvent.EndElapsed = int32(event.endElapsed)
	aSpanEvent.ServiceType = event.serviceType
	aSpanEvent.Annotation = b.annotationsWithApi(&event.annotations, event.apiId, event.operationName)
	aSpanEvent.ApiId = event.apiId
	aSpanEvent.AsyncEvent = event.asyncId

	if event.errorString != "" {
		exceptionInfo := b.intStringValues.get()
		exceptionInfo.IntValue = event.errorFuncId
		exceptionInfo.StringValue = b.stringValue(event.errorString)
		aSpanEvent.ExceptionInfo = exceptionInfo
	}

	if event.destinationId != "" {
		messageEvent := b.messageEvents.get()
		// Only an event that actually injected a trace context has a next span
		// id; without this guard the message event would point the downstream
		// call at a span id no node will ever report.
		if event.nextSpanId != noneSpanId {
			messageEvent.NextSpanId = event.nextSpanId
		}
		messageEvent.EndPoint = validUTF8(event.endPoint)
		messageEvent.DestinationId = validUTF8(event.destinationId)
		oneof := b.nextEventOneofs.get()
		oneof.MessageEvent = messageEvent
		next := b.nextEvents.get()
		next.Field = oneof
		aSpanEvent.NextEvent = next
	}

	return aSpanEvent
}
