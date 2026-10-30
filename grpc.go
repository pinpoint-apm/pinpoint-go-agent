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
	"sync/atomic"
	"time"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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
	// name is spelled that way here.
	headerSupportCommandCode = "supportcommandcode"
)

// internalContextKey marks the contexts of the agent's own RPCs to the
// collector. Instrumentation of google.golang.org/grpc that is applied to
// every client connection in the process - the compile-time instrumentation
// tool - reads it through IsInternalContext to leave those RPCs alone.
type internalContextKey struct{}

func internalContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalContextKey{}, true)
}

// IsInternalContext reports whether ctx belongs to one of the agent's own
// RPCs to the collector (agent registration and ping, span, stat and command
// streams). gRPC client instrumentation applied process-wide skips such a
// call: it carries no tracer anyway, and tracing the agent's transport would
// be self-instrumentation.
func IsInternalContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(internalContextKey{}).(bool)
	return ok && v
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
	return metadata.NewOutgoingContext(internalContext(context.Background()), metadata.New(m))
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
		// v4: agentName is always present; servicename is sent, and apikey
		// only when one is configured.
		m[headerAgentName] = agent.agentName
		m[headerServiceName] = agent.serviceName
		if agent.objName.apiKey != "" {
			m[headerApiKey] = agent.objName.apiKey
		}
	} else if agent.agentName != "" {
		// v1/v3: agentName is optional on the wire.
		m[headerAgentName] = agent.agentName
	}
	return m
}

