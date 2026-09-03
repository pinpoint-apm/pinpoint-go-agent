package pinpoint

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"math/rand"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	wrappers "google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	headerAppName         = "applicationname"
	headerAgentID         = "agentid"
	headerAgentName       = "agentname"
	headerStartTime       = "starttime"
	headerSocketID        = "socketid"
	headerServiceType     = "servicetype"
	headerProtocolVersion = "protocol.version"
	headerServiceName     = "servicename"
	headerApiKey          = "apikey"
)

func grpcMetadataContext(agent *agent, socketId int64) context.Context {
	// The common case (socketId <= 0) carries only immutable agent headers, so
	// reuse a context built once instead of allocating a metadata map per send.
	if socketId <= 0 {
		return agent.baseOutgoingContext()
	}

	// Only the ping stream sets a socketId; it is low frequency, so build fresh.
	m := agentHeaderMap(agent)
	m[headerSocketID] = strconv.FormatInt(socketId, 10)
	return metadata.NewOutgoingContext(context.Background(), metadata.New(m))
}

func agentHeaderMap(agent *agent) map[string]string {
	// Headers branch on the ObjectName version, mirroring the Java agent's
	// ClientHeaderFactoryV1 (v1/v3, protocol.version=100) and
	// ClientHeaderFactoryV4 (v4, protocol.version=400).
	m := map[string]string{
		headerAppName:         agent.appName,
		headerAgentID:         agent.agentID,
		headerStartTime:       strconv.FormatInt(agent.startTime, 10),
		headerServiceType:     strconv.Itoa(int(agent.appType)),
		headerProtocolVersion: strconv.Itoa(agent.objName.protocolVersion()),
	}
	if agent.objName.isV4() {
		// v4: agentName is always present; servicename and apikey are sent.
		m[headerAgentName] = agent.agentName
		m[headerServiceName] = agent.serviceName
		m[headerApiKey] = agent.objName.apiKey
	} else if agent.agentName != "" {
		// v1/v3: agentName is optional on the wire.
		m[headerAgentName] = agent.agentName
	}
	return m
}

func (agent *agent) baseOutgoingContext() context.Context {
	agent.grpcMetaOnce.Do(func() {
		md := metadata.New(agentHeaderMap(agent))
		agent.grpcMetaCtx = metadata.NewOutgoingContext(context.Background(), md)
	})
	return agent.grpcMetaCtx
}

const (
	// Reconnect back-off, matching the C++ agent's GrpcClientTuning: a gentle
	// x1.2 ramp from 3s to a 30s ceiling, randomized +/-30%.
	backOffInitialInterval = 3 * time.Second
	backOffMultiplier      = 1.2
	backOffMaxInterval     = 30 * time.Second
	backOffJitter          = 0.3
)

// backOffSleep returns how long to wait before reconnect attempt+1, with the
// first attempt numbered 0.
func backOffSleep(attempt int) time.Duration {
	dur := float64(backOffInitialInterval) * math.Pow(backOffMultiplier, float64(attempt))
	if dur > float64(backOffMaxInterval) {
		dur = float64(backOffMaxInterval)
	}

	// Randomize so agents restarted together do not reconnect in lockstep. The
	// jitter is applied after the clamp, as in the C++ agent, so a capped
	// interval lands within +/-30% of the ceiling rather than always on it.
	return randomize(time.Duration(dur), backOffJitter)
}

// randomize returns d scaled by a uniform factor in [1-jitter, 1+jitter], the
// Go counterpart of the Java agent's IntervalFunction.ofRandomized.
func randomize(d time.Duration, jitter float64) time.Duration {
	return time.Duration(float64(d) * (1 - jitter + rand.Float64()*2*jitter))
}

// streamAgeJitter randomizes every connection and stream max age by +/-10%,
// as the Java agent does with ofRandomized(maxRpcAgeMillis, 0.1), so agents
// deployed together do not renew in lockstep.
const streamAgeJitter = 0.1

// streamAge is embedded in the long-lived streams: expiresAt is set when the
// stream is opened and zero when Collector.Grpc.StreamMaxAge is off.
type streamAge struct {
	expiresAt time.Time
}

func newStreamAge(agent *agent) streamAge {
	maxAge := time.Duration(agent.config.Int(CfgCollectorGrpcStreamMaxAge)) * time.Millisecond
	if maxAge <= 0 {
		return streamAge{}
	}
	return streamAge{expiresAt: time.Now().Add(randomize(maxAge, streamAgeJitter))}
}

func (a streamAge) expired() bool {
	return !a.expiresAt.IsZero() && time.Now().After(a.expiresAt)
}

type expiringStream interface {
	expired() bool
	close()
}

// renewIfExpired closes a stream past its max age and opens its replacement,
// the Go counterpart of the Java agent's SpanGrpcDataSender.renewStream. A
// renewal is the normal path, logged at info and kept apart from the error
// path the workers take when a send fails.
func renewIfExpired[S expiringStream](stream S, reopen func() S, which string) S {
	if !stream.expired() {
		return stream
	}
	Log("grpc").Infof("renew %s stream: max age reached", which)
	stream.close()
	return reopen()
}

const (
	// agentGrpcTimeOut bounds the AgentInfo RPC (boot-time registration and
	// the periodic refresh). It stays longer than metaGrpcTimeOut on purpose:
	// AgentInfo is one request from one goroutine with no queue or cache
	// behind it, so a slow reply only delays that caller -- nothing piles up
	// and nothing is invalidated. Registration retries with backOffUntilReady
	// until it succeeds, so a long wait on a hung collector costs boot latency
	// at most, while a tight deadline would only add spurious re-registrations
	// of the largest message the agent sends. (The C++ agent uses 5s here too;
	// matching it is a separate decision from the metadata fix.)
	agentGrpcTimeOut = 60 * time.Second

	// metaGrpcTimeOut bounds each metadata RPC (api/string/sql/sqlUid/
	// exception). Unlike AgentInfo these run under sendMetaWorker's
	// metaMaxConcurrentRequests permits, and a failed send evicts the item's
	// cache entry so it is re-registered on next use. Under the former 60s
	// deadline a hung collector pinned every permit for up to
	// 60s x metaRetryMaxAttempts; metaChan overflowed, tryEnqueueMeta
	// head-dropped a queued item (evicting its cache entry), and each drop or
	// timeout re-queued the same metadata -- an amplification loop that lasted
	// until the collector recovered. 5s matches the C++ agent's
	// request_timeout for unary RPCs: ample for a healthy collector, short
	// enough that permits recycle before the queue fills. Kept as a constant
	// like the C++ agent's value; a Collector.Grpc.* key can be added if a
	// deployment ever needs to tune it.
	metaGrpcTimeOut = 5 * time.Second

	sendStreamTimeOut    = 5 * time.Second
	closeStreamTimeOut   = 1 * time.Second
	commandStreamTimeOut = 1 * time.Second

	// Defaults for the Collector.Grpc.* config keys. Keepalive and message size
	// match the C++ and Java agents; flowControlWindow, writeBufferSize and
	// maxHeaderListSize follow the Java agent (ClientOption.java), which pins a
	// static 1MiB window with auto flow control off. The C++ agent leaves these
	// at the gRPC C-core defaults (BDP auto-tuned window).
	grpcKeepAliveTime               = 30000 // ms
	grpcKeepAliveTimeout            = 60000 // ms
	grpcKeepAlivePermitWithoutCalls = false
	grpcFlowControlWindow           = 1 * 1024 * 1024
	grpcWriteBufferSize             = 1 * 1024 * 1024
	grpcMaxMessageSize              = 4 * 1024 * 1024
	grpcMaxHeaderListSize           = 8 * 1024

	// Connection and stream renewal are off by default, as in the Java agent
	// (profiler.transport.grpc.loadbalancer.renew.period.millis and
	// profiler.transport.grpc.span.sender.rpc.age.max.millis default to a
	// value the agent treats as disabled).
	grpcConnectionMaxAge = 0 // ms
	grpcStreamMaxAge     = 0 // ms
)

