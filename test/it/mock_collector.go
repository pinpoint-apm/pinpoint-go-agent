// Package it provides an in-process Pinpoint collector and the integration
// tests that drive a real agent against it. It is the Go counterpart of the
// C++ agent's test/it suite.
package it

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Endpoint names one of the three ports a real collector listens on.
type Endpoint int

const (
	// EndpointAgent carries Agent, Metadata and ProfilerCommandService.
	EndpointAgent Endpoint = iota
	// EndpointSpan carries Span.
	EndpointSpan
	// EndpointStat carries Stat.
	EndpointStat
)

func (e Endpoint) String() string {
	switch e {
	case EndpointAgent:
		return "agent"
	case EndpointSpan:
		return "span"
	case EndpointStat:
		return "stat"
	}
	return "unknown"
}

// Rpc identifies an individual RPC for deterministic fault injection.
type Rpc string

const (
	RpcAgentInfo                      Rpc = "AgentInfo"
	RpcPingSession                    Rpc = "PingSession"
	RpcSqlMetadata                    Rpc = "SqlMetadata"
	RpcSqlUidMetadata                 Rpc = "SqlUidMetadata"
	RpcApiMetadata                    Rpc = "ApiMetadata"
	RpcStringMetadata                 Rpc = "StringMetadata"
	RpcExceptionMetadata              Rpc = "ExceptionMetadata"
	RpcSendSpan                       Rpc = "SendSpan"
	RpcSendSpanBatch                  Rpc = "SendSpanBatch"
	RpcSendAgentStat                  Rpc = "SendAgentStat"
	RpcHandleCommand                  Rpc = "HandleCommand"
	RpcHandleCommandV2                Rpc = "HandleCommandV2"
	RpcCommandEcho                    Rpc = "CommandEcho"
	RpcCommandStreamActiveThreadCount Rpc = "CommandStreamActiveThreadCount"
	RpcCommandActiveThreadDump        Rpc = "CommandActiveThreadDump"
	RpcCommandActiveThreadLightDump   Rpc = "CommandActiveThreadLightDump"
)

// RpcMetadata is a copy of the client metadata attached to one gRPC call.
type RpcMetadata struct {
	md metadata.MD
}

// Value returns the first value recorded for key, and whether it was present.
func (m RpcMetadata) Value(key string) (string, bool) {
	v := m.md.Get(key)
	if len(v) == 0 {
		return "", false
	}
	return v[0], true
}

// ValueOr returns the first value recorded for key, or def when absent.
func (m RpcMetadata) ValueOr(key, def string) string {
	if v, ok := m.Value(key); ok {
		return v
	}
	return def
}

// Has reports whether key was present in the call metadata.
func (m RpcMetadata) Has(key string) bool {
	_, ok := m.Value(key)
	return ok
}

// Int64 returns the metadata value for key parsed as an int64.
func (m RpcMetadata) Int64(key string) (int64, bool) {
	v, ok := m.Value(key)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return i, true
}

// Received is a protobuf received by the mock collector plus its call headers.
type Received[T proto.Message] struct {
	Message  T
	Metadata RpcMetadata
}

// RpcResult is the outcome one mock service handler produced.
type RpcResult struct {
	Rpc     Rpc
	Code    codes.Code
	Success bool
	Message string
}

// Snapshot is an immutable copy of everything received so far.
//
// Stream slices are appended to when the RPC opens, even when the stream never
// carries a message; message slices hold a full protobuf copy of every
// request/write the server received.
type Snapshot struct {
	RpcResults []RpcResult

	AgentInfos  []Received[*pb.PAgentInfo]
	PingStreams []RpcMetadata
	Pings       []Received[*pb.PPing]

	SqlMetadata       []Received[*pb.PSqlMetaData]
	SqlUidMetadata    []Received[*pb.PSqlUidMetaData]
	ApiMetadata       []Received[*pb.PApiMetaData]
	StringMetadata    []Received[*pb.PStringMetaData]
	ExceptionMetadata []Received[*pb.PExceptionMetaData]

	SpanMessages []Received[*pb.PSpanMessage]
	SpanBatches  []Received[*pb.PSpanMessageBatch]

	StatStreams []RpcMetadata
	Stats       []Received[*pb.PStatMessage]

	CommandStreams             []RpcMetadata
	CommandStreamMessages      []Received[*pb.PCmdMessage]
	EchoResponses              []Received[*pb.PCmdEchoResponse]
	ActiveThreadCountStreams   []RpcMetadata
	ActiveThreadCountResponses []Received[*pb.PCmdActiveThreadCountRes]
	ActiveThreadDumpResponses  []Received[*pb.PCmdActiveThreadDumpRes]
	ActiveThreadLightDumps     []Received[*pb.PCmdActiveThreadLightDumpRes]
}

