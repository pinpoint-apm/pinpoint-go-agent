package pinpoint

import (
	"context"
	"testing"

	gomock "github.com/golang/mock/gomock"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
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
	agent.startTime = 12345
	agent.sequence = 1

	return &agent
}

func (agent *mockAgent) setMockAgentGrpc(t *testing.T) {
	agent.agentGrpc = newMockAgentGrpc(agent, t)
}

func (agent *mockAgent) setMockSpanGrpc(t *testing.T) {
	agent.spanGrpc = newMockSpanGrpc(agent, t)
}

func (agent *mockAgent) setMockStatGrpc(t *testing.T) {
	agent.statGrpc = newMockStatGrpc(agent, t)
}

func (agent *mockAgent) Shutdown() {
}

func (agent *mockAgent) NewSpanTracer(operation string) Tracer {
	return newNoopSpan(agent)
}

func (agent *mockAgent) NewSpanTracerWithReader(operation string, reader DistributedTracingContextReader) Tracer {
	return newNoopSpan(agent)
}

func (agent *mockAgent) RegisterSpanApiId(descriptor string, apiType int) int32 {
	return 1
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

type mockPingStreamInvoker struct {
	stream *MockAgent_PingSessionClient
}

func (invoker *mockPingStreamInvoker) Send(p *pb.PPing) error {
	invoker.stream.EXPECT().Send(gomock.Any()).Return(nil)
	return invoker.stream.Send(p)
}

func (invoker *mockPingStreamInvoker) Recv() (*pb.PPing, error) {
	invoker.stream.EXPECT().Recv().Return(nil)
	return invoker.stream.Recv()
}

func (invoker *mockPingStreamInvoker) CloseSend() error {
	invoker.stream.EXPECT().CloseSend().Return(nil)
	return invoker.stream.CloseSend()
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
	return &agentGrpc{nil, &agentClient, &metadataClient, -1, &mockPingStreamInvoker{stream}, agent}
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
	return &spanGrpc{nil, &spanClient, &mockSpanStreamInvoker{stream}, agent}
}

type mockSpanStreamInvoker struct {
	stream *MockSpan_SendSpanClient
}

func (invoker *mockSpanStreamInvoker) Send(span *pb.PSpanMessage) error {
	invoker.stream.EXPECT().Send(gomock.Any()).Return(nil)
	return invoker.stream.Send(span)
}

func (invoker *mockSpanStreamInvoker) CloseAndRecv() error {
	invoker.stream.EXPECT().CloseAndRecv().Return(nil)
	return nil
}

func (invoker *mockSpanStreamInvoker) CloseSend() error {
	invoker.stream.EXPECT().CloseSend().Return(nil)
	return nil
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
	return &statGrpc{nil, &statClient, &mockStatStreamInvoker{stream}, agent}
}

type mockStatStreamInvoker struct {
	stream *MockStat_SendAgentStatClient
}

func (invoker *mockStatStreamInvoker) Send(span *pb.PStatMessage) error {
	invoker.stream.EXPECT().Send(gomock.Any()).Return(nil)
	return invoker.stream.Send(span)
}

func (invoker *mockStatStreamInvoker) CloseAndRecv() error {
	invoker.stream.EXPECT().CloseAndRecv().Return(nil)
	return nil
}

func (invoker *mockStatStreamInvoker) CloseSend() error {
	invoker.stream.EXPECT().CloseSend().Return(nil)
	return nil
}
