package pphttp

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHttpConfigReloadRace republishes the plugin config concurrently with the
// reads a request performs. Run it with -race: before the derived filters and
// recorders were published as one immutable value, the reload callback
// reassigned ten plain package globals that every request read.
func TestHttpConfigReloadRace(t *testing.T) {
	// startAgent, not a bare NewTestAgent: it shuts the agent down.
	pptest.StartAgent(t,
		WithHttpServerExcludeUrl([]string{"/skip/*", "/**/*.do"}),
		WithHttpServerExcludeMethod([]string{"put", "delete"}),
		WithHttpServerStatusCodeError([]string{"5xx", "302"}),
		WithHttpServerRecordRequestHeader([]string{"foo", "bar"}),
		WithHttpServerRecordRespondHeader([]string{"HEADERS-ALL"}),
	)

	const iterations = 2000

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // stands in for the config reload callback
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			curHttpConfig.Store(newHttpConfig())
		}
	}()

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { // request goroutines
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = isExcludedUrl("/skip/index.html")
				_ = isExcludedUrl("/keep/index.html")
				_ = isExcludedMethod("PUT")
				_ = isExcludedMethod("GET")

				cfg := httpCfg()
				_ = cfg.srvStatus.isError(500)
				_ = cfg.recordHandlerError
				if !assert.NotNil(t, cfg.srvReqHeader) ||
					!assert.NotNil(t, cfg.srvResHeader) ||
					!assert.NotNil(t, cfg.cltCookie) {
					return // a partially initialized config was published
				}
			}
		}()
	}

	wg.Wait()
}

// A reload publishes one whole config, so a request never reads a filter built
// from one generation next to a recorder built from another.
func TestHttpConfigReloadIsAtomic(t *testing.T) {
	pptest.StartAgent(t,
		WithHttpServerExcludeUrl([]string{"/skip/**"}),
		WithHttpServerStatusCodeError([]string{"4xx"}),
	)

	before := httpCfg()
	require.True(t, before.srvUrl.isFiltered("/skip/a"))
	require.True(t, before.srvStatus.isError(404))

	// Rebuilding under a new agent config swaps every derived value at once.
	pptest.StartAgent(t,
		WithHttpServerExcludeUrl([]string{"/other/**"}),
		WithHttpServerStatusCodeError([]string{"5xx"}),
	)

	after := httpCfg()
	assert.NotSame(t, before, after, "a reload must publish a new config value, not mutate the old one")
	assert.True(t, after.srvUrl.isFiltered("/other/a"))
	assert.False(t, after.srvUrl.isFiltered("/skip/a"))
	assert.True(t, after.srvStatus.isError(500))
	assert.False(t, after.srvStatus.isError(404))

	// The value the first reader took keeps answering from its own generation.
	assert.True(t, before.srvUrl.isFiltered("/skip/a"), "a published config must be immutable once handed out")
	assert.True(t, before.srvStatus.isError(404))
}

func TestHttpConfigFollowsAgentRestart(t *testing.T) {
	first := pptest.StartAgent(t,
		WithHttpServerExcludeUrl([]string{"/old"}),
		pinpoint.WithHttpUrlStatEnable(false),
	)
	require.True(t, isExcludedUrl("/old"))
	config := first.Config()
	first.Shutdown()

	config.Set(CfgHttpServerExcludeUrl, []string{"/new"})
	config.Set(pinpoint.CfgHttpUrlStatEnable, true)
	second, err := pinpoint.NewTestAgent(config)
	require.NoError(t, err)
	t.Cleanup(second.Shutdown)

	assert.False(t, isExcludedUrl("/old"))
	assert.True(t, isExcludedUrl("/new"))
	assert.True(t, IsUrlStatEnabled())
}

func TestQueryOptions_EnvAndYaml(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("Http:\n  Server:\n    RecordRequestParam: true\n"), 0o600))
	t.Setenv("PINPOINT_GO_HTTP_CLIENT_RECORDURLQUERY", "true")

	pptest.StartAgent(t, pinpoint.WithConfigFile(path))
	cfg := httpCfg()
	assert.True(t, cfg.cltUrlQuery, "env")
	assert.True(t, cfg.srvRequestParam, "yaml")
}
