package pinpoint

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"
	grpcmock "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type recordingStatServer struct {
	pb.UnimplementedStatServer
	mu       sync.Mutex
	messages []*pb.PStatMessage
	entries  int
}

// The wire encoder is the size oracle. The URI lengths sweep the points below
// 16 KiB where a length prefix the split computes itself grows by a byte: an
// entry's own, and the envelope's around one entry and around two. The
// ten-byte varint of a negative bucket version moves the envelope's points off
// the entries'. The six lengths tried before sat on the URI field's own
// boundaries, which proto.Size counts, and a formula that took the envelope's
// prefix from the entries alone passed them. No URL/tick entry may be split
// across messages.
func Test_splitUrlStatEntries_fitsTheCompleteEnvelope(t *testing.T) {
	const version = -1 // A negative int32 takes a ten-byte varint.
	var sizes []int
	for _, r := range [][2]int{{1, 200}, {8100, 8250}, {16280, 16420}} {
		for size := r[0]; size <= r[1]; size++ {
			sizes = append(sizes, size)
		}
	}
	for _, size := range sizes {
		entry := &pb.PEachUriStat{
			Uri:             strings.Repeat("u", size),
			Timestamp:       1700000000000,
			TotalHistogram:  &pb.PUriHistogram{Total: 20, Max: 10, Histogram: []int32{2, 0, 0, 0, 0, 0, 0, 0}},
			FailedHistogram: &pb.PUriHistogram{Total: 10, Max: 10, Histogram: []int32{1, 0, 0, 0, 0, 0, 0, 0}},
		}
		entries := []*pb.PEachUriStat{entry, entry, entry}
		stats := &pb.PAgentUriStat{BucketVersion: version, EachUriStat: entries}
		limit := proto.Size(makePAgentUriStatMessage(version, entries[:2]))
		for _, maxSize := range []int{limit, limit - 1} {
			batches := splitUrlStatEntries(stats, maxSize)
			want := 2 // two entries fit exactly, the third starts a new message
			if maxSize < limit {
				want = 3 // one byte below the boundary leaves one entry per message
			}
			require.Len(t, batches, want, "uri length %d, max size %d", size, maxSize)
			var sent []*pb.PEachUriStat
			for _, batch := range batches {
				wire, err := proto.Marshal(makePAgentUriStatMessage(version, batch))
				require.NoError(t, err)
				require.LessOrEqual(t, len(wire), maxSize, "uri length %d", size)
				sent = append(sent, batch...)
			}
			require.Equal(t, entries, sent, "histograms and timestamps remain intact")
		}
	}
}

func Test_splitUrlStatEntries_dropsOnlyAnUnsendableEntry(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()
	urlStatMessageOversizeLog.next.Store(0)
	const limit = 128
	first := &pb.PEachUriStat{Uri: "/first", Timestamp: 1700000000000}
	last := &pb.PEachUriStat{Uri: "/last", Timestamp: 1700000030000}
	oversize := &pb.PEachUriStat{Uri: strings.Repeat("x", limit)}
	stats := &pb.PAgentUriStat{EachUriStat: []*pb.PEachUriStat{oversize, first, oversize, last, oversize}}
	batches := splitUrlStatEntries(stats, limit)
	var sent []*pb.PEachUriStat
	for _, batch := range batches {
		require.NotEmpty(t, batch)
		assert.LessOrEqual(t, proto.Size(makePAgentUriStatMessage(0, batch)), limit)
		sent = append(sent, batch...)
	}
	assert.Equal(t, []*pb.PEachUriStat{first, last}, sent)
	assert.Equal(t, 1, strings.Count(buf.String(), "url stat entry dropped:"), "the warning is rate-limited")
	assert.Contains(t, buf.String(), "exceeds "+CfgCollectorGrpcMaxSendMessageSize+" 128")
	assert.Empty(t, splitUrlStatEntries(&pb.PAgentUriStat{EachUriStat: []*pb.PEachUriStat{oversize}}, limit))
	assert.Empty(t, splitUrlStatEntries(stats, 1), "even the envelope cannot fit")
}

