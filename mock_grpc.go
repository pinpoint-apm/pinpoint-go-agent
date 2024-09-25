package pinpoint

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	lru "github.com/hashicorp/golang-lru"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/pinpoint-apm/pinpoint-go-agent/protobuf/mock"
)

func newTestAgent(config *Config) *agent {
	a := &agent{
		appName:   "testApp",
		agentID:   "testAgent",
		appType:   ServiceTypeGoApp,
		startTime: time.Now().UnixNano() / int64(time.Millisecond),
		enable:    true,
		spanChan:  make(chan *spanChunk, cacheSize),
		metaChan:  make(chan interface{}, cacheSize),
		sampler:   newBasicTraceSampler(newRateSampler(1)),
		config:    config,
	}
	a.errorCache, _ = lru.New(cacheSize)
	a.sqlCache, _ = lru.New(cacheSize)
	a.sqlUidCache, _ = lru.New(cacheSize)
	a.apiCache, _ = lru.New(cacheSize)
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

type mockSpanGrpcClient struct {
	client *mock.MockSpanClient
	stream *mock.MockSpan_SendSpanClient
}

func (spanGrpcClient *mockSpanGrpcClient) SendSpan(ctx context.Context) (pb.Span_SendSpanClient, error) {
	_ = spanGrpcClient.client.EXPECT().SendSpan(gomock.Any()).Return(spanGrpcClient.stream, nil)
	return spanGrpcClient.client.SendSpan(ctx)
}

func newMockSpanGrpc(agent *agent, t *testing.T) *spanGrpc {
	ctrl := gomock.NewController(t)
	stream := mock.NewMockSpan_SendSpanClient(ctrl)
	spanClient := mockSpanGrpcClient{mock.NewMockSpanClient(ctrl), stream}

	stream.EXPECT().Send(gomock.Any()).Return(nil)

	return &spanGrpc{nil, &spanClient, nil, agent}
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
