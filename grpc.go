package pinpoint

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/golang/protobuf/ptypes/wrappers"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	headerAppName   = "applicationname"
	headerAgentID   = "agentid"
	headerAgentName = "agentname"
	headerStartTime = "starttime"
	headerSocketID  = "socketid"
)

func grpcMetadataContext(agent *agent, socketId int64) context.Context {
	m := map[string]string{}

	m[headerAppName] = agent.appName
	m[headerAgentID] = agent.agentID
	m[headerStartTime] = strconv.FormatInt(agent.startTime, 10)
	if agent.agentName != "" {
		m[headerAgentName] = agent.agentName
	}
	if socketId > 0 {
		m[headerSocketID] = strconv.FormatInt(socketId, 10)
	}

	md := metadata.New(m)
	return metadata.NewOutgoingContext(context.Background(), md)
}

func backOffSleep(attempt int) time.Duration {
	base := float64(1 * time.Second)
	max := float64(60 * time.Second)

	dur := base * math.Pow(2, float64(attempt))
	if dur > max {
		dur = max
	}

	return time.Duration(rand.Float64()*(dur-base) + base)
}

type agentClient interface {
	RequestAgentInfo(ctx context.Context, agentInfo *pb.PAgentInfo) (*pb.PResult, error)
	PingSession(ctx context.Context) (pb.Agent_PingSessionClient, error)
}

type agentGrpcClient struct {
	client pb.AgentClient
}

func (agentGrpcClient *agentGrpcClient) RequestAgentInfo(ctx context.Context, agentInfo *pb.PAgentInfo) (*pb.PResult, error) {
	result, err := agentGrpcClient.client.RequestAgentInfo(ctx, agentInfo)
	return result, err
}

func (agentGrpcClient *agentGrpcClient) PingSession(ctx context.Context) (pb.Agent_PingSessionClient, error) {
	session, err := agentGrpcClient.client.PingSession(ctx)
	return session, err
}

type metaClient interface {
	RequestApiMetaData(ctx context.Context, in *pb.PApiMetaData) (*pb.PResult, error)
	RequestSqlMetaData(ctx context.Context, in *pb.PSqlMetaData) (*pb.PResult, error)
	RequestSqlUidMetaData(ctx context.Context, in *pb.PSqlUidMetaData) (*pb.PResult, error)
	RequestStringMetaData(ctx context.Context, in *pb.PStringMetaData) (*pb.PResult, error)
	RequestExceptionMetaData(ctx context.Context, in *pb.PExceptionMetaData) (*pb.PResult, error)
}

type metaGrpcClient struct {
	client pb.MetadataClient
}

func (metaGrpcClient *metaGrpcClient) RequestApiMetaData(ctx context.Context, in *pb.PApiMetaData) (*pb.PResult, error) {
	result, err := metaGrpcClient.client.RequestApiMetaData(ctx, in)
	return result, err
}

func (metaGrpcClient *metaGrpcClient) RequestSqlMetaData(ctx context.Context, in *pb.PSqlMetaData) (*pb.PResult, error) {
	result, err := metaGrpcClient.client.RequestSqlMetaData(ctx, in)
	return result, err
}

func (metaGrpcClient *metaGrpcClient) RequestSqlUidMetaData(ctx context.Context, in *pb.PSqlUidMetaData) (*pb.PResult, error) {
	result, err := metaGrpcClient.client.RequestSqlUidMetaData(ctx, in)
	return result, err
}

func (metaGrpcClient *metaGrpcClient) RequestStringMetaData(ctx context.Context, in *pb.PStringMetaData) (*pb.PResult, error) {
	result, err := metaGrpcClient.client.RequestStringMetaData(ctx, in)
	return result, err
}

func (metaGrpcClient *metaGrpcClient) RequestExceptionMetaData(ctx context.Context, in *pb.PExceptionMetaData) (*pb.PResult, error) {
	result, err := metaGrpcClient.client.RequestExceptionMetaData(ctx, in)
	return result, err
}

const (
	agentGrpcTimeOut      = 60 * time.Second
	sendStreamTimeOut     = 5 * time.Second
	closeStreamTimeOut    = 1 * time.Second
	grpcFlowControlWindow = 1 * 1024 * 1024
	grpcWriteBufferSize   = 1 * 1024 * 1024
	grpcMaxMessageSize    = 4 * 1024 * 1024
	grpcMaxHeaderListSize = 8 * 1024
)

var kacp = keepalive.ClientParameters{
	Time:                30 * time.Second,
	Timeout:             60 * time.Second,
	PermitWithoutStream: true,
}

func connectCollector(config *Config, portOption string) (*grpc.ClientConn, error) {
	opts := make([]grpc.DialOption, 0)
	opts = append(opts, grpc.WithKeepaliveParams(kacp))
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithInitialWindowSize(grpcFlowControlWindow))
	opts = append(opts, grpc.WithWriteBufferSize(grpcWriteBufferSize))
	opts = append(opts, grpc.WithMaxHeaderListSize(grpcMaxHeaderListSize))
	opts = append(opts, grpc.WithDefaultCallOptions(
		grpc.MaxCallSendMsgSize(grpcMaxMessageSize),
		grpc.MaxCallRecvMsgSize(grpcMaxMessageSize)))

	addr := serverAddr(config, portOption)
	Log("grpc").Infof("connect to collector: %s", addr)
	conn, err := grpc.Dial(addr, opts...)
	if err != nil {
		Log("grpc").Errorf("connect to collector - %s, %v", addr, err)
	}
	return conn, err
}