type faultKind int

const (
	faultFail faultKind = iota
	faultTimeout
	faultReject
)

type fault struct {
	kind  faultKind
	code  codes.Code
	msg   string
	after int
}

type endpointServer struct {
	mu       sync.Mutex
	port     int
	server   *grpc.Server
	listener net.Listener
	creds    credentials.TransportCredentials
	register func(*grpc.Server)
}

// MockCollector is an in-process Pinpoint collector used by the integration
// tests. It exposes the five services from pinpoint-grpc-idl on the same
// three-port topology as a real collector:
//
//   - Agent + Metadata + ProfilerCommandService on AgentPort()
//   - Span on SpanPort()
//   - Stat on StatPort()
//
// Every server binds to 127.0.0.1:0, so the operating system picks an
// ephemeral port. All records and waits are safe for concurrent use.
type MockCollector struct {
	mu       sync.Mutex
	snapshot Snapshot
	faults   map[Rpc][]fault

	outage    bool
	outageErr *status.Status
	// outageCh is closed for the duration of an outage so streams already in
	// flight observe it instead of blocking until their next message.
	outageCh chan struct{}

	endpoints [3]*endpointServer

	// commands carries collector-originated requests to whichever command
	// stream is currently open.
	commands chan *pb.PCmdRequest
}

// NewMockCollector returns a collector that is not listening yet.
func NewMockCollector() *MockCollector {
	c := &MockCollector{
		faults:   make(map[Rpc][]fault),
		commands: make(chan *pb.PCmdRequest, 32),
	}
	c.endpoints[EndpointAgent] = &endpointServer{register: func(s *grpc.Server) {
		pb.RegisterAgentServer(s, &agentService{c: c})
		pb.RegisterMetadataServer(s, &metadataService{c: c})
		pb.RegisterProfilerCommandServiceServer(s, &commandService{c: c})
	}}
	c.endpoints[EndpointSpan] = &endpointServer{register: func(s *grpc.Server) {
		pb.RegisterSpanServer(s, &spanService{c: c})
	}}
	c.endpoints[EndpointStat] = &endpointServer{register: func(s *grpc.Server) {
		pb.RegisterStatServer(s, &statService{c: c})
	}}
	return c
}

// Start binds all three endpoints to ephemeral ports and starts serving.
func (c *MockCollector) Start() error {
	for _, e := range c.endpoints {
		if err := e.start(0); err != nil {
			c.Shutdown()
			return err
		}
	}
	return nil
}

// UseTLS makes every endpoint serve TLS with the given certificate, so an
// agent configured with Collector.Grpc.SslEnable can be exercised end to end.
// It must be called before Start.
func (c *MockCollector) UseTLS(certFile, keyFile string) error {
	creds, err := credentials.NewServerTLSFromFile(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load collector certificate: %w", err)
	}
	for _, e := range c.endpoints {
		e.creds = creds
	}
	return nil
}

// Shutdown stops every endpoint.
func (c *MockCollector) Shutdown() {
	for _, e := range c.endpoints {
		e.stop()
	}
}

// Host returns the collector host the agent must be configured with.
func (c *MockCollector) Host() string { return "127.0.0.1" }

// AgentPort returns the Agent/Metadata/Command port.
func (c *MockCollector) AgentPort() int { return c.endpoints[EndpointAgent].boundPort() }

// SpanPort returns the Span port.
func (c *MockCollector) SpanPort() int { return c.endpoints[EndpointSpan].boundPort() }