func (agent *agent) baseOutgoingContext() context.Context {
	agent.grpcMetaOnce.Do(func() {
		md := metadata.New(agentHeaderMap(agent))
		agent.grpcMetaCtx = metadata.NewOutgoingContext(internalContext(context.Background()), md)
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

// randomize returns d scaled by a uniform factor in [1-jitter, 1+jitter].
func randomize(d time.Duration, jitter float64) time.Duration {
	return time.Duration(float64(d) * (1 - jitter + rand.Float64()*2*jitter))
}

// streamAgeJitter randomizes every connection and stream max age by +/-10%, so
// agents deployed together do not renew in lockstep.
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

// renewIfExpired closes a stream past its max age and opens its replacement.
// Renewal is the normal path, logged at info and kept apart from the error path
// the workers take when a send fails.
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
	// refresh). Registration retries with backOffUntilReady until it succeeds,
	// so a hung collector costs a short wait and a retry rather than a minute
	// of boot latency.
	agentGrpcTimeOut = 5 * time.Second

	// metaGrpcTimeOut bounds each metadata RPC (api/string/sql/sqlUid/
	// exception). These run under sendMetaWorker's permits (agent.
	// metaMaxConcurrentRequests), and a failed send evicts the item's cache entry so it is
	// re-registered on next use. Kept short so a hung collector cannot pin every
	// permit until metaChan overflows and each head-drop re-queues the same
	// metadata: ample for a healthy collector, short enough that permits recycle
	// before the queue fills.
	metaGrpcTimeOut = 5 * time.Second

	sendStreamTimeOut    = 5 * time.Second
	closeStreamTimeOut   = 1 * time.Second
	commandStreamTimeOut = 1 * time.Second

	// Defaults for the Collector.Grpc.* config keys.
	grpcKeepAliveTime               = 30000 // ms
	grpcKeepAliveTimeout            = 60000 // ms
	grpcKeepAlivePermitWithoutCalls = false
	grpcFlowControlWindow           = 1 * 1024 * 1024
	// grpc-go shares write buffers through a sync.Pool that GC empties, so
	// each connection allocates a fresh buffer of this size after most GC
	// cycles; at 1MB that was ~40% of the agent process's allocated bytes.
	grpcWriteBufferSize   = 64 * 1024
	grpcMaxMessageSize    = 4 * 1024 * 1024
	grpcMaxHeaderListSize = 8 * 1024
	// grpc-go v1.82.1 puts a channel into IDLE after 30 minutes without an RPC
	// (dialoptions.go defaultDialOptions: idleTimeout 30 * time.Minute) and
	// documents WithIdleTimeout(0) as the way to disable idling. 0 here is
	// that disable value, and it is the default: see dialOptions.
	grpcIdleTimeout = 0 // ms

	// 0 disables the periodic renewal of connections and streams, which is the
	// default: a connection is replaced only when it breaks.
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
		// channel sends unary SendSpanBatch RPCs and goes quiet whenever the
		// application has no traffic, which is exactly where the 30-minute
		// default would bite.
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
		// The channel takes its service config from this process alone. Without
		// this the dns resolver queries a _grpc_config.<collector host> TXT
		// record on every resolution - a wasted lookup, every 30 seconds while
		// the collector is down - and a record it finds there outranks the
		// default service config below, so a DNS zone the agent does not own
		// could swap the balancer the agent selected, or hand it a config that
		// does not parse. The agent never wants a service config from DNS.
		grpc.WithDisableServiceConfig(),
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
// list and keeps re-resolving, which the expiringPickFirst balancer
// (grpc_balancer.go) relies on: with several A records the picked SubConn holds
// them all, so a rotation or a failure moves to another collector instance, and
// its ResolveNow on failure and its address-change readdressing both act on a
// resolver that can answer.
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

	// Two-value form: LocalAddr is nil when getsockname failed, and a panic
	// here would disable the agent at registration.
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && !addr.IP.IsLoopback() {
		return addr.IP.String()
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
		ServiceName: "Go (" + runtime.GOOS + ", " + runtime.GOARCH + ")",
		ServiceLib:  libs,
	}
}

// makeServerMetaData builds PServerMetaData: the configured ServerInfo (or the
// default), argv, and the agent's own Go build entry followed by the host's
// WithServiceInfo entries. Host strings are sanitized like argv: the collector
// rejects a PAgentInfo with invalid UTF-8 and the same bytes would be re-sent
// on every retry.
func makeServerMetaData(config *Config) *pb.PServerMetaData {
	// os.Args is empty for a process exec'd with no argv at all; [1:] on it
	// panicked the registration goroutine, and with it the host.
	var vmArgs []string
	if config.Bool(CfgCollectorAgentInfoSendArgs) && len(os.Args) > 1 {
		vmArgs = make([]string, 0, len(os.Args)-1)
		for _, arg := range os.Args[1:] {
			vmArgs = append(vmArgs, validUTF8(arg))
		}
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
			VmVersion: fmt.Sprintf("%s(%d)", runtime.Version(), goIdOffset),
			// Same reason as PJvmGc.type in makePAgentStat: Go's runtime is
			// none of the VM types the field enumerates.
			GcType: pb.PJvmGcType_JVM_GC_TYPE_UNKNOWN,
		},
		Container: agentGrpc.agent.config.Bool(CfgIsContainerEnv),
	}

	if IsDebugLogLevelEnabled() {
		Log("grpc").Debugf("agent info: %s", agentInfo.String())
	}

	ctx := metadata.NewOutgoingContext(internalContext(agentGrpc.agent.stopSignal()), metadata.New(agentHeaderMap(agentGrpc.agent)))
	return ctx, agentInfo
}

func (agentGrpc *agentGrpc) sendAgentInfo(ctx context.Context, agentInfo *pb.PAgentInfo) (*pb.PResult, error) {
	ctx, cancel := context.WithTimeout(ctx, agentGrpcTimeOut)
	defer cancel()

	result, err := agentGrpc.agentClient.RequestAgentInfo(ctx, agentInfo)
	if err != nil {
		// Throttled: registration retries at Collector.AgentInfo.SendRetryInterval
		// for as long as the collector is down, and the "still waiting" line
		// of registerAgentWithRetry already says tracing is off.
		agentInfoSendLog.errorf("send agent info - %v", err)
	}

	return result, err
}

var (
	agentInfoSendLog       = logThrottle{src: "grpc"}
	agentRegisterRejectLog = logThrottle{src: "agent"}
)

// registrationWaitLogInterval paces the line that says why tracing is off while
// registration is still in progress. A variable so tests can shorten it.
var registrationWaitLogInterval = 30 * time.Second

// registerRetryInterval is the pause registerAgentWithRetry takes between boot
// registration attempts, randomized +/-30% so agents restarted together do not
// retry in lockstep. It does not escalate: a rejecting collector is polled at
// the interval the operator configured, and the connection readiness wait that
// follows each attempt is what backs off during an outage.
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
	// consequence, so they read as a plain connectivity problem. The periodic
	// line below says it outright, so an operator looking at a silent agent can
	// tell this wait apart from one nobody instrumented.
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

		// Rebuilt per attempt: an outage outlives the values in here. The IP is
		// the usual one - a NIC still coming up at boot leaves it empty - but
		// the hostname and the server metadata can move too, and whatever this
		// loop captured first is what the collector would carry until the next
		// refresh cycle. Nothing here does I/O beyond a local route lookup and
		// an interface scan, and attempts are at least backOffInitialInterval
		// apart, so the repeat is free.
		ctx, agentInfo := agentGrpc.makeAgentInfo()

		res, err := agentGrpc.sendAgentInfo(ctx, agentInfo)
		if err == nil {
			if res.Success {
				Log("agent").Infof("agent registered")
				return true
			}
			// A rejection is retried rather than fatal: the collector answers
			// this way while initializing or briefly refusing, and giving up
			// would leave this process untraced until it restarts. Throttled
			// like the send failure: a collector that keeps rejecting is asked
			// again every SendRetryInterval, and the "still waiting" line
			// above already names the rejection.
			agentRegisterRejectLog.warnf("register agent - %s, retrying", res.Message)
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
		if !backOffUntilReady(agentGrpc.agent, agentGrpc.agentConn, "agent") {
			return false
		}
	}
	return false
}