func serverAddr(config *Config, portOption string) string {
	return fmt.Sprintf("%s:%d", config.String(CfgCollectorHost), config.Int(portOption))
}

type agentGrpc struct {
	agentConn    *grpc.ClientConn
	agentClient  agentClient
	metaClient   metaClient
	pingSocketId int64
	pingStream   *pingStream
	agent        *agent
}

func newAgentGrpc(agent *agent) (*agentGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorAgentPort)
	if err != nil {
		return nil, err
	}

	return &agentGrpc{
		agentConn:   conn,
		agentClient: &agentGrpcClient{pb.NewAgentClient(conn)},
		metaClient:  &metaGrpcClient{pb.NewMetadataClient(conn)},
		agent:       agent,
	}, nil
}

func getHostName() string {
	if hostName, err := os.Hostname(); err == nil {
		return hostName
	}
	return "unknown host"
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String()
}

func makeGoLibraryInfo() *pb.PServiceInfo {
	libs := make([]string, 0)
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			libs = append(libs, dep.Path+" ("+dep.Version+")")
		}
	}

	return &pb.PServiceInfo{
		ServiceName: "Go (" + runtime.GOOS + ", " + runtime.GOARCH + ", " + runtime.GOROOT() + ")",
		ServiceLib:  libs,
	}
}

func (agentGrpc *agentGrpc) makeAgentInfo() (context.Context, *pb.PAgentInfo) {
	agentInfo := &pb.PAgentInfo{
		Hostname:     getHostName(),
		Ip:           getOutboundIP(),
		ServiceType:  agentGrpc.agent.appType,
		Pid:          int32(os.Getpid()),
		AgentVersion: Version,
		VmVersion:    runtime.Version(),

		ServerMetaData: &pb.PServerMetaData{
			ServerInfo:  "Go Application",
			VmArg:       os.Args[1:],
			ServiceInfo: []*pb.PServiceInfo{makeGoLibraryInfo()},
		},

		JvmInfo: &pb.PJvmInfo{
			Version:   0,
			VmVersion: fmt.Sprintf("%s(%d)", runtime.Version(), goIdOffset),
			GcType:    pb.PJvmGcType_JVM_GC_TYPE_CMS,
		},
		Container: agentGrpc.agent.config.Bool(CfgIsContainerEnv),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("agent info: %s", agentInfo.String())
	}

	ctx := grpcMetadataContext(agentGrpc.agent, -1)
	return ctx, agentInfo
}

func (agentGrpc *agentGrpc) sendAgentInfo(ctx context.Context, agentInfo *pb.PAgentInfo) (*pb.PResult, error) {
	ctx, cancel := context.WithTimeout(ctx, agentGrpcTimeOut)
	defer cancel()

	result, err := agentGrpc.agentClient.RequestAgentInfo(ctx, agentInfo)
	if err != nil {
		Log("grpc").Errorf("send agent info - %v", err)
	}

	return result, err
}

func (agentGrpc *agentGrpc) registerAgentWithRetry() bool {
	ctx, agentInfo := agentGrpc.makeAgentInfo()

	for !agentGrpc.agent.shutdown {
		if res, err := agentGrpc.sendAgentInfo(ctx, agentInfo); err == nil {
			if res.Success {
				Log("agent").Infof("success to register agent")
				return true
			} else {
				Log("agent").Errorf("register agent - %s", res.Message)
				break
			}
		}

		for retry := 1; !agentGrpc.agent.shutdown; retry++ {
			if waitUntilReady(agentGrpc.agentConn, backOffSleep(retry), "agent") {
				break
			}
		}
		if agentInfo.Ip == "" {
			agentInfo.Ip = getOutboundIP()
		}
	}
	return false
}

func (agentGrpc *agentGrpc) sendApiMetadata(ctx context.Context, in *pb.PApiMetaData) error {
	ctx, cancel := context.WithTimeout(ctx, agentGrpcTimeOut)
	defer cancel()

	_, err := agentGrpc.metaClient.RequestApiMetaData(ctx, in)
	if err != nil {
		Log("grpc").Errorf("send api metadata - %v", err)
	}
	return err
}

func (agentGrpc *agentGrpc) sendApiMetadataWithRetry(apiId int32, api string, line int, apiType int) bool {
	ctx := grpcMetadataContext(agentGrpc.agent, -1)
	apiMeta := pb.PApiMetaData{
		ApiId:   apiId,
		ApiInfo: api,
		Line:    int32(line),
		Type:    int32(apiType),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("api metadata: %s", apiMeta.String())
	}

	for agentGrpc.agent.Enable() {
		if err := agentGrpc.sendApiMetadata(ctx, &apiMeta); err == nil {
			return true
		}

		if !agentGrpc.agent.config.offGrpc {
			for retry := 1; agentGrpc.agent.Enable(); retry++ {
				if waitUntilReady(agentGrpc.agentConn, backOffSleep(retry), "agent") {
					break
				}
			}
		}
	}
	return false
}

func (agentGrpc *agentGrpc) sendStringMetadata(ctx context.Context, in *pb.PStringMetaData) error {
	ctx, cancel := context.WithTimeout(ctx, agentGrpcTimeOut)
	defer cancel()

	_, err := agentGrpc.metaClient.RequestStringMetaData(ctx, in)
	if err != nil {
		Log("grpc").Errorf("send string metadata - %v", err)
	}
	return err
}

