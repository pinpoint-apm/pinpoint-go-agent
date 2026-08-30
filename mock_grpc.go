package pinpoint

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	empty "github.com/golang/protobuf/ptypes/empty"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/pinpoint-apm/pinpoint-go-agent/protobuf/mock"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
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
	a.errorCache = newMetaCache[string, int32](cacheSize, hashStringKey)
	a.sqlCache = newMetaCache[string, int32](cacheSize, hashStringKey)
	a.sqlUidCache = newMetaCache[string, []byte](cacheSize, hashStringKey)
	a.rawSqlCache = newMetaCache[string, normalizedSql](cacheSize, hashStringKey)
	a.apiCache = newMetaCache[apiCacheKey, int32](cacheSize, hashApiCacheKey)
	a.config.offGrpc = true

	return a
}

//mock grpc

type mockAgentGrpcClient struct {
	client *mock.MockAgentClient
	stream *mock.MockAgent_PingSessionClient
}

func (agentGrpcClient *mockAgentGrpcClient) RequestAgentInfo(ctx context.Context, agentinfo *pb.PAgentInfo) (*pb.PResult, error) {
	_ = agentGrpcClient.client.EXPECT().RequestAgentInfo(gomock.Any(), gomock.Any()).Return(&pb.PResult{Success: true, Message: "success"}, nil)
	return agentGrpcClient.client.RequestAgentInfo(ctx, agentinfo)
}

func (agentGrpcClient *mockAgentGrpcClient) PingSession(ctx context.Context) (pb.Agent_PingSessionClient, error) {
	_ = agentGrpcClient.client.EXPECT().PingSession(gomock.Any()).Return(agentGrpcClient.stream, nil)
	return agentGrpcClient.client.PingSession(ctx)
}

type mockMetaGrpcClient struct {
	client *mock.MockMetadataClient
}

func (metaGrpcClient *mockMetaGrpcClient) RequestApiMetaData(ctx context.Context, in *pb.PApiMetaData) (*pb.PResult, error) {
	_ = metaGrpcClient.client.EXPECT().RequestApiMetaData(gomock.Any(), gomock.Any()).Return(&pb.PResult{Success: true, Message: "success"}, nil)
	return metaGrpcClient.client.RequestApiMetaData(ctx, in)
}

func (metaGrpcClient *mockMetaGrpcClient) RequestSqlMetaData(ctx context.Context, in *pb.PSqlMetaData) (*pb.PResult, error) {
	_ = metaGrpcClient.client.EXPECT().RequestSqlMetaData(gomock.Any(), gomock.Any()).Return(&pb.PResult{Success: true, Message: "success"}, nil)
	return metaGrpcClient.client.RequestSqlMetaData(ctx, in)
}

func (metaGrpcClient *mockMetaGrpcClient) RequestSqlUidMetaData(ctx context.Context, in *pb.PSqlUidMetaData) (*pb.PResult, error) {
	//_ = metaGrpcClient.client.EXPECT().RequestSqlMetaData(gomock.Any(), gomock.Any()).Return(&pb.PResult{Success: true, Message: "success"}, nil)
	//return metaGrpcClient.client.RequestSqlUidMetaData(ctx, in)
	return nil, nil
}

func (metaGrpcClient *mockMetaGrpcClient) RequestStringMetaData(ctx context.Context, in *pb.PStringMetaData) (*pb.PResult, error) {
	_ = metaGrpcClient.client.EXPECT().RequestStringMetaData(gomock.Any(), gomock.Any()).Return(&pb.PResult{Success: true, Message: "success"}, nil)
	return metaGrpcClient.client.RequestStringMetaData(ctx, in)
}

func (metaGrpcClient *mockMetaGrpcClient) RequestExceptionMetaData(ctx context.Context, in *pb.PExceptionMetaData) (*pb.PResult, error) {
	//_ = metaGrpcClient.client.EXPECT().RequestExceptionMetaData(gomock.Any(), gomock.Any()).Return(&pb.PResult{Success: true, Message: "success"}, nil)
	//return metaGrpcClient.client.RequestExceptionMetaData(ctx, in)
	return nil, nil
}

func newMockAgentGrpc(agent *agent, t *testing.T) *agentGrpc {
	ctrl := gomock.NewController(t)
	stream := mock.NewMockAgent_PingSessionClient(ctrl)
	agentClient := mockAgentGrpcClient{mock.NewMockAgentClient(ctrl), stream}
	metadataClient := mockMetaGrpcClient{mock.NewMockMetadataClient(ctrl)}

	return &agentGrpc{nil, &agentClient, &metadataClient, -1, nil, agent}
}

func newMockAgentGrpcPing(agent *agent, t *testing.T) *agentGrpc {
	ctrl := gomock.NewController(t)
	stream := mock.NewMockAgent_PingSessionClient(ctrl)
	agentClient := mockAgentGrpcClient{mock.NewMockAgentClient(ctrl), stream}
	metadataClient := mockMetaGrpcClient{mock.NewMockMetadataClient(ctrl)}

	stream.EXPECT().Send(gomock.Any()).Return(nil)
	stream.EXPECT().Recv().Return(nil, nil)

	return &agentGrpc{nil, &agentClient, &metadataClient, -1, nil, agent}
}

