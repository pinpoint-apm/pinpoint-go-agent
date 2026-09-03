package pinpoint

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	empty "google.golang.org/protobuf/types/known/emptypb"
)

func newTestAgent(config *Config) *agent {
	a := &agent{
		appName:   "testApp",
		agentID:   "testAgent",
		appType:   ServiceTypeGoApp,
		startTime: time.Now().UnixNano() / int64(time.Millisecond),
		spanQueue: newSpanQueue(cacheSize),
		metaChan:  make(chan interface{}, cacheSize),
		config:    config,
		stats:     newAgentStats(),
		urlStats:  newUrlStats(config),
		objName: &objectName{
			version:         nameV3,
			agentID:         "testAgent",
			agentName:       "testAgent",
			applicationName: "testApp",
		},
	}
	a.enable.Store(true)
	a.errorCache = newMetaCache[string, int32](cacheSize)
	a.sqlCache = newMetaCache[string, int32](cacheSize)
	a.sqlUidCache = newMetaCache[string, []byte](cacheSize)
	a.rawSqlCache = newMetaCache[string, normalizedSql](cacheSize)
	a.apiCache = newMetaCache[apiCacheKey, int32](cacheSize)
	a.config.offGrpc = true

	return a
}

// mockAgentGrpcClient answers RequestAgentInfo and records what it was sent.
// The zero value always succeeds; failures and reject script the collector
// side of the register-with-retry loop.
type mockAgentGrpcClient struct {
	mu       sync.Mutex
	requests []*pb.PAgentInfo
	callAt   []time.Time
	failures int // leading calls that fail with Unavailable
	rejects  int // calls past failures that answer Success=false
}

func (agentGrpcClient *mockAgentGrpcClient) RequestAgentInfo(ctx context.Context, agentinfo *pb.PAgentInfo, _ ...grpc.CallOption) (*pb.PResult, error) {
	agentGrpcClient.mu.Lock()
	defer agentGrpcClient.mu.Unlock()

	agentGrpcClient.requests = append(agentGrpcClient.requests, agentinfo)
	agentGrpcClient.callAt = append(agentGrpcClient.callAt, time.Now())
	if len(agentGrpcClient.requests) <= agentGrpcClient.failures {
		return nil, status.Errorf(codes.Unavailable, "collector down")
	}
	if len(agentGrpcClient.requests) <= agentGrpcClient.failures+agentGrpcClient.rejects {
		return &pb.PResult{Success: false, Message: "rejected"}, nil
	}
	return &pb.PResult{Success: true, Message: "success"}, nil
}

func (agentGrpcClient *mockAgentGrpcClient) sentAgentInfo() []*pb.PAgentInfo {
	agentGrpcClient.mu.Lock()
	defer agentGrpcClient.mu.Unlock()
	return append([]*pb.PAgentInfo(nil), agentGrpcClient.requests...)
}

func (agentGrpcClient *mockAgentGrpcClient) callTimes() []time.Time {
	agentGrpcClient.mu.Lock()
	defer agentGrpcClient.mu.Unlock()
	return append([]time.Time(nil), agentGrpcClient.callAt...)
}

func (agentGrpcClient *mockAgentGrpcClient) PingSession(ctx context.Context, _ ...grpc.CallOption) (pb.Agent_PingSessionClient, error) {
	return &mockPingStream{}, nil
}

type mockPingStream struct {
	grpc.ClientStream // never called; the agent only uses Send/Recv/CloseSend
}

func (s *mockPingStream) Send(*pb.PPing) error     { return nil }
func (s *mockPingStream) Recv() (*pb.PPing, error) { return nil, nil }
func (s *mockPingStream) CloseSend() error         { return nil }

// mockMetaGrpcClient accepts every metadata request and keeps it, so tests can
// assert on the payload the agent actually put on the wire.
type mockMetaGrpcClient struct {
	mu     sync.Mutex
	api    []*pb.PApiMetaData
	str    []*pb.PStringMetaData
	sql    []*pb.PSqlMetaData
	sqlUid []*pb.PSqlUidMetaData
	except []*pb.PExceptionMetaData
}

