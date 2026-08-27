package pphttp

import (
	"sync"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
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
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pinpoint.NewTestAgent(config, t); err != nil {
		t.Fatal(err)
	}

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
				if cfg.srvReqHeader == nil || cfg.srvResHeader == nil || cfg.cltCookie == nil {
					t.Error("published http config has an uninitialized recorder")
					return
				}
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	close(done)
	wg.Wait()
}