func (agentGrpc *agentGrpc) sendStringMetadataWithRetry(strId int32, str string) bool {
	ctx := grpcMetadataContext(agentGrpc.agent, -1)
	strMeta := pb.PStringMetaData{
		StringId:    strId,
		StringValue: str,
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("string metadata: %s", strMeta.String())
	}

	for agentGrpc.agent.Enable() {
		if err := agentGrpc.sendStringMetadata(ctx, &strMeta); err == nil {
			return true
		}

		if !agentGrpc.agent.config.offGrpc {
			for retry := 1; agentGrpc.agent.Enable(); retry++ {
				if waitUntilReady(agentGrpc.agentConn, backOffSleep(retry), "agent") {
					break
				}
			}
		}
	}
	return false
}

func (agentGrpc *agentGrpc) sendSqlMetadata(ctx context.Context, in *pb.PSqlMetaData) error {
	ctx, cancel := context.WithTimeout(ctx, agentGrpcTimeOut)
	defer cancel()

	_, err := agentGrpc.metaClient.RequestSqlMetaData(ctx, in)
	if err != nil {
		Log("grpc").Errorf("send sql metadata - %v", err)
	}

	return err
}

func (agentGrpc *agentGrpc) sendSqlMetadataWithRetry(sqlId int32, sql string) bool {
	ctx := grpcMetadataContext(agentGrpc.agent, -1)
	sqlMeta := pb.PSqlMetaData{
		SqlId: sqlId,
		Sql:   sql,
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("sql metadata: %s", sqlMeta.String())
	}

	for agentGrpc.agent.Enable() {
		if err := agentGrpc.sendSqlMetadata(ctx, &sqlMeta); err == nil {
			return true
		}

		if !agentGrpc.agent.config.offGrpc {
			for retry := 1; agentGrpc.agent.Enable(); retry++ {
				if waitUntilReady(agentGrpc.agentConn, backOffSleep(retry), "agent") {
					break
				}
			}
		}
	}
	return false
}

func (agentGrpc *agentGrpc) sendSqlUidMetadata(ctx context.Context, in *pb.PSqlUidMetaData) error {
	ctx, cancel := context.WithTimeout(ctx, agentGrpcTimeOut)
	defer cancel()

	_, err := agentGrpc.metaClient.RequestSqlUidMetaData(ctx, in)
	if err != nil {
		Log("grpc").Errorf("send sql uid metadata - %v", err)
	}

	return err
}

func (agentGrpc *agentGrpc) sendSqlUidMetadataWithRetry(sqlUid []byte, sql string) bool {
	ctx := grpcMetadataContext(agentGrpc.agent, -1)
	sqlUidMeta := pb.PSqlUidMetaData{
		SqlUid: sqlUid,
		Sql:    sql,
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("sql uid metadata: %s", sqlUidMeta.String())
	}

	for agentGrpc.agent.Enable() {
		if err := agentGrpc.sendSqlUidMetadata(ctx, &sqlUidMeta); err == nil {
			return true
		}

		if !agentGrpc.agent.config.offGrpc {
			for retry := 1; agentGrpc.agent.Enable(); retry++ {
				if waitUntilReady(agentGrpc.agentConn, backOffSleep(retry), "agent") {
					break
				}
			}
		}
	}
	return false
}

func (agentGrpc *agentGrpc) sendExceptionMetadata(ctx context.Context, in *pb.PExceptionMetaData) error {
	ctx, cancel := context.WithTimeout(ctx, agentGrpcTimeOut)
	defer cancel()

	_, err := agentGrpc.metaClient.RequestExceptionMetaData(ctx, in)
	if err != nil {
		Log("grpc").Errorf("send sql metadata - %v", err)
	}

	return err
}

func (agentGrpc *agentGrpc) sendExceptionMetadataWithRetry(exception *exceptionMeta) bool {
	ctx := grpcMetadataContext(agentGrpc.agent, -1)
	exceptMeta := makePExceptionMetaData(exception)

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("exception metadata: %s", exceptMeta.String())
	}

	for agentGrpc.agent.Enable() {
		if err := agentGrpc.sendExceptionMetadata(ctx, exceptMeta); err == nil {
			return true
		}

		if !agentGrpc.agent.config.offGrpc {
			for retry := 1; agentGrpc.agent.Enable(); retry++ {
				if waitUntilReady(agentGrpc.agentConn, backOffSleep(retry), "agent") {
					break
				}
			}
		}
	}
	return false
}

func makePExceptionMetaData(e *exceptionMeta) *pb.PExceptionMetaData {
	return &pb.PExceptionMetaData{
		TransactionId: &pb.PTransactionId{
			AgentId:        e.txId.AgentId,
			AgentStartTime: e.txId.StartTime,
			Sequence:       e.txId.Sequence,
		},
		SpanId:      e.spanId,
		UriTemplate: e.uriTemplate,
		Exceptions:  makePExceptionList(e.exceptions),
	}
}

func makePExceptionList(exceptions []*exception) []*pb.PException {
	list := make([]*pb.PException, 0)
	for _, e := range exceptions {
		list = append(list, makePException(e))
	}
	return list
}

func makePException(e *exception) *pb.PException {
	frames := e.callstack.stackTrace()
	return &pb.PException{
		ExceptionClassName: frames[0].moduleName,
		ExceptionMessage:   e.callstack.err.Error(),
		StartTime:          e.callstack.errorTime.UnixNano() / int64(time.Millisecond),
		ExceptionId:        e.exceptionId,
		ExceptionDepth:     1,
		StackTraceElement:  makePStackTraceElementList(frames),
	}
}

