package pphttp

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatRequestParams(t *testing.T) {
	long := strings.Repeat("v", 100)
	var many []string
	for i := 0; i < 100; i++ {
		many = append(many, "k"+strings.Repeat("0", 2)+"=vvvvvvvvvv")
	}

	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain", "a=1&b=x%20y&empty=", "a=1&b=x y&empty="},
		{"blank items skipped", "&&a=1&&", "a=1"},
		{"bare key", "a+b=c+d&flag", "a b=c d&flag="},
		{"undecodable kept verbatim", "bad=%zz%4&ok=%41", "bad=%zz%4&ok=A"},
		{"long value cut", "k=" + long, "k=" + strings.Repeat("v", 64) + "..."},
		{"long key cut", long + "=1", strings.Repeat("v", 64) + "...=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatRequestParams(tt.in))
		})
	}

	got := FormatRequestParams(strings.Join(many, "&"))
	assert.True(t, strings.HasSuffix(got, "&..."), got)
	assert.LessOrEqual(t, len(got), 512+4)
}

func TestClientUrl(t *testing.T) {
	u, err := url.Parse("https://h/p?token=x#frag")
	require.NoError(t, err)

	usePluginConfig(t)
	assert.Equal(t, "GET https://h/p#frag", ClientUrl("GET", u))
	assert.Equal(t, "GET https://h/p#frag", ClientUrlString("GET", "https://h/p?token=x#frag"))
	assert.Equal(t, "GET https://h/p", ClientUrlString("GET", "https://h/p?token=x"))
	assert.Equal(t, "GET https://h/p", ClientUrlString("GET", "https://h/p"))
	assert.Equal(t, "GET", ClientUrl("GET", nil))
	assert.Equal(t, "https://h/p?token=x#frag", u.String(), "the caller's URL must not be modified")

	usePluginConfig(t, WithHttpClientRecordUrlQuery(true))
	assert.Equal(t, "GET https://h/p?token=x#frag", ClientUrl("GET", u))
	assert.Equal(t, "GET https://h/p?token=x#frag", ClientUrlString("GET", "https://h/p?token=x#frag"))
}

func TestQueryOptions_Defaults(t *testing.T) {
	usePluginConfig(t)
	cfg := httpCfg()
	assert.False(t, cfg.cltUrlQuery)
	assert.False(t, cfg.srvRequestParam)

	usePluginConfig(t, WithHttpClientRecordUrlQuery(true), WithHttpServerRecordRequestParam(true))
	cfg = httpCfg()
	assert.True(t, cfg.cltUrlQuery)
	assert.True(t, cfg.srvRequestParam)
}

func TestQueryOptions_EnvAndYaml(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("Http:\n  Server:\n    RecordRequestParam: true\n"), 0o600))
	t.Setenv("PINPOINT_GO_HTTP_CLIENT_RECORDURLQUERY", "true")

	usePluginConfig(t, pinpoint.WithConfigFile(path))
	cfg := httpCfg()
	assert.True(t, cfg.cltUrlQuery, "env")
	assert.True(t, cfg.srvRequestParam, "yaml")
}

// A config file reload republishes the plugin config through its reload
// callback, so a request sees the new value without a restart.
func TestQueryOptions_Reload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	write := func(v bool) {
		body := "Http:\n  Server:\n    RecordRequestParam: %t\n  Client:\n    RecordUrlQuery: %t\n"
		require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(body, "%t", map[bool]string{true: "true", false: "false"}[v])), 0o600))
	}
	write(false)
	usePluginConfig(t, pinpoint.WithConfigFile(path))
	require.False(t, httpCfg().srvRequestParam)
	require.False(t, httpCfg().cltUrlQuery)

	write(true)
	require.Eventually(t, func() bool {
		cfg := httpCfg()
		return cfg.srvRequestParam && cfg.cltUrlQuery
	}, 3*time.Second, 10*time.Millisecond, "reload did not reach the plugin config")
}
