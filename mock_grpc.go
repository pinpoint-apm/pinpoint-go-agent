package pinpoint

import (
	"context"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"google.golang.org/grpc"
)

// Canned-success stubs backing NewTestAgent. They live in the non-test build
// because NewTestAgent is exported for plugin tests, but they must not pull
// testing-only dependencies (gomock, protobuf/mock) into the shipped library.
// Test-only mocks live in mock_grpc_test.go.

type mockAgentGrpcClient struct{}

func (agentGrpcClient *mockAgentGrpcClient) RequestAgentInfo(ctx context.Context, agentinfo *pb.PAgentInfo, _ ...grpc.CallOption) (*pb.PResult, error) {
	return &pb.PResult{Success: true, Message: "success"}, nil
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

type mockMetaGrpcClient struct{}

func (metaGrpcClient *mockMetaGrpcClient) RequestApiMetaData(ctx context.Context, in *pb.PApiMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	return &pb.PResult{Success: true, Message: "success"}, nil
}

func (metaGrpcClient *mockMetaGrpcClient) RequestSqlMetaData(ctx context.Context, in *pb.PSqlMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	return &pb.PResult{Success: true, Message: "success"}, nil
}

func (metaGrpcClient *mockMetaGrpcClient) RequestSqlUidMetaData(ctx context.Context, in *pb.PSqlUidMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	return nil, nil
}

func (metaGrpcClient *mockMetaGrpcClient) RequestStringMetaData(ctx context.Context, in *pb.PStringMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	return &pb.PResult{Success: true, Message: "success"}, nil
}

func (metaGrpcClient *mockMetaGrpcClient) RequestExceptionMetaData(ctx context.Context, in *pb.PExceptionMetaData, _ ...grpc.CallOption) (*pb.PResult, error) {
	return nil, nil
}

func newMockAgentGrpc(agent *agent) *agentGrpc {
	return &agentGrpc{nil, &mockAgentGrpcClient{}, &mockMetaGrpcClient{}, -1, nil, agent}
}