// grpcChannelOptions holds the channel options connectCollector applies to
// every collector connection, resolved from the Collector.Grpc.* config keys.
type grpcChannelOptions struct {
	keepAlive         keepalive.ClientParameters
	flowControlWindow int32
	writeBufferSize   int
	maxSendMsgSize    int
	maxRecvMsgSize    int
	maxHeaderListSize uint32
	connectionMaxAge  time.Duration
}

func newGrpcChannelOptions(config *Config) grpcChannelOptions {
	return grpcChannelOptions{
		connectionMaxAge: time.Duration(config.Int(CfgCollectorGrpcConnectionMaxAge)) * time.Millisecond,
		keepAlive: keepalive.ClientParameters{
			Time:                time.Duration(config.Int(CfgCollectorGrpcKeepAliveTime)) * time.Millisecond,
			Timeout:             time.Duration(config.Int(CfgCollectorGrpcKeepAliveTimeout)) * time.Millisecond,
			PermitWithoutStream: config.Bool(CfgCollectorGrpcKeepAlivePermitWithoutCalls),
		},
		flowControlWindow: int32(config.Int(CfgCollectorGrpcFlowControlWindow)),
		writeBufferSize:   config.Int(CfgCollectorGrpcWriteBufferSize),
		maxSendMsgSize:    config.Int(CfgCollectorGrpcMaxSendMessageSize),
		maxRecvMsgSize:    config.Int(CfgCollectorGrpcMaxReceiveMessageSize),
		maxHeaderListSize: uint32(config.Int(CfgCollectorGrpcMaxHeaderListSize)),
	}
}

func (o grpcChannelOptions) dialOptions(creds credentials.TransportCredentials) []grpc.DialOption {
	opts := []grpc.DialOption{
		grpc.WithKeepaliveParams(o.keepAlive),
		grpc.WithTransportCredentials(creds),
		// HTTP/2 has two independent receive windows and a sender is bound by
		// both: SETTINGS_INITIAL_WINDOW_SIZE caps the bytes in flight on each
		// stream, and the stream-0 window caps the bytes in flight on the whole
		// connection. grpc-go exposes them as separate options and leaves the
		// connection window at its 64KB default when only the stream window is
		// set, so a 1MB stream window alone lets the collector push at most 64KB
		// per round trip across every stream. Setting either option also turns
		// off grpc-go's BDP-based auto-tuning, so the values below are static.
		// Applying the one FlowControlWindow key to both matches the Java
		// agent, where NettyChannelBuilder.flowControlWindow sets both windows
		// and disables auto-tuning as well.
		grpc.WithInitialWindowSize(o.flowControlWindow),
		grpc.WithInitialConnWindowSize(o.flowControlWindow),
		grpc.WithWriteBufferSize(o.writeBufferSize),
		grpc.WithMaxHeaderListSize(o.maxHeaderListSize),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(o.maxSendMsgSize),
			grpc.MaxCallRecvMsgSize(o.maxRecvMsgSize)),
	}
	// Only when enabled: with the option absent the channel keeps grpc-go's
	// default pick_first policy, so the default configuration is unchanged.
	if o.connectionMaxAge > 0 {
		opts = append(opts, grpc.WithDefaultServiceConfig(expiringPickFirstServiceConfig(o.connectionMaxAge)))
	}
	return opts
}

// collectorCredentials mirrors the C++ agent's make_channel_credentials:
// TLS disabled is insecure, a configured trust cert path is the trust root,
// and an empty path with TLS enabled falls back to the system root CAs. An
// unreadable or invalid cert is an error, never a silent insecure downgrade.
func collectorCredentials(config *Config) (credentials.TransportCredentials, error) {
	if !config.Bool(CfgCollectorGrpcSslEnable) {
		return insecure.NewCredentials(), nil
	}

	certPath := config.String(CfgCollectorGrpcTrustCertFilePath)
	if certPath == "" {
		return credentials.NewTLS(&tls.Config{}), nil
	}

	creds, err := credentials.NewClientTLSFromFile(certPath, "")
	if err != nil {
		return nil, fmt.Errorf("gRPC TLS trust certificate %s: %w", certPath, err)
	}
	return creds, nil
}

func connectCollector(config *Config, portOption string) (*grpc.ClientConn, error) {
	creds, err := collectorCredentials(config)
	if err != nil {
		Log("grpc").Errorf("collector TLS credentials - %v", err)
		return nil, err
	}

	opts := newGrpcChannelOptions(config).dialOptions(creds)
	addr := serverAddr(config, portOption)
	Log("grpc").Infof("connect to collector: %s (ssl: %v)", addr, config.Bool(CfgCollectorGrpcSslEnable))
	// NewClient defaults to the dns resolver, which hands one resolved address
	// list to the channel and refreshes it only on failure. The passthrough
	// scheme keeps grpc.Dial's behavior: the dialer resolves the collector host
	// for every new connection, so a replacement connection (see
	// Collector.Grpc.ConnectionMaxAge) sees the current DNS records. The
	// channel starts idle; the first RPC or waitUntilReady connects it.
	conn, err := grpc.NewClient("passthrough:///"+addr, opts...)
	if err != nil {
		Log("grpc").Errorf("connect to collector - %s, %v", addr, err)
	}
	return conn, err
}

// serverAddr joins the collector host and port. JoinHostPort rather than
// "%s:%d": an IPv6 literal host needs the brackets, without which neither the
// gRPC target nor localIP's SplitHostPort parses.
func serverAddr(config *Config, portOption string) string {
	return net.JoinHostPort(config.String(CfgCollectorHost), strconv.Itoa(config.Int(portOption)))
}

type agentGrpc struct {
	agentConn    *grpc.ClientConn
	agentClient  pb.AgentClient
	metaClient   pb.MetadataClient
	pingSocketId int64
	pingStream   *pingStream
	agent        *agent
	// retryDelay is the pause between metadata retries: metaRetryDelay in
	// production, shortened by tests.
	retryDelay time.Duration
	// registerRetryDelay overrides registration backoff in tests. Production
	// uses the jittered exponential backOffSleep sequence.
	registerRetryDelay time.Duration
}

func newAgentGrpc(agent *agent) (*agentGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorAgentPort)
	if err != nil {
		return nil, err
	}

	return &agentGrpc{
		agentConn:   conn,
		agentClient: pb.NewAgentClient(conn),
		metaClient:  pb.NewMetadataClient(conn),
		agent:       agent,
		retryDelay:  metaRetryDelay,
	}, nil
}

func getHostName() string {
	if hostName, err := os.Hostname(); err == nil {
		return hostName
	}
	return "unknown host"
}