func (metaGrpcClient *mockMetaGrpcClient) RequestApiMetaData(ctx context.Context, in *pb.PApiMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	metaGrpcClient.mu.Lock()
	defer metaGrpcClient.mu.Unlock()
	metaGrpcClient.api = append(metaGrpcClient.api, in)
	return &pb.PResult{Success: true, Message: "success"}, nil
}

func (metaGrpcClient *mockMetaGrpcClient) RequestSqlMetaData(ctx context.Context, in *pb.PSqlMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	metaGrpcClient.mu.Lock()
	defer metaGrpcClient.mu.Unlock()
	metaGrpcClient.sql = append(metaGrpcClient.sql, in)
	return &pb.PResult{Success: true, Message: "success"}, nil
}

func (metaGrpcClient *mockMetaGrpcClient) RequestSqlUidMetaData(ctx context.Context, in *pb.PSqlUidMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	metaGrpcClient.mu.Lock()
	defer metaGrpcClient.mu.Unlock()
	metaGrpcClient.sqlUid = append(metaGrpcClient.sqlUid, in)
	return &pb.PResult{Success: true, Message: "success"}, nil
}

func (metaGrpcClient *mockMetaGrpcClient) RequestStringMetaData(ctx context.Context, in *pb.PStringMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	metaGrpcClient.mu.Lock()
	defer metaGrpcClient.mu.Unlock()
	metaGrpcClient.str = append(metaGrpcClient.str, in)
	return &pb.PResult{Success: true, Message: "success"}, nil
}

func (metaGrpcClient *mockMetaGrpcClient) RequestExceptionMetaData(ctx context.Context, in *pb.PExceptionMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	metaGrpcClient.mu.Lock()
	defer metaGrpcClient.mu.Unlock()
	metaGrpcClient.except = append(metaGrpcClient.except, in)
	return &pb.PResult{Success: true, Message: "success"}, nil
}

// sentMeta returns a snapshot of every metadata request received so far.
func (metaGrpcClient *mockMetaGrpcClient) sentMeta() (api []*pb.PApiMetaData, str []*pb.PStringMetaData,
	sql []*pb.PSqlMetaData, sqlUid []*pb.PSqlUidMetaData, except []*pb.PExceptionMetaData) {
	metaGrpcClient.mu.Lock()
	defer metaGrpcClient.mu.Unlock()
	return append([]*pb.PApiMetaData(nil), metaGrpcClient.api...),
		append([]*pb.PStringMetaData(nil), metaGrpcClient.str...),
		append([]*pb.PSqlMetaData(nil), metaGrpcClient.sql...),
		append([]*pb.PSqlUidMetaData(nil), metaGrpcClient.sqlUid...),
		append([]*pb.PExceptionMetaData(nil), metaGrpcClient.except...)
}

func newMockAgentGrpc(agent *agent) *agentGrpc {
	return &agentGrpc{agentClient: &mockAgentGrpcClient{}, metaClient: &mockMetaGrpcClient{}, pingSocketId: -1, agent: agent}
}

// mockSpanStream stands in for the collector side of a span stream.
type mockSpanStream struct {
	grpc.ClientStream // never called; the agent only uses Send/CloseAndRecv
}

func (s *mockSpanStream) Send(*pb.PSpanMessage) error         { return nil }
func (s *mockSpanStream) CloseAndRecv() (*empty.Empty, error) { return &empty.Empty{}, nil }

// mockSpanGrpcClient supports both span transports used by tests:
// it returns a stub SendSpan stream for legacy mode and records SendSpanBatch payloads for batch mode.
type mockSpanGrpcClient struct {
	mu       sync.Mutex
	requests []*pb.PSpanMessageBatch
	response *pb.PSpanResultBatch
	err      error
	// hold, when set, parks every SendSpanBatch until it is closed, which is
	// how a test keeps a request in flight and its permit held.
	hold chan struct{}
}

func (spanGrpcClient *mockSpanGrpcClient) SendSpan(ctx context.Context, _ ...grpc.CallOption) (pb.Span_SendSpanClient, error) {
	return &mockSpanStream{}, nil
}