func makePStackTraceElementList(frames []frame) []*pb.PStackTraceElement {
	list := make([]*pb.PStackTraceElement, 0)
	for _, f := range frames {
		list = append(list, &pb.PStackTraceElement{
			ClassName:  f.moduleName,
			FileName:   f.file,
			LineNumber: f.line,
			MethodName: f.funcName,
		})
	}
	return list
}

func sendStreamWithTimeout(send func() error, timeout time.Duration, which string) error {
	errChan := make(chan error, 1)
	go func() {
		errChan <- send()
		close(errChan)
	}()

	t := time.NewTimer(timeout)
	select {
	case <-t.C:
		return status.Errorf(codes.DeadlineExceeded, which+" stream.Send() - too slow or blocked")
	case err := <-errChan:
		if !t.Stop() {
			<-t.C
		}
		return err
	}
}

func waitUntilReady(grpcConn *grpc.ClientConn, timeout time.Duration, which string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	state := grpcConn.GetState()
	Log("grpc").Infof("wait %s connection ready - state: %s, timeout: %s", which, state.String(), timeout.String())

	for state != connectivity.Ready {
		if grpcConn.WaitForStateChange(ctx, state) {
			state = grpcConn.GetState()
		} else {
			// To exit IDLE state, it needs to try grpc action.
			if state == connectivity.Idle && timeout.Seconds() > 30 {
				return true
			} else {
				return false
			}
		}
	}

	return true
}

func newStreamWithRetry(agent *agent, grpcConn *grpc.ClientConn, newStreamFunc func() bool, which string) bool {
	for agent.Enable() {
		if newStreamFunc() {
			return true
		}
		if !agent.config.offGrpc {
			for retry := 1; agent.Enable(); retry++ {
				if waitUntilReady(grpcConn, backOffSleep(retry), which) {
					break
				}
			}
		}
	}
	return false
}

type pingStream struct {
	stream pb.Agent_PingSessionClient
}

func (agentGrpc *agentGrpc) newPingStream() bool {
	agentGrpc.pingSocketId++
	ctx := grpcMetadataContext(agentGrpc.agent, agentGrpc.pingSocketId)
	stream, err := agentGrpc.agentClient.PingSession(ctx)
	if err != nil {
		Log("grpc").Errorf("make ping stream - %v", err)
		return false
	}

	agentGrpc.pingStream = &pingStream{stream}
	return true
}

func (agentGrpc *agentGrpc) newPingStreamWithRetry() *pingStream {
	if newStreamWithRetry(agentGrpc.agent, agentGrpc.agentConn, agentGrpc.newPingStream, "ping") {
		Log("grpc").Infof("success to make ping stream")
		return agentGrpc.pingStream
	}
	return &pingStream{}
}

var ping = pb.PPing{}

func (s *pingStream) sendPing() error {
	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "ping stream is nil")
	}
	err := sendStreamWithTimeout(func() error { return s.stream.Send(&ping) }, sendStreamTimeOut, "ping")
	if err != nil {
		return err
	}

	errChan := make(chan error, 1)
	go func() {
		_, err = s.stream.Recv()
		errChan <- err
		close(errChan)
	}()

	t := time.NewTimer(sendStreamTimeOut)
	select {
	case <-t.C:
		return status.Errorf(codes.DeadlineExceeded, "ping stream.Recv() - too slow or blocked")
	case err = <-errChan:
		if !t.Stop() {
			<-t.C
		}
		return err
	}
}

func (s *pingStream) close() {
	if s.stream == nil {
		return
	}

	sendStreamWithTimeout(func() error { return s.stream.CloseSend() }, closeStreamTimeOut, "ping")
	s.stream = nil
	Log("grpc").Infof("close ping stream")
}

func (agentGrpc *agentGrpc) close() {
	if agentGrpc.agentConn != nil {
		agentGrpc.agentConn.Close()
	}
}

type spanClient interface {
	SendSpan(ctx context.Context) (pb.Span_SendSpanClient, error)
}

type spanGrpcClient struct {
	client pb.SpanClient
}

func (spanGrpcClient *spanGrpcClient) SendSpan(ctx context.Context) (pb.Span_SendSpanClient, error) {
	return spanGrpcClient.client.SendSpan(ctx)
}

type spanGrpc struct {
	spanConn   *grpc.ClientConn
	spanClient spanClient
	stream     *spanStream
	agent      *agent
}

type spanStream struct {
	stream pb.Span_SendSpanClient
}

func newSpanGrpc(agent *agent) (*spanGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorSpanPort)
	if err != nil {
		return nil, err
	}

	return &spanGrpc{
		spanConn:   conn,
		spanClient: &spanGrpcClient{pb.NewSpanClient(conn)},
		agent:      agent,
	}, nil
}

func (spanGrpc *spanGrpc) close() {
	if spanGrpc.spanConn != nil {
		spanGrpc.spanConn.Close()
	}
}

func (spanGrpc *spanGrpc) newSpanStream() bool {
	ctx := grpcMetadataContext(spanGrpc.agent, -1)
	stream, err := spanGrpc.spanClient.SendSpan(ctx)
	if err != nil {
		Log("grpc").Errorf("make span stream - %v", err)
		return false
	}

	spanGrpc.stream = &spanStream{stream}
	return true
}

