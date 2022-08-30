package pinpoint

import (
	"context"
	"errors"
	"github.com/golang/mock/gomock"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"testing"
	"time"
)

type mockAgent struct {
	config    Config
	startTime int64
	sequence  int64
	agentGrpc *agentGrpc
	spanGrpc  *spanGrpc
	statGrpc  *statGrpc
}

func newMockAgent() Agent {
	agent := mockAgent{}

	agent.config = *defaultConfig()
	agent.config.ApplicationName = "mock"
	agent.config.AgentId = "m1234"
	agent.config.OffGrpc = true
	agent.startTime = 12345
	agent.sequence = 1

	return &agent
}

func (agent *mockAgent) setMockAgentGrpc(agentGrpc *agentGrpc) {
	agent.agentGrpc = agentGrpc
}

func (agent *mockAgent) setMockSpanGrpc(t *testing.T) {
	agent.spanGrpc = newMockSpanGrpc(agent, t)
}

func (agent *mockAgent) setMockStatGrpc(statGrpc *statGrpc) {
	agent.statGrpc = statGrpc
}

func (agent *mockAgent) Shutdown() {
}

func (agent *mockAgent) NewSpanTracer(operation string, rpcName string) Tracer {
	return newUnSampledSpan(rpcName)
}

func (agent *mockAgent) NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	return newUnSampledSpan(rpcName)
}

func (agent *mockAgent) Config() Config {
	return agent.config
}

func (agent *mockAgent) GenerateTransactionId() TransactionId {
	return TransactionId{agent.config.AgentId, agent.startTime, agent.sequence}
}

func (agent *mockAgent) Enable() bool {
	return true
}

func (agent *mockAgent) StartTime() int64 {
	return agent.startTime
}

func (agent *mockAgent) TryEnqueueSpan(span *span) bool {
	return true
}

func (agent *mockAgent) CacheErrorFunc(funcname string) int32 {
	return 1
}

func (agent *mockAgent) CacheSql(sql string) int32 {
	return 1
}

func (agent *mockAgent) CacheSpanApiId(descriptor string, apiType int) int32 {
	return 1
}

func (agent *mockAgent) IsHttpError(code int) bool {
	return false
}

func (agent *mockAgent) IsExcludedUrl(url string) bool {
	return false
}

func (agent *mockAgent) IsExcludedMethod(method string) bool {
	return false
}

func (agent *mockAgent) HttpHeaderRecorder(key int) httpHeaderRecorder {
	return newNoopHttpHeaderRecoder()
}

//mock grpc

type mockAgentGrpcClient struct {
	client *MockAgentClient
	stream *MockAgent_PingSessionClient
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
	client *MockMetadataClient
}

func (metaGrpcClient *mockMetaGrpcClient) RequestApiMetaData(ctx context.Context, in *pb.PApiMetaData) (*pb.PResult, error) {
	_ = metaGrpcClient.client.EXPECT().RequestApiMetaData(gomock.Any(), gomock.Any()).Return(&pb.PResult{Success: true, Message: "success"}, nil)
	return metaGrpcClient.client.RequestApiMetaData(ctx, in)
}

func (metaGrpcClient *mockMetaGrpcClient) RequestSqlMetaData(ctx context.Context, in *pb.PSqlMetaData) (*pb.PResult, error) {
	_ = metaGrpcClient.client.EXPECT().RequestSqlMetaData(gomock.Any(), gomock.Any()).Return(&pb.PResult{Success: true, Message: "success"}, nil)
	return metaGrpcClient.client.RequestSqlMetaData(ctx, in)
}

func (metaGrpcClient *mockMetaGrpcClient) RequestStringMetaData(ctx context.Context, in *pb.PStringMetaData) (*pb.PResult, error) {
	_ = metaGrpcClient.client.EXPECT().RequestStringMetaData(gomock.Any(), gomock.Any()).Return(&pb.PResult{Success: true, Message: "success"}, nil)
	return metaGrpcClient.client.RequestStringMetaData(ctx, in)
}

func newMockAgentGrpc(agent Agent, t *testing.T) *agentGrpc {
	ctrl := gomock.NewController(t)
	stream := NewMockAgent_PingSessionClient(ctrl)
	agentClient := mockAgentGrpcClient{NewMockAgentClient(ctrl), stream}
	metadataClient := mockMetaGrpcClient{NewMockMetadataClient(ctrl)}

	return &agentGrpc{nil, &agentClient, &metadataClient, -1, nil, agent}
}

func newMockAgentGrpcPing(agent Agent, t *testing.T) *agentGrpc {
	ctrl := gomock.NewController(t)
	stream := NewMockAgent_PingSessionClient(ctrl)
	agentClient := mockAgentGrpcClient{NewMockAgentClient(ctrl), stream}
	metadataClient := mockMetaGrpcClient{NewMockMetadataClient(ctrl)}

	stream.EXPECT().Send(gomock.Any()).Return(nil)
	//stream.EXPECT().CloseSend().Return(nil)

	return &agentGrpc{nil, &agentClient, &metadataClient, -1, nil, agent}
}

type mockSpanGrpcClient struct {
	client *MockSpanClient
	stream *MockSpan_SendSpanClient
}

func (spanGrpcClient *mockSpanGrpcClient) SendSpan(ctx context.Context) (pb.Span_SendSpanClient, error) {
	_ = spanGrpcClient.client.EXPECT().SendSpan(gomock.Any()).Return(spanGrpcClient.stream, nil)
	return spanGrpcClient.client.SendSpan(ctx)
}

func newMockSpanGrpc(agent Agent, t *testing.T) *spanGrpc {
	ctrl := gomock.NewController(t)
	stream := NewMockSpan_SendSpanClient(ctrl)
	spanClient := mockSpanGrpcClient{NewMockSpanClient(ctrl), stream}

	stream.EXPECT().Send(gomock.Any()).Return(nil)

	return &spanGrpc{nil, &spanClient, nil, agent}
}

type mockStaGrpcClient struct {
	client *MockStatClient
	stream *MockStat_SendAgentStatClient
}

func (statGrpcClient *mockStaGrpcClient) SendAgentStat(ctx context.Context) (pb.Stat_SendAgentStatClient, error) {
	_ = statGrpcClient.client.EXPECT().SendAgentStat(gomock.Any()).Return(statGrpcClient.stream, nil)
	return statGrpcClient.client.SendAgentStat(ctx)
}

func newMockStatGrpc(agent Agent, t *testing.T) *statGrpc {
	ctrl := gomock.NewController(t)
	stream := NewMockStat_SendAgentStatClient(ctrl)
	statClient := mockStaGrpcClient{NewMockStatClient(ctrl), stream}

	stream.EXPECT().Send(gomock.Any()).Return(nil)

	return &statGrpc{nil, &statClient, nil, agent}
}

type mockRetryStaGrpcClient struct {
	client *MockStatClient
	stream *MockStat_SendAgentStatClient
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

func newRetryMockStatGrpc(agent Agent, t *testing.T) *statGrpc {
	ctrl := gomock.NewController(t)
	stream := NewMockStat_SendAgentStatClient(ctrl)
	statClient := mockRetryStaGrpcClient{NewMockStatClient(ctrl), stream, 0}

	stream.EXPECT().Send(gomock.Any()).Return(nil)
	//stream.EXPECT().Send(gomock.Any()).Return(errors.New("stat send fail"))

	return &statGrpc{nil, &statClient, nil, agent}
}