// refreshAgentInfo re-sends AgentInfo once, trying up to maxTry sends spaced
// retryInterval apart. Unlike boot-time registration it never loops forever: a
// failed refresh is left for the next refresh cycle.
func (agentGrpc *agentGrpc) refreshAgentInfo(maxTry int, retryInterval time.Duration) bool {
	for try := 0; try < maxTry && !agentGrpc.agent.stopping(); try++ {
		// Rebuilt per attempt, as registerAgentWithRetry does: attempts are
		// retryInterval apart, so the host name, IP or server metadata can move
		// between them, and this refresh is what corrects the collector's copy.
		// Cheap to repeat - a local route lookup and an interface scan - and
		// maxTry bounds how often. The context is rebuilt with it: it carries
		// the agent headers over the agent-lifetime stop signal and no deadline
		// of its own, so per-attempt and per-call are the same context here.
		ctx, agentInfo := agentGrpc.makeAgentInfo()

		if res, err := agentGrpc.sendAgentInfo(ctx, agentInfo); err == nil && res.Success {
			Log("agent").Infof("agent info refreshed")
			return true
		}
		if try+1 < maxTry && !sleepUnlessStopped(agentGrpc.agent, retryInterval) {
			return false
		}
	}

	Log("agent").Warnf("failed to refresh agent info")
	return false
}

func isRetryableError(e error) bool {
	// Retry network errors only.
	code := status.Code(e)
	return code == codes.Unavailable || code == codes.DeadlineExceeded
}

// metaRetryMaxAttempts is the send budget of one metadata item. Once it is
// spent the item's cache entry is released so its next use registers it again;
// without a bound, an item facing a dead collector would circulate through the
// retry schedule forever.
const metaRetryMaxAttempts = 3

// metaRetryDelay is the pause between two sends of one metadata item. It is
// also how long a rejected item's cache entry stays in place before it is
// released (see metaVerdictOf). A collector that is up but refusing
// (Unavailable) answers at once, so without a pause every attempt in the
// budget would fire back to back against an already overloaded collector.
const metaRetryDelay = time.Second

// metaGiveUpParks is how many retry delays a given-up item's cache entry is
// held before its release while give-ups follow one another with no delivery
// between them, unless the collector delivers another item first. The first
// give-up since a delivery is released at once: its next use re-registers it
// under a fresh id, which is all a failure the collector is already over
// needs. Released at once throughout an outage, though, each hot API, SQL or
// error name minted a new id and cycled through its attempt budget every
// couple of seconds - an RPC and an error line each time - for as long as the
// collector stayed down; held, it cycles once per metaGiveUpParks delays. A
// delivery means the collector is back, so the entry is released at its next
// delay and the re-registration is not held up once the outage is over. The
// schedule keeps a single delay (see metaRetryQueue), so a long wait is
// served as repeated parks of one delay each.
const metaGiveUpParks = 30

// metaRetryQueueSize bounds the retry schedule. It is budgeted separately from
// metaChan on purpose: while a collector outage lasts every failed send comes
// back as a retry, and a shared budget would let those retries fill the queue
// and starve new metadata. New metadata dropped on overflow releases its cache
// entry, which makes the next span register the same item again - a
// drop-feeds-inflow loop that runs until the collector recovers. Two bounds keep
// the retries and the new items out of each other's way.
const metaRetryQueueSize = 1000