func (spanGrpc *spanGrpc) newSpanStreamWithRetry() *spanStream {
	if newStreamWithRetry(spanGrpc.agent, spanGrpc.spanConn, spanGrpc.newSpanStream, "span") {
		Log("grpc").Infof("success to make span stream")
		return spanGrpc.stream
	}
	return &spanStream{}
}

func (s *spanStream) close() {
	if s.stream == nil {
		return
	}

	sendStreamWithTimeout(
		func() error {
			_, err := s.stream.CloseAndRecv()
			return err
		},
		closeStreamTimeOut, "span",
	)
	s.stream = nil
	Log("grpc").Infof("close span stream")
}

func (s *spanStream) sendSpan(chunk *spanChunk) error {
	var gspan *pb.PSpanMessage

	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "span stream is nil")
	}

	if !chunk.final || chunk.span.isAsyncSpan() {
		gspan = makePSpanChunk(chunk)
	} else {
		gspan = makePSpan(chunk)
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("PSpanMessage Size: %d", proto.Size(gspan))
	}
	if IsLogLevelEnabled(logrus.TraceLevel) {
		Log("grpc").Tracef("PSpanMessage: %s", gspan.String())
	}

	return sendStreamWithTimeout(func() error { return s.stream.Send(gspan) }, sendStreamTimeOut, "span")
}

func makePSpan(chunk *spanChunk) *pb.PSpanMessage {
	span := chunk.span
	if span.apiId == 0 && span.operationName != "" {
		span.annotations.AppendString(AnnotationApi, span.operationName)
	}

	spanEventList := make([]*pb.PSpanEvent, 0)
	for _, event := range chunk.eventChunk {
		aSpanEvent := makePSpanEvent(event)
		spanEventList = append(spanEventList, aSpanEvent)
	}

	gspan := &pb.PSpanMessage{
		Field: &pb.PSpanMessage_Span{
			Span: &pb.PSpan{
				Version: 1,
				TransactionId: &pb.PTransactionId{
					AgentId:        span.txId.AgentId,
					AgentStartTime: span.txId.StartTime,
					Sequence:       span.txId.Sequence,
				},
				SpanId:       span.spanId,
				ParentSpanId: span.parentSpanId,
				StartTime:    span.startTime.UnixMilli(),
				Elapsed:      int32(span.elapsed),
				ServiceType:  span.serviceType,
				AcceptEvent: &pb.PAcceptEvent{
					Rpc:        span.rpcName,
					EndPoint:   span.endPoint,
					RemoteAddr: span.remoteAddr,
					ParentInfo: &pb.PParentInfo{
						ParentApplicationName: span.parentAppName,
						ParentApplicationType: int32(span.parentAppType),
						AcceptorHost:          span.acceptorHost,
					},
				},
				Annotation:             span.annotations.getList(),
				ApiId:                  span.apiId,
				Flag:                   int32(span.flags),
				SpanEvent:              spanEventList,
				Err:                    int32(span.err),
				ExceptionInfo:          nil,
				ApplicationServiceType: span.agent.appType,
				LoggingTransactionInfo: span.loggingInfo,
			},
		},
	}

	if span.errorString != "" {
		gspan.GetSpan().ExceptionInfo = &pb.PIntStringValue{
			IntValue:    span.errorFuncId,
			StringValue: &wrappers.StringValue{Value: span.errorString},
		}
	}

	return gspan
}

func makePSpanChunk(chunk *spanChunk) *pb.PSpanMessage {
	span := chunk.span

	spanEventList := make([]*pb.PSpanEvent, 0)
	for _, event := range chunk.eventChunk {
		aSpanEvent := makePSpanEvent(event)
		spanEventList = append(spanEventList, aSpanEvent)
	}

	gspan := &pb.PSpanMessage{
		Field: &pb.PSpanMessage_SpanChunk{
			SpanChunk: &pb.PSpanChunk{
				Version: 1,
				TransactionId: &pb.PTransactionId{
					AgentId:        span.txId.AgentId,
					AgentStartTime: span.txId.StartTime,
					Sequence:       span.txId.Sequence,
				},
				SpanId:                 span.spanId,
				KeyTime:                chunk.keyTime,
				EndPoint:               span.endPoint,
				SpanEvent:              spanEventList,
				ApplicationServiceType: span.agent.appType,
				LocalAsyncId:           nil,
			},
		},
	}

	if span.isAsyncSpan() {
		gspan.GetSpanChunk().LocalAsyncId = &pb.PLocalAsyncId{
			AsyncId:  span.asyncId,
			Sequence: span.asyncSequence,
		}
	}

	return gspan
}

func makePSpanEvent(event *spanEvent) *pb.PSpanEvent {
	if event.apiId == 0 && event.operationName != "" {
		event.annotations.AppendString(AnnotationApi, event.operationName)
	}

	aSpanEvent := pb.PSpanEvent{
		Sequence:      event.sequence,
		Depth:         event.depth,
		StartElapsed:  int32(event.startElapsed),
		EndElapsed:    int32(event.endElapsed),
		ServiceType:   event.serviceType,
		Annotation:    event.annotations.getList(),
		ApiId:         event.apiId,
		ExceptionInfo: nil,
		NextEvent:     nil,
		AsyncEvent:    event.asyncId,
	}

	if event.errorString != "" {
		aSpanEvent.ExceptionInfo = &pb.PIntStringValue{
			IntValue:    event.errorFuncId,
			StringValue: &wrappers.StringValue{Value: event.errorString},
		}
	}

	if event.destinationId != "" {
		next := &pb.PNextEvent{
			Field: &pb.PNextEvent_MessageEvent{
				MessageEvent: &pb.PMessageEvent{
					NextSpanId:    event.nextSpanId,
					EndPoint:      event.endPoint,
					DestinationId: event.destinationId,
				},
			},
		}

		aSpanEvent.NextEvent = next
	}

	return &aSpanEvent
}

