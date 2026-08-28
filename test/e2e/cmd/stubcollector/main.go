// stubcollector is a minimal Pinpoint collector that accepts everything and
// records nothing. It exists so the end-to-end stack can be exercised without a
// dev collector -- run_e2e.sh --local-collector starts it. For assertions on
// what the agent actually sent, use the recording collector in test/it.
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net"
	"strconv"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

var ok = &pb.PResult{Success: true, Message: "success"}

type agentService struct{ pb.UnimplementedAgentServer }

func (agentService) RequestAgentInfo(context.Context, *pb.PAgentInfo) (*pb.PResult, error) {
	return ok, nil
}

func (agentService) PingSession(stream grpc.BidiStreamingServer[pb.PPing, pb.PPing]) error {
	for {
		if _, err := stream.Recv(); err != nil {
			return nil
		}
		if err := stream.Send(&pb.PPing{}); err != nil {
			return nil
		}
	}
}

type metadataService struct{ pb.UnimplementedMetadataServer }

func (metadataService) RequestSqlMetaData(context.Context, *pb.PSqlMetaData) (*pb.PResult, error) {
	return ok, nil
}
func (metadataService) RequestSqlUidMetaData(context.Context, *pb.PSqlUidMetaData) (*pb.PResult, error) {
	return ok, nil
}
func (metadataService) RequestApiMetaData(context.Context, *pb.PApiMetaData) (*pb.PResult, error) {
	return ok, nil
}
func (metadataService) RequestStringMetaData(context.Context, *pb.PStringMetaData) (*pb.PResult, error) {
	return ok, nil
}
func (metadataService) RequestExceptionMetaData(context.Context, *pb.PExceptionMetaData) (*pb.PResult, error) {
	return ok, nil
}

type spanService struct{ pb.UnimplementedSpanServer }

func (spanService) SendSpan(stream grpc.ClientStreamingServer[pb.PSpanMessage, emptypb.Empty]) error {
	return drain(stream)
}

func (spanService) SendSpanBatch(context.Context, *pb.PSpanMessageBatch) (*pb.PSpanResultBatch, error) {
	return &pb.PSpanResultBatch{}, nil
}

type statService struct{ pb.UnimplementedStatServer }

func (statService) SendAgentStat(stream grpc.ClientStreamingServer[pb.PStatMessage, emptypb.Empty]) error {
	return drain(stream)
}

type commandService struct {
	pb.UnimplementedProfilerCommandServiceServer
}

func (commandService) HandleCommand(stream grpc.BidiStreamingServer[pb.PCmdMessage, pb.PCmdRequest]) error {
	for {
		if _, err := stream.Recv(); err != nil {
			return nil
		}
	}
}

func (c commandService) HandleCommandV2(stream grpc.BidiStreamingServer[pb.PCmdMessage, pb.PCmdRequest]) error {
	return c.HandleCommand(stream)
}

func (commandService) CommandEcho(context.Context, *pb.PCmdEchoResponse) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (commandService) CommandStreamActiveThreadCount(stream grpc.ClientStreamingServer[pb.PCmdActiveThreadCountRes, emptypb.Empty]) error {
	return drain(stream)
}

func (commandService) CommandActiveThreadDump(context.Context, *pb.PCmdActiveThreadDumpRes) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (commandService) CommandActiveThreadLightDump(context.Context, *pb.PCmdActiveThreadLightDumpRes) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// drain reads a client stream to completion and closes it with an empty reply.
func drain[T any](stream interface {
	Recv() (T, error)
	SendAndClose(*emptypb.Empty) error
}) error {
	for {
		if _, err := stream.Recv(); err != nil {
			if err == io.EOF {
				return stream.SendAndClose(&emptypb.Empty{})
			}
			return nil
		}
	}
}

func serve(port int, register func(*grpc.Server)) {
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		log.Fatalf("listen on %d: %v", port, err)
	}
	server := grpc.NewServer()
	register(server)
	log.Printf("stub collector listening on %s", listener.Addr())
	go func() {
		if err := server.Serve(listener); err != nil {
			log.Fatalf("serve on %d: %v", port, err)
		}
	}()
}

func main() {
	agentPort := flag.Int("agent-port", 9991, "agent/metadata/command port")
	statPort := flag.Int("stat-port", 9992, "stat port")
	spanPort := flag.Int("span-port", 9993, "span port")
	flag.Parse()

	serve(*agentPort, func(s *grpc.Server) {
		pb.RegisterAgentServer(s, agentService{})
		pb.RegisterMetadataServer(s, metadataService{})
		pb.RegisterProfilerCommandServiceServer(s, commandService{})
	})
	serve(*spanPort, func(s *grpc.Server) { pb.RegisterSpanServer(s, spanService{}) })
	serve(*statPort, func(s *grpc.Server) { pb.RegisterStatServer(s, statService{}) })

	select {}
}