// The per-send log sites of the metadata and span batch paths are throttled:
// while the collector is down every send fails, and a line per failed send -
// several per hot metadata key a second, one per batch - buried the host's own
// log. What was lost is still counted (dropReporter), and each line says how
// many it held back. The span drop lines are WARN like the drop totals of
// reportSpanDrops: they carry the cause the totals lack, and the
// troubleshooting guide sends an operator looking for them.
var (
	metaSendLog          = logThrottle{src: "grpc"}
	metaSkipLog          = logThrottle{src: "grpc"}
	spanBatchSkipLog     = logThrottle{src: "grpc"}
	spanBatchFailLog     = logThrottle{src: "grpc"}
	spanBatchPartialLog  = logThrottle{src: "grpc"}
	spanBatchOversizeLog = logThrottle{src: "grpc"}
)

// The stream open sites are throttled for the same reason: newStreamWithRetry
// retries at the reconnect back-off for as long as the collector is down, and
// the connection-not-ready WARN already says data is not being sent meanwhile.
// One per stream so the ping stream's line cannot hide the stat stream's. The
// active thread count stream is opened once per command, so its site is not.
var (
	pingStreamOpenLog = logThrottle{src: "grpc"}
	statStreamOpenLog = logThrottle{src: "grpc"}
	cmdStreamOpenLog  = logThrottle{src: "grpc"}
)

// metaVerdict is what becomes of a metadata item after one send attempt.
type metaVerdict int

const (
	// metaDelivered: the collector accepted the item.
	metaDelivered metaVerdict = iota
	// metaRetryLater: a transport failure with attempt budget left. The item
	// goes to the retry schedule; the cache entry stays.
	metaRetryLater
	// metaGiveUp: the attempt budget is spent. The cache entry is released so
	// the next use registers the item again: at once, or after up to
	// metaGiveUpParks retry delays while the give-ups keep coming.
	metaGiveUp
	// metaRejected: a failure a retry cannot change. The cache entry is released
	// after one metaRetryDelay rather than at once: the release is what makes
	// the next span miss the cache and re-send, so releasing immediately would
	// turn a rejecting collector into a re-send per span, bounded only by the
	// in-flight permits. Parking it makes the recovery probe periodic.
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
// like), so re-sending the same bytes twice more is pure load on the collector.
// metaVerdictOf therefore never retries it, and sendMetaWorker releases the
// cache entry after one delay so the next use registers a fresh id instead of
// every later span referencing one the collector never stored.
func metaResult(res *pb.PResult, err error) error {
	if err != nil {
		return err
	}
	if !res.GetSuccess() {
		return status.Errorf(codes.FailedPrecondition, "collector rejected metadata: %s", res.GetMessage())
	}
	return nil
}

// sendMeta makes one metadata RPC: kind names it in the log lines, in is the
// message and call is the MetadataClient method that carries it.
func sendMeta[M proto.Message](agentGrpc *agentGrpc, kind string, in M, call func(context.Context, M, ...grpc.CallOption) (*pb.PResult, error)) error {
	if IsDebugLogLevelEnabled() {
		Log("grpc").Debugf("%s metadata: %v", kind, in)
	}

	ctx, cancel := context.WithTimeout(grpcMetadataContext(agentGrpc.agent, -1), metaGrpcTimeOut)
	defer cancel()

	err := metaResult(call(ctx, in))
	if err != nil {
		metaSendLog.errorf("send %s metadata - %v", kind, err)
	}
	return err
}

// sendMetadata makes one send of md, built into the message of its kind.
func (agentGrpc *agentGrpc) sendMetadata(md any) error {
	client := agentGrpc.metaClient
	switch md := md.(type) {
	case apiMeta:
		return sendMeta(agentGrpc, "api", &pb.PApiMetaData{
			ApiId: md.id, ApiInfo: validUTF8(md.descriptor), Line: -1, Type: int32(md.apiType),
		}, client.RequestApiMetaData)
	case stringMeta:
		return sendMeta(agentGrpc, "string", &pb.PStringMetaData{StringId: md.id, StringValue: validUTF8(md.funcName)},
			client.RequestStringMetaData)
	case sqlMeta:
		return sendMeta(agentGrpc, "sql", &pb.PSqlMetaData{SqlId: md.id, Sql: validUTF8(md.sql)}, client.RequestSqlMetaData)
	case sqlUidMeta:
		return sendMeta(agentGrpc, "sql uid", &pb.PSqlUidMetaData{SqlUid: md.uid, Sql: validUTF8(md.sql)},
			client.RequestSqlUidMetaData)
	case exceptionMeta:
		return agentGrpc.sendExceptionMetadata(makePExceptionMetaData(&md))
	}
	return fmt.Errorf("unknown metadata type %T", md)
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
		metaSkipLog.warnf("skip exception metadata - %v", err)
		return err
	}
	return sendMeta(agentGrpc, "exception", in, agentGrpc.metaClient.RequestExceptionMetaData)
}

