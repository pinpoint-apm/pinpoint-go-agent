package pinpoint

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"
	wrappers "google.golang.org/protobuf/types/known/wrapperspb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// supportedCommandCodes lists the commands this agent serves. It must stay in
// step with the switch in serveCommandStream.
var supportedCommandCodes = []int32{
	int32(pb.PCommandType_ECHO),
	int32(pb.PCommandType_ACTIVE_THREAD_COUNT),
	int32(pb.PCommandType_ACTIVE_THREAD_DUMP),
	int32(pb.PCommandType_ACTIVE_THREAD_LIGHT_DUMP),
}

// supportCommandCodeHeader renders supportedCommandCodes as the ';'-separated
// list the collector expects in the header.
func supportCommandCodeHeader() string {
	codes := make([]string, len(supportedCommandCodes))
	for i, c := range supportedCommandCodes {
		codes[i] = strconv.Itoa(int(c))
	}
	return strings.Join(codes, ";")
}

// commandMetadataContext is grpcMetadataContext plus the supportcommandcode
// header the HandleCommandV2 stream requires. The header replaces the V1
// handshake message: the collector registers the connection from it as soon as
// the stream opens, and rejects a V2 stream that lacks it.
func commandMetadataContext(agent *agent) context.Context {
	m := agentHeaderMap(agent)
	m[headerSupportCommandCode] = supportCommandCodeHeader()
	return metadata.NewOutgoingContext(internalContext(context.Background()), metadata.New(m))
}

type cmdGrpc struct {
	cmdConn    *grpc.ClientConn
	cmdClient  pb.ProfilerCommandServiceClient
	stream     *cmdStream
	agent      *agent
	atcStreams atcStreams
}

type cmdStream struct {
	streamHandle[pb.ProfilerCommandService_HandleCommandV2Client]
}

func newCommandGrpc(agent *agent) (*cmdGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorAgentPort)
	if err != nil {
		return nil, err
	}

	cmdClient := pb.NewProfilerCommandServiceClient(conn)
	return &cmdGrpc{cmdConn: conn, cmdClient: cmdClient, agent: agent, atcStreams: atcStreams{agent: agent}}, nil
}

func (cmdGrpc *cmdGrpc) close() {
	if cmdGrpc.cmdConn != nil {
		cmdGrpc.cmdConn.Close()
	}
}

func (cmdGrpc *cmdGrpc) newHandleCommandStream() bool {
	// The command worker sits in Recv waiting for the collector, so unlike the
	// sending streams it cannot check the age before each operation. The max
	// age is the stream deadline instead: at expiry Recv returns
	// DeadlineExceeded and runCommandService reopens the stream.
	age := newStreamAge(cmdGrpc.agent)
	var ctx context.Context
	var cancel context.CancelFunc
	if age.expiresAt.IsZero() {
		ctx, cancel = context.WithCancel(commandMetadataContext(cmdGrpc.agent))
	} else {
		ctx, cancel = context.WithDeadline(commandMetadataContext(cmdGrpc.agent), age.expiresAt)
	}
	// V2 only: the collector's V1 handler drops a stream that carries the
	// supportcommandcode header, so the RPC and the header go together.
	stream, err := cmdGrpc.cmdClient.HandleCommandV2(ctx)
	if err != nil {
		cancel()
		cmdStreamOpenLog.errorf("make command stream - %v", err)
		return false
	}

	cmdGrpc.stream = &cmdStream{newStreamHandle(stream, cancel, age)}
	return true
}

func (cmdGrpc *cmdGrpc) newCommandStreamWithRetry() *cmdStream {
	if newStreamWithRetry(cmdGrpc.agent, cmdGrpc.cmdConn, cmdGrpc.newHandleCommandStream, "command") {
		return cmdGrpc.stream
	}
	return &cmdStream{}
}

func (s *cmdStream) close() {
	s.streamHandle.close(func(c pb.ProfilerCommandService_HandleCommandV2Client) error { return c.CloseSend() }, "command")
}

// sendFailMessage rejects a command on the command stream itself, which is the
// only channel the protocol offers for a request the agent will not serve:
// sets the request id and a reason and leaves status at its default.
func (s *cmdStream) sendFailMessage(reqId int32, msg string) error {
	gCmd := &pb.PCmdMessage{
		Message: &pb.PCmdMessage_FailMessage{
			FailMessage: &pb.PCmdResponse{
				ResponseId: reqId,
				Message:    &wrappers.StringValue{Value: msg},
			},
		},
	}

	if s.open() && IsDebugLogLevelEnabled() {
		Log("grpc").Debugf("PCmdMessage: %s", gCmd.String())
	}
	return s.send(func(c pb.ProfilerCommandService_HandleCommandV2Client) error { return c.Send(gCmd) }, "cmd stream.Send()")
}