// localIP returns the address this host uses to reach the collector.
//
// It first asks the kernel which source address it would route toward the
// collector (or, if the collector is a hostname, toward the public internet).
// That is a route lookup, not a network round trip, so it is instant and works
// with egress blocked; on a multi-NIC host it is the only way to pick the
// interface that actually faces the collector. It fails only when no route
// exists at all (closed network without a default gateway, isolated container
// network), in which case the first up, non-loopback interface address is used
// instead. The empty string means the host has no usable address right now.
func localIP(collectorAddr string) string {
	// Only probe the collector itself when it is an IP literal: a hostname
	// would go through DNS, which can hang in exactly the closed networks this
	// fallback exists for.
	if host, _, err := net.SplitHostPort(collectorAddr); err == nil && net.ParseIP(host) != nil {
		if ip := routeSourceIP(collectorAddr); ip != "" {
			return ip
		}
	}
	if ip := routeSourceIP("8.8.8.8:80"); ip != "" {
		return ip
	}
	return firstInterfaceIP()
}

// routeSourceIP returns the local address the kernel picks for addr. Dialing
// UDP sends nothing; connect(2) only consults the routing table. Loopback is
// not reported: with a local relay as the collector it would hide the host.
func routeSourceIP(addr string) string {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return ""
	}
	defer conn.Close()

	if ip := conn.LocalAddr().(*net.UDPAddr).IP; !ip.IsLoopback() {
		return ip.String()
	}
	return ""
}

func firstInterfaceIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if addrs, err := iface.Addrs(); err == nil {
			if ip := firstUnicastIP(addrs); ip != "" {
				return ip
			}
		}
	}
	return ""
}

// firstUnicastIP picks the first routable address, preferring IPv4 since
// interfaces usually list a link-local IPv6 address before anything else.
func firstUnicastIP(addrs []net.Addr) string {
	v6 := ""
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() || ipNet.IP.IsUnspecified() {
			continue
		}
		if ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
		if v6 == "" {
			v6 = ipNet.IP.String()
		}
	}
	return v6
}

// maxIPLookups bounds how often a still-empty IP is looked up again while
// registration keeps failing. Each retry is at least backOffInitialInterval
// apart, so this covers well over a minute for a NIC that is still coming up
// at boot; after that the lookup would not start succeeding on its own.
const maxIPLookups = 10

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
		Ip:           localIP(serverAddr(agentGrpc.agent.config, CfgCollectorAgentPort)),
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

	ctx := metadata.NewOutgoingContext(agentGrpc.agent.stopSignal(), metadata.New(agentHeaderMap(agentGrpc.agent)))
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

	for lookups, attempt := 0, 0; !agentGrpc.agent.shutdown.Load(); attempt++ {
		if res, err := agentGrpc.sendAgentInfo(ctx, agentInfo); err == nil {
			if res.Success {
				Log("agent").Infof("success to register agent")
				return true
			} else {
				Log("agent").Errorf("register agent - %s", res.Message)
				break
			}
		}

		retryDelay := agentGrpc.registerRetryDelay
		if retryDelay <= 0 {
			retryDelay = backOffSleep(attempt)
		}
		if !sleepUnlessStopped(agentGrpc.agent, retryDelay) {
			return false
		}
		backOffUntilReady(agentGrpc.agent, agentGrpc.agentConn, "agent")
		if agentInfo.Ip == "" && lookups < maxIPLookups {
			lookups++
			agentInfo.Ip = localIP(serverAddr(agentGrpc.agent.config, CfgCollectorAgentPort))
		}
	}
	return false
}

// refreshAgentInfo re-sends AgentInfo once, trying up to maxTry sends spaced
// retryInterval apart. Unlike boot-time registration it never loops forever:
// a failed refresh is simply left for the next refresh cycle, mirroring the
// C++ agent's send_agent_info_with_retries.
func (agentGrpc *agentGrpc) refreshAgentInfo(maxTry int, retryInterval time.Duration) bool {
	ctx, agentInfo := agentGrpc.makeAgentInfo()

	for try := 0; try < maxTry && !agentGrpc.agent.shutdown.Load(); try++ {
		if res, err := agentGrpc.sendAgentInfo(ctx, agentInfo); err == nil && res.Success {
			Log("agent").Infof("success to refresh agent info")
			return true
		}
		if try+1 < maxTry {
			select {
			case <-agentGrpc.agent.stopSignal().Done():
				return false
			case <-time.After(retryInterval):
			}
		}
	}

	Log("agent").Warnf("failed to refresh agent info")
	return false
}

func isRetryableError(e error) bool {
	// retry only for network error
	code := status.Code(e)
	return code == codes.Unavailable || code == codes.DeadlineExceeded
}

// metaRetryMaxAttempts bounds sends of one metadata item, matching the C++
// agent's meta_retry_max_attempts. All metadata is sent serially from the
// single sendMetaWorker goroutine; without a bound, one item facing a slow
// collector occupies the worker forever while metaChan overflows and new
// metadata is dropped.
const metaRetryMaxAttempts = 3

// metaRetryDelay is the pause between two sends of one metadata item, matching
// the C++ agent's meta_retry_delay and the Java agent's retryDelayMillis. The
// readiness wait alone is not enough: a collector that is up but refusing
// (Unavailable) leaves the channel Ready, so without a pause every attempt in
// the budget fires back to back against an already overloaded collector.
const metaRetryDelay = time.Second

// metaMaxConcurrentRequests bounds how many metadata sends sendMetaWorker
// keeps in flight at once, matching the C++ agent's
// meta_max_concurrent_requests.
const metaMaxConcurrentRequests = 4

// retryMeta reports failure once the attempt budget is exhausted so that
// sendMetaWorker releases the item's cache entry and the metadata is
// re-registered on its next use.
func (agentGrpc *agentGrpc) retryMeta(send func() error) bool {
	for attempt := 1; agentGrpc.agent.Enable(); attempt++ {
		err := send()
		if err == nil {
			return true
		}
		if !isRetryableError(err) || attempt >= metaRetryMaxAttempts {
			break
		}

		// Pause first, then wait for the channel. A Ready channel passes the
		// readiness wait at once, so the pause is the whole interval; a channel
		// that is still down after the pause is simply waited on, so the two
		// never stack a second pause on top of the reconnect wait.
		if !sleepUnlessStopped(agentGrpc.agent, agentGrpc.retryDelay) {
			return false
		}
		if !agentGrpc.agent.config.offGrpc {
			backOffUntilReady(agentGrpc.agent, agentGrpc.agentConn, "agent")
		}
	}
	return false
}

// metaResult turns a collector rejection (PResult.Success=false) into an error
// so the caller stops treating the send as delivered. The code is
// FailedPrecondition, deliberately outside isRetryableError's list: a
// rejection is a semantic verdict on the payload (schema mismatch and the
// like), so re-sending the same bytes twice more is pure load on the
// collector. retryMeta therefore leaves the attempt loop at once and returns
// false, and sendMetaWorker drops the cache entry so the next use registers a
// fresh id -- instead of every later span referencing an id the collector
// never accepted. The Java agent (RetryResponseStreamObserver.onNext ->
// retryScheduler.isSuccess) and the C++ agent (GrpcMetadata::process_completed
// checking call->reply.success()) do retry a rejection; Go failing fast here
// is an intended divergence.
func metaResult(res *pb.PResult, err error) error {
	if err != nil {
		return err
	}
	if !res.GetSuccess() {
		return status.Errorf(codes.FailedPrecondition, "collector rejected metadata: %s", res.GetMessage())
	}
	return nil
}

