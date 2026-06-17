package pinpoint

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseNameVersion(t *testing.T) {
	cases := map[string]nameVersion{
		"v1":      nameV1,
		"V1":      nameV1,
		"v3":      nameV3,
		"V3":      nameV3,
		"v4":      nameV4,
		"V4":      nameV4,
		" v4 ":    nameV4,
		"":        nameV3, // missing -> default v3
		"v2":      nameV3, // unknown -> fallback v3
		"unknown": nameV3,
	}
	for in, want := range cases {
		assert.Equal(t, want, parseNameVersion(in), "parseNameVersion(%q)", in)
	}
}

func newCfg(t *testing.T, opts ...ConfigOption) *Config {
	t.Helper()
	c, err := NewConfig(opts...)
	assert.NoError(t, err)
	return c
}

func TestResolveObjectName_V3_Default(t *testing.T) {
	c := newCfg(t, WithAppName("MyApp"), WithAgentId("my-agent"), WithAgentName("my-name"))
	o, err := resolveObjectName(c)
	assert.NoError(t, err)
	assert.Equal(t, nameV3, o.version)
	assert.Equal(t, "my-agent", o.agentID)
	assert.Equal(t, "my-name", o.agentName)
	assert.Equal(t, "MyApp", o.applicationName)
	assert.Empty(t, o.serviceName)
	assert.Empty(t, o.apiKey)
	assert.Equal(t, protocolVersionV1, o.protocolVersion())
}

func TestResolveObjectName_AgentNameFallsBackToAgentId(t *testing.T) {
	c := newCfg(t, WithAppName("MyApp"), WithAgentId("my-agent"))
	o, err := resolveObjectName(c)
	assert.NoError(t, err)
	assert.Equal(t, "my-agent", o.agentName)
}

func TestResolveObjectName_AutoGenAgentId(t *testing.T) {
	c := newCfg(t, WithAppName("MyApp")) // no agent id
	o, err := resolveObjectName(c)
	assert.NoError(t, err)
	assert.Len(t, o.agentID, uidBase64Len)  // base64(UUIDv7)
	assert.Equal(t, o.agentID, o.agentName) // agentName falls back to agentId
}

func TestResolveObjectName_AppNameRequired(t *testing.T) {
	c := newCfg(t, WithAgentId("my-agent")) // no app name
	_, err := resolveObjectName(c)
	assert.Error(t, err)
}

// applicationName length boundary: 24 (v1) vs 254 (v3).
func TestResolveObjectName_AppNameLength_V1vsV3(t *testing.T) {
	name25 := strings.Repeat("a", 25)
	name254 := strings.Repeat("a", 254)
	name255 := strings.Repeat("a", 255)

	// v1: max 24 -> 25 fails
	_, err := resolveObjectName(newCfg(t, WithUidVersion("v1"), WithAppName(name25)))
	assert.Error(t, err, "v1 should reject 25-char appName")

	// v1: 24 passes
	o, err := resolveObjectName(newCfg(t, WithUidVersion("v1"), WithAppName(strings.Repeat("a", 24))))
	assert.NoError(t, err)
	assert.Equal(t, nameV1, o.version)

	// v3: 25 passes (limit 254)
	o, err = resolveObjectName(newCfg(t, WithUidVersion("v3"), WithAppName(name25)))
	assert.NoError(t, err, "v3 should accept 25-char appName")
	assert.Equal(t, nameV3, o.version)

	// v3: 254 passes, 255 fails
	_, err = resolveObjectName(newCfg(t, WithUidVersion("v3"), WithAppName(name254)))
	assert.NoError(t, err)
	_, err = resolveObjectName(newCfg(t, WithUidVersion("v3"), WithAppName(name255)))
	assert.Error(t, err, "v3 should reject 255-char appName")
}

func TestResolveObjectName_InvalidPattern(t *testing.T) {
	_, err := resolveObjectName(newCfg(t, WithAppName("bad name!")))
	assert.Error(t, err)
}

func TestResolveObjectName_V4_Success(t *testing.T) {
	c := newCfg(t,
		WithUidVersion("v4"),
		WithAppName("MyApp"),
		WithServiceName("MyService"),
		WithApiKey("secret-key"),
		WithAgentId("ignored-input"),
	)
	o, err := resolveObjectName(c)
	assert.NoError(t, err)
	assert.Equal(t, nameV4, o.version)
	assert.True(t, o.isV4())
	assert.Equal(t, protocolVersionV4, o.protocolVersion())
	// agentId is always generated, input ignored.
	assert.Len(t, o.agentID, uidBase64Len)
	assert.NotEqual(t, "ignored-input", o.agentID)
	assert.Equal(t, encodeUID(o.agentUID), o.agentID)
	// agentName falls back to base64(agentId UUID) when not provided.
	assert.Equal(t, o.agentID, o.agentName)
	assert.Equal(t, "MyService", o.serviceName)
	assert.Equal(t, "secret-key", o.apiKey)
}

func TestResolveObjectName_V4_MissingServiceName(t *testing.T) {
	c := newCfg(t, WithUidVersion("v4"), WithAppName("MyApp"), WithApiKey("key"))
	_, err := resolveObjectName(c)
	assert.Error(t, err)
}

func TestResolveObjectName_V4_MissingApiKey(t *testing.T) {
	c := newCfg(t, WithUidVersion("v4"), WithAppName("MyApp"), WithServiceName("Svc"))
	_, err := resolveObjectName(c)
	assert.Error(t, err)
}

func TestResolveObjectName_V4_MissingAppName(t *testing.T) {
	c := newCfg(t, WithUidVersion("v4"), WithServiceName("Svc"), WithApiKey("key"))
	_, err := resolveObjectName(c)
	assert.Error(t, err)
}

func TestObjectName_String_MasksApiKey(t *testing.T) {
	o := &objectName{
		version:         nameV4,
		agentID:         "agent",
		agentName:       "agent",
		applicationName: "app",
		serviceName:     "svc",
		apiKey:          "super-secret",
	}
	s := o.String()
	assert.NotContains(t, s, "super-secret")
	assert.Contains(t, s, "****")
}

func TestValidateID_ByteLength(t *testing.T) {
	// A 2-byte UTF-8 char counts as 2 bytes (matching Java UTF-8 byte length).
	assert.False(t, validateID("é", 1), "multibyte char exceeds 1-byte limit")
	// Also rejected by pattern, but the length check is what we assert here.
	assert.True(t, validateID("ab", 2))
	assert.False(t, validateID("abc", 2))
	assert.False(t, validateID("", 5))
}