func (spanGrpcClient *mockSpanGrpcClient) SendSpanBatch(ctx context.Context, in *pb.PSpanMessageBatch, _ ...grpc.CallOption) (*pb.PSpanResultBatch, error) {
	// Clone like the real transport, which marshals the request during the
	// call: the sender recycles the message once SendSpanBatch returns, so a
	// retained pointer would later observe recycled memory.
	spanGrpcClient.mu.Lock()
	spanGrpcClient.requests = append(spanGrpcClient.requests, proto.Clone(in).(*pb.PSpanMessageBatch))
	hold := spanGrpcClient.hold
	spanGrpcClient.mu.Unlock()

	if hold != nil {
		<-hold
	}

	if spanGrpcClient.response != nil || spanGrpcClient.err != nil {
		return spanGrpcClient.response, spanGrpcClient.err
	}
	return &pb.PSpanResultBatch{}, nil
}

func (spanGrpcClient *mockSpanGrpcClient) requestCount() int {
	spanGrpcClient.mu.Lock()
	defer spanGrpcClient.mu.Unlock()
	return len(spanGrpcClient.requests)
}

func (spanGrpcClient *mockSpanGrpcClient) lastRequest() *pb.PSpanMessageBatch {
	spanGrpcClient.mu.Lock()
	defer spanGrpcClient.mu.Unlock()
	if len(spanGrpcClient.requests) == 0 {
		return nil
	}
	return spanGrpcClient.requests[len(spanGrpcClient.requests)-1]
}

func newMockSpanGrpc(agent *agent) *spanGrpc {
	return &spanGrpc{
		spanClient:              &mockSpanGrpcClient{},
		agent:                   agent,
		stream:                  nil,
		batchSize:               defaultSpanBatchSize,
		batchFlushTimeout:       time.Duration(defaultSpanBatchFlushInterval) * time.Millisecond,
		batchCollectDeadline:    time.Duration(defaultSpanBatchCollectDeadline) * time.Millisecond,
		maxConcurrentRequests:   defaultSpanBatchMaxConcurrentRequests,
		concurrentRequestPermit: make(chan struct{}, defaultSpanBatchMaxConcurrentRequests),
	}
}

// mockStatStream stands in for the collector side of a stat stream.
type mockStatStream struct {
	grpc.ClientStream // never called; the agent only uses Send/CloseAndRecv
}

func (s *mockStatStream) Send(*pb.PStatMessage) error         { return nil }
func (s *mockStatStream) CloseAndRecv() (*empty.Empty, error) { return &empty.Empty{}, nil }

type mockStaGrpcClient struct{}

func (statGrpcClient *mockStaGrpcClient) SendAgentStat(ctx context.Context, _ ...grpc.CallOption) (pb.Stat_SendAgentStatClient, error) {
	return &mockStatStream{}, nil
}

func newMockStatGrpc(agent *agent) *statGrpc {
	return &statGrpc{nil, &mockStaGrpcClient{}, nil, agent}
}

type mockRetryStaGrpcClient struct {
	retry int
}

func (statGrpcClient *mockRetryStaGrpcClient) SendAgentStat(ctx context.Context, _ ...grpc.CallOption) (pb.Stat_SendAgentStatClient, error) {
	if statGrpcClient.retry < 3 {
		time.Sleep(1 * time.Second)
		statGrpcClient.retry++
		return nil, errors.New("")
	}
	statGrpcClient.retry++
	return &mockStatStream{}, nil
}

func newRetryMockStatGrpc(agent *agent) *statGrpc {
	return &statGrpc{nil, &mockRetryStaGrpcClient{}, nil, agent}
}

// mockAtcStream stands in for the collector side of an active thread count
// stream: it counts samples and records that it was closed.
type mockAtcStream struct {
	grpc.ClientStream // never called; the agent only uses Send/CloseAndRecv
	mu                sync.Mutex
	sends             int
	responses         []*pb.PCmdActiveThreadCountRes
	closed            bool
	sendErr           error
}

func (s *mockAtcStream) Send(response *pb.PCmdActiveThreadCountRes) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
	s.responses = append(s.responses, response)
	return s.sendErr
}