func (agentGrpc *agentGrpc) sendApiMetadata(in *pb.PApiMetaData) error {
	ctx, cancel := context.WithTimeout(grpcMetadataContext(agentGrpc.agent, -1), metaGrpcTimeOut)
	defer cancel()

	err := metaResult(agentGrpc.metaClient.RequestApiMetaData(ctx, in))
	if err != nil {
		Log("grpc").Errorf("send api metadata - %v", err)
	}
	return err
}

func (agentGrpc *agentGrpc) sendApiMetadataWithRetry(apiId int32, api string, line int, apiType int) bool {
	apiMeta := pb.PApiMetaData{
		ApiId:   apiId,
		ApiInfo: validUTF8(api),
		Line:    int32(line),
		Type:    int32(apiType),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("api metadata: %s", apiMeta.String())
	}

	return agentGrpc.retryMeta(func() error {
		return agentGrpc.sendApiMetadata(&apiMeta)
	})
}

func (agentGrpc *agentGrpc) sendStringMetadata(in *pb.PStringMetaData) error {
	ctx, cancel := context.WithTimeout(grpcMetadataContext(agentGrpc.agent, -1), metaGrpcTimeOut)
	defer cancel()

	err := metaResult(agentGrpc.metaClient.RequestStringMetaData(ctx, in))
	if err != nil {
		Log("grpc").Errorf("send string metadata - %v", err)
	}
	return err
}

func (agentGrpc *agentGrpc) sendStringMetadataWithRetry(strId int32, str string) bool {
	strMeta := pb.PStringMetaData{
		StringId:    strId,
		StringValue: validUTF8(str),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("string metadata: %s", strMeta.String())
	}

	return agentGrpc.retryMeta(func() error {
		return agentGrpc.sendStringMetadata(&strMeta)
	})
}

func (agentGrpc *agentGrpc) sendSqlMetadata(in *pb.PSqlMetaData) error {
	ctx, cancel := context.WithTimeout(grpcMetadataContext(agentGrpc.agent, -1), metaGrpcTimeOut)
	defer cancel()

	err := metaResult(agentGrpc.metaClient.RequestSqlMetaData(ctx, in))
	if err != nil {
		Log("grpc").Errorf("send sql metadata - %v", err)
	}

	return err
}

func (agentGrpc *agentGrpc) sendSqlMetadataWithRetry(sqlId int32, sql string) bool {
	sqlMeta := pb.PSqlMetaData{
		SqlId: sqlId,
		Sql:   validUTF8(sql),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("sql metadata: %s", sqlMeta.String())
	}

	return agentGrpc.retryMeta(func() error {
		return agentGrpc.sendSqlMetadata(&sqlMeta)
	})
}

func (agentGrpc *agentGrpc) sendSqlUidMetadata(in *pb.PSqlUidMetaData) error {
	ctx, cancel := context.WithTimeout(grpcMetadataContext(agentGrpc.agent, -1), metaGrpcTimeOut)
	defer cancel()

	err := metaResult(agentGrpc.metaClient.RequestSqlUidMetaData(ctx, in))
	if err != nil {
		Log("grpc").Errorf("send sql uid metadata - %v", err)
	}

	return err
}

func (agentGrpc *agentGrpc) sendSqlUidMetadataWithRetry(sqlUid []byte, sql string) bool {
	sqlUidMeta := pb.PSqlUidMetaData{
		SqlUid: sqlUid,
		Sql:    sql,
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("sql uid metadata: %s", sqlUidMeta.String())
	}

	return agentGrpc.retryMeta(func() error {
		return agentGrpc.sendSqlUidMetadata(&sqlUidMeta)
	})
}

func (agentGrpc *agentGrpc) sendExceptionMetadata(in *pb.PExceptionMetaData) error {
	// Drop a message the channel would reject anyway, before encoding it.
	// The bound is the configured Collector.Grpc.MaxSendMessageSize, the same
	// value connectCollector passes to grpc.MaxCallSendMsgSize. No margin is
	// needed: grpc-go compares the serialized message body alone against that
	// limit (payloadLen > maxSendMessageSize), and the collector's inbound
	// limit likewise counts only the message body. The 5-byte length prefix
	// and HTTP/2 framing/headers are outside both checks, and proto.Size is
	// exactly the serialized body length when no compressor is configured.
	maxSize := agentGrpc.agent.config.Int(CfgCollectorGrpcMaxSendMessageSize)
	if size := proto.Size(in); size > maxSize {
		err := status.Errorf(codes.ResourceExhausted, "gRPC message exceeds maximum size: %d > %d", size, maxSize)
		Log("grpc").Warnf("skip exception metadata - %v", err)
		return err
	}

	ctx, cancel := context.WithTimeout(grpcMetadataContext(agentGrpc.agent, -1), metaGrpcTimeOut)
	defer cancel()

	err := metaResult(agentGrpc.metaClient.RequestExceptionMetaData(ctx, in))
	if err != nil {
		Log("grpc").Errorf("send exception metadata - %v", err)
	}

	return err
}

func (agentGrpc *agentGrpc) sendExceptionMetadataWithRetry(exception *exceptionMeta) bool {
	exceptMeta := makePExceptionMetaData(exception)

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("exception metadata: %s", exceptMeta.String())
	}

	// Unlike the other metadata types, a failure here releases nothing:
	// exception metadata is never cached (deleteMetaCache is a no-op for
	// exceptionMeta), so there is no stale id to invalidate.
	return agentGrpc.retryMeta(func() error {
		return agentGrpc.sendExceptionMetadata(exceptMeta)
	})
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
	list := make([]*pb.PException, 0, len(exceptions))
	for _, e := range exceptions {
		list = append(list, makePException(e))
	}
	return list
}

func makePException(e *exception) *pb.PException {
	frames := e.callstack.stackTrace()
	// A user error's StackTrace() may return an empty trace; this goroutine
	// has no recover, so an unguarded frames[0] would kill the host process.
	className := "unknown"
	if len(frames) > 0 {
		className = frames[0].moduleName
	}
	return &pb.PException{
		ExceptionClassName: className,
		ExceptionMessage:   e.callstack.err.Error(),
		StartTime:          e.callstack.errorTime.UnixNano() / int64(time.Millisecond),
		ExceptionId:        e.exceptionId,
		ExceptionDepth:     1,
		StackTraceElement:  makePStackTraceElementList(frames),
	}
}

func makePStackTraceElementList(frames []frame) []*pb.PStackTraceElement {
	list := make([]*pb.PStackTraceElement, 0, len(frames))
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

// sendWatchdog is a reusable timeout guard for one stream operation, pooled so
// the per-chunk sends of the legacy span stream do not allocate a timer, a
// closure and a channel on every call. The armed state (cancel, timedOut) is
// exchanged with the timer callback under mu, which is what orders a Reset's
// arming writes before the callback's reads.
type sendWatchdog struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	timedOut bool
	timer    *time.Timer
	// fired receives exactly one token per expiry, so a caller whose Stop came
	// too late can wait for the cancel to finish before reporting the timeout.
	fired chan struct{}
}