type statClient interface {
	SendAgentStat(ctx context.Context) (pb.Stat_SendAgentStatClient, error)
}

type statGrpcClient struct {
	client pb.StatClient
}

func (statGrpcClient *statGrpcClient) SendAgentStat(ctx context.Context) (pb.Stat_SendAgentStatClient, error) {
	return statGrpcClient.client.SendAgentStat(ctx)
}

type statGrpc struct {
	statConn   *grpc.ClientConn
	statClient statClient
	stream     *statStream
	agent      *agent
}

type statStream struct {
	stream pb.Stat_SendAgentStatClient
}

func newStatGrpc(agent *agent) (*statGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorStatPort)
	if err != nil {
		return nil, err
	}

	return &statGrpc{
		statConn:   conn,
		statClient: &statGrpcClient{pb.NewStatClient(conn)},
		agent:      agent,
	}, nil
}

func (statGrpc *statGrpc) close() {
	if statGrpc.statConn != nil {
		statGrpc.statConn.Close()
	}
}

func (statGrpc *statGrpc) newStatStream() bool {
	ctx := grpcMetadataContext(statGrpc.agent, -1)
	stream, err := statGrpc.statClient.SendAgentStat(ctx)
	if err != nil {
		Log("grpc").Errorf("make stat stream - %v", err)
		return false
	}

	statGrpc.stream = &statStream{stream}
	return true
}

func (statGrpc *statGrpc) newStatStreamWithRetry() *statStream {
	if newStreamWithRetry(statGrpc.agent, statGrpc.statConn, statGrpc.newStatStream, "stat") {
		Log("grpc").Infof("success to make stat stream")
		return statGrpc.stream
	}
	return &statStream{}
}

func (s *statStream) close() {
	if s.stream == nil {
		return
	}

	sendStreamWithTimeout(
		func() error {
			_, err := s.stream.CloseAndRecv()
			return err
		},
		closeStreamTimeOut, "stat",
	)
	s.stream = nil
	Log("grpc").Infof("close stat stream")
}

func (s *statStream) sendStats(stats *pb.PStatMessage) error {
	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "stat stream is nil")
	}
	if IsLogLevelEnabled(logrus.TraceLevel) {
		Log("grpc").Tracef("PStatMessage: %s", stats.String())
	}
	return sendStreamWithTimeout(func() error { return s.stream.Send(stats) }, sendStreamTimeOut, "stat")
}

func makePAgentStatBatch(stats []*inspectorStats) *pb.PStatMessage {
	l := make([]*pb.PAgentStat, 0)
	for _, s := range stats {
		l = append(l, makePAgentStat(s))
	}
	return &pb.PStatMessage{
		Field: &pb.PStatMessage_AgentStatBatch{
			AgentStatBatch: &pb.PAgentStatBatch{
				AgentStat: l,
			},
		},
	}
}

func makePAgentStat(stat *inspectorStats) *pb.PAgentStat {
	return &pb.PAgentStat{
		Timestamp:       stat.sampleTime.UnixNano() / int64(time.Millisecond),
		CollectInterval: stat.interval,
		Gc: &pb.PJvmGc{
			Type:                 pb.PJvmGcType_JVM_GC_TYPE_CMS,
			JvmMemoryHeapUsed:    stat.heapUsed,
			JvmMemoryHeapMax:     stat.heapMax,
			JvmMemoryNonHeapUsed: stat.nonHeapUsed,
			JvmMemoryNonHeapMax:  stat.nonHeapMax,
			JvmGcOldCount:        stat.gcNum,
			JvmGcOldTime:         stat.gcTime,
			JvmGcDetailed:        nil,
		},
		CpuLoad: &pb.PCpuLoad{
			JvmCpuLoad:    stat.cpuProcLoad,
			SystemCpuLoad: stat.cpuSysLoad,
		},
		Transaction: &pb.PTransaction{
			SampledNewCount:            stat.sampleNew,
			SampledContinuationCount:   stat.sampleCont,
			UnsampledNewCount:          stat.unSampleNew,
			UnsampledContinuationCount: stat.unSampleCont,
			SkippedNewCount:            stat.skipNew,
			SkippedContinuationCount:   stat.skipCont,
		},
		ActiveTrace: &pb.PActiveTrace{
			Histogram: &pb.PActiveTraceHistogram{
				Version:             1,
				HistogramSchemaType: 2, //NORMAL SCHEMA
				ActiveTraceCount:    stat.activeSpan,
			},
		},
		DataSourceList: nil,
		ResponseTime: &pb.PResponseTime{
			Avg: stat.responseAvg,
			Max: stat.responseMax,
		},
		Deadlock: nil,
		FileDescriptor: &pb.PFileDescriptor{
			OpenFileDescriptorCount: stat.numOpenFD,
		},
		DirectBuffer: nil,
		Metadata:     "",
		TotalThread: &pb.PTotalThread{
			TotalThreadCount: stat.numThreads,
		},
		LoadedClass: nil,
	}
}