// StatPort returns the Stat port.
func (c *MockCollector) StatPort() int { return c.endpoints[EndpointStat].boundPort() }

// StopEndpoint abruptly closes one listening endpoint and every connection on
// it, keeping the port reserved for a later StartEndpoint.
func (c *MockCollector) StopEndpoint(e Endpoint) { c.endpoints[e].stop() }

// StartEndpoint rebinds an endpoint stopped by StopEndpoint on its original port.
func (c *MockCollector) StartEndpoint(e Endpoint) error {
	s := c.endpoints[e]
	return s.start(s.boundPort())
}

// BeginOutage enters a sustained collector outage: every stream in flight is
// released and every subsequent RPC on all three endpoints fails with code
// until EndOutage is called. Unlike FailNext the fault is not consumed per
// call; unlike StopEndpoint the ports stay open, so the agent observes an
// unhealthy collector rather than a dead host. Queued
// FailNext/TimeoutNext/RejectNext faults are left untouched and apply again
// once the outage ends, and every failed call is still recorded in
// Snapshot.RpcResults.
func (c *MockCollector) BeginOutage(code ...codes.Code) {
	st := status.New(codes.Unavailable, "injected collector outage")
	if len(code) > 0 {
		st = status.New(code[0], "injected collector outage")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.outage {
		return
	}
	c.outage = true
	c.outageErr = st
	c.outageCh = make(chan struct{})
	close(c.outageCh)
}

// EndOutage ends a BeginOutage period.
func (c *MockCollector) EndOutage() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outage = false
	c.outageErr = nil
	c.outageCh = nil
}

// FailNext returns a gRPC error from the next matching RPC or stream. For
// streams after delays the error until that many client messages have been
// recorded; zero rejects the stream as it opens.
func (c *MockCollector) FailNext(rpc Rpc, code codes.Code, msg string, after ...int) {
	f := fault{kind: faultFail, code: code, msg: msg}
	if len(after) > 0 {
		f.after = after[0]
	}
	c.queueFault(rpc, f)
}

// TimeoutNext withholds the next response until the client deadline or
// cancellation. after has the same stream semantics as FailNext.
func (c *MockCollector) TimeoutNext(rpc Rpc, after ...int) {
	f := fault{kind: faultTimeout}
	if len(after) > 0 {
		f.after = after[0]
	}
	c.queueFault(rpc, f)
}

// RejectNext returns codes.OK with PResult.Success=false on the next unary RPC.
func (c *MockCollector) RejectNext(rpc Rpc, msg string) {
	c.queueFault(rpc, fault{kind: faultReject, msg: msg})
}

// SendCommand queues a collector-originated request for the active command stream.
func (c *MockCollector) SendCommand(req *pb.PCmdRequest) {
	select {
	case c.commands <- req:
	default:
	}
}

// SendEchoCommand queues an ECHO command.
func (c *MockCollector) SendEchoCommand(requestID int32, msg string) {
	c.SendCommand(&pb.PCmdRequest{
		RequestId: requestID,
		Command: &pb.PCmdRequest_CommandEcho{
			CommandEcho: &pb.PCmdEcho{Message: msg},
		},
	})
}

// SendActiveThreadCountCommand queues an ACTIVE_THREAD_COUNT command.
func (c *MockCollector) SendActiveThreadCountCommand(requestID int32) {
	c.SendCommand(&pb.PCmdRequest{
		RequestId: requestID,
		Command: &pb.PCmdRequest_CommandActiveThreadCount{
			CommandActiveThreadCount: &pb.PCmdActiveThreadCount{},
		},
	})
}

// SendActiveThreadDumpCommand queues an ACTIVE_THREAD_DUMP command.
func (c *MockCollector) SendActiveThreadDumpCommand(requestID int32, limit int32) {
	c.SendCommand(&pb.PCmdRequest{
		RequestId: requestID,
		Command: &pb.PCmdRequest_CommandActiveThreadDump{
			CommandActiveThreadDump: &pb.PCmdActiveThreadDump{Limit: limit},
		},
	})
}

