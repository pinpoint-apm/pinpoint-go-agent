package pinpoint

import (
	"errors"
	"strconv"
	"strings"

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
func parseNameVersion(version string) nameVersion {
	switch strings.ToLower(strings.TrimSpace(version)) {
	case "v1":
		return nameV1
	case "v4":
		return nameV4
	default:
		return nameV3
	}
}

// objectName holds the resolved agent self-identification.
type objectName struct {
	version         nameVersion
	agentID         string    // always auto-generated base64(UUIDv7)
	agentName       string    // always non-empty after resolution
	applicationName string    // required
	serviceName     string    // v4 only; "" for v1/v3
	apiKey          string    // v4 only; "" for v1/v3 (never logged)
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
// every version, serviceName and apiKey for v4 as well. A missing required
// field aborts agent startup with an error. v1 and v3 differ only in the
// applicationName length limit; v4 allows a longer agentName.
func resolveObjectName(config *Config) (*objectName, error) {
	version := parseNameVersion(config.String(CfgUIDVersion))
	appNameMax, agentNameMax := appNameMaxLenV3, agentNameMaxLen
	switch version {
	case nameV1:
		appNameMax = appNameMaxLenV1
	case nameV4:
		agentNameMax = agentNameMaxLenV4
	}

	uid, err := uuid.NewV7()
	if err != nil {
		return nil, errors.New("failed to generate AgentID: " + err.Error())
	}
	agentID := encodeUID(uid)
	Log("config").Infof("auto-generated AgentID: %v", agentID)

	appName := config.String(CfgAppName)
	if !IsValidId(appName, appNameMax) {
		return nil, errors.New("application name is required and must match " +
			cfgIdPattern + " within " + strconv.Itoa(appNameMax) + " bytes")
	}
	o := &objectName{
		version:         version,
		agentID:         agentID,
		agentName:       resolveAgentName(config, agentID, agentNameMax),
		applicationName: appName,
	}
	if version != nameV4 {
		return o, nil
	}

	o.serviceName = config.String(CfgServiceName)
	if !IsValidId(o.serviceName, serviceNameMaxLen) {
		return nil, errors.New("service name is required and must match " +
			cfgIdPattern + " within " + strconv.Itoa(serviceNameMaxLen) + " bytes")
	}
	// apiKey: required, only checked for non-emptiness.
	o.apiKey = config.String(CfgApiKey)
	if o.apiKey == "" {
		return nil, errors.New("api key (" + CfgApiKey + ") is required")
	}
	o.agentUID = uid
	return o, nil
}

// resolveAgentName returns the configured agent name, or agentID when it is
// unset or invalid. agentName is a display label, not a required value, so an
// so a typo does not stay invisible.
func resolveAgentName(config *Config, agentID string, maxLen int) string {
	agentName := config.String(CfgAgentName)
	if IsValidId(agentName, maxLen) {
		return agentName
	}
	if agentName != "" {
		Log("config").Warnf("%s = %q must match %s within %d bytes, using AgentID %q instead",
			CfgAgentName, agentName, cfgIdPattern, maxLen, agentID)
	}
	return agentID
}