var sendWatchdogPool = sync.Pool{New: func() any {
	w := &sendWatchdog{fired: make(chan struct{}, 1)}
	w.timer = time.AfterFunc(time.Duration(math.MaxInt64), w.onTimeout)
	w.timer.Stop()
	return w
}}

func (w *sendWatchdog) onTimeout() {
	w.mu.Lock()
	cancel := w.cancel
	w.timedOut = true
	w.mu.Unlock()
	cancel()
	w.fired <- struct{}{}
}

// sendStreamWithTimeout runs op on the calling goroutine and cancels the
// stream if op blocks past timeout. grpc-go unblocks a flow-control-blocked
// Send/Recv/CloseSend once the stream context is cancelled, so no operation
// goroutine is spawned or abandoned — the Go analog of the C++ agent's bounded
// wait plus TryCancel. Killing the stream on timeout matches the callers: they
// already close and re-create the stream on any send error.
func sendStreamWithTimeout(op func() error, cancelStream context.CancelFunc, timeout time.Duration, which string) error {
	w := sendWatchdogPool.Get().(*sendWatchdog)
	w.mu.Lock()
	w.cancel = cancelStream
	w.timedOut = false
	w.mu.Unlock()
	w.timer.Reset(timeout)

	err := op()

	if !w.timer.Stop() {
		<-w.fired
	}
	w.mu.Lock()
	timedOut := w.timedOut
	w.cancel = nil // don't retain the stream while idling in the pool
	w.mu.Unlock()
	sendWatchdogPool.Put(w)

	if timedOut {
		return status.Errorf(codes.DeadlineExceeded, "%s - too slow or blocked", which)
	}
	return err
}

// waitUntilReady waits up to timeout for the connection to become ready. The
// wait is bound to ctx as well, so cancelling ctx aborts it immediately.
func waitUntilReady(ctx context.Context, grpcConn *grpc.ClientConn, timeout time.Duration, which string) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	state := grpcConn.GetState()
	Log("grpc").Infof("wait %s connection ready - state: %s, timeout: %s", which, state.String(), timeout.String())

	for state != connectivity.Ready {
		// An IDLE channel never leaves that state on its own, so waiting on it
		// would burn the whole interval; ask it to connect instead, mirroring
		// the C++ agent's GetState(try_to_connect=true).
		if state == connectivity.Idle {
			grpcConn.Connect()
		}
		if !grpcConn.WaitForStateChange(ctx, state) {
			return false
		}
		state = grpcConn.GetState()
	}

	return true
}

// backOffUntilReady waits for the connection to become ready, backing off
// between attempts. It returns as soon as shutdown begins, so a pending
// back-off interval does not delay it.
func backOffUntilReady(agent *agent, grpcConn *grpc.ClientConn, which string) {
	for attempt := 0; !agent.shutdown.Load(); attempt++ {
		if waitUntilReady(agent.stopSignal(), grpcConn, backOffSleep(attempt), which) {
			return
		}
	}
}

// sleepUnlessStopped waits d, returning false as soon as shutdown begins so
// a pending pause does not delay it.
func sleepUnlessStopped(agent *agent, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-agent.stopSignal().Done():
		return false
	case <-timer.C:
		return true
	}
}

func newStreamWithRetry(agent *agent, grpcConn *grpc.ClientConn, newStreamFunc func() bool, which string) bool {
	for agent.Enable() {
		if newStreamFunc() {
			Log("grpc").Infof("success to make %s stream", which)
			return true
		}
		if !agent.config.offGrpc {
			backOffUntilReady(agent, grpcConn, which)
		}
	}
	return false
}

type pingStream struct {
	stream pb.Agent_PingSessionClient
	cancel context.CancelFunc
	streamAge
}

func (agentGrpc *agentGrpc) newPingStream() bool {
	agentGrpc.pingSocketId++
	ctx, cancel := context.WithCancel(grpcMetadataContext(agentGrpc.agent, agentGrpc.pingSocketId))
	stream, err := agentGrpc.agentClient.PingSession(ctx)
	if err != nil {
		cancel()
		Log("grpc").Errorf("make ping stream - %v", err)
		return false
	}

	agentGrpc.pingStream = &pingStream{stream: stream, cancel: cancel, streamAge: newStreamAge(agentGrpc.agent)}
	return true
}

func (agentGrpc *agentGrpc) newPingStreamWithRetry() *pingStream {
	if newStreamWithRetry(agentGrpc.agent, agentGrpc.agentConn, agentGrpc.newPingStream, "ping") {
		return agentGrpc.pingStream
	}
	return &pingStream{}
}

var ping = pb.PPing{}

func (s *pingStream) sendPing() error {
	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "ping stream is nil")
	}
	err := sendStreamWithTimeout(func() error { return s.stream.Send(&ping) }, s.cancel, sendStreamTimeOut, "ping stream.Send()")
	if err != nil {
		s.cancel()
		return err
	}

	return sendStreamWithTimeout(
		func() error {
			_, err := s.stream.Recv()
			return err
		},
		s.cancel, sendStreamTimeOut, "ping stream.Recv()",
	)
}

func (s *pingStream) close() {
	if s.stream == nil {
		return
	}
	defer s.cancel()

	sendStreamWithTimeout(func() error { return s.stream.CloseSend() }, s.cancel, closeStreamTimeOut, "ping stream.CloseSend()")
	s.stream = nil
	Log("grpc").Infof("close ping stream")
}

func (agentGrpc *agentGrpc) close() {
	if agentGrpc.agentConn != nil {
		agentGrpc.agentConn.Close()
	}
}

// spanGrpc supports both span send transports: the legacy long-lived SendSpan stream
// and the SendSpanBatch unary sender selected by Span.Batch.Enable.
type spanGrpc struct {
	spanConn              *grpc.ClientConn
	spanClient            pb.SpanClient
	stream                *spanStream
	agent                 *agent
	batchSize             int
	batchFlushTimeout     time.Duration
	batchCollectDeadline  time.Duration
	maxConcurrentRequests int

	// Buffered channel used as a semaphore that bounds the number of
	// in-flight SendSpanBatch requests. Acquire = send to it; release =
	// receive from it.
	concurrentRequestPermit chan struct{}
	inFlight                sync.WaitGroup
}

type spanStream struct {
	stream pb.Span_SendSpanClient
	cancel context.CancelFunc
	streamAge
}

func newSpanGrpc(agent *agent) (*spanGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorSpanPort)
	if err != nil {
		return nil, err
	}

	return &spanGrpc{
		spanConn:                conn,
		spanClient:              pb.NewSpanClient(conn),
		agent:                   agent,
		batchSize:               agent.config.Int(CfgSpanBatchSize),
		batchFlushTimeout:       time.Duration(agent.config.Int(CfgSpanBatchFlushInterval)) * time.Millisecond,
		batchCollectDeadline:    time.Duration(agent.config.Int(CfgSpanBatchCollectDeadline)) * time.Millisecond,
		maxConcurrentRequests:   agent.config.Int(CfgSpanBatchMaxConcurrentRequests),
		concurrentRequestPermit: make(chan struct{}, agent.config.Int(CfgSpanBatchMaxConcurrentRequests)),
	}, nil
}