// A part whose Send fails is sent again on a new stream, and the parts sent
// before it are not replayed. What the mock stream accepted counts as sent
// here; on a real stream a Send that returned nil has only queued its part,
// which the failure of a later one can still lose (agent.sendStats). The
// partition uses the connection's original limit even after a config change.
func Test_sendStats_retriesOnlyTheFailedPartAtTheDialedLimit(t *testing.T) {
	const limit = 260
	cfg, err := NewConfig(WithAppName("TestApp"), WithCollectorGrpcMaxSendMessageSize(limit))
	require.NoError(t, err)
	a := newTestAgent(cfg)
	a.statGrpc, err = newStatGrpc(a)
	require.NoError(t, err)
	defer a.statGrpc.close()
	a.config.Set(CfgCollectorGrpcMaxSendMessageSize, 4*limit)

	var entries []*pb.PEachUriStat
	for i := range 5 {
		entries = append(entries, &pb.PEachUriStat{Uri: strings.Repeat("u", 100), Timestamp: int64(i)})
	}
	message := makePAgentUriStatMessage(7, entries)
	original := proto.Clone(message)
	var sent []*pb.PEachUriStat
	collect := func(args mock.Arguments) {
		msg := args.Get(0).(*pb.PStatMessage)
		assert.LessOrEqual(t, proto.Size(msg), limit)
		assert.EqualValues(t, 7, msg.GetAgentUriStat().GetBucketVersion())
		sent = append(sent, msg.GetAgentUriStat().GetEachUriStat()...)
	}
	broken := committable(grpcmock.NewMockStat_SendAgentStatClient())
	broken.OnSend(mock.Anything).Run(collect).Return(nil).Once()
	broken.OnSend(mock.Anything).Return(io.EOF).Once()
	broken.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)
	healthy := committable(grpcmock.NewMockStat_SendAgentStatClient())
	healthy.OnSend(mock.Anything).Run(collect).Return(nil)
	healthy.OnCloseAndRecv().Return(&emptypb.Empty{}, nil)
	client := grpcmock.NewMockStatClient()
	client.OnSendAgentStat(mock.Anything).Return(broken, nil).Once()
	client.OnSendAgentStat(mock.Anything).Return(healthy, nil)
	a.statGrpc.statClient = client

	stream := a.statGrpc.newStatStreamWithRetry()
	stream = a.sendStats(stream, message)
	stream.close()

	assert.Equal(t, entries, sent, "every entry is handed to a stream once and in order")
	assert.True(t, proto.Equal(original, message), "partitioning must not mutate the queued message")
	broken.AssertNumberOfCalls(t, "Send", 2)
	healthy.AssertNumberOfCalls(t, "Send", 2)
	healthy.AssertNumberOfCalls(t, "CloseAndRecv", 1)
	client.AssertNumberOfCalls(t, "SendAgentStat", 2)
}

func (s *recordingStatServer) SendAgentStat(stream pb.Stat_SendAgentStatServer) error {
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&emptypb.Empty{})
		}
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.messages = append(s.messages, msg)
		s.entries += len(msg.GetAgentUriStat().GetEachUriStat())
		s.mu.Unlock()
	}
}

func (s *recordingStatServer) snapshot() ([]*pb.PStatMessage, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.messages), s.entries
}