func (s *cmdStream) recvCommandRequest() (*pb.PCmdRequest, error) {
	if !s.open() {
		return nil, status.Errorf(codes.Unavailable, "command stream is nil")
	}

	gCmdReq, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}

	if IsDebugLogLevelEnabled() {
		Log("grpc").Debugf("PCmdRequest: %s", gCmdReq.String())
	}
	return gCmdReq, nil
}

type activeThreadCountStream struct {
	streams  *atcStreams
	stream   pb.ProfilerCommandService_CommandStreamActiveThreadCountClient
	reqId    int32
	actCount int32
	cancel   context.CancelFunc

	// stop is the only cross-goroutine signal to this stream, closed by
	// requestStop. cancel stays owned by the goroutine running the stream, so
	// a stop never races with the stream being opened.
	stop     chan struct{}
	stopOnce sync.Once
}

func newActiveThreadCountStream(streams *atcStreams, reqId int32) *activeThreadCountStream {
	return &activeThreadCountStream{streams: streams, reqId: reqId, stop: make(chan struct{})}
}

// openActiveThreadCountStream opens the gRPC stream for an already registered
// s and reports whether it succeeded.
func (cmdGrpc *cmdGrpc) openActiveThreadCountStream(s *activeThreadCountStream) bool {
	ctx, cancel := context.WithCancel(grpcMetadataContext(cmdGrpc.agent, -1))
	stream, err := cmdGrpc.cmdClient.CommandStreamActiveThreadCount(ctx)
	if err != nil {
		cancel()
		Log("grpc").Errorf("make active thread count stream - %v", err)
		return false
	}

	s.stream, s.cancel = stream, cancel
	return true
}

// requestStop asks the stream to finish. Safe from any goroutine, and safe to
// call more than once - a stream can be superseded while already stopping.
func (s *activeThreadCountStream) requestStop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *activeThreadCountStream) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *activeThreadCountStream) close() {
	if s.stream == nil {
		return
	}
	closeStream(s.cancel, func() error { _, err := s.stream.CloseAndRecv(); return err }, "arc stream.CloseAndRecv()")
	s.stream = nil
}

func (s *activeThreadCountStream) sendActiveThreadCount() error {
	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "active thread count stream is nil")
	}

	now := time.Now()
	activeThreadCount := s.streams.activeSpanCount(now)
	s.actCount++

	gRes := &pb.PCmdActiveThreadCountRes{
		CommonStreamResponse: &pb.PCmdStreamResponse{
			ResponseId: s.reqId,
			SequenceId: s.actCount,
			Message:    &wrappers.StringValue{Value: ""},
		},
		HistogramSchemaType: 2,
		ActiveThreadCount:   activeThreadCount,
		TimeStamp:           now.UnixNano() / int64(time.Millisecond),
	}

	if IsDebugLogLevelEnabled() {
		Log("grpc").Debugf("PCmdActiveThreadCountRes: %s", gRes.String())
	}

	err := sendStreamWithTimeout(func() error { return s.stream.Send(gRes) }, s.cancel, sendStreamTimeOut, "arc stream.Send()")
	if err != nil {
		s.cancel()
	}
	return err
}

// callCommand makes one unary command RPC under commandStreamTimeOut: res is
// the response it carries, logged at debug, and what names it in the logs.
func (cmdGrpc *cmdGrpc) callCommand(what string, res interface{ String() string }, call func(context.Context) error) {
	if IsDebugLogLevelEnabled() {
		Log("grpc").Debugf("send %s: %s", what, res.String())
	}

	ctx, cancel := context.WithTimeout(grpcMetadataContext(cmdGrpc.agent, -1), commandStreamTimeOut)
	defer cancel()

	if err := call(ctx); err != nil {
		Log("grpc").Errorf("send %s - %v", what, err)
	}
}

// dumpResponse is the common response of a thread dump: failed when the
// goroutines could not be dumped.
func dumpResponse(reqId int32, dump *goroutineDump) *pb.PCmdResponse {
	res := &pb.PCmdResponse{ResponseId: reqId, Message: &wrappers.StringValue{Value: ""}}
	if dump == nil {
		res.Status = -1
		res.Message.Value = "An error occurred while dumping Goroutine"
	}
	return res
}