// SendActiveThreadLightDumpCommand queues an ACTIVE_THREAD_LIGHT_DUMP command.
func (c *MockCollector) SendActiveThreadLightDumpCommand(requestID int32, limit int32) {
	c.SendCommand(&pb.PCmdRequest{
		RequestId: requestID,
		Command: &pb.PCmdRequest_CommandActiveThreadLightDump{
			CommandActiveThreadLightDump: &pb.PCmdActiveThreadLightDump{Limit: limit},
		},
	})
}

// Snapshot returns a copy of everything received so far.
func (c *MockCollector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.snapshot
	s.RpcResults = append([]RpcResult(nil), c.snapshot.RpcResults...)
	s.AgentInfos = append([]Received[*pb.PAgentInfo](nil), c.snapshot.AgentInfos...)
	s.PingStreams = append([]RpcMetadata(nil), c.snapshot.PingStreams...)
	s.Pings = append([]Received[*pb.PPing](nil), c.snapshot.Pings...)
	s.SqlMetadata = append([]Received[*pb.PSqlMetaData](nil), c.snapshot.SqlMetadata...)
	s.SqlUidMetadata = append([]Received[*pb.PSqlUidMetaData](nil), c.snapshot.SqlUidMetadata...)
	s.ApiMetadata = append([]Received[*pb.PApiMetaData](nil), c.snapshot.ApiMetadata...)
	s.StringMetadata = append([]Received[*pb.PStringMetaData](nil), c.snapshot.StringMetadata...)
	s.ExceptionMetadata = append([]Received[*pb.PExceptionMetaData](nil), c.snapshot.ExceptionMetadata...)
	s.SpanMessages = append([]Received[*pb.PSpanMessage](nil), c.snapshot.SpanMessages...)
	s.SpanBatches = append([]Received[*pb.PSpanMessageBatch](nil), c.snapshot.SpanBatches...)
	s.StatStreams = append([]RpcMetadata(nil), c.snapshot.StatStreams...)
	s.Stats = append([]Received[*pb.PStatMessage](nil), c.snapshot.Stats...)
	s.CommandStreams = append([]RpcMetadata(nil), c.snapshot.CommandStreams...)
	s.CommandStreamMessages = append([]Received[*pb.PCmdMessage](nil), c.snapshot.CommandStreamMessages...)
	s.EchoResponses = append([]Received[*pb.PCmdEchoResponse](nil), c.snapshot.EchoResponses...)
	s.ActiveThreadCountStreams = append([]RpcMetadata(nil), c.snapshot.ActiveThreadCountStreams...)
	s.ActiveThreadCountResponses = append([]Received[*pb.PCmdActiveThreadCountRes](nil), c.snapshot.ActiveThreadCountResponses...)
	s.ActiveThreadDumpResponses = append([]Received[*pb.PCmdActiveThreadDumpRes](nil), c.snapshot.ActiveThreadDumpResponses...)
	s.ActiveThreadLightDumps = append([]Received[*pb.PCmdActiveThreadLightDumpRes](nil), c.snapshot.ActiveThreadLightDumps...)
	return s
}

// WaitFor polls until predicate matches a coherent snapshot, or timeout elapses.
func (c *MockCollector) WaitFor(predicate func(Snapshot) bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate(c.Snapshot()) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return predicate(c.Snapshot())
}

// --- internals -------------------------------------------------------------

func (e *endpointServer) start(port int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.server != nil {
		return nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", port, err)
	}
	e.listener = ln
	e.port = ln.Addr().(*net.TCPAddr).Port
	var opts []grpc.ServerOption
	if e.creds != nil {
		opts = append(opts, grpc.Creds(e.creds))
	}
	e.server = grpc.NewServer(opts...)
	e.register(e.server)
	go e.server.Serve(ln)
	return nil
}

func (e *endpointServer) stop() {
	e.mu.Lock()
	server := e.server
	e.server = nil
	e.listener = nil
	e.mu.Unlock()
	if server != nil {
		server.Stop()
	}
}

func (e *endpointServer) boundPort() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.port
}

