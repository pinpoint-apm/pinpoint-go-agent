package pinpoint

import (
	"cmp"
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
	"strings"
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

	// headerSupportCommandCode carries the command codes this agent serves on
	// the HandleCommandV2 stream. grpc-go lower-cases metadata keys, so the
	headerSupportCommandCode = "supportcommandcode"
)

// supportedCommandCodes lists the commands serveCommandStream dispatches, in
// switch in serveCommandStream.
var supportedCommandCodes = []int32{
	int32(pb.PCommandType_ECHO),
	int32(pb.PCommandType_ACTIVE_THREAD_COUNT),
	int32(pb.PCommandType_ACTIVE_THREAD_DUMP),
	int32(pb.PCommandType_ACTIVE_THREAD_LIGHT_DUMP),
}

// supportCommandCodeHeader renders supportedCommandCodes the way the collector
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
	return metadata.NewOutgoingContext(context.Background(), metadata.New(m))
}

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
	// interval lands within +/-30% of the ceiling rather than always on it.
	return randomize(time.Duration(dur), backOffJitter)
}

// randomize returns d scaled by a uniform factor in [1-jitter, 1+jitter], the
func randomize(d time.Duration, jitter float64) time.Duration {
	return time.Duration(float64(d) * (1 - jitter + rand.Float64()*2*jitter))
}

// streamAgeJitter randomizes every connection and stream max age by +/-10%,
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
	// with backOffUntilReady until it succeeds, so a hung collector costs a
	// short wait and a retry instead of a minute of boot latency.
	agentGrpcTimeOut = 5 * time.Second

	// metaGrpcTimeOut bounds each metadata RPC (api/string/sql/sqlUid/
	// exception). Unlike AgentInfo these run under sendMetaWorker's
	// metaMaxConcurrentRequests permits, and a failed send evicts the item's
	// cache entry so it is re-registered on next use. Under the former 60s
	// deadline a hung collector pinned every permit for up to
	// 60s x metaRetryMaxAttempts; metaChan overflowed, tryEnqueueMeta
	// head-dropped a queued item (evicting its cache entry), and each drop or
	// timeout re-queued the same metadata -- an amplification loop that lasted
	// request_timeout for unary RPCs: ample for a healthy collector, short
	// enough that permits recycle before the queue fills. Kept as a constant
	// deployment ever needs to tune it.
	metaGrpcTimeOut = 5 * time.Second

	sendStreamTimeOut    = 5 * time.Second
	closeStreamTimeOut   = 1 * time.Second
	commandStreamTimeOut = 1 * time.Second

	// Defaults for the Collector.Grpc.* config keys. doc/java_parity.md ("gRPC
	// comments here only say what this agent does and why.
	grpcKeepAliveTime               = 30000 // ms
	grpcKeepAliveTimeout            = 60000 // ms
	grpcKeepAlivePermitWithoutCalls = false
	grpcFlowControlWindow           = 1 * 1024 * 1024
	grpcWriteBufferSize             = 1 * 1024 * 1024
	grpcMaxMessageSize              = 4 * 1024 * 1024
	grpcMaxHeaderListSize           = 8 * 1024
	// grpc-go v1.82.1 puts a channel into IDLE after 30 minutes without an RPC
	// (dialoptions.go defaultDialOptions: idleTimeout 30 * time.Minute) and
	// documents WithIdleTimeout(0) as the way to disable idling. 0 here is
	// that disable value, and it is the default: see dialOptions.
	grpcIdleTimeout = 0 // ms

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
	idleTimeout       time.Duration
}