// close releases the connection without waiting for in-flight batches: the
// batch worker awaits them itself on exit, and Shutdown only reaches this
// after that worker finished or was abandoned at shutdownTimeout. Waiting here
// too ran inFlight.Wait concurrently with the abandoned worker's inFlight.Add,
// which sync.WaitGroup forbids and punishes with a panic on the waiting
// goroutine, outside recoverPanic.
func (spanGrpc *spanGrpc) close() {
	if spanGrpc.spanConn != nil {
		spanGrpc.spanConn.Close()
	}
}

func (spanGrpc *spanGrpc) newSpanStream() bool {
	ctx, cancel := context.WithCancel(grpcMetadataContext(spanGrpc.agent, -1))
	stream, err := spanGrpc.spanClient.SendSpan(ctx)
	if err != nil {
		cancel()
		Log("grpc").Errorf("make span stream - %v", err)
		return false
	}

	spanGrpc.stream = &spanStream{stream: stream, cancel: cancel, streamAge: newStreamAge(spanGrpc.agent)}
	return true
}

func (spanGrpc *spanGrpc) newSpanStreamWithRetry() *spanStream {
	if newStreamWithRetry(spanGrpc.agent, spanGrpc.spanConn, spanGrpc.newSpanStream, "span") {
		return spanGrpc.stream
	}
	return &spanStream{}
}

func (s *spanStream) close() {
	if s.stream == nil {
		return
	}
	defer s.cancel()

	sendStreamWithTimeout(
		func() error {
			_, err := s.stream.CloseAndRecv()
			return err
		},
		s.cancel, closeStreamTimeOut, "span stream.CloseAndRecv()",
	)
	s.stream = nil
	Log("grpc").Infof("close span stream")
}

func (s *spanStream) sendSpan(chunk *spanChunk) error {
	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "span stream is nil")
	}

	builder := acquireSpanMessageBuilder()
	defer releaseSpanMessageBuilder(builder)

	gspan := builder.makePSpanMessage(chunk)

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("PSpanMessage Size: %d", proto.Size(gspan))
	}
	if IsLogLevelEnabled(logrus.TraceLevel) {
		Log("grpc").Tracef("PSpanMessage: %s", gspan.String())
	}
	if grpc.EnableTracing {
		// grpc-go's lazy trace keeps the request after Send returns.
		gspan = proto.Clone(gspan).(*pb.PSpanMessage)
	}

	err := sendStreamWithTimeout(func() error { return s.stream.Send(gspan) }, s.cancel, sendStreamTimeOut, "span stream.Send()")
	if err != nil {
		s.cancel()
	}
	return err
}

// collectSpanBatch gathers the first span plus queued spans until batch size or collect deadline is reached.
// The batch worker blocks for the first item, then uses a short collection window to improve
// batch density without delaying sparse traffic too long.
func (spanGrpc *spanGrpc) collectSpanBatch(first *spanChunk, queue *spanQueue) ([]*spanChunk, bool) {
	batch := make([]*spanChunk, 0, spanGrpc.batchSize)
	batch = append(batch, first)

	// The deadline timer is armed only once the queue runs dry: a batch that
	// fills straight from the queue never waits, so it never needs one.
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for len(batch) < spanGrpc.batchSize {
		if chunk, ok := queue.tryDequeue(); ok {
			batch = append(batch, chunk)
			continue
		}
		if timer == nil {
			timer = time.NewTimer(spanGrpc.batchCollectDeadline)
		}
		select {
		case <-queue.wake:
		case <-queue.done:
			// Drain what remains; report closed only once the queue is empty so
			// the worker comes back for a leftover larger than one batch.
			for len(batch) < spanGrpc.batchSize {
				chunk, ok := queue.tryDequeue()
				if !ok {
					return batch, true
				}
				batch = append(batch, chunk)
			}
			return batch, false
		case <-timer.C:
			return batch, false
		}
	}

	return batch, false
}

// sendSpanBatchAsync applies concurrent request limiting and sends a unary batch request.
// If no permit becomes available within the flush timeout, the whole batch is skipped rather than
// blocking the worker forever behind slow in-flight requests; completed calls always release their permit.
func (spanGrpc *spanGrpc) sendSpanBatchAsync(chunks []*spanChunk) {
	if !spanGrpc.acquireSpanBatchPermit() {
		// Counted with the queue's head-drops: these spans are lost the same
		// way, and reportSpanDrops would otherwise under-report the loss.
		spanGrpc.agent.spanDrops.record(int64(len(chunks)))
		Log("grpc").Infof(
			"SendSpanBatch skipped: %d spans dropped, no available permits within %s concurrentRequests:%d/%d",
			len(chunks),
			spanGrpc.batchFlushTimeout.String(),
			len(spanGrpc.concurrentRequestPermit),
			spanGrpc.maxConcurrentRequests,
		)
		return
	}

	builder := acquireSpanMessageBuilder()
	spanMessageBatch := builder.makePSpanMessageBatch(chunks)
	if len(spanMessageBatch.GetSpan()) == 0 {
		releaseSpanMessageBuilder(builder)
		spanGrpc.releaseSpanBatchPermit()
		return
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("SendSpanBatch size=%d messageSize=%d", len(spanMessageBatch.GetSpan()), proto.Size(spanMessageBatch))
	}
	if IsLogLevelEnabled(logrus.TraceLevel) {
		Log("grpc").Tracef("PSpanMessageBatch: %s", spanMessageBatch.String())
	}

	if grpc.EnableTracing {
		// Completed unary traces can also retain their request.
		spanMessageBatch = proto.Clone(spanMessageBatch).(*pb.PSpanMessageBatch)
	}

	spanGrpc.inFlight.Add(1)
	go func() {
		defer spanGrpc.inFlight.Done()
		defer spanGrpc.releaseSpanBatchPermit()
		defer releaseSpanMessageBuilder(builder)

		// Recovered like every other agent goroutine: a panic here must not
		// take the host process down, and the defers above still release the
		// permit and the builder.
		recoverPanic("span batch send", func() {
			ctx, cancel := context.WithTimeout(grpcMetadataContext(spanGrpc.agent, -1), sendStreamTimeOut)
			defer cancel()

			response, err := spanGrpc.spanClient.SendSpanBatch(ctx, spanMessageBatch)
			if err != nil {
				Log("grpc").Infof("SendSpanBatch failed - %v", err)
				return
			}
			handleSpanBatchResponse(response)
		})
	}()
}