func makePAgentUriStat(stat *urlStatSnapshot) *pb.PStatMessage {
	return &pb.PStatMessage{
		Field: &pb.PStatMessage_AgentUriStat{
			AgentUriStat: &pb.PAgentUriStat{
				BucketVersion: urlStatBucketVersion,
				EachUriStat:   makePEachUriStatList(stat),
			},
		},
	}
}

func makePEachUriStatList(stat *urlStatSnapshot) []*pb.PEachUriStat {
	l := make([]*pb.PEachUriStat, 0)
	for _, e := range stat.urlMap {
		l = append(l, makePEachUriStat(e))
	}
	return l
}

func makePEachUriStat(e *eachUrlStat) *pb.PEachUriStat {
	return &pb.PEachUriStat{
		Uri:             e.url,
		TotalHistogram:  makePUriHistogram(e.totalHistogram),
		FailedHistogram: makePUriHistogram(e.failedHistogram),
		Timestamp:       e.tickTime.UnixNano() / int64(time.Millisecond),
	}
}

func makePUriHistogram(h *urlStatHistogram) *pb.PUriHistogram {
	return &pb.PUriHistogram{
		Total:     h.total,
		Max:       h.max,
		Histogram: h.histogram,
	}
}

type cmdGrpc struct {
	cmdConn   *grpc.ClientConn
	cmdClient pb.ProfilerCommandServiceClient
	stream    *cmdStream
	agent     *agent
}

type cmdStream struct {
	stream pb.ProfilerCommandService_HandleCommandClient
}

func newCommandGrpc(agent *agent) (*cmdGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorAgentPort)
	if err != nil {
		return nil, err
	}

	cmdClient := pb.NewProfilerCommandServiceClient(conn)
	return &cmdGrpc{cmdConn: conn, cmdClient: cmdClient, agent: agent}, nil
}

func (cmdGrpc *cmdGrpc) close() {
	if cmdGrpc.cmdConn != nil {
		cmdGrpc.cmdConn.Close()
	}
}

func (cmdGrpc *cmdGrpc) newHandleCommandStream() bool {
	ctx := grpcMetadataContext(cmdGrpc.agent, -1)
	stream, err := cmdGrpc.cmdClient.HandleCommand(ctx)
	if err != nil {
		Log("grpc").Errorf("make command stream - %v", err)
		return false
	}

	cmdGrpc.stream = &cmdStream{stream}
	return true
}

func (cmdGrpc *cmdGrpc) newCommandStreamWithRetry() *cmdStream {
	if newStreamWithRetry(cmdGrpc.agent, cmdGrpc.cmdConn, cmdGrpc.newHandleCommandStream, "command") {
		Log("grpc").Infof("success to make command stream")
		return cmdGrpc.stream
	}
	return &cmdStream{}
}

func (s *cmdStream) close() {
	if s.stream == nil {
		return
	}

	sendStreamWithTimeout(func() error { return s.stream.CloseSend() }, closeStreamTimeOut, "cmd")
	s.stream = nil
	Log("grpc").Infof("close command stream")
}

func (s *cmdStream) sendCommandMessage() error {
	var gCmd *pb.PCmdMessage

	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "command stream is nil")
	}

	sKeys := make([]int32, 0)
	sKeys = append(sKeys, int32(pb.PCommandType_ECHO))
	sKeys = append(sKeys, int32(pb.PCommandType_ACTIVE_THREAD_COUNT))
	sKeys = append(sKeys, int32(pb.PCommandType_ACTIVE_THREAD_DUMP))
	sKeys = append(sKeys, int32(pb.PCommandType_ACTIVE_THREAD_LIGHT_DUMP))

	gCmd = &pb.PCmdMessage{
		Message: &pb.PCmdMessage_HandshakeMessage{
			HandshakeMessage: &pb.PCmdServiceHandshake{
				SupportCommandServiceKey: sKeys,
			},
		},
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("PCmdMessage: %s", gCmd.String())
	}
	return sendStreamWithTimeout(func() error { return s.stream.Send(gCmd) }, sendStreamTimeOut, "cmd")
}

func (s *cmdStream) recvCommandRequest() (*pb.PCmdRequest, error) {
	var gCmdReq *pb.PCmdRequest

	if s.stream == nil {
		return nil, status.Errorf(codes.Unavailable, "command stream is nil")
	}

	gCmdReq, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("PCmdRequest: %s", gCmdReq.String())
	}
	return gCmdReq, nil
}

type activeThreadCountStream struct {
	stream   pb.ProfilerCommandService_CommandStreamActiveThreadCountClient
	reqId    int32
	actCount int32
}

func (cmdGrpc *cmdGrpc) newActiveThreadCountStream(reqId int32) *activeThreadCountStream {
	ctx := grpcMetadataContext(cmdGrpc.agent, -1)
	stream, err := cmdGrpc.cmdClient.CommandStreamActiveThreadCount(ctx)
	if err != nil {
		Log("grpc").Errorf("make active thread count stream - %v", err)
		return &activeThreadCountStream{nil, -1, 0}
	}

	return &activeThreadCountStream{stream, reqId, 0}
}

func (s *activeThreadCountStream) close() {
	if s.stream == nil {
		return
	}

	sendStreamWithTimeout(
		func() error {
			_, err := s.stream.CloseAndRecv()
			return err
		},
		closeStreamTimeOut, "arc",
	)
	s.stream = nil
}