func newGrpcChannelOptions(config *Config) grpcChannelOptions {
	return grpcChannelOptions{
		connectionMaxAge: time.Duration(config.Int(CfgCollectorGrpcConnectionMaxAge)) * time.Millisecond,
		idleTimeout:      time.Duration(config.Int(CfgCollectorGrpcIdleTimeout)) * time.Millisecond,
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
		// The one FlowControlWindow key is applied to both windows.
		grpc.WithInitialWindowSize(o.flowControlWindow),
		grpc.WithInitialConnWindowSize(o.flowControlWindow),
		grpc.WithWriteBufferSize(o.writeBufferSize),
		grpc.WithMaxHeaderListSize(o.maxHeaderListSize),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(o.maxSendMsgSize),
			grpc.MaxCallRecvMsgSize(o.maxRecvMsgSize)),
		// Idle timeout, disabled by default (Collector.Grpc.IdleTimeout = 0).
		// Without this option grpc-go v1.82.1 uses 30 minutes (dialoptions.go
		// defaultDialOptions; WithIdleTimeout's doc: "A default timeout of 30
		// minutes will be used if this dial option is not set at dial time and
		// idleness can be disabled by passing a timeout of zero"). When the
		// timer fires the channel drops its transport and goes IDLE, so the
		// next send pays a reconnect, backOffUntilReady may wait out a backoff,
		// and the keepalive pings stop with the transport, which lets a
		// firewall or L4 balancer forget the connection unnoticed. The idle
		// manager counts an open stream as an ongoing RPC (stream.go
		// OnCallBegin/OnCallEnd), so the agent channel (ping and command
		// streams) and the stat channel (stat stream) rarely qualify; the span
		// channel in Span.Batch.Enable mode sends unary SendSpanBatch RPCs and
		// goes quiet whenever the application has no traffic, which is exactly
		// where the 30-minute default would bite.
		//
		// Two things this does not change. Keepalive pings only run while a
		// stream is open unless PermitWithoutStream is set (default false), so
		// a unary-only channel with no traffic sends no pings even with idling
		// off; keeping the transport up still spares the reconnect and the
		// backoff. And a channel that leaves idle re-resolves the collector
		// host (dns resolver, see collectorTarget), so an idle reconnect would
		// pick up a DNS change; Collector.Grpc.ConnectionMaxAge
		// already provides that on purpose, while traffic flows, without
		// tearing the transport down first. ConnectionMaxAge and the idle
		// timeout are independent: renewal acts only on a channel that is
		// sending, idling only on one that is not.
		grpc.WithIdleTimeout(o.idleTimeout),
	}
	// Only when enabled: with the option absent the channel keeps grpc-go's
	// default pick_first policy, so the default configuration is unchanged.
	if o.connectionMaxAge > 0 {
		opts = append(opts, grpc.WithDefaultServiceConfig(expiringPickFirstServiceConfig(o.connectionMaxAge)))
	}
	return opts
}

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
	target := collectorTarget(config, addr)
	Log("grpc").Infof("connect to collector: %s (ssl: %v)", target, config.Bool(CfgCollectorGrpcSslEnable))
	// The channel starts idle; the first RPC or waitUntilReady connects it.
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		Log("grpc").Errorf("connect to collector - %s, %v", target, err)
	}
	return conn, err
}

// collectorTarget turns the collector address into a gRPC target by naming the
// resolver explicitly. The scheme belongs here and not in serverAddr, whose
// bare host:port is also what localIP probes.
//
// dns is the default. It resolves the collector host into the channel's address
// list and keeps re-resolving. SubconnectionExpiringLoadBalancer
// (grpc_balancer.go) relies on this: with
// several A records the picked SubConn holds them all, so a rotation or a
// failure moves to another collector instance, and its ResolveNow on failure
// and its address-change readdressing both act on a resolver that can answer.
// The dns resolver also re-resolves at most every 30s
// (internal/resolver/dns.MinResolutionInterval), so a very short
// Collector.Grpc.ConnectionMaxAge rotates faster than the records refresh -
// see doc/config.md.
//
// passthrough is the pre-dns-resolver behavior, kept only as a rollback lever
// (Collector.Grpc.DnsResolverEnable=false). It hands the target to the dialer
// untouched: the dialer resolves the host for every new connection, so a
// replacement connection still sees current DNS records, but the channel holds
// a one-element address list, which leaves the balancer no address to move to
// and its ResolveNow nothing to re-resolve.
//
// An IP literal (including a bracketed IPv6 one) or a name in /etc/hosts, such
// as the default localhost, is handled by the dns resolver itself: a literal
// resolves once with no lookup at all, and a name goes through the same
// stdlib resolution that honors /etc/hosts.
func collectorTarget(config *Config, addr string) string {
	if !config.Bool(CfgCollectorGrpcDnsResolverEnable) {
		return "passthrough:///" + addr
	}
	return "dns:///" + addr
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
	// registerRetryDelay overrides the registration pause in tests. Production
	// uses registerRetryInterval, the configured Collector.AgentInfo.SendRetryInterval.
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

var getHostName = func() string {
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

func makeGoLibraryInfo() *pb.PServiceInfo {
	libs := make([]string, 0)
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			libs = append(libs, validUTF8(dep.Path+" ("+dep.Version+")"))
		}
	}

	return &pb.PServiceInfo{
		ServiceName: "Go (" + runtime.GOOS + ", " + runtime.GOARCH + ", " + runtime.GOROOT() + ")",
		ServiceLib:  libs,
	}
}