// acquireSpanBatchPermit waits up to the configured flush timeout for an async batch request slot.
// The buffered channel acts like the semaphore: its capacity is maxConcurrentRequests.
func (spanGrpc *spanGrpc) acquireSpanBatchPermit() bool {
	// A free permit is the normal case; take it without allocating a timer.
	select {
	case spanGrpc.concurrentRequestPermit <- struct{}{}:
		return true
	default:
	}

	timer := time.NewTimer(spanGrpc.batchFlushTimeout)
	defer timer.Stop()

	select {
	case spanGrpc.concurrentRequestPermit <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

func (spanGrpc *spanGrpc) releaseSpanBatchPermit() {
	<-spanGrpc.concurrentRequestPermit
}

// awaitInFlightSpanBatch waits briefly for async sends.
// Shutdown is best effort: wait up to three seconds for accepted requests, then continue closing.
func (spanGrpc *spanGrpc) awaitInFlightSpanBatch() {
	done := make(chan struct{})
	go func() {
		spanGrpc.inFlight.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		Log("grpc").Warnf("Timed out waiting for in-flight span requests to complete")
	}
}

// handleSpanBatchResponse logs collector-side partial success without failing the sender loop.
// A partial success means the collector accepted the request but rejected some spans, so the
// sender records the warning and continues with later batches.
func handleSpanBatchResponse(response *pb.PSpanResultBatch) {
	if response == nil || response.GetPartialSuccess() == nil {
		return
	}

	partialSuccess := response.GetPartialSuccess()
	rejectedSpans := partialSuccess.GetRejectedSpans()
	if rejectedSpans > 0 {
		Log("grpc").Warnf(
			"SendSpanBatch partial success: rejectedSpans=%d, errorId=%d, errorMessage=%s",
			rejectedSpans,
			partialSuccess.GetErrorId(),
			partialSuccess.GetErrorMessage(),
		)
		return
	}

	if partialSuccess.GetErrorMessage() != "" {
		Log("grpc").Infof(
			"SendSpanBatch warning: errorId=%d, %s",
			partialSuccess.GetErrorId(),
			partialSuccess.GetErrorMessage(),
		)
	}
}

func (b *spanMessageBuilder) makePSpanMessageBatch(chunks []*spanChunk) *pb.PSpanMessageBatch {
	spanMessages := b.messageLists.take(len(chunks))[:0]
	for _, chunk := range chunks {
		if chunk == nil || chunk.span == nil {
			continue
		}
		spanMessages = append(spanMessages, b.makePSpanMessage(chunk))
	}

	return &pb.PSpanMessageBatch{Span: spanMessages}
}

// makePSpanMessage converts one dequeued chunk: final synchronous spans become
// PSpan messages; non-final chunks and async spans keep the PSpanChunk shape.
func (b *spanMessageBuilder) makePSpanMessage(chunk *spanChunk) *pb.PSpanMessage {
	if !chunk.final || chunk.span.isAsyncSpan() {
		return b.makePSpanChunk(chunk)
	}
	return b.makePSpan(chunk)
}

func (b *spanMessageBuilder) makePSpan(chunk *spanChunk) *pb.PSpanMessage {
	span := chunk.span
	if span.apiId == 0 && span.operationName != "" {
		span.annotations.AppendString(AnnotationApi, span.operationName)
	}

	pspan := b.spans.get()
	pspan.Version = 1
	txId := b.txIds.get()
	txId.AgentId = span.txId.AgentId
	txId.AgentStartTime = span.txId.StartTime
	txId.Sequence = span.txId.Sequence
	pspan.TransactionId = txId
	pspan.SpanId = span.spanId
	pspan.ParentSpanId = span.parentSpanId
	pspan.StartTime = span.startTime.UnixMilli()
	pspan.Elapsed = int32(span.elapsed)
	pspan.ServiceType = span.serviceType

	acceptEvent := b.acceptEvents.get()
	acceptEvent.Rpc = validUTF8(span.rpcName)
	acceptEvent.EndPoint = validUTF8(span.endPoint)
	acceptEvent.RemoteAddr = validUTF8(span.remoteAddr)
	parentInfo := b.parentInfos.get()
	parentInfo.ParentApplicationName = validUTF8(span.parentAppName)
	parentInfo.ParentApplicationType = int32(span.parentAppType)
	parentInfo.AcceptorHost = validUTF8(span.acceptorHost)
	parentInfo.ParentServiceName = validUTF8(span.parentServiceName)
	acceptEvent.ParentInfo = parentInfo
	pspan.AcceptEvent = acceptEvent

	pspan.Annotation = span.annotations.getListInto(b)
	pspan.ApiId = span.apiId
	pspan.Flag = int32(span.flags)
	pspan.SpanEvent = b.makePSpanEventList(chunk)
	pspan.Err = int32(span.err)
	pspan.ApplicationServiceType = span.agent.appType
	pspan.LoggingTransactionInfo = span.loggingInfo

	if span.errorString != "" {
		exceptionInfo := b.intStringValues.get()
		exceptionInfo.IntValue = span.errorFuncId
		exceptionInfo.StringValue = b.stringValue(span.errorString)
		pspan.ExceptionInfo = exceptionInfo
	}

	oneof := b.spanOneofs.get()
	oneof.Span = pspan
	gspan := b.messages.get()
	gspan.Field = oneof
	return gspan
}

func (b *spanMessageBuilder) makePSpanChunk(chunk *spanChunk) *pb.PSpanMessage {
	span := chunk.span

	pchunk := b.chunks.get()
	pchunk.Version = 1
	txId := b.txIds.get()
	txId.AgentId = span.txId.AgentId
	txId.AgentStartTime = span.txId.StartTime
	txId.Sequence = span.txId.Sequence
	pchunk.TransactionId = txId
	pchunk.SpanId = span.spanId
	pchunk.KeyTime = chunk.keyTime
	pchunk.EndPoint = validUTF8(chunk.endPoint)
	pchunk.SpanEvent = b.makePSpanEventList(chunk)
	pchunk.ApplicationServiceType = span.agent.appType

	if span.isAsyncSpan() {
		localAsyncId := b.localAsyncIds.get()
		localAsyncId.AsyncId = span.asyncId
		localAsyncId.Sequence = span.asyncSequence
		pchunk.LocalAsyncId = localAsyncId
	}

	oneof := b.chunkOneofs.get()
	oneof.SpanChunk = pchunk
	gspan := b.messages.get()
	gspan.Field = oneof
	return gspan
}

func (b *spanMessageBuilder) makePSpanEventList(chunk *spanChunk) []*pb.PSpanEvent {
	spanEventList := b.eventLists.take(len(chunk.eventChunk))
	for i, event := range chunk.eventChunk {
		spanEventList[i] = b.makePSpanEvent(event)
	}
	return spanEventList
}

func (b *spanMessageBuilder) makePSpanEvent(event *spanEvent) *pb.PSpanEvent {
	if event.apiId == 0 && event.operationName != "" {
		event.annotations.AppendString(AnnotationApi, event.operationName)
	}

	aSpanEvent := b.events.get()
	aSpanEvent.Sequence = event.sequence
	aSpanEvent.Depth = event.depth
	aSpanEvent.StartElapsed = int32(event.startElapsed)
	aSpanEvent.EndElapsed = int32(event.endElapsed)
	aSpanEvent.ServiceType = event.serviceType
	aSpanEvent.Annotation = event.annotations.getListInto(b)
	aSpanEvent.ApiId = event.apiId
	aSpanEvent.AsyncEvent = event.asyncId

	if event.errorString != "" {
		exceptionInfo := b.intStringValues.get()
		exceptionInfo.IntValue = event.errorFuncId
		exceptionInfo.StringValue = b.stringValue(event.errorString)
		aSpanEvent.ExceptionInfo = exceptionInfo
	}

	if event.destinationId != "" {
		messageEvent := b.messageEvents.get()
		messageEvent.NextSpanId = event.nextSpanId
		messageEvent.EndPoint = validUTF8(event.endPoint)
		messageEvent.DestinationId = validUTF8(event.destinationId)
		oneof := b.nextEventOneofs.get()
		oneof.MessageEvent = messageEvent
		next := b.nextEvents.get()
		next.Field = oneof
		aSpanEvent.NextEvent = next
	}

	return aSpanEvent
}

type statGrpc struct {
	statConn   *grpc.ClientConn
	statClient pb.StatClient
	stream     *statStream
	agent      *agent
}

type statStream struct {
	stream pb.Stat_SendAgentStatClient
	cancel context.CancelFunc
	streamAge
}

func newStatGrpc(agent *agent) (*statGrpc, error) {
	conn, err := connectCollector(agent.config, CfgCollectorStatPort)
	if err != nil {
		return nil, err
	}

	return &statGrpc{
		statConn:   conn,
		statClient: pb.NewStatClient(conn),
		agent:      agent,
	}, nil
}

func (statGrpc *statGrpc) close() {
	if statGrpc.statConn != nil {
		statGrpc.statConn.Close()
	}
}

func (statGrpc *statGrpc) newStatStream() bool {
	ctx, cancel := context.WithCancel(grpcMetadataContext(statGrpc.agent, -1))
	stream, err := statGrpc.statClient.SendAgentStat(ctx)
	if err != nil {
		cancel()
		Log("grpc").Errorf("make stat stream - %v", err)
		return false
	}

	statGrpc.stream = &statStream{stream: stream, cancel: cancel, streamAge: newStreamAge(statGrpc.agent)}
	return true
}

func (statGrpc *statGrpc) newStatStreamWithRetry() *statStream {
	if newStreamWithRetry(statGrpc.agent, statGrpc.statConn, statGrpc.newStatStream, "stat") {
		return statGrpc.stream
	}
	return &statStream{}
}

func (s *statStream) close() {
	if s.stream == nil {
		return
	}
	defer s.cancel()

	sendStreamWithTimeout(
		func() error {
			_, err := s.stream.CloseAndRecv()
			return err
		},
		s.cancel, closeStreamTimeOut, "stat stream.CloseAndRecv()",
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

	err := sendStreamWithTimeout(func() error { return s.stream.Send(stats) }, s.cancel, sendStreamTimeOut, "stat stream.Send()")
	if err != nil {
		s.cancel()
	}
	return err
}

func makePAgentStatBatch(stats []*inspectorStats) *pb.PStatMessage {
	l := make([]*pb.PAgentStat, 0, len(stats))
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
	l := make([]*pb.PEachUriStat, 0, len(stat.urlMap))
	for _, e := range stat.urlMap {
		l = append(l, makePEachUriStat(e))
	}
	return l
}

func makePEachUriStat(e *eachUrlStat) *pb.PEachUriStat {
	return &pb.PEachUriStat{
		Uri:             validUTF8(e.url),
		TotalHistogram:  makePUriHistogram(e.totalHistogram),
		FailedHistogram: makePUriHistogram(e.failedHistogram),
		Timestamp:       e.tickTime.UnixNano() / int64(time.Millisecond),
	}
}

func makePUriHistogram(h *urlStatHistogram) *pb.PUriHistogram {
	if h == nil || h.isEmpty() {
		return &pb.PUriHistogram{}
	}

	return &pb.PUriHistogram{
		Total:     h.total,
		Max:       h.max,
		Histogram: h.histogram,
	}
}

type cmdGrpc struct {
	cmdConn    *grpc.ClientConn
	cmdClient  pb.ProfilerCommandServiceClient
	stream     *cmdStream
	agent      *agent
	atcStreams atcStreams
}

type cmdStream struct {
	stream pb.ProfilerCommandService_HandleCommandClient
	cancel context.CancelFunc
	streamAge
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
		ctx, cancel = context.WithCancel(grpcMetadataContext(cmdGrpc.agent, -1))
	} else {
		ctx, cancel = context.WithDeadline(grpcMetadataContext(cmdGrpc.agent, -1), age.expiresAt)
	}
	stream, err := cmdGrpc.cmdClient.HandleCommand(ctx)
	if err != nil {
		cancel()
		Log("grpc").Errorf("make command stream - %v", err)
		return false
	}

	cmdGrpc.stream = &cmdStream{stream: stream, cancel: cancel, streamAge: age}
	return true
}

func (cmdGrpc *cmdGrpc) newCommandStreamWithRetry() *cmdStream {
	if newStreamWithRetry(cmdGrpc.agent, cmdGrpc.cmdConn, cmdGrpc.newHandleCommandStream, "command") {
		return cmdGrpc.stream
	}
	return &cmdStream{}
}

func (s *cmdStream) close() {
	if s.stream == nil {
		return
	}
	defer s.cancel()

	sendStreamWithTimeout(func() error { return s.stream.CloseSend() }, s.cancel, closeStreamTimeOut, "cmd stream.CloseSend()")
	s.stream = nil
	Log("grpc").Infof("close command stream")
}

func (s *cmdStream) sendCommandMessage() error {
	var gCmd *pb.PCmdMessage

	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "command stream is nil")
	}

	sKeys := make([]int32, 0, 4)
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

	err := sendStreamWithTimeout(func() error { return s.stream.Send(gCmd) }, s.cancel, sendStreamTimeOut, "cmd stream.Send()")
	if err != nil {
		s.cancel()
	}
	return err
}