// mockSpanGrpcClient supports both span transports used by tests:
// it returns a mock SendSpan stream for legacy mode and records SendSpanBatch payloads for batch mode.
type mockSpanGrpcClient struct {
	mu       sync.Mutex
	requests []*pb.PSpanMessageBatch
	response *pb.PSpanResultBatch
	err      error
	stream   *mock.MockSpan_SendSpanClient
	client   *mock.MockSpanClient
	useMock  bool
}

func (spanGrpcClient *mockSpanGrpcClient) SendSpan(ctx context.Context) (pb.Span_SendSpanClient, error) {
	if spanGrpcClient.useMock {
		_ = spanGrpcClient.client.EXPECT().SendSpan(gomock.Any()).Return(spanGrpcClient.stream, nil)
		return spanGrpcClient.client.SendSpan(ctx)
	}
	return spanGrpcClient.stream, nil
}

func (spanGrpcClient *mockSpanGrpcClient) SendSpanBatch(ctx context.Context, in *pb.PSpanMessageBatch) (*pb.PSpanResultBatch, error) {
	// Clone like the real transport, which marshals the request during the
	// call: the sender recycles the message once SendSpanBatch returns, so a
	// retained pointer would later observe recycled memory.
	spanGrpcClient.mu.Lock()
	spanGrpcClient.requests = append(spanGrpcClient.requests, proto.Clone(in).(*pb.PSpanMessageBatch))
	spanGrpcClient.mu.Unlock()

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

func newMockSpanGrpc(agent *agent, t *testing.T) *spanGrpc {
	t.Helper()
	ctrl := gomock.NewController(t)
	stream := mock.NewMockSpan_SendSpanClient(ctrl)
	stream.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
	stream.EXPECT().CloseAndRecv().Return(&empty.Empty{}, nil).AnyTimes()
	spanClient := &mockSpanGrpcClient{
		client:  mock.NewMockSpanClient(ctrl),
		stream:  stream,
		useMock: true,
	}

	return &spanGrpc{
		spanClient:              spanClient,
		agent:                   agent,
		stream:                  nil,
		batchSize:               defaultSpanBatchSize,
		batchFlushTimeout:       time.Duration(defaultSpanBatchFlushInterval) * time.Millisecond,
		batchCollectDeadline:    time.Duration(defaultSpanBatchCollectDeadline) * time.Millisecond,
		maxConcurrentRequests:   defaultSpanBatchMaxConcurrentRequests,
		concurrentRequestPermit: make(chan struct{}, defaultSpanBatchMaxConcurrentRequests),
	}
}

type mockStaGrpcClient struct {
	client *mock.MockStatClient
	stream *mock.MockStat_SendAgentStatClient
}

func (statGrpcClient *mockStaGrpcClient) SendAgentStat(ctx context.Context) (pb.Stat_SendAgentStatClient, error) {
	_ = statGrpcClient.client.EXPECT().SendAgentStat(gomock.Any()).Return(statGrpcClient.stream, nil)
	return statGrpcClient.client.SendAgentStat(ctx)
}

func newMockStatGrpc(agent *agent, t *testing.T) *statGrpc {
	ctrl := gomock.NewController(t)
	stream := mock.NewMockStat_SendAgentStatClient(ctrl)
	statClient := mockStaGrpcClient{mock.NewMockStatClient(ctrl), stream}

	stream.EXPECT().Send(gomock.Any()).Return(nil)

	return &statGrpc{nil, &statClient, nil, agent}
}

type mockRetryStaGrpcClient struct {
	client *mock.MockStatClient
	stream *mock.MockStat_SendAgentStatClient
	retry  int
}

func (statGrpcClient *mockRetryStaGrpcClient) SendAgentStat(ctx context.Context) (pb.Stat_SendAgentStatClient, error) {
	if statGrpcClient.retry < 3 {
		time.Sleep(1 * time.Second)
		_ = statGrpcClient.client.EXPECT().SendAgentStat(gomock.Any()).Return(nil, errors.New(""))
	} else {
		_ = statGrpcClient.client.EXPECT().SendAgentStat(gomock.Any()).Return(statGrpcClient.stream, nil)
	}
	statGrpcClient.retry++
	return statGrpcClient.client.SendAgentStat(ctx)
}

func newRetryMockStatGrpc(agent *agent, t *testing.T) *statGrpc {
	ctrl := gomock.NewController(t)
	stream := mock.NewMockStat_SendAgentStatClient(ctrl)
	statClient := mockRetryStaGrpcClient{mock.NewMockStatClient(ctrl), stream, 0}

	stream.EXPECT().Send(gomock.Any()).Return(nil)
	//stream.EXPECT().Send(gomock.Any()).Return(errors.New("stat send fail"))

	return &statGrpc{nil, &statClient, nil, agent}
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