func makePExceptionMetaData(e *exceptionMeta) *pb.PExceptionMetaData {
	return &pb.PExceptionMetaData{
		TransactionId: &pb.PTransactionId{
			AgentId:        e.txId.AgentId,
			AgentStartTime: e.txId.StartTime,
			Sequence:       e.txId.Sequence,
		},
		SpanId:      e.spanId,
		UriTemplate: validUTF8(e.uriTemplate),
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
		ExceptionClassName: validUTF8(e.className),
		ExceptionMessage:   validUTF8(abbreviateString(e.callstack.err.Error(), maxExceptionMessageSize)),
		StartTime:          e.callstack.errorTime.UnixNano() / int64(time.Millisecond),
		ExceptionId:        e.exceptionId,
		ExceptionDepth:     e.depth,
		StackTraceElement:  makePStackTraceElementList(frames),
	}
}

func makePStackTraceElementList(frames []frame) []*pb.PStackTraceElement {
	list := make([]*pb.PStackTraceElement, 0, len(frames))
	for _, f := range frames {
		// Only the file needs it: a source path from the build machine can
		// hold invalid UTF-8 (a Latin-1 directory name), while the compiler
		// rejects such bytes in identifiers and import paths.
		list = append(list, &pb.PStackTraceElement{
			ClassName:  f.moduleName,
			FileName:   validUTF8(f.file),
			LineNumber: f.line,
			MethodName: f.funcName,
		})
	}
	return list
}

// sendStreamWithTimeout runs op on the calling goroutine and cancels the stream
// if op blocks past timeout. grpc-go unblocks a flow-control-blocked
// Send/Recv/CloseSend once the stream context is cancelled, so cancelling is
// what ends the wait. Killing the stream on timeout matches the callers: they
// already close and re-create the stream on any send error.
//
// A timer, a closure and a channel per call: ping, stat and command send
// seconds apart, so pooling these would buy nothing.
func sendStreamWithTimeout(op func() error, cancelStream context.CancelFunc, timeout time.Duration, which string) error {
	cancelled := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		cancelStream()
		close(cancelled)
	})

	err := op()

	if timer.Stop() {
		return err
	}
	// The timer fired: join the cancel so it cannot escape into the stream the
	// caller opens next, and report the timeout rather than op's own error.
	<-cancelled
	return status.Errorf(codes.DeadlineExceeded, "%s - too slow or blocked", which)
}

// channelStateLog is the diagnostic record of one collector channel's
// readiness, keyed by the `which` label the wait loops pass: the throttles
// for its log lines and the lifetime counters the recovery summary prints.
// Package level, like the other logThrottle sites, because backOffUntilReady
// calls waitUntilReady once per attempt and the throttle window must span
// those calls or a dead collector logs a state line per attempt. Everything it
// records goes to the log; nothing is added to PAgentStat.
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
// that happen between two WaitForStateChange returns are never seen, so the log
// shows the states the waits happened to sample, not every state the channel
// passed through.
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
	// One line per attempt, deliberately unthrottled: with the state lines and
	// the not-ready WARN held back across attempts, this is what shows the
	// back-off loop is still trying.
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
// between attempts, and reports whether it did. It returns false as soon as
// shutdown begins, so a pending back-off interval does not delay it.
func backOffUntilReady(agent *agent, grpcConn *grpc.ClientConn, which string) bool {
	for attempt := 0; !agent.stopping(); attempt++ {
		if waitUntilReady(agent.stopSignal(), grpcConn, backOffSleep(attempt), which) {
			return true
		}
	}
	return false
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

// newStreamWithRetry opens a stream with open, which returns nil on a failure,
// until one opens; a wait cut short by shutdown ends the retries with a
// stream that holds none.
func newStreamWithRetry[S any](agent *agent, grpcConn *grpc.ClientConn, open func() *S, which string) *S {
	readyFailures := 0
	// workerContinues, not Enable: a stream is (re)opened by a worker, and
	// the stat worker still sends the final url stat tick while stopping.
	for agent.workerContinues() {
		if s := open(); s != nil {
			Log("grpc").Infof("%s stream opened", which)
			return s
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
				return new(S)
			}
			readyFailures++
			continue
		}
		readyFailures = 0
		// A wait cut short by shutdown ends the retries. The loop condition
		// alone did not: workerContinues holds through the stopping phase,
		// while the wait returns at once from it, so a connection that was
		// not READY at shutdown spun this loop flat out - a failed open and
		// an error line per turn - until the phase reached stopped.
		if !backOffUntilReady(agent, grpcConn, which) {
			return new(S)
		}
	}
	return new(S)
}