// One tick fits the limit, but five retained ticks do not. Exercise the real
// worker and gRPC streams after recovery and on the shutdown fallback through
// statChan, with and without gzip. The limit is scaled down to 64 KiB, so the
// test moves kilobytes rather than megabytes: at the default 4 MiB it took 6s
// under -race, a second or two per case against its five-second deadlines.
func Test_sendStatsWorker_splitsRetainedUrlStats(t *testing.T) {
	const limit = 64 << 10
	for _, compression := range []string{compressionNone, compressionGzip} {
		for _, shutdown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/shutdown=%t", compression, shutdown), func(t *testing.T) {
				server := &recordingStatServer{}
				port, _ := startEncodingServer(t, func(s *grpc.Server) { pb.RegisterStatServer(s, server) })
				cfg, err := NewConfig(WithAppName("TestApp"), WithCollectorHost("127.0.0.1"),
					WithCollectorStatPort(port), WithCollectorGrpcCompression(compression),
					WithCollectorGrpcMaxSendMessageSize(limit), WithHttpUrlStatEnable(true))
				require.NoError(t, err)
				a := newTestAgent(cfg)
				a.statChan = make(chan *pb.PStatMessage, 16)
				a.statGrpc, err = newStatGrpc(a)
				require.NoError(t, err)
				t.Cleanup(a.statGrpc.close)

				tick := time.Unix(1700000000, 0).UTC().Truncate(urlStatCollectInterval)
				setNow := fixUrlStatClock(t, tick.Add(time.Second))
				const ticks, routes = 8, 300
				suffix := strings.Repeat("x", 90)
				for window := range ticks {
					at := tick.Add(time.Duration(window) * urlStatCollectInterval)
					setNow(at.Add(time.Second))
					for route := range routes {
						uri := fmt.Sprintf("/routes/%05d/%s", route, suffix)
						a.urlStats.add(newTestUrlStat(uri, int64(10+window), at))
					}
					a.flushUrlStat(false)
				}
				require.Len(t, a.urlStatSendChan, 1)
				require.Empty(t, a.statChan, "the outage must not fill the stat queue")
				require.Len(t, a.urlStats.completed, maxCompletedUrlStatSnapshots)

				lastWindow := ticks - 2
				if shutdown {
					a.flushUrlStat(true)
					require.Len(t, a.statChan, 1, "the final flush uses the shutdown fallback")
					a.signalShutdown()
					lastWindow++
				}
				a.workerWg.Add(1)
				go a.superviseWorker("send stats", a.sendStatsWorker)
				t.Cleanup(func() {
					a.signalShutdown()
					assert.True(t, waitTimeout(&a.workerWg, 5*time.Second))
				})
				if !shutdown {
					require.Eventually(t, func() bool {
						_, n := server.snapshot()
						return n == routes
					}, 5*time.Second, time.Millisecond)
					a.flushUrlStat(false)
				}
				wantEntries := (1 + maxCompletedUrlStatSnapshots) * routes
				if shutdown {
					wantEntries += routes
				}
				require.Eventually(t, func() bool {
					_, n := server.snapshot()
					return n == wantEntries
				}, 5*time.Second, time.Millisecond, "all retained URL/tick entries must reach the collector")

				messages, _ := server.snapshot()
				require.GreaterOrEqual(t, len(messages), 5, "the retained ticks went out in parts")
				seen := make(map[urlKey]bool, wantEntries)
				for _, msg := range messages {
					// The test server takes up to 4 MiB, so this is the agent's
					// own bound, on the decoded size also under gzip.
					require.LessOrEqual(t, proto.Size(msg), limit)
					for _, entry := range msg.GetAgentUriStat().GetEachUriStat() {
						at := time.UnixMilli(entry.GetTimestamp()).UTC()
						window := int(at.Sub(tick) / urlStatCollectInterval)
						require.True(t, window == 0 || (window >= ticks-1-maxCompletedUrlStatSnapshots && window <= lastWindow))
						key := urlKey{url: entry.GetUri(), tick: at}
						require.False(t, seen[key], "a URL/tick must not be sent twice")
						seen[key] = true
						require.Equal(t, int64(10+window), entry.GetTotalHistogram().GetTotal())
					}
				}
				require.Len(t, seen, wantEntries)
			})
		}
	}
}