func (c *MockCollector) queueFault(rpc Rpc, f fault) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.faults[rpc] = append(c.faults[rpc], f)
}

// takeFault pops the head fault queued for rpc when it is due after msgCount
// stream messages. Unary calls pass 0.
func (c *MockCollector) takeFault(rpc Rpc, msgCount int) (fault, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.faults[rpc]
	if len(q) == 0 || q[0].after > msgCount {
		return fault{}, false
	}
	f := q[0]
	c.faults[rpc] = q[1:]
	return f, true
}

func (c *MockCollector) outageState() (*status.Status, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.outage {
		return nil, nil
	}
	return c.outageErr, c.outageCh
}

func (c *MockCollector) record(fn func(*Snapshot)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(&c.snapshot)
}

func (c *MockCollector) addResult(rpc Rpc, code codes.Code, success bool, msg string) {
	c.record(func(s *Snapshot) {
		s.RpcResults = append(s.RpcResults, RpcResult{Rpc: rpc, Code: code, Success: success, Message: msg})
	})
}

func mdOf(ctx context.Context) RpcMetadata {
	md, _ := metadata.FromIncomingContext(ctx)
	return RpcMetadata{md: md.Copy()}
}

func clone[T proto.Message](m T) T {
	return proto.Clone(m).(T)
}

// waitForCancel blocks until the client gives up (deadline or cancellation) or
// an outage releases the call, and returns the recorded status code.
//
// A server context reports Canceled for the RST_STREAM a client sends when its
// deadline expires, so an expired deadline is reported as DeadlineExceeded --
// the code the client itself observed.
func (c *MockCollector) waitForCancel(ctx context.Context, outageCh <-chan struct{}) codes.Code {
	select {
	case <-ctx.Done():
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return codes.DeadlineExceeded
		}
		return status.FromContextError(ctx.Err()).Code()
	case <-outageCh:
		return codes.Unavailable
	}
}

// applyUnary resolves the fault state for a unary call and returns the PResult
// and error the handler must produce.
func (c *MockCollector) applyUnary(ctx context.Context, rpc Rpc) (*pb.PResult, error) {
	if st, _ := c.outageState(); st != nil {
		c.addResult(rpc, st.Code(), false, st.Message())
		return nil, st.Err()
	}
	if f, ok := c.takeFault(rpc, 0); ok {
		switch f.kind {
		case faultFail:
			c.addResult(rpc, f.code, false, f.msg)
			return nil, status.Error(f.code, f.msg)
		case faultTimeout:
			code := c.waitForCancel(ctx, nil)
			c.addResult(rpc, code, false, "injected timeout")
			return nil, status.Error(code, "injected timeout")
		case faultReject:
			c.addResult(rpc, codes.OK, false, f.msg)
			return &pb.PResult{Success: false, Message: f.msg}, nil
		}
	}
	c.addResult(rpc, codes.OK, true, "success")
	return &pb.PResult{Success: true, Message: "success"}, nil
}

// applyUnaryEmpty is applyUnary for the command RPCs, which answer with Empty
// rather than PResult.
func (c *MockCollector) applyUnaryEmpty(ctx context.Context, rpc Rpc) (*emptypb.Empty, error) {
	if st, _ := c.outageState(); st != nil {
		c.addResult(rpc, st.Code(), false, st.Message())
		return nil, st.Err()
	}
	if f, ok := c.takeFault(rpc, 0); ok {
		switch f.kind {
		case faultFail:
			c.addResult(rpc, f.code, false, f.msg)
			return nil, status.Error(f.code, f.msg)
		case faultTimeout:
			code := c.waitForCancel(ctx, nil)
			c.addResult(rpc, code, false, "injected timeout")
			return nil, status.Error(code, "injected timeout")
		case faultReject:
			c.addResult(rpc, codes.OK, false, f.msg)
			return &emptypb.Empty{}, nil
		}
	}
	c.addResult(rpc, codes.OK, true, "success")
	return &emptypb.Empty{}, nil
}

// streamGate is the shared stream-side fault handling. It is consulted when a
// stream opens (msgCount 0) and after every recorded message.
type streamGate struct {
	c        *MockCollector
	rpc      Rpc
	ctx      context.Context
	msgCount int
}