// sendFailMessage rejects a command on the command stream itself, which is the
// only channel the protocol offers for a request the agent will not serve:
// PCmdMessage.failMessage. Matches the C++ agent's write_fail_message(), which
// sets the request id and a reason and leaves status at its default.
func (s *cmdStream) sendFailMessage(reqId int32, msg string) error {
	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "command stream is nil")
	}

	gCmd := &pb.PCmdMessage{
		Message: &pb.PCmdMessage_FailMessage{
			FailMessage: &pb.PCmdResponse{
				ResponseId: reqId,
				Message:    &wrappers.StringValue{Value: msg},
			},
		},
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("PCmdMessage: %s", gCmd.String())
	}

	err := sendStreamWithTimeout(func() error { return s.stream.Send(gCmd) }, s.cancel, sendStreamTimeOut, "cmd stream.Send()")
	if err != nil {
		s.cancel()
	}
	return err
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
	defer s.cancel()

	sendStreamWithTimeout(
		func() error {
			_, err := s.stream.CloseAndRecv()
			return err
		},
		s.cancel, closeStreamTimeOut, "arc stream.CloseAndRecv()",
	)
	s.stream = nil
}

func (s *activeThreadCountStream) sendActiveThreadCount() error {
	var gRes *pb.PCmdActiveThreadCountRes

	if s.stream == nil {
		return status.Errorf(codes.Unavailable, "active thread count stream is nil")
	}

	now := time.Now()
	activeThreadCount := s.streams.activeSpanCount(now)
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

	err := sendStreamWithTimeout(func() error { return s.stream.Send(gRes) }, s.cancel, sendStreamTimeOut, "arc stream.Send()")
	if err != nil {
		s.cancel()
	}
	return err
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

	ctx, cancel := context.WithTimeout(grpcMetadataContext(cmdGrpc.agent, -1), commandStreamTimeOut)
	defer cancel()

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

		byHeader := dump.indexByHeader(threadName)
		selected := make([]*goroutine, 0)
		for _, tn := range threadName {
			g := byHeader[tn]
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

	ctx, cancel := context.WithTimeout(grpcMetadataContext(cmdGrpc.agent, -1), commandStreamTimeOut)
	defer cancel()

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

	ctx, cancel := context.WithTimeout(grpcMetadataContext(cmdGrpc.agent, -1), commandStreamTimeOut)
	defer cancel()

	_, err := cmdGrpc.cmdClient.CommandEcho(ctx, gRes)
	if err != nil {
		Log("grpc").Errorf("send echo response - %v", err)
	}
}