func (s *mockAtcStream) CloseAndRecv() (*empty.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return &empty.Empty{}, nil
}

func (s *mockAtcStream) sendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}

func (s *mockAtcStream) sentResponses() []*pb.PCmdActiveThreadCountRes {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*pb.PCmdActiveThreadCountRes(nil), s.responses...)
}

func (s *mockAtcStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// mockCmdStream records what the agent writes back on the command stream.
type mockCmdStream struct {
	grpc.ClientStream // never called; the agent only uses Send/Recv
	mu                sync.Mutex
	sent              []*pb.PCmdMessage
}

func (s *mockCmdStream) Send(m *pb.PCmdMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return nil
}

func (s *mockCmdStream) Recv() (*pb.PCmdRequest, error) {
	return nil, io.EOF
}

func (s *mockCmdStream) sentMessages() []*pb.PCmdMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*pb.PCmdMessage(nil), s.sent...)
}

func (s *mockCmdStream) failMessages() []*pb.PCmdResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	fails := make([]*pb.PCmdResponse, 0, len(s.sent))
	for _, m := range s.sent {
		if f := m.GetFailMessage(); f != nil {
			fails = append(fails, f)
		}
	}
	return fails
}

// mockCmdGrpcClient hands out mockAtcStreams and keeps them in issue order.
type mockCmdGrpcClient struct {
	pb.ProfilerCommandServiceClient // never called; only the method below is
	mu                              sync.Mutex
	streams                         []*mockAtcStream
	sendErr                         error
	openErr                         error
	echoes                          []*pb.PCmdEchoResponse
	dumps                           []*pb.PCmdActiveThreadDumpRes
	lightDumps                      []*pb.PCmdActiveThreadLightDumpRes
}

func (c *mockCmdGrpcClient) CommandStreamActiveThreadCount(ctx context.Context, opts ...grpc.CallOption) (pb.ProfilerCommandService_CommandStreamActiveThreadCountClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.openErr != nil {
		return nil, c.openErr
	}
	s := &mockAtcStream{sendErr: c.sendErr}
	c.streams = append(c.streams, s)
	return s, nil
}

func (c *mockCmdGrpcClient) CommandEcho(ctx context.Context, in *pb.PCmdEchoResponse, _ ...grpc.CallOption) (*empty.Empty, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.echoes = append(c.echoes, in)
	return &empty.Empty{}, nil
}

func (c *mockCmdGrpcClient) CommandActiveThreadDump(ctx context.Context, in *pb.PCmdActiveThreadDumpRes, _ ...grpc.CallOption) (*empty.Empty, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dumps = append(c.dumps, in)
	return &empty.Empty{}, nil
}

func (c *mockCmdGrpcClient) CommandActiveThreadLightDump(ctx context.Context, in *pb.PCmdActiveThreadLightDumpRes, _ ...grpc.CallOption) (*empty.Empty, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lightDumps = append(c.lightDumps, in)
	return &empty.Empty{}, nil
}

func (c *mockCmdGrpcClient) sentEchoes() []*pb.PCmdEchoResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*pb.PCmdEchoResponse(nil), c.echoes...)
}

func (c *mockCmdGrpcClient) sentDumps() ([]*pb.PCmdActiveThreadDumpRes, []*pb.PCmdActiveThreadLightDumpRes) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*pb.PCmdActiveThreadDumpRes(nil), c.dumps...),
		append([]*pb.PCmdActiveThreadLightDumpRes(nil), c.lightDumps...)
}

func (c *mockCmdGrpcClient) stream(i int) *mockAtcStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[i]
}

func (c *mockCmdGrpcClient) streamCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.streams)
}

// newMockCmdGrpc wires a test agent to a mock command service, returning the
// command stream the agent answers on and the client that hands out streams.
func newMockCmdGrpc(agent *agent) (*cmdStream, *mockCmdGrpcClient) {
	client := &mockCmdGrpcClient{}
	agent.cmdGrpc = &cmdGrpc{cmdClient: client, agent: agent, atcStreams: atcStreams{agent: agent}}
	return &cmdStream{stream: &mockCmdStream{}, cancel: func() {}}, client
}