// check reports the error the stream must terminate with, or nil to continue.
func (g *streamGate) check() error {
	if st, _ := g.c.outageState(); st != nil {
		g.c.addResult(g.rpc, st.Code(), false, st.Message())
		return st.Err()
	}
	f, ok := g.c.takeFault(g.rpc, g.msgCount)
	if !ok {
		return nil
	}
	switch f.kind {
	case faultFail, faultReject:
		code := f.code
		if f.kind == faultReject {
			code = codes.Internal
		}
		g.c.addResult(g.rpc, code, false, f.msg)
		return status.Error(code, f.msg)
	case faultTimeout:
		_, ch := g.c.outageState()
		code := g.c.waitForCancel(g.ctx, ch)
		g.c.addResult(g.rpc, code, false, "injected timeout")
		return status.Error(code, "injected timeout")
	}
	return nil
}

// done records the normal completion of a stream.
func (g *streamGate) done(err error) error {
	if err != nil {
		g.c.addResult(g.rpc, status.Code(err), false, err.Error())
		return err
	}
	g.c.addResult(g.rpc, codes.OK, true, "success")
	return nil
}

// --- Agent service ---------------------------------------------------------

type agentService struct {
	pb.UnimplementedAgentServer
	c *MockCollector
}

func (s *agentService) RequestAgentInfo(ctx context.Context, in *pb.PAgentInfo) (*pb.PResult, error) {
	md := mdOf(ctx)
	msg := clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.AgentInfos = append(snap.AgentInfos, Received[*pb.PAgentInfo]{msg, md})
	})
	return s.c.applyUnary(ctx, RpcAgentInfo)
}

func (s *agentService) PingSession(stream grpc.BidiStreamingServer[pb.PPing, pb.PPing]) error {
	md := mdOf(stream.Context())
	s.c.record(func(snap *Snapshot) {
		snap.PingStreams = append(snap.PingStreams, md)
	})
	g := &streamGate{c: s.c, rpc: RpcPingSession, ctx: stream.Context()}
	if err := g.check(); err != nil {
		return err
	}
	for {
		ping, err := stream.Recv()
		if err != nil {
			return g.done(nil)
		}
		msg := clone(ping)
		s.c.record(func(snap *Snapshot) {
			snap.Pings = append(snap.Pings, Received[*pb.PPing]{msg, md})
		})
		g.msgCount++
		if err := g.check(); err != nil {
			return err
		}
		if err := stream.Send(&pb.PPing{}); err != nil {
			return g.done(nil)
		}
	}
}

// --- Metadata service ------------------------------------------------------

type metadataService struct {
	pb.UnimplementedMetadataServer
	c *MockCollector
}

func (s *metadataService) RequestSqlMetaData(ctx context.Context, in *pb.PSqlMetaData) (*pb.PResult, error) {
	md, msg := mdOf(ctx), clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.SqlMetadata = append(snap.SqlMetadata, Received[*pb.PSqlMetaData]{msg, md})
	})
	return s.c.applyUnary(ctx, RpcSqlMetadata)
}

func (s *metadataService) RequestSqlUidMetaData(ctx context.Context, in *pb.PSqlUidMetaData) (*pb.PResult, error) {
	md, msg := mdOf(ctx), clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.SqlUidMetadata = append(snap.SqlUidMetadata, Received[*pb.PSqlUidMetaData]{msg, md})
	})
	return s.c.applyUnary(ctx, RpcSqlUidMetadata)
}

func (s *metadataService) RequestApiMetaData(ctx context.Context, in *pb.PApiMetaData) (*pb.PResult, error) {
	md, msg := mdOf(ctx), clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.ApiMetadata = append(snap.ApiMetadata, Received[*pb.PApiMetaData]{msg, md})
	})
	return s.c.applyUnary(ctx, RpcApiMetadata)
}

func (s *metadataService) RequestStringMetaData(ctx context.Context, in *pb.PStringMetaData) (*pb.PResult, error) {
	md, msg := mdOf(ctx), clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.StringMetadata = append(snap.StringMetadata, Received[*pb.PStringMetaData]{msg, md})
	})
	return s.c.applyUnary(ctx, RpcStringMetadata)
}