// gzipCallOptions compresses a call: SendSpanBatch requests and the stat
// stream. Importing encoding/gzip registers the compressor process-wide, which
// also lets the client read a gzip response.
var gzipCallOptions = []grpc.CallOption{grpc.UseCompressor(gzip.Name)}

// disableGzipIfRejected turns compress off, for the life of the sender as in
// the Java agent, when err is a collector that cannot decompress gzip:
// grpc-java answers UNIMPLEMENTED "Can't find decompressor for gzip", grpc-go
// "grpc: Decompressor is not installed for grpc-encoding".
func disableGzipIfRejected(compress *atomic.Bool, err error, which string) {
	s := status.Convert(err)
	if s.Code() != codes.Unimplemented || !strings.Contains(strings.ToLower(s.Message()), "decompressor") {
		return
	}
	if compress.CompareAndSwap(true, false) {
		Log("grpc").Warnf("collector does not accept gzip %s (%v); sending them uncompressed from now on", which, err)
	}
}

// streamHandle is what the long-lived client streams share: the generated
// stream S, the cancel of its context and its max age. The zero value stands
// for no stream, what the openers hand back once shutdown began during their
// wait; every S is an interface type, so its zero value is nil.
type streamHandle[S comparable] struct {
	stream S
	cancel context.CancelFunc
	streamAge
}

func newStreamHandle[S comparable](stream S, cancel context.CancelFunc, age streamAge) streamHandle[S] {
	return streamHandle[S]{stream: stream, cancel: cancel, streamAge: age}
}

func (h *streamHandle[S]) open() bool {
	var none S
	return h.stream != none
}

// send runs op, one call on the stream, under sendStreamTimeOut. A failed
// send cancels the stream: the worker closes and reopens it next.
func (h *streamHandle[S]) send(op func(S) error, which string) error {
	if !h.open() {
		return status.Errorf(codes.Unavailable, "%s: stream is nil", which)
	}
	err := sendStreamWithTimeout(func() error { return op(h.stream) }, h.cancel, sendStreamTimeOut, which)
	if err != nil {
		h.cancel()
	}
	return err
}

// close runs op, the stream's own close, under closeStreamTimeOut, cancels
// the stream's context and forgets the stream; a handle holding none does
// nothing.
func (h *streamHandle[S]) close(op func(S) error, which string) {
	if !h.open() {
		return
	}
	sendStreamWithTimeout(func() error { return op(h.stream) }, h.cancel, closeStreamTimeOut, which+" stream close")
	h.cancel()
	var none S
	h.stream = none
	Log("grpc").Infof("close %s stream", which)
}

type pingStream struct {
	streamHandle[pb.Agent_PingSessionClient]
}

func (agentGrpc *agentGrpc) newPingStream() *pingStream {
	agentGrpc.pingSocketId++
	ctx, cancel := context.WithCancel(grpcMetadataContext(agentGrpc.agent, agentGrpc.pingSocketId))
	stream, err := agentGrpc.agentClient.PingSession(ctx)
	if err != nil {
		cancel()
		pingStreamOpenLog.errorf("make ping stream - %v", err)
		return nil
	}
	return &pingStream{newStreamHandle(stream, cancel, newStreamAge(agentGrpc.agent))}
}

func (agentGrpc *agentGrpc) newPingStreamWithRetry() *pingStream {
	return newStreamWithRetry(agentGrpc.agent, agentGrpc.agentConn, agentGrpc.newPingStream, "ping")
}

var ping = pb.PPing{}

func (s *pingStream) sendPing() error {
	if err := s.send(func(c pb.Agent_PingSessionClient) error { return c.Send(&ping) }, "ping stream.Send()"); err != nil {
		return err
	}
	return s.send(func(c pb.Agent_PingSessionClient) error { _, err := c.Recv(); return err }, "ping stream.Recv()")
}

func (s *pingStream) close() {
	s.streamHandle.close(func(c pb.Agent_PingSessionClient) error { return c.CloseSend() }, "ping")
}

func (agentGrpc *agentGrpc) close() {
	if agentGrpc.agentConn != nil {
		agentGrpc.agentConn.Close()
	}
}