func (cmdGrpc *cmdGrpc) sendActiveThreadDump(reqId int32, limit int32, threadName []string, dump *goroutineDump) {
	gRes := &pb.PCmdActiveThreadDumpRes{
		CommonResponse: dumpResponse(reqId, dump),
		ThreadDump:     makePActiveThreadDumpList(dump, int(limit), threadName),
		Type:           "Go",
		Version:        runtime.Version(),
	}
	cmdGrpc.callCommand("active thread dump", gRes, func(ctx context.Context) error {
		_, err := cmdGrpc.cmdClient.CommandActiveThreadDump(ctx, gRes)
		return err
	})
}

func makePActiveThreadDumpList(dump *goroutineDump, limit int, threadName []string) []*pb.PActiveThreadDump {
	dumpList := make([]*pb.PActiveThreadDump, 0)

	if dump != nil {
		if limit < 1 {
			limit = len(dump.goroutines)
		}

		byHeader := dump.indexByHeader(threadName)
		selected := make([]*goroutine, 0)
		for _, tn := range threadName {
			g := byHeader[tn]
			if g != nil {
				selected = append(selected, g)
			}
		}

		if IsDebugLogLevelEnabled() {
			Log("grpc").Debugf("send makePActiveThreadDumpList: %v", selected)
		}

		for i := 0; i < limit && i < len(selected); i++ {
			aDump := makePActiveThreadDump(selected[i])
			dumpList = append(dumpList, aDump)
		}
	}

	return dumpList
}

func makePActiveThreadDump(g *goroutine) *pb.PActiveThreadDump {
	aDump := &pb.PActiveThreadDump{
		StartTime: g.span.startTime.UnixNano() / int64(time.Millisecond),
		ThreadDump: &pb.PThreadDump{
			ThreadName:  g.header,
			ThreadId:    g.id,
			ThreadState: g.threadState(),
			StackTrace:  []string{validUTF8(g.buf.String())},
		},
		Sampled:       g.span.sampled,
		TransactionId: g.span.txId,
		EntryPoint:    validUTF8(g.span.entryPoint),
	}

	return aDump
}

func (cmdGrpc *cmdGrpc) sendActiveThreadLightDump(reqId int32, limit int32, dump *goroutineDump) {
	gRes := &pb.PCmdActiveThreadLightDumpRes{
		CommonResponse: dumpResponse(reqId, dump),
		ThreadDump:     makePActiveThreadLightDumpList(dump, int(limit)),
		Type:           "Go",
		Version:        runtime.Version(),
	}
	cmdGrpc.callCommand("active thread light dump", gRes, func(ctx context.Context) error {
		_, err := cmdGrpc.cmdClient.CommandActiveThreadLightDump(ctx, gRes)
		return err
	})
}

func makePActiveThreadLightDumpList(dump *goroutineDump, limit int) []*pb.PActiveThreadLightDump {
	dumpList := make([]*pb.PActiveThreadLightDump, 0)

	if dump != nil {
		if limit < 1 {
			limit = len(dump.goroutines)
		}

		for i := 0; i < limit && i < len(dump.goroutines); i++ {
			aDump := makePActiveThreadLightDump(dump.goroutines[i])
			dumpList = append(dumpList, aDump)
		}
	}

	return dumpList
}

func makePActiveThreadLightDump(g *goroutine) *pb.PActiveThreadLightDump {
	aDump := &pb.PActiveThreadLightDump{
		StartTime: g.span.startTime.UnixNano() / int64(time.Millisecond),
		ThreadDump: &pb.PThreadLightDump{
			ThreadName:  g.header,
			ThreadId:    g.id,
			ThreadState: g.threadState(),
		},
		Sampled:       g.span.sampled,
		TransactionId: g.span.txId,
		EntryPoint:    validUTF8(g.span.entryPoint),
	}

	return aDump
}

func (cmdGrpc *cmdGrpc) sendEcho(reqId int32, msg string) {
	gRes := &pb.PCmdEchoResponse{
		CommonResponse: &pb.PCmdResponse{
			ResponseId: reqId,
			Message:    &wrappers.StringValue{Value: ""}, //error message
		},
		Message: msg,
	}

	cmdGrpc.callCommand("echo response", gRes, func(ctx context.Context) error {
		_, err := cmdGrpc.cmdClient.CommandEcho(ctx, gRes)
		return err
	})
}