func (s *metadataService) RequestExceptionMetaData(ctx context.Context, in *pb.PExceptionMetaData) (*pb.PResult, error) {
	md, msg := mdOf(ctx), clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.ExceptionMetadata = append(snap.ExceptionMetadata, Received[*pb.PExceptionMetaData]{msg, md})
	})
	return s.c.applyUnary(ctx, RpcExceptionMetadata)
}

// --- Span service ----------------------------------------------------------

type spanService struct {
	pb.UnimplementedSpanServer
	c *MockCollector
}

func (s *spanService) SendSpan(stream grpc.ClientStreamingServer[pb.PSpanMessage, emptypb.Empty]) error {
	md := mdOf(stream.Context())
	g := &streamGate{c: s.c, rpc: RpcSendSpan, ctx: stream.Context()}
	if err := g.check(); err != nil {
		return err
	}
	for {
		span, err := stream.Recv()
		if err != nil {
			_ = g.done(nil)
			return stream.SendAndClose(&emptypb.Empty{})
		}
		msg := clone(span)
		s.c.record(func(snap *Snapshot) {
			snap.SpanMessages = append(snap.SpanMessages, Received[*pb.PSpanMessage]{msg, md})
		})
		g.msgCount++
		if err := g.check(); err != nil {
			return err
		}
	}
}

func (s *spanService) SendSpanBatch(ctx context.Context, in *pb.PSpanMessageBatch) (*pb.PSpanResultBatch, error) {
	md, msg := mdOf(ctx), clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.SpanBatches = append(snap.SpanBatches, Received[*pb.PSpanMessageBatch]{msg, md})
	})
	if st, _ := s.c.outageState(); st != nil {
		s.c.addResult(RpcSendSpanBatch, st.Code(), false, st.Message())
		return nil, st.Err()
	}
	if f, ok := s.c.takeFault(RpcSendSpanBatch, 0); ok {
		switch f.kind {
		case faultFail:
			s.c.addResult(RpcSendSpanBatch, f.code, false, f.msg)
			return nil, status.Error(f.code, f.msg)
		case faultTimeout:
			_, ch := s.c.outageState()
			code := s.c.waitForCancel(ctx, ch)
			s.c.addResult(RpcSendSpanBatch, code, false, "injected timeout")
			return nil, status.Error(code, "injected timeout")
		case faultReject:
			s.c.addResult(RpcSendSpanBatch, codes.OK, false, f.msg)
			return &pb.PSpanResultBatch{}, nil
		}
	}
	s.c.addResult(RpcSendSpanBatch, codes.OK, true, "success")
	return &pb.PSpanResultBatch{}, nil
}

// --- Stat service ----------------------------------------------------------

type statService struct {
	pb.UnimplementedStatServer
	c *MockCollector
}

func (s *statService) SendAgentStat(stream grpc.ClientStreamingServer[pb.PStatMessage, emptypb.Empty]) error {
	md := mdOf(stream.Context())
	s.c.record(func(snap *Snapshot) {
		snap.StatStreams = append(snap.StatStreams, md)
	})
	g := &streamGate{c: s.c, rpc: RpcSendAgentStat, ctx: stream.Context()}
	if err := g.check(); err != nil {
		return err
	}
	for {
		stat, err := stream.Recv()
		if err != nil {
			_ = g.done(nil)
			return stream.SendAndClose(&emptypb.Empty{})
		}
		msg := clone(stat)
		s.c.record(func(snap *Snapshot) {
			snap.Stats = append(snap.Stats, Received[*pb.PStatMessage]{msg, md})
		})
		g.msgCount++
		if err := g.check(); err != nil {
			return err
		}
	}
}

// --- ProfilerCommandService ------------------------------------------------

type commandService struct {
	pb.UnimplementedProfilerCommandServiceServer
	c *MockCollector
}

