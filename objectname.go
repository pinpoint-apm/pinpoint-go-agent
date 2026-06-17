package pinpoint

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// nameVersion selects the agent self-identification (ObjectName) scheme,
// mirroring the Java agent's pinpoint.modules.uid.version property.
type nameVersion int

const (
	nameV1 nameVersion = iota
	nameV3
	nameV4
)

// ID length limits (UTF-8 byte length), matching Java PinpointConstants.
const (
	agentIDMaxLen     = 24  // AGENT_ID_MAX_LEN
	agentNameMaxLen   = 255 // AGENT_NAME_MAX_LEN
	serviceNameMaxLen = 254 // SERVICE_NAME_MAX_LEN
	appNameMaxLenV1   = 24  // v1: applicationName uses the agentId limit
	appNameMaxLenV3   = 254 // APPLICATION_NAME_MAX_LEN_V3 (= SERVICE_NAME_MAX_LEN)
	agentNameMaxLenV4 = 254 // AGENT_NAME_MAX_LEN_V4
)

// Protocol versions sent on the gRPC protocol.version header, matching Java
// ProtocolVersion (V1 = 1_00, V4 = 4_00).
const (
	protocolVersionV1 = 100
	protocolVersionV4 = 400
)

// idPattern is the allowed-character rule for ids: [a-zA-Z0-9], '.', '-', '_'.
// Identical to Java IdValidateUtils.ID_PATTERN_VALUE.
var idPattern = regexp.MustCompile("^[a-zA-Z0-9._\\-]+$")

// validateID reports whether value is a non-empty id within maxLen UTF-8 bytes
// and contains only allowed characters. Equivalent to IdValidateUtils.validateId.
func validateID(value string, maxLen int) bool {
	if len(value) == 0 || len(value) > maxLen {
		return false
	}
	return idPattern.MatchString(value)
}

// parseNameVersion parses the configured version string (case-insensitive).
// Unknown or empty values fall back to v3, matching the Java agent default.
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
	agentID         string    // base64(UUID) for v4, otherwise user/auto value
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
// configured version. Missing required fields abort agent startup with an error.
func resolveObjectName(config *Config) (*objectName, error) {
	version := parseNameVersion(config.String(CfgUIDVersion))
	switch version {
	case nameV1:
		return resolveV1V3(config, nameV1, appNameMaxLenV1)
	case nameV4:
		return resolveV4(config)
	default:
		return resolveV1V3(config, nameV3, appNameMaxLenV3)
	}
}

// resolveV1V3 produces an ObjectNameV1-style identity (shared by v1 and v3,
// differing only in the applicationName length limit).
func resolveV1V3(config *Config, version nameVersion, appNameMax int) (*objectName, error) {
	// agentId: use the validated input, else auto-generate base64(UUIDv7).
	agentID := config.String(CfgAgentID)
	if !validateID(agentID, agentIDMaxLen) {
		uid, err := newAgentUID()
		if err != nil {
			return nil, errors.New("failed to generate AgentID: " + err.Error())
		}
		agentID = encodeUID(uid)
		Log("config").Infof("auto-generated AgentID: %v", agentID)
	}

	// applicationName: required.
	appName := config.String(CfgAppName)
	if !validateID(appName, appNameMax) {
		return nil, errors.New("application name is required and must match " +
			cfgIdPattern + " within " + strconv.Itoa(appNameMax) + " bytes")
	}

	// agentName: optional, falls back to agentId.
	agentName := config.String(CfgAgentName)
	if !validateID(agentName, agentNameMaxLen) {
		if agentName != "" {
			return nil, errors.New("agent name must match " + cfgIdPattern +
				" within " + strconv.Itoa(agentNameMaxLen) + " bytes")
		}
		agentName = agentID
	}

	return &objectName{
		version:         version,
		agentID:         agentID,
		agentName:       agentName,
		applicationName: appName,
	}, nil
}

// resolveV4 produces an ObjectNameV4 identity. agentId is always a freshly
// generated UUIDv7; serviceName and apiKey are required.
func resolveV4(config *Config) (*objectName, error) {
	uid, err := newAgentUID()
	if err != nil {
		return nil, errors.New("failed to generate AgentID: " + err.Error())
	}
	agentID := encodeUID(uid)

	// agentName: optional, falls back to base64(agentId UUID).
	agentName := config.String(CfgAgentName)
	if !validateID(agentName, agentNameMaxLenV4) {
		if agentName != "" {
			return nil, errors.New("agent name must match " + cfgIdPattern +
				" within " + strconv.Itoa(agentNameMaxLenV4) + " bytes")
		}
		agentName = agentID
	}

	// applicationName: required.
	appName := config.String(CfgAppName)
	if !validateID(appName, appNameMaxLenV3) {
		return nil, errors.New("application name is required and must match " +
			cfgIdPattern + " within " + strconv.Itoa(appNameMaxLenV3) + " bytes")
	}

	// serviceName: required.
	serviceName := config.String(CfgServiceName)
	if !validateID(serviceName, serviceNameMaxLen) {
		return nil, errors.New("service name is required and must match " +
			cfgIdPattern + " within " + strconv.Itoa(serviceNameMaxLen) + " bytes")
	}

	// apiKey: required, only checked for non-emptiness.
	apiKey := config.String(CfgApiKey)
	if apiKey == "" {
		return nil, errors.New("api key (" + CfgApiKey + ") is required")
	}

	return &objectName{
		version:         nameV4,
		agentID:         agentID,
		agentName:       agentName,
		applicationName: appName,
		serviceName:     serviceName,
		apiKey:          apiKey,
		agentUID:        uid,
	}, nil
}
