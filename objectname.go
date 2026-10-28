package pinpoint

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// nameVersion selects the agent self-identification (ObjectName) scheme, which
// decides the id length limits and the protocol version header.
type nameVersion int

const (
	nameV1 nameVersion = iota
	nameV3
	nameV4
)

const (
	agentIDMaxLen     = 24  // AGENT_ID_MAX_LEN
	agentNameMaxLen   = 255 // AGENT_NAME_MAX_LEN
	serviceNameMinLen = 3   // SERVICE_NAME_MIN_LEN
	serviceNameMaxLen = 254 // SERVICE_NAME_MAX_LEN
	appNameMaxLenV1   = 24  // v1: applicationName uses the agentId limit
	appNameMaxLenV3   = 254 // APPLICATION_NAME_MAX_LEN_V3 (= SERVICE_NAME_MAX_LEN)
	agentNameMaxLenV4 = 254 // AGENT_NAME_MAX_LEN_V4
)

// ProtocolVersion (V1 = 1_00, V4 = 4_00).
const (
	protocolVersionV1 = 100
	protocolVersionV4 = 400
)

// isIDChars reports whether every byte of s is an allowed id character:
// IdValidateUtils.ID_PATTERN_VALUE. A byte loop rather than a regexp because
// splitTransactionId validates the agent id of every inbound trace id header:
// the equivalent `^[a-zA-Z0-9._\-]+$` match costs ~316ns against ~18ns here
// (see BenchmarkValidateID). Every allowed character is single-byte ASCII, so
// any byte of a multi-byte rune fails, as it did under the regexp.
func isIDChars(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// isPrintableASCII reports whether every byte of s is in [0x20, 0x7E], the
// characters grpc-go allows in a non-binary metadata value: it refuses every
// RPC that carries any other byte.
func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

// IsValidId reports whether value is a non-empty id of at most maxLen UTF-8
// bytes made only of allowed id characters (IdValidateUtils.validateId).
// Plugins use it for ids that arrive on the wire, such as the app= token of
// the Pinpoint-ProxyApp header.
func IsValidId(value string, maxLen int) bool {
	if len(value) == 0 || len(value) > maxLen {
		return false
	}
	return isIDChars(value)
}

// parseNameVersion parses the configured version string (case-insensitive).
// An empty or unknown value is v4, as NameVersion.getVersion of the Java agent
// resolves it.
func parseNameVersion(version string) nameVersion {
	switch strings.ToLower(strings.TrimSpace(version)) {
	case "v1":
		return nameV1
	case "v3":
		return nameV3
	default:
		return nameV4
	}
}

// objectName holds the resolved agent self-identification.
type objectName struct {
	version         nameVersion
	agentID         string    // always auto-generated base64(UUIDv7)
	agentName       string    // always non-empty after resolution: configured or "host-pid"
	applicationName string    // required
	serviceName     string    // v4 only; "" for v1/v3
	apiKey          string    // v4 only, optional; "" for v1/v3 or when unset (never logged)
	agentUID        uuid.UUID // v4 only; zero value otherwise
}

func (o *objectName) isV4() bool { return o.version == nameV4 }

// protocolVersion returns the gRPC protocol.version header value for this scheme.
func (o *objectName) protocolVersion() int {
	if o.isV4() {
		return protocolVersionV4
	}
	return protocolVersionV1
}

// String renders the object name with the apiKey masked, so it is safe to log.
func (o *objectName) String() string {
	apiKey := ""
	if o.apiKey != "" {
		apiKey = "****"
	}
	return "objectName{version=" + o.versionString() +
		", agentId=" + o.agentID +
		", agentName=" + o.agentName +
		", applicationName=" + o.applicationName +
		", serviceName=" + o.serviceName +
		", apiKey=" + apiKey + "}"
}

func (o *objectName) versionString() string {
	switch o.version {
	case nameV1:
		return "v1"
	case nameV4:
		return "v4"
	default:
		return "v3"
	}
}

// resolveObjectName builds the agent ObjectName from config according to the
// configured version (Uid.Version). agentId is always a freshly generated
// base64(UUIDv7), never user-configurable; applicationName is required for
// every version, serviceName for v4 as well, and apiKey is optional. A missing
// required field aborts agent startup with an error. v1 and v3 differ only in the
// applicationName length limit; v4 allows a longer agentName.
func resolveObjectName(config *Config) (*objectName, error) {
	uid, err := uuid.NewV7()
	if err != nil {
		return nil, errors.New("failed to generate AgentID: " + err.Error())
	}
	agentID := encodeUID(uid)
	Log("config").Infof("auto-generated AgentID: %v", agentID)
	return resolveObjectNameWithID(config, agentID, uid)
}

// resolveObjectNameKeeping resolves the identity that config describes with
// the agent id of running kept, for ReloadConfig's check of a candidate
// configuration: the id is never configurable, so with it held fixed the
// candidate differs from the running identity only where the configuration
// does. Without a running identity it resolves as resolveObjectName does.
func resolveObjectNameKeeping(config *Config, running *objectName) (*objectName, error) {
	if running == nil {
		return resolveObjectName(config)
	}
	return resolveObjectNameWithID(config, running.agentID, running.agentUID)
}

// resolveObjectNameWithID is resolveObjectName with the agent id given.
// Surrounding whitespace is dropped from every identity value, as the Java
// agent trims each of its sources: an environment variable or a flag easily
// carries a stray space or newline that would otherwise fail validation.
func resolveObjectNameWithID(config *Config, agentID string, uid uuid.UUID) (*objectName, error) {
	version := parseNameVersion(config.String(CfgUIDVersion))
	appNameMax, agentNameMax := appNameMaxLenV3, agentNameMaxLen
	switch version {
	case nameV1:
		appNameMax = appNameMaxLenV1
	case nameV4:
		agentNameMax = agentNameMaxLenV4
	}

	appName := strings.TrimSpace(config.String(CfgAppName))
	if !IsValidId(appName, appNameMax) {
		return nil, errors.New("application name is required and must match " +
			cfgIdPattern + " within " + strconv.Itoa(appNameMax) + " bytes")
	}
	o := &objectName{
		version:         version,
		agentID:         agentID,
		agentName:       resolveAgentName(config, agentNameMax),
		applicationName: appName,
	}
	if version != nameV4 {
		return o, nil
	}

	// serviceName: the collector only checks the characters and the maximum
	// length, but the Java agent (IdValidatorV4) and the web UI's service
	// registry also require SERVICE_NAME_MIN_LEN, so a shorter name would
	// be accepted on the wire yet could never be registered as a service.
	o.serviceName = strings.TrimSpace(config.String(CfgServiceName))
	if !IsValidId(o.serviceName, serviceNameMaxLen) || len(o.serviceName) < serviceNameMinLen {
		return nil, errors.New("service name is required and must match " +
			cfgIdPattern + " between " + strconv.Itoa(serviceNameMinLen) +
			" and " + strconv.Itoa(serviceNameMaxLen) + " bytes")
	}
	// apiKey: optional; an empty value is kept and the apikey header is then
	// not sent. No length check, and no character check beyond the one gRPC
	// metadata imposes: a key it cannot carry would pass startup and then fail
	// every RPC, which reads as an unreachable collector. The message leaves
	// the key out, as everything that logs it does.
	o.apiKey = strings.TrimSpace(config.String(CfgApiKey))
	if !isPrintableASCII(o.apiKey) {
		return nil, errors.New("api key must contain only printable ASCII characters (0x20-0x7E)")
	}
	o.agentUID = uid
	return o, nil
}

// resolveClusterNamespace returns the configured ClusterNamespace, trimmed as
// the identity values are, or "" for one a header cannot carry. It goes out in
// the Pinpoint-pAppNamespace header of every outbound call, and gRPC metadata
// refuses a value outside printable ASCII (0x20-0x7E): such a namespace failed
// every traced gRPC call with Internal, and a control character every net/http
// one too, which reads as the callee being down. Dropped, the agent behaves as
// with none configured, and the warning keeps the mistake visible.
func resolveClusterNamespace(config *Config) string {
	namespace := strings.TrimSpace(config.String(CfgClusterNamespace))
	if isPrintableASCII(namespace) {
		return namespace
	}
	Log("config").Warnf("%s = %q must contain only printable ASCII characters (0x20-0x7E), so no namespace is used",
		CfgClusterNamespace, namespace)
	return ""
}

// resolveAgentName returns the configured agent name, or defaultAgentName when
// it is unset or invalid. agentName is a display label, not a required value, so
// an invalid one falls back with a warning rather than failing startup, and the
// warning is what keeps a typo from staying invisible.
func resolveAgentName(config *Config, maxLen int) string {
	agentName := strings.TrimSpace(config.String(CfgAgentName))
	if IsValidId(agentName, maxLen) {
		return agentName
	}
	generated := defaultAgentName()
	if agentName != "" {
		Log("config").Warnf("%s = %q must match %s within %d bytes, using %q instead",
			CfgAgentName, agentName, cfgIdPattern, maxLen, generated)
	}
	return generated
}

// defaultAgentName is the agent name of a process that sets none: its host name
// and pid, which say which instance it is in the web UI where the random agent
// id cannot. It is made once per process, so a host name
// that changes later, such as a laptop's DHCP name, does not read to
// ReloadConfig as an AgentName change.
var defaultAgentName = sync.OnceValue(func() string {
	return makeAgentName(getHostName(), os.Getpid())
})

// makeAgentName returns "host-pid", with every character of host that is not an
// id character replaced by '_' and host cut to fit the v4 limit, the shorter of
// the two, so the name is valid for every Uid.Version.
func makeAgentName(host string, pid int) string {
	suffix := "-" + strconv.Itoa(pid)
	host = strings.Map(func(r rune) rune {
		if isIDChars(string(r)) {
			return r
		}
		return '_'
	}, host)
	if maxHost := agentNameMaxLenV4 - len(suffix); len(host) > maxHost {
		host = host[:maxHost]
	}
	return host + suffix
}