func (s *commandService) HandleCommand(stream grpc.BidiStreamingServer[pb.PCmdMessage, pb.PCmdRequest]) error {
	return s.handleCommand(RpcHandleCommand, stream)
}

func (s *commandService) HandleCommandV2(stream grpc.BidiStreamingServer[pb.PCmdMessage, pb.PCmdRequest]) error {
	return s.handleCommand(RpcHandleCommandV2, stream)
}

func (s *commandService) handleCommand(rpc Rpc, stream grpc.BidiStreamingServer[pb.PCmdMessage, pb.PCmdRequest]) error {
	md := mdOf(stream.Context())
	s.c.record(func(snap *Snapshot) {
		snap.CommandStreams = append(snap.CommandStreams, md)
	})
	g := &streamGate{c: s.c, rpc: rpc, ctx: stream.Context()}
	if err := g.check(); err != nil {
		return err
	}

	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			cloned := clone(msg)
			s.c.record(func(snap *Snapshot) {
				snap.CommandStreamMessages = append(snap.CommandStreamMessages, Received[*pb.PCmdMessage]{cloned, md})
			})
		}
	}()

	for {
		_, outageCh := s.c.outageState()
		select {
		case <-recvDone:
			return g.done(nil)
		case <-stream.Context().Done():
			return g.done(nil)
		case <-outageCh:
			st, _ := s.c.outageState()
			if st == nil {
				continue
			}
			s.c.addResult(rpc, st.Code(), false, st.Message())
			return st.Err()
		case req := <-s.c.commands:
			if err := stream.Send(req); err != nil {
				return g.done(nil)
			}
		case <-time.After(50 * time.Millisecond):
			// Re-evaluate the outage channel, which is created on demand.
		}
	}
}

func (s *commandService) CommandEcho(ctx context.Context, in *pb.PCmdEchoResponse) (*emptypb.Empty, error) {
	md, msg := mdOf(ctx), clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.EchoResponses = append(snap.EchoResponses, Received[*pb.PCmdEchoResponse]{msg, md})
	})
	return s.c.applyUnaryEmpty(ctx, RpcCommandEcho)
}

func (s *commandService) CommandStreamActiveThreadCount(stream grpc.ClientStreamingServer[pb.PCmdActiveThreadCountRes, emptypb.Empty]) error {
	md := mdOf(stream.Context())
	s.c.record(func(snap *Snapshot) {
		snap.ActiveThreadCountStreams = append(snap.ActiveThreadCountStreams, md)
	})
	g := &streamGate{c: s.c, rpc: RpcCommandStreamActiveThreadCount, ctx: stream.Context()}
	if err := g.check(); err != nil {
		return err
	}
	for {
		res, err := stream.Recv()
		if err != nil {
			_ = g.done(nil)
			return stream.SendAndClose(&emptypb.Empty{})
		}
		msg := clone(res)
		s.c.record(func(snap *Snapshot) {
			snap.ActiveThreadCountResponses = append(snap.ActiveThreadCountResponses, Received[*pb.PCmdActiveThreadCountRes]{msg, md})
		})
		g.msgCount++
		if err := g.check(); err != nil {
			return err
		}
	}
}

func (s *commandService) CommandActiveThreadDump(ctx context.Context, in *pb.PCmdActiveThreadDumpRes) (*emptypb.Empty, error) {
	md, msg := mdOf(ctx), clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.ActiveThreadDumpResponses = append(snap.ActiveThreadDumpResponses, Received[*pb.PCmdActiveThreadDumpRes]{msg, md})
	})
	return s.c.applyUnaryEmpty(ctx, RpcCommandActiveThreadDump)
}

func (s *commandService) CommandActiveThreadLightDump(ctx context.Context, in *pb.PCmdActiveThreadLightDumpRes) (*emptypb.Empty, error) {
	md, msg := mdOf(ctx), clone(in)
	s.c.record(func(snap *Snapshot) {
		snap.ActiveThreadLightDumps = append(snap.ActiveThreadLightDumps, Received[*pb.PCmdActiveThreadLightDumpRes]{msg, md})
	})
	return s.c.applyUnaryEmpty(ctx, RpcCommandActiveThreadLightDump)
}