// makeServerMetaData builds PServerMetaData: the configured ServerInfo (or the
// default), argv, and the agent's own Go build entry followed by the host's
// WithServiceInfo entries. Host strings are sanitized like argv: the collector
// rejects a PAgentInfo with invalid UTF-8 and the same bytes would be re-sent
// on every retry.
func makeServerMetaData(config *Config) *pb.PServerMetaData {
	vmArgs := make([]string, 0, len(os.Args)-1)
	for _, arg := range os.Args[1:] {
		vmArgs = append(vmArgs, validUTF8(arg))
	}

	services := []*pb.PServiceInfo{makeGoLibraryInfo()}
	for _, si := range config.serviceInfo {
		libs := make([]string, 0, len(si.libs))
		for _, lib := range si.libs {
			libs = append(libs, validUTF8(lib))
		}
		services = append(services, &pb.PServiceInfo{ServiceName: validUTF8(si.name), ServiceLib: libs})
	}

	return &pb.PServerMetaData{
		ServerInfo:  validUTF8(cmp.Or(config.String(CfgServerInfo), "Go Application")),
		VmArg:       vmArgs,
		ServiceInfo: services,
	}
}

func (agentGrpc *agentGrpc) makeAgentInfo() (context.Context, *pb.PAgentInfo) {
	// Registration carries the only strings on this path the agent does not
	// produce itself - raw argv, a host name and the server metadata - and a
	// PAgentInfo the collector rejects for invalid UTF-8 is a permanent failure:
	// the same bytes are sent on every retry, and an unregistered agent has no
	// traces stored at all.

	agentInfo := &pb.PAgentInfo{
		Hostname:     validUTF8(getHostName()),
		Ip:           validUTF8(localIP(serverAddr(agentGrpc.agent.config, CfgCollectorAgentPort))),
		ServiceType:  agentGrpc.agent.appType,
		Pid:          int32(os.Getpid()),
		AgentVersion: Version,
		VmVersion:    runtime.Version(),

		ServerMetaData: makeServerMetaData(agentGrpc.agent.config),

		JvmInfo: &pb.PJvmInfo{
			Version:   0,
			VmVersion: fmt.Sprintf("%s(%d)", runtime.Version(), goIdOffset),
			// Same reason as PJvmGc.type in makePAgentStat: Go's GC is none of
			// the JVM collectors. See doc/java_parity.md.
			GcType: pb.PJvmGcType_JVM_GC_TYPE_UNKNOWN,
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

// registrationWaitLogInterval paces the line that says why tracing is off while
// registration_wait_log_interval. A variable so tests can shorten it.
var registrationWaitLogInterval = 30 * time.Second

// registerRetryInterval is the pause between boot registration attempts:
// registerAgentWithRetry, randomized +/-30% so agents restarted together do not
// retry in lockstep. Non-escalating, as there too - a rejecting collector is
// polled at the interval the operator configured, and the connection readiness
// wait that follows each attempt is what backs off during an outage.
func registerRetryInterval(config *Config) time.Duration {
	interval := time.Duration(config.Int(CfgCollectorAgentInfoSendRetryInterval)) * time.Millisecond
	if interval <= 0 {
		// NewConfig floors this at 1ms, but Config.Set bypasses that, and this
		// loop is unbounded: a zero pause would poll the collector flat out.
		interval = defaultAgentInfoSendRetryInterval * time.Millisecond
	}
	return randomize(interval, backOffJitter)
}

func (agentGrpc *agentGrpc) registerAgentWithRetry() bool {
	// Tracing is off for the whole wait - NewSpan is a noop and nothing is
	// collected - and the per-attempt failure lines say nothing about that
	// consequence, so they read as a plain connectivity problem. Report the
	// a silent agent - one whose agent port alone is blocked, say - can tell
	// this wait apart from a healthy agent nobody instrumented.
	started := time.Now()
	nextLog := started.Add(registrationWaitLogInterval)
	rejected := false

	for !agentGrpc.agent.stopping() {
		if now := time.Now(); !now.Before(nextLog) {
			reason := "collector unreachable or the send failed"
			if rejected {
				reason = "collector rejected the registration, likely permanent"
			}
			Log("agent").Infof("still waiting for agent registration after %dms (%s): tracing stays disabled "+
				"(NewSpan is a noop and no stats are collected) until the collector accepts AgentInfo",
				now.Sub(started).Milliseconds(), reason)
			nextLog = now.Add(registrationWaitLogInterval)
		}

		// build_agent_info per attempt) do: an outage outlives the values in
		// here. The IP is the usual one - a NIC still coming up at boot leaves
		// it empty - but the hostname and the server metadata can move too, and
		// whatever this loop happened to capture first is what the collector
		// would carry until the next refresh cycle. Nothing in here does I/O
		// beyond a local route lookup and an interface scan, and attempts are
		// at least backOffInitialInterval apart, so the repeat is free.
		ctx, agentInfo := agentGrpc.makeAgentInfo()

		res, err := agentGrpc.sendAgentInfo(ctx, agentInfo)
		if err == nil {
			if res.Success {
				Log("agent").Infof("success to register agent")
				return true
			}
			// does: the collector answers it while initializing or briefly
			// refusing, and giving up would leave this process dead until restart.
			Log("agent").Warnf("register agent - %s, retrying", res.Message)
		}
		// A transport that worked and a collector that said no are different
		// stories for the waiting operator: only the second one is likely to
		// stay broken until the config changes.
		rejected = err == nil

		retryDelay := agentGrpc.registerRetryDelay
		if retryDelay <= 0 {
			retryDelay = registerRetryInterval(agentGrpc.agent.config)
		}
		if !sleepUnlessStopped(agentGrpc.agent, retryDelay) {
			return false
		}
		backOffUntilReady(agentGrpc.agent, agentGrpc.agentConn, "agent")
	}
	return false
}

// refreshAgentInfo re-sends AgentInfo once, trying up to maxTry sends spaced
// retryInterval apart. Unlike boot-time registration it never loops forever:
// a failed refresh is simply left for the next refresh cycle, mirroring the
func (agentGrpc *agentGrpc) refreshAgentInfo(maxTry int, retryInterval time.Duration) bool {
	for try := 0; try < maxTry && !agentGrpc.agent.stopping(); try++ {
		// Rebuilt per attempt for the same reason registerAgentWithRetry does
		// build_agent_info per send_agent_info_once) do: attempts are
		// retryInterval apart, so the host name, IP or server metadata can move
		// between them, and this refresh is what corrects the collector's copy.
		// Cheap to repeat - a local route lookup and an interface scan - and
		// maxTry bounds how often. The context is rebuilt with it: it carries
		// the agent headers over the agent-lifetime stop signal and no deadline
		// of its own, so per-attempt and per-call are the same context here.
		ctx, agentInfo := agentGrpc.makeAgentInfo()

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

// agent's meta_retry_max_attempts. Once the budget is spent the item's cache
// entry is released so its next use registers it again; without a bound a
// metadata item facing a dead collector would circulate through the retry
// schedule forever.
const metaRetryMaxAttempts = 3

// metaRetryDelay is the pause between two sends of one metadata item. It is
// also how long a rejected item's cache entry stays in place before it is
// released (see metaVerdictOf). A collector that is up but refusing
// (Unavailable) answers at once, so without a pause every attempt in the
// budget would fire back to back against an already overloaded collector.
const metaRetryDelay = time.Second

// meta_retry_queue_size. The schedule is budgeted separately from metaChan on
// purpose: while a collector outage lasts, every failed send comes back as a
// retry, and a shared budget lets those retries fill the queue and starve new
// metadata. New metadata dropped on overflow releases its cache entry, which
// makes the next span register the same item again -- a drop-feeds-inflow
// amplification loop that runs until the collector recovers. Two bounds keep
// separation from its HashedWheelTimer, which never queues a retry at all.
const metaRetryQueueSize = 1000

// metaMaxConcurrentRequests bounds how many metadata sends sendMetaWorker
// meta_max_concurrent_requests.
const metaMaxConcurrentRequests = 4

// metaVerdict is what becomes of a metadata item after one send attempt.
type metaVerdict int

const (
	// metaDelivered: the collector accepted the item.
	metaDelivered metaVerdict = iota
	// metaRetryLater: a transport failure with attempt budget left. The item
	// goes to the retry schedule; the cache entry stays.
	metaRetryLater
	// metaGiveUp: the attempt budget is spent. The cache entry is released at
	// once so the next use registers the item again.
	metaGiveUp
	// metaRejected: a failure a retry cannot change. The cache entry is
	// released after one metaRetryDelay rather than at once: the release is
	// what makes the next span miss the cache and re-send, so an immediate
	// one turned a rejecting collector into a re-send per span, bounded only
	// by the in-flight permits. Parking it makes the recovery probe periodic,
	metaRejected
)

// metaVerdictOf classifies the error of a metadata send. attempts counts the
// sends made so far, this one included. The send itself never waits: a retry
// is a new send from the retry schedule, so no goroutine sits on a permit
// while the collector is down, and no in-flight slot is pinned by the wait.
func metaVerdictOf(err error, attempts int) metaVerdict {
	switch {
	case err == nil:
		return metaDelivered
	case !isRetryableError(err):
		return metaRejected
	case attempts >= metaRetryMaxAttempts:
		return metaGiveUp
	default:
		return metaRetryLater
	}
}

// metaResult turns a collector rejection (PResult.Success=false) into an error
// so the caller stops treating the send as delivered. The code is
// FailedPrecondition, deliberately outside isRetryableError's list: a
// rejection is a semantic verdict on the payload (schema mismatch and the
// like), so re-sending the same bytes twice more is pure load on the
// collector. metaVerdictOf therefore never retries it, and sendMetaWorker
// releases the cache entry after one delay so the next use registers a
// fresh id -- instead of every later span referencing an id the collector
// (GrpcMetadata::process_completed) drops it and delays the cache release,
// and Go does the same (metaRejected). See doc/java_parity.md.
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

func (agentGrpc *agentGrpc) sendApiMetadataOnce(apiId int32, api string, line int, apiType int) error {
	apiMeta := pb.PApiMetaData{
		ApiId:   apiId,
		ApiInfo: validUTF8(api),
		Line:    int32(line),
		Type:    int32(apiType),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("api metadata: %s", apiMeta.String())
	}

	return agentGrpc.sendApiMetadata(&apiMeta)
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

func (agentGrpc *agentGrpc) sendStringMetadataOnce(strId int32, str string) error {
	strMeta := pb.PStringMetaData{
		StringId:    strId,
		StringValue: validUTF8(str),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("string metadata: %s", strMeta.String())
	}

	return agentGrpc.sendStringMetadata(&strMeta)
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

func (agentGrpc *agentGrpc) sendSqlMetadataOnce(sqlId int32, sql string) error {
	sqlMeta := pb.PSqlMetaData{
		SqlId: sqlId,
		Sql:   validUTF8(sql),
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("sql metadata: %s", sqlMeta.String())
	}

	return agentGrpc.sendSqlMetadata(&sqlMeta)
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

func (agentGrpc *agentGrpc) sendSqlUidMetadataOnce(sqlUid []byte, sql string) error {
	sqlUidMeta := pb.PSqlUidMetaData{
		SqlUid: sqlUid,
		Sql:    sql,
	}

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("sql uid metadata: %s", sqlUidMeta.String())
	}

	return agentGrpc.sendSqlUidMetadata(&sqlUidMeta)
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

func (agentGrpc *agentGrpc) sendExceptionMetadataOnce(exception *exceptionMeta) error {
	exceptMeta := makePExceptionMetaData(exception)

	if IsLogLevelEnabled(logrus.DebugLevel) {
		Log("grpc").Debugf("exception metadata: %s", exceptMeta.String())
	}

	// Unlike the other metadata types, a failure here releases nothing:
	// exception metadata is never cached (deleteMetaCache is a no-op for
	// exceptionMeta), so there is no stale id to invalidate.
	return agentGrpc.sendExceptionMetadata(exceptMeta)
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
	return &pb.PException{
		ExceptionClassName: e.className,
		ExceptionMessage:   abbreviateString(e.callstack.err.Error(), maxExceptionMessageSize),
		StartTime:          e.callstack.errorTime.UnixNano() / int64(time.Millisecond),
		ExceptionId:        e.exceptionId,
		ExceptionDepth:     e.depth,
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

// channelStateLog is the diagnostic record of one collector channel's
// readiness, keyed by the `which` label the wait loops pass: the throttles
// for its log lines and the lifetime counters the recovery summary prints.
// Package level, like the other logThrottle sites, because backOffUntilReady
// calls waitUntilReady once per attempt and the throttle window must span
// those calls or a dead collector logs a state line per attempt.
//
// ConnectivityStateMonitor of AbstractGrpcDataSender (transition lines) and
// the Channelz reporters under sender/grpc/metric (counters). Nothing is
// added to PAgentStat.
type channelStateLog struct {
	mu sync.Mutex
	// unreadySince is when the channel was last observed leaving READY (or
	// first observed not READY); zero while READY.
	unreadySince time.Time
	everReady    bool // READY was observed at least once
	lostReady    bool // READY was observed, then lost, and not yet regained
	// recoveryLogged is set once a recovery line stood in the current throttle
	// window, so a flapping channel gets one unthrottled recovery per window
	// rather than one per flap.
	recoveryLogged bool
	lostCount      int64         // lifetime: times READY was observed lost
	unreadyTotal   time.Duration // lifetime: time spent waiting for READY

	// transitions throttles the state lines, outages the recovery summary and
	// notReady the WARN a wait that timed out leaves; each on its own window
	// so one kind cannot starve another.
	transitions, outages, notReady logThrottle
}

var (
	channelStateLogsMu sync.Mutex
	// channelStateLogs is one record per channel label. Per channel rather
	// than shared: the four channels flap together when the collector goes,
	// and a shared window would let the agent channel's lines hide the span
	// channel's first recovery.
	channelStateLogs = map[string]*channelStateLog{}
)

func channelStateLogFor(which string) *channelStateLog {
	channelStateLogsMu.Lock()
	defer channelStateLogsMu.Unlock()
	l := channelStateLogs[which]
	if l == nil {
		l = &channelStateLog{
			transitions: logThrottle{src: "grpc"},
			outages:     logThrottle{src: "grpc"},
			notReady:    logThrottle{src: "grpc"},
		}
		channelStateLogs[which] = l
	}
	return l
}

// observeNotReady records that a wait found the channel in state, which is
// not READY. Counted as a loss only once per outage: the wait loops re-enter
// with the same outage in progress on every back-off attempt.
func (l *channelStateLog) observeNotReady() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unreadySince.IsZero() {
		l.unreadySince = time.Now()
	}
	if l.everReady && !l.lostReady {
		l.lostReady = true
		l.lostCount++
	}
}

// logTransition logs an observed state change of the channel as one line,
// throttled to a line per dropReportInterval with the count of lines it held
// back, except for the line the outage is diagnosed from: the first READY of
// the channel, and the first recovery to READY within a throttle window, are
// always logged.
//
// "Observed": GetState is a sampled read, not a stream of states. Transitions
// that happen between two WaitForStateChange returns are never seen, so from
// agent's log_channel_state_change so doc/troubleshooting.md can be shared.
func (l *channelStateLog) logTransition(which string, from, to connectivity.State) {
	l.mu.Lock()
	defer l.mu.Unlock()
	recovery := to == connectivity.Ready && l.lostReady
	firstReady := to == connectivity.Ready && !l.everReady
	if to == connectivity.Ready {
		l.everReady = true
	}
	if held, ok := l.transitions.acquire(); ok {
		// This line opens a throttle window; a recovery it reports counts as
		// that window's, and the next one starts clean.
		l.recoveryLogged = recovery
		if held > 1 {
			Log("grpc").Infof("%s connection state %s -> %s (%d transitions since the last state line)",
				which, from.String(), to.String(), held)
		} else {
			Log("grpc").Infof("%s connection state %s -> %s", which, from.String(), to.String())
		}
		return
	}
	if firstReady || (recovery && !l.recoveryLogged) {
		l.recoveryLogged = l.recoveryLogged || recovery
		Log("grpc").Infof("%s connection state %s -> %s (other transitions are folded into the next state line)",
			which, from.String(), to.String())
	}
}

// logReady closes the outage, if there was one, and logs its summary with
// the lifetime counters: the Channelz stand-in. The recovery instant itself
// is the "-> READY" state line; this one is throttled like the state lines,
// so a flapping channel gets one summary per interval carrying the totals.
func (l *channelStateLog) logReady(which string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.everReady = true
	if l.unreadySince.IsZero() {
		return
	}
	outage := time.Since(l.unreadySince)
	l.unreadySince = time.Time{}
	if !l.lostReady {
		return // the first connect, not a recovery
	}
	l.lostReady = false
	l.unreadyTotal += outage
	if _, ok := l.outages.acquire(); ok {
		Log("grpc").Infof("%s connection ready again after %s; lifetime: not ready %d times, %s waiting for READY in total, %d rotations",
			which, outage.Round(time.Millisecond).String(), l.lostCount,
			l.unreadyTotal.Round(time.Millisecond).String(), channelRotations.Load())
	}
}

// logNotReady is the WARN for a wait that ran out without READY. WARN rather
// than the INFO of the entry line because by now data is not being sent; one
// line per dropReportInterval per channel, since backOffUntilReady times out
// once per attempt for as long as the collector is down.
func (l *channelStateLog) logNotReady(which string, state connectivity.State) {
	l.mu.Lock()
	waited := time.Since(l.unreadySince)
	l.mu.Unlock()
	l.notReady.warnf("%s connection not ready (state %s): waited %s so far",
		which, state.String(), waited.Round(time.Millisecond).String())
}

// waitUntilReady waits up to timeout for the connection to become ready. The
// wait is bound to ctx as well, so cancelling ctx aborts it immediately.
//
// Every observed state change is logged through channelStateLog, so the
// moment of recovery (-> READY) is on record; see logTransition for what
// "observed" means here.
func waitUntilReady(ctx context.Context, grpcConn *grpc.ClientConn, timeout time.Duration, which string) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	state := grpcConn.GetState()
	Log("grpc").Infof("wait %s connection ready - state: %s, timeout: %s", which, state.String(), timeout.String())

	stateLog := channelStateLogFor(which)
	if state != connectivity.Ready {
		stateLog.observeNotReady()
	}

	for state != connectivity.Ready {
		// An IDLE channel never leaves that state on its own, so waiting on it
		// would burn the whole interval; ask it to connect instead.
		if state == connectivity.Idle {
			grpcConn.Connect()
		}
		if !grpcConn.WaitForStateChange(ctx, state) {
			if ctx.Err() == context.DeadlineExceeded {
				stateLog.logNotReady(which, grpcConn.GetState())
			}
			return false
		}
		if next := grpcConn.GetState(); next != state {
			stateLog.logTransition(which, state, next)
			state = next
		}
	}

	stateLog.logReady(which)
	return true
}

// backOffUntilReady waits for the connection to become ready, backing off
// between attempts. It returns as soon as shutdown begins, so a pending
// back-off interval does not delay it.
func backOffUntilReady(agent *agent, grpcConn *grpc.ClientConn, which string) {
	for attempt := 0; !agent.stopping(); attempt++ {
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
	readyFailures := 0
	// workerContinues, not Enable: a stream is (re)opened by a worker, and
	// the stat worker still sends the final url stat tick while stopping.
	for agent.workerContinues() {
		if newStreamFunc() {
			Log("grpc").Infof("success to make %s stream", which)
			return true
		}
		if agent.config.offGrpc {
			continue
		}
		// backOffUntilReady returns at once on a Ready connection, so a stream
		// that keeps failing to open while the transport is up (a GOAWAY the
		// state has not caught up with, a collector refusing the RPC) retried
		// flat out, logging one error per turn. Pace those attempts with the
		// reconnect back-off instead; a connection that is not Ready still
		// waits for readiness as before, and resets the pacing.
		if grpcConn.GetState() == connectivity.Ready {
			if !sleepUnlessStopped(agent, backOffSleep(readyFailures)) {
				return false
			}
			readyFailures++
			continue
		}
		readyFailures = 0
		backOffUntilReady(agent, grpcConn, which)
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

	spanGrpc.inFlight.Add(1)
	go func() {
		defer spanGrpc.inFlight.Done()
		defer spanGrpc.releaseSpanBatchPermit()

		// The message is built here rather than on the worker so that the
		// defers above cover the build too. A panic in makePSpanMessageBatch
		// on the worker was recovered by superviseWorker, which restarted the
		// worker but could not return this permit: maxConcurrentRequests such
		// panics left every later batch waiting out the flush timeout and
		// being dropped for the life of the agent.
		builder := acquireSpanMessageBuilder()
		defer releaseSpanMessageBuilder(builder)

		// Recovered like every other agent goroutine: a panic here must not
		// take the host process down, and the defers above still release the
		// permit and the builder.
		recoverPanic("span batch send", func() {
			spanMessageBatch := builder.makePSpanMessageBatch(chunks)
			if len(spanMessageBatch.GetSpan()) == 0 {
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
	// empty string leaves the web UI with a blank inbound node instead of one
	// labelled unknown.
	acceptEvent.EndPoint = validUTF8(cmp.Or(span.endPoint, unknownAddress))
	acceptEvent.RemoteAddr = validUTF8(cmp.Or(span.remoteAddr, unknownAddress))
	// A root span has no parent to describe, so it carries no PParentInfo
	// naming an empty parent (ServerRequestRecorder records parent info only
	// when Pinpoint-pAppName is present). ParentApplicationType is -1
	// (UNDEFINED) when the name came without a parseable type, as in Java.
	if span.parentAppName != "" {
		parentInfo := b.parentInfos.get()
		parentInfo.ParentApplicationName = validUTF8(span.parentAppName)
		parentInfo.ParentApplicationType = int32(span.parentAppType)
		parentInfo.AcceptorHost = validUTF8(span.acceptorHost)
		parentInfo.ParentServiceName = validUTF8(span.parentServiceName)
		acceptEvent.ParentInfo = parentInfo
	}
	pspan.AcceptEvent = acceptEvent

	pspan.Annotation = b.annotationsWithApi(&span.annotations, span.apiId, span.operationName)
	pspan.ApiId = span.apiId
	pspan.Flag = int32(span.flags)
	pspan.SpanEvent = b.makePSpanEventList(chunk)
	pspan.Err = span.err.Load()
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

// annotationsWithApi materializes a's list, appending an AnnotationApi
// fallback when no api id was cached. The fallback lives only in the builder
// output: mutating the span's own annotation here would race with the
// application goroutine and duplicate the entry on a re-serialization.
func (b *spanMessageBuilder) annotationsWithApi(a *annotation, apiId int32, operationName string) []*pb.PAnnotation {
	if apiId != 0 || operationName == "" {
		return a.getListInto(b)
	}
	a.annotationLock.Lock()
	defer a.annotationLock.Unlock()

	list := b.annotationLists.take(len(a.values) + 1)
	for i := range a.values {
		list[i] = a.values[i].toProtoInto(b)
	}
	api := annotationValue{key: AnnotationApi, typ: annotationTypeString, s1: operationName}
	list[len(a.values)] = api.toProtoInto(b)
	return list
}

func (b *spanMessageBuilder) makePSpanEvent(event *spanEvent) *pb.PSpanEvent {
	aSpanEvent := b.events.get()
	aSpanEvent.Sequence = event.sequence
	aSpanEvent.Depth = event.depth
	aSpanEvent.StartElapsed = int32(event.startElapsed)
	aSpanEvent.EndElapsed = int32(event.endElapsed)
	aSpanEvent.ServiceType = event.serviceType
	aSpanEvent.Annotation = b.annotationsWithApi(&event.annotations, event.apiId, event.operationName)
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
		// Only an event that actually injected a trace context has a next span
		// call to a span id no node will ever report.
		if event.nextSpanId != noneSpanId {
			messageEvent.NextSpanId = event.nextSpanId
		}
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
			// agent sends for the same reason. The counts below are Go's own:
			// NumGC counts whole cycles (Go has no generations) and
			// PauseTotalNs sums stop-the-world time only, so both read lower
			// doc/java_parity.md.
			Type:                 pb.PJvmGcType_JVM_GC_TYPE_UNKNOWN,
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
	stream pb.ProfilerCommandService_HandleCommandV2Client
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
		ctx, cancel = context.WithCancel(commandMetadataContext(cmdGrpc.agent))
	} else {
		ctx, cancel = context.WithDeadline(commandMetadataContext(cmdGrpc.agent), age.expiresAt)
	}
	// in the IDL and the collector's V1 handler drops a stream that carries the
	// supportcommandcode header, so the RPC and the header go together.
	stream, err := cmdGrpc.cmdClient.HandleCommandV2(ctx)
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

// sendFailMessage rejects a command on the command stream itself, which is the
// only channel the protocol offers for a request the agent will not serve:
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
