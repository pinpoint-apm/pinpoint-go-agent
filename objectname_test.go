package pinpoint

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
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
	c := newCfg(t, WithAppName("MyApp"), WithAgentName("my-name"))
	o, err := resolveObjectName(c)
	assert.NoError(t, err)
	assert.Equal(t, nameV3, o.version)
	assert.Len(t, o.agentID, uidBase64Len)
	assert.Equal(t, "my-name", o.agentName)
	assert.Equal(t, "MyApp", o.applicationName)
	assert.Empty(t, o.serviceName)
	assert.Empty(t, o.apiKey)
	assert.Equal(t, protocolVersionV1, o.protocolVersion())
}

func TestResolveObjectName_AutoGenAgentId(t *testing.T) {
	c := newCfg(t, WithAppName("MyApp"))
	o, err := resolveObjectName(c)
	assert.NoError(t, err)
	assert.Len(t, o.agentID, uidBase64Len)  // base64(UUIDv7)
	assert.Equal(t, o.agentID, o.agentName) // agentName falls back to agentId
}

func TestResolveObjectName_AppNameRequired(t *testing.T) {
	c := newCfg(t) // no app name
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
	)
	o, err := resolveObjectName(c)
	assert.NoError(t, err)
	assert.Equal(t, nameV4, o.version)
	assert.True(t, o.isV4())
	assert.Equal(t, protocolVersionV4, o.protocolVersion())
	// agentId is always generated.
	assert.Len(t, o.agentID, uidBase64Len)
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

// An invalid non-empty agentName falls back to the agentId with a warning,
// rather than aborting startup, for both v1/v3 and v4.
func TestResolveObjectName_InvalidAgentNameFallsBack(t *testing.T) {
	for _, tc := range []struct {
		version string
		opts    []ConfigOption
	}{
		{"v3", nil},
		{"v4", []ConfigOption{WithServiceName("MyService"), WithApiKey("key")}},
	} {
		t.Run(tc.version, func(t *testing.T) {
			for _, agentName := range []string{"bad name!", strings.Repeat("a", 256)} {
				var buf bytes.Buffer
				restore := captureLogAt(&buf, logrus.WarnLevel)
				o, err := resolveObjectName(newCfg(t, append([]ConfigOption{
					WithUidVersion(tc.version),
					WithAppName("MyApp"),
					WithAgentName(agentName),
				}, tc.opts...)...))
				restore()

				assert.NoError(t, err, "invalid agentName must not abort startup")
				assert.Equal(t, o.agentID, o.agentName, "agentName falls back to agentId")
				assert.Contains(t, buf.String(), CfgAgentName, "the fallback is warned about")
				assert.Equal(t, 1, strings.Count(buf.String(), "\n"), "exactly one warning")
			}
		})
	}
}

// Regression: an empty agentName falls back silently, a valid one is kept, and
// either way the value reaches the gRPC headers.
func TestResolveObjectName_AgentNameHeader(t *testing.T) {
	for _, version := range []string{"v1", "v3", "v4"} {
		t.Run(version, func(t *testing.T) {
			opts := []ConfigOption{WithUidVersion(version), WithAppName("MyApp")}
			if version == "v4" {
				opts = append(opts, WithServiceName("MyService"), WithApiKey("key"))
			}

			var buf bytes.Buffer
			restore := captureLogAt(&buf, logrus.WarnLevel)
			o, err := resolveObjectName(newCfg(t, opts...))
			restore()
			assert.NoError(t, err)
			assert.Equal(t, o.agentID, o.agentName, "empty agentName falls back to agentId")
			assert.Empty(t, buf.String(), "an unset agentName is not warned about")
			assert.Equal(t, o.agentID, agentHeaderMap(agentWith(o))[headerAgentName])

			o, err = resolveObjectName(newCfg(t, append(opts, WithAgentName("my-name"))...))
			assert.NoError(t, err)
			assert.Equal(t, "my-name", o.agentName, "a valid agentName is kept")
			assert.Equal(t, "my-name", agentHeaderMap(agentWith(o))[headerAgentName])
		})
	}
}
