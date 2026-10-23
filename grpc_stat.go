package pinpoint

import (
	"context"
	"sync/atomic"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"

	"google.golang.org/grpc"
)

type statGrpc struct {
	statConn   *grpc.ClientConn
	statClient pb.StatClient
	stream     *statStream
	agent      *agent

	// compress is Collector.Grpc.Compression=gzip until a collector fails
	// a stream because it cannot decompress gzip; the streams opened after
	// that are uncompressed.
	compress atomic.Bool
}

type statStream struct {
	streamHandle[pb.Stat_SendAgentStatClient]
	// compress is the statGrpc flag, turned off by close when the collector
	// rejected this stream's gzip.
	compress *atomic.Bool
}

func newStatGrpc(agent *agent) (*statGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorStatPort)
	if err != nil {
		return nil, err
	}

	s := &statGrpc{
		statConn:   conn,
		statClient: pb.NewStatClient(conn),
		agent:      agent,
	}
	s.compress.Store(agent.config.String(CfgCollectorGrpcCompression) == compressionGzip)
	return s, nil
}

func (statGrpc *statGrpc) close() {
	if statGrpc.statConn != nil {
		statGrpc.statConn.Close()
	}
}

func (statGrpc *statGrpc) newStatStream() bool {
	ctx, cancel := context.WithCancel(grpcMetadataContext(statGrpc.agent, -1))
	// Unlike grpc-java, grpc-go cannot compress per message, so every message
	// of a gzip stream is compressed, a lone small stat included.
	var opts []grpc.CallOption
	if statGrpc.compress.Load() {
		opts = gzipCallOptions
	}
	stream, err := statGrpc.statClient.SendAgentStat(ctx, opts...)
	if err != nil {
		cancel()
		Log("grpc").Errorf("make stat stream - %v", err)
		return false
	}

	statGrpc.stream = &statStream{newStreamHandle(stream, cancel, newStreamAge(statGrpc.agent)), &statGrpc.compress}
	return true
}

func (statGrpc *statGrpc) newStatStreamWithRetry() *statStream {
	if newStreamWithRetry(statGrpc.agent, statGrpc.statConn, statGrpc.newStatStream, "stat") {
		return statGrpc.stream
	}
	return &statStream{}
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
	if s.open() && IsTraceLogLevelEnabled() {
		Log("grpc").Tracef("PStatMessage: %s", stats.String())
	}
	return s.send(func(c pb.Stat_SendAgentStatClient) error { return c.Send(stats) }, "stat stream.Send()")
}

func makePAgentStatBatch(stats []*inspectorStats) *pb.PStatMessage {
	l := make([]*pb.PAgentStat, 0, len(stats))
	for _, s := range stats {
		l = append(l, makePAgentStat(s))
	}
	return &pb.PStatMessage{
		Field: &pb.PStatMessage_AgentStatBatch{
			AgentStatBatch: &pb.PAgentStatBatch{
				AgentStat: l,
			},
		},
	}
}

func makePAgentStat(stat *inspectorStats) *pb.PAgentStat {
	return &pb.PAgentStat{
		Timestamp:       stat.sampleTime.UnixNano() / int64(time.Millisecond),
		CollectInterval: stat.interval,
		Gc: &pb.PJvmGc{
			// Go's collector is none of the types this field enumerates. The
			// counts below are Go's own: NumGC counts whole cycles (Go has no
			// generations) and PauseTotalNs sums stop-the-world time only.
			Type:                 pb.PJvmGcType_JVM_GC_TYPE_UNKNOWN,
			JvmMemoryHeapUsed:    stat.heapUsed,
			JvmMemoryHeapMax:     stat.heapMax,
			JvmMemoryNonHeapUsed: stat.nonHeapUsed,
			JvmMemoryNonHeapMax:  stat.nonHeapMax,
			JvmGcOldCount:        stat.gcNum,
			JvmGcOldTime:         stat.gcTime,
		},
		CpuLoad: &pb.PCpuLoad{
			JvmCpuLoad:    stat.cpuProcLoad,
			SystemCpuLoad: stat.cpuSysLoad,
		},
		Transaction: &pb.PTransaction{
			SampledNewCount:            stat.sampleNew,
			SampledContinuationCount:   stat.sampleCont,
			UnsampledNewCount:          stat.unSampleNew,
			UnsampledContinuationCount: stat.unSampleCont,
			SkippedNewCount:            stat.skipNew,
			SkippedContinuationCount:   stat.skipCont,
		},
		ActiveTrace: &pb.PActiveTrace{
			Histogram: &pb.PActiveTraceHistogram{
				Version:             1,
				HistogramSchemaType: 2, //NORMAL SCHEMA
				ActiveTraceCount:    stat.activeSpan,
			},
		},
		ResponseTime: &pb.PResponseTime{
			Avg: stat.responseAvg,
			Max: stat.responseMax,
		},
		FileDescriptor: &pb.PFileDescriptor{
			OpenFileDescriptorCount: stat.numOpenFD,
		},
		TotalThread: &pb.PTotalThread{
			TotalThreadCount: stat.numThreads,
		},
	}
}

func makePAgentUriStat(stat *urlStatSnapshot) *pb.PStatMessage {
	return &pb.PStatMessage{
		Field: &pb.PStatMessage_AgentUriStat{
			AgentUriStat: &pb.PAgentUriStat{
				BucketVersion: urlStatBucketVersion,
				EachUriStat:   makePEachUriStatList(stat),
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
		TotalHistogram:  makePUriHistogram(e.totalHistogram),
		FailedHistogram: makePUriHistogram(e.failedHistogram),
		Timestamp:       e.tickTime.UnixNano() / int64(time.Millisecond),
	}
}

func makePUriHistogram(h *urlStatHistogram) *pb.PUriHistogram {
	if h == nil || h.isEmpty() {
		return &pb.PUriHistogram{}
	}

	return &pb.PUriHistogram{
		Total:     h.total,
		Max:       h.max,
		Histogram: h.histogram,
	}
}
