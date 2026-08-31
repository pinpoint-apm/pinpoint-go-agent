package pphttp

import (
	"sync"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHttpConfigReloadRace republishes the plugin config concurrently with the
// reads a request performs. Run it with -race: before the derived filters and
// recorders were published as one immutable value, the reload callback
// reassigned ten plain package globals that every request read.
func TestHttpConfigReloadRace(t *testing.T) {
	config, err := pinpoint.NewConfig(
		pinpoint.WithAppName("raceApp"),
		WithHttpServerExcludeUrl([]string{"/skip/*", "/**/*.do"}),
		WithHttpServerExcludeMethod([]string{"put", "delete"}),
		WithHttpServerStatusCodeError([]string{"5xx", "302"}),
		WithHttpServerRecordRequestHeader([]string{"foo", "bar"}),
		WithHttpServerRecordRespondHeader([]string{"HEADERS-ALL"}),
	)
	require.NoError(t, err)

	_, err = pinpoint.NewTestAgent(config, t)
	require.NoError(t, err)

	done := make(chan struct{})
	stopped := func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // stands in for the config reload callback
		defer wg.Done()
		for !stopped() {
			curHttpConfig.Store(newHttpConfig())
		}
	}()

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { // request goroutines
			defer wg.Done()
			for !stopped() {
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

	time.Sleep(300 * time.Millisecond)
	close(done)
	wg.Wait()
}

// A reload publishes one whole config, so a request never reads a filter built
// from one generation next to a recorder built from another.
func TestHttpConfigReloadIsAtomic(t *testing.T) {
	usePluginConfig(t,
		WithHttpServerExcludeUrl([]string{"/skip/**"}),
		WithHttpServerStatusCodeError([]string{"4xx"}),
	)

	before := httpCfg()
	require.True(t, before.srvUrl.isFiltered("/skip/a"))
	require.True(t, before.srvStatus.isError(404))

	// Rebuilding under a new agent config swaps every derived value at once.
	usePluginConfig(t,
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