func (s *activeThreadCountStream) sendActiveThreadCount() error {
	var gRes *pb.PCmdActiveThreadCountRes

	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "active thread count stream is nil")
	}

	now := time.Now()
	activeThreadCount := getActiveSpanCount(now)
	s.actCount++

	gRes = &pb.PCmdActiveThreadCountRes{
		CommonStreamResponse: &pb.PCmdStreamResponse{
			ResponseId: s.reqId,
			SequenceId: s.actCount,
			Message:    &wrappers.StringValue{Value: ""},
		},
		HistogramSchemaType: 2,
		ActiveThreadCount:   activeThreadCount,
		TimeStamp:           now.UnixNano() / int64(time.Millisecond),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("PCmdActiveThreadCountRes: %s", gRes.String())
	}
	return sendStreamWithTimeout(func() error { return s.stream.Send(gRes) }, sendStreamTimeOut, "arc")
}

func (cmdGrpc *cmdGrpc) sendActiveThreadDump(reqId int32, limit int32, threadName []string, localId []int64, dump *goroutineDump) {
	var gRes *pb.PCmdActiveThreadDumpRes

	status := int32(0)
	msg := ""

	if dump == nil {
		status = -1
		msg = "An error occurred while dumping Goroutine"
	}

	gRes = &pb.PCmdActiveThreadDumpRes{
		CommonResponse: &pb.PCmdResponse{
			ResponseId: reqId,
			Status:     status,
			Message:    &wrappers.StringValue{Value: msg},
		},
		ThreadDump: makePActiveThreadDumpList(dump, int(limit), threadName, localId),
		Type:       "Go",
		SubType:    "",
		Version:    runtime.Version(),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("send PCmdActiveThreadDumpRes: %s", gRes.String())
	}

	ctx := grpcMetadataContext(cmdGrpc.agent, -1)
	_, err := cmdGrpc.cmdClient.CommandActiveThreadDump(ctx, gRes)
	if err != nil {
		Log("grpc").Errorf("send active thread dump - %v", err)
	}
}

func makePActiveThreadDumpList(dump *goroutineDump, limit int, threadName []string, localId []int64) []*pb.PActiveThreadDump {
	dumpList := make([]*pb.PActiveThreadDump, 0)

	if dump != nil {
		if limit < 1 {
			limit = len(dump.goroutines)
		}

		selected := make([]*goroutine, 0)
		for _, tn := range threadName {
			g := dump.search(tn)
			if g != nil {
				selected = append(selected, g)
			}
		}

		if IsLogLevelEnabled(logrus.DebugLevel) {
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
		StartTime:    g.span.startTime.UnixNano() / int64(time.Millisecond),
		LocalTraceId: 0,
		ThreadDump: &pb.PThreadDump{
			ThreadName:         g.header,
			ThreadId:           g.id,
			BlockedTime:        0,
			BlockedCount:       0,
			WaitedTime:         0,
			WaitedCount:        0,
			LockName:           "",
			LockOwnerId:        0,
			LockOwnerName:      "",
			InNative:           false,
			Suspended:          false,
			ThreadState:        g.threadState(),
			StackTrace:         g.stackTrace(),
			LockedMonitor:      nil,
			LockedSynchronizer: nil,
		},
		Sampled:       g.span.sampled,
		TransactionId: g.span.txId,
		EntryPoint:    g.span.entryPoint,
	}

	return aDump
}

func (cmdGrpc *cmdGrpc) sendActiveThreadLightDump(reqId int32, limit int32, dump *goroutineDump) {
	var gRes *pb.PCmdActiveThreadLightDumpRes

	status := int32(0)
	msg := ""

	if dump == nil {
		status = -1
		msg = "An error occurred while dumping Goroutine"
	}

	gRes = &pb.PCmdActiveThreadLightDumpRes{
		CommonResponse: &pb.PCmdResponse{
			ResponseId: reqId,
			Status:     status,                            //error
			Message:    &wrappers.StringValue{Value: msg}, //error message
		},
		ThreadDump: makePActiveThreadLightDumpList(dump, int(limit)),
		Type:       "Go",
		SubType:    "",
		Version:    runtime.Version(),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("send PCmdActiveThreadLightDumpRes: %s", gRes.String())
	}

	ctx := grpcMetadataContext(cmdGrpc.agent, -1)
	_, err := cmdGrpc.cmdClient.CommandActiveThreadLightDump(ctx, gRes)
	if err != nil {
		Log("grpc").Errorf("send active thread light dump - %v", err)
	}
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
		StartTime:    g.span.startTime.UnixNano() / int64(time.Millisecond),
		LocalTraceId: 0,
		ThreadDump: &pb.PThreadLightDump{
			ThreadName:  g.header,
			ThreadId:    g.id,
			ThreadState: g.threadState(),
		},
		Sampled:       g.span.sampled,
		TransactionId: g.span.txId,
		EntryPoint:    g.span.entryPoint,
	}

	return aDump
}

func (cmdGrpc *cmdGrpc) sendEcho(reqId int32, msg string) {
	var gRes *pb.PCmdEchoResponse

	gRes = &pb.PCmdEchoResponse{
		CommonResponse: &pb.PCmdResponse{
			ResponseId: reqId,
			Status:     0,                                //error
			Message:    &wrappers.StringValue{Value: ""}, //error message
		},
		Message: msg,
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("send PCmdEchoResponse: %s", gRes.String())
	}

	ctx := grpcMetadataContext(cmdGrpc.agent, -1)
	_, err := cmdGrpc.cmdClient.CommandEcho(ctx, gRes)
	if err != nil {
		Log("grpc").Errorf("send echo response - %v", err)
	}
}
