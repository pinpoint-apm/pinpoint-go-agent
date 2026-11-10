package pinpoint

import (
	"context"
	"slices"
	"sync/atomic"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type statGrpc struct {
	statConn   *grpc.ClientConn
	statClient pb.StatClient
	agent      *agent
	// The limit statConn was dialed with; changing the non-dynamic config
	// later does not change grpc-go's limit on the connection.
	maxSendMsgSize int

	// compress is Collector.Grpc.Compression=gzip until a collector fails
	// a stream because it cannot decompress gzip; the streams opened after
	// that are uncompressed.
	compress atomic.Bool
	// gzipConn is a connection dialed with gzipFreeList, which compresses
	// every stream on it unless the stream says identity.
	gzipConn bool
}

type statStream struct {
	streamHandle[pb.Stat_SendAgentStatClient]
	// compress is the statGrpc flag, turned off by close when the collector
	// rejected this stream's gzip.
	compress *atomic.Bool
}

func newStatGrpc(agent *agent) (*statGrpc, error) {
	opts, gzipConn := gzipDialOptions(agent.config, statGzipWriters)
	conn, err := connectCollector(agent.config, CfgCollectorStatPort, opts...)
	if err != nil {
		return nil, err
	}

	s := &statGrpc{
		statConn:       conn,
		statClient:     pb.NewStatClient(conn),
		agent:          agent,
		maxSendMsgSize: agent.config.Int(CfgCollectorGrpcMaxSendMessageSize),
		gzipConn:       gzipConn,
	}
	s.compress.Store(gzipConn)
	return s, nil
}

func (statGrpc *statGrpc) close() {
	if statGrpc.statConn != nil {
		statGrpc.statConn.Close()
	}
}

func (statGrpc *statGrpc) newStatStream() *statStream {
	ctx, cancel := context.WithCancel(grpcMetadataContext(statGrpc.agent, -1))
	// Unlike grpc-java, grpc-go cannot compress per message, so every message
	// of a gzip stream is compressed, a lone small stat included.
	opts := compressCallOptions(statGrpc.gzipConn, statGrpc.compress.Load())
	stream, err := statGrpc.statClient.SendAgentStat(ctx, opts...)
	if err != nil {
		cancel()
		statStreamOpenLog.errorf("make stat stream - %v", err)
		return nil
	}
	return &statStream{newStreamHandle(stream, cancel, newStreamAge(statGrpc.agent)), &statGrpc.compress}
}

func (statGrpc *statGrpc) newStatStreamWithRetry() *statStream {
	return newStreamWithRetry(statGrpc.agent, statGrpc.statConn, statGrpc.newStatStream, "stat")
}

// close also learns why the collector ended the stream: a send only sees
// io.EOF, and the status comes from CloseAndRecv.
func (s *statStream) close() {
	s.streamHandle.close(func(c pb.Stat_SendAgentStatClient) error {
		_, err := c.CloseAndRecv()
		disableGzipIfRejected(s.compress, err, "stat streams")
		return err
	}, "stat")
}

func (s *statStream) sendStats(stats *pb.PStatMessage) error {
	if s.open() && isTraceLogLevelEnabled() {
		Log("grpc").Tracef("PStatMessage: %s", stats.String())
	}
	return s.send(func(c pb.Stat_SendAgentStatClient) error { return c.Send(stats) }, "stat stream.Send()")
}

// sendStats splits a retained URL-stat snapshot only after the worker takes
// it, so the one-message hand-off still bounds the outage backlog. Measure
// the uncompressed size as for span batches, so the collector's decoded limit
// and a fallback from gzip to an uncompressed stream are both respected.
//
// The parts go out back to back on the stream, each through
// sendStatsOrReopen, and their delivery is at most once. A part whose own
// Send fails is sent once more on the replacement stream. The parts sent
// before it are not replayed, and may be lost: a Send that returned nil has
// only queued its message, grpc-go writes it out afterwards, and the cancel
// that follows a failed Send discards what the collector has not read by
// then. Such a part goes without a log line of its own; the one error names
// the part that failed. Against a collector that did not read its stream for
// 7s, an uncompressed 8 MB message in two parts delivered the second part
// only. Nothing tells the sender which of the earlier parts arrived, and
// replaying one that did would count it twice: the collector stores every
// URL/tick entry it receives as a row of its own and sums the rows.
func (agent *agent) sendStats(stream *statStream, stats *pb.PStatMessage) *statStream {
	uriStats := stats.GetAgentUriStat()
	maxSize := agent.statGrpc.maxSendMsgSize
	if uriStats == nil || proto.Size(stats) <= maxSize {
		return agent.sendStatsOrReopen(stream, stats)
	}
	for _, entries := range splitUrlStatEntries(uriStats, maxSize) {
		msg := makePAgentUriStatMessage(uriStats.GetBucketVersion(), entries)
		stream = agent.sendStatsOrReopen(stream, msg)
	}
	return stream
}

var urlStatMessageOversizeLog = logThrottle{src: "url stat"}

// splitUrlStatEntries keeps each URL/tick histogram whole and fits groups to
// maxSize, including the two protobuf envelopes. Only an entry that cannot
// fit by itself is dropped; the entries around it can still be sent. The
// groups share the original slice and do not copy the histogram payloads.
func splitUrlStatEntries(stats *pb.PAgentUriStat, maxSize int) [][]*pb.PEachUriStat {
	headerSize := proto.Size(&pb.PAgentUriStat{BucketVersion: stats.GetBucketVersion()})
	messageSize := func(entriesSize int) int {
		// PStatMessage.agentUriStat is field 3, a length-delimited message.
		return protowire.SizeTag(3) + protowire.SizeBytes(headerSize+entriesSize)
	}
	entries := stats.GetEachUriStat()
	var batches [][]*pb.PEachUriStat
	start, size := 0, 0
	for i, entry := range entries {
		// PAgentUriStat.eachUriStat is repeated message field 2.
		n := protowire.SizeTag(2) + protowire.SizeBytes(proto.Size(entry))
		if entrySize := messageSize(n); entrySize > maxSize {
			if i > start {
				batches = append(batches, entries[start:i])
			}
			start, size = i+1, 0
			urlStatMessageOversizeLog.warnf("url stat entry dropped: %d bytes serialized exceeds %s %d",
				entrySize, CfgCollectorGrpcMaxSendMessageSize, maxSize)
			continue
		}
		if messageSize(size+n) > maxSize {
			batches = append(batches, entries[start:i])
			start, size = i, 0
		}
		size += n
	}
	if start < len(entries) {
		batches = append(batches, entries[start:])
	}
	return batches
}

// makePAgentStatBatch copies the slice: the stat worker reuses its array for
// the next batch.
func makePAgentStatBatch(stats []*pb.PAgentStat) *pb.PStatMessage {
	return &pb.PStatMessage{
		Field: &pb.PStatMessage_AgentStatBatch{
			AgentStatBatch: &pb.PAgentStatBatch{
				AgentStat: slices.Clone(stats),
			},
		},
	}
}

func makePAgentUriStat(stat *urlStatSnapshot) *pb.PStatMessage {
	return makePAgentUriStatMessage(urlStatBucketVersion, makePEachUriStatList(stat))
}

func makePAgentUriStatMessage(bucketVersion int32, entries []*pb.PEachUriStat) *pb.PStatMessage {
	return &pb.PStatMessage{
		Field: &pb.PStatMessage_AgentUriStat{
			AgentUriStat: &pb.PAgentUriStat{
				BucketVersion: bucketVersion,
				EachUriStat:   entries,
			},
		},
	}
}

func makePEachUriStatList(stat *urlStatSnapshot) []*pb.PEachUriStat {
	l := make([]*pb.PEachUriStat, 0, len(stat.urlMap))
	for _, e := range stat.urlMap {
		l = append(l, makePEachUriStat(e))
	}
	return l
}

func makePEachUriStat(e *eachUrlStat) *pb.PEachUriStat {
	return &pb.PEachUriStat{
		Uri:             validUTF8(e.url),
		TotalHistogram:  makePUriHistogram(&e.totalHistogram),
		FailedHistogram: makePUriHistogram(&e.failedHistogram),
		Timestamp:       e.tickTime.UnixMilli(),
	}
}

func makePUriHistogram(h *urlStatHistogram) *pb.PUriHistogram {
	if h.isEmpty() {
		return &pb.PUriHistogram{}
	}

	return &pb.PUriHistogram{
		Total:     h.total,
		Max:       h.max,
		Histogram: h.histogram[:],
	}
}
