package pinpoint

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// TestConfigReloadRace drives a config reload concurrently with the reads a
// request goroutine performs, which is what the fsnotify watcher does in
// production. Run it with -race: before the config was published as an
// immutable snapshot, the reloader wrote Config's typed fields, cfgMap[k].value
// and agent.sampler in place while spans and the generic accessors read them.
func TestConfigReloadRace(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeCfgFile := func(i int) {
		body := fmt.Sprintf(`
Sampling:
  Type: %s
  CounterRate: %d
  NewThroughput: %d
SQL:
  TraceBindValue: %t
  MaxBindValueSize: %d
  TraceQueryStat: %t
Span:
  MaxCallStackDepth: %d
  MaxCallStackSequence: %d
  EventChunkSize: %d
Error:
  TraceCallStack: %t
  CallStackDepth: %d
Http:
  UrlStat:
    Enable: %t
    LimitSize: %d
`,
			[]string{samplingTypeCounter, samplingTypePercent}[i%2], 1+i%8, i%4,
			i%2 == 0, 128+i%64, i%2 == 1,
			8+i%32, 16+i%64, 4+i%16,
			i%2 == 0, 8+i%16,
			i%2 == 1, 512+i%256)
		if err := os.WriteFile(cfgFile, []byte(body), 0o600); err != nil {
			t.Error(err)
		}
	}
	writeCfgFile(0)

	// The reload is driven directly rather than through WithConfigFile so the
	// test does not depend on fsnotify timing.
	config, err := NewConfig(WithAppName("raceApp"))
	if err != nil {
		t.Fatal(err)
	}
	agent := newTestAgent(config)
	// sqlConn reads the live config through the global agent, which newTestAgent
	// registered above with this very Config.
	conn := &sqlConn{}

	cfgFileViper := viper.New()
	cfgFileViper.SetConfigFile(cfgFile)

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
	go func() { // stands in for the fsnotify goroutine
		defer wg.Done()
		for i := 1; !stopped(); i++ {
			writeCfgFile(i)
			config.reloadConfig(cfgFileViper)
		}
	}()

	wg.Add(1)
	go func() { // AddReloadCallback races the reloader's walk of the callback list
		defer wg.Done()
		for i := 0; i < 50 && !stopped(); i++ {
			config.AddReloadCallback([]string{CfgSamplingCounterRate}, func() {})
			time.Sleep(time.Millisecond)
		}
	}()

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { // request goroutines
			defer wg.Done()
			named := []driver.NamedValue{{Ordinal: 1, Value: "1"}}
			for !stopped() {
				_ = config.Bool(CfgSQLTraceBindValue)
				_ = config.Int(CfgSpanMaxCallStackDepth)
				_ = config.Float(CfgSamplingPercentRate)
				_ = config.String(CfgSamplingType)
				_ = config.StringSlice(CfgActiveProfile)
				_ = conn.namedValueToString(named)

				tracer := agent.NewSpanTracer("raceOp", "/race")
				tracer.NewSpanEvent("evt")
				tracer.SpanEvent().SetSQL("select * from t where id = ?", "1")
				tracer.SpanEvent().SetError(errors.New("race error"))
				tracer.EndSpanEvent()
				tracer.AddMetric(MetricURLStat, &UrlStatEntry{Url: "/race", Method: "GET", Status: 200})
				tracer.EndSpan()
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	close(done)
	wg.Wait()
}
