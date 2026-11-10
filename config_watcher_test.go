package pinpoint

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfigRate(t testing.TB, path string, rate int) {
	t.Helper()
	assert.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("Sampling:\n  CounterRate: %d\n", rate)), 0o600))
}

func configWatcherDone(config *Config) <-chan struct{} {
	config.watchMu.Lock()
	defer config.watchMu.Unlock()
	return config.watcherDone
}

func requireWatcher(t *testing.T, config *Config) <-chan struct{} {
	t.Helper()
	done := configWatcherDone(config)
	require.NotNil(t, done, "config file watcher was not started")
	return done
}

func requireWatcherDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	default:
		t.Fatal("watcher goroutine remained after lifecycle shutdown returned")
	}
}

// The poller is paced for a process, not a test: every watcher test waits on a
// reload, so the whole test binary polls fast.
func init() { configPollInterval = 20 * time.Millisecond }

func TestConfigWatcherReloadAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(WithAppName("watcher-app"), WithConfigFile(path))
	require.NoError(t, err)
	done := requireWatcher(t, config)
	t.Cleanup(config.Close)

	writeConfigRate(t, path, 2)
	require.Eventually(t, func() bool {
		return config.Int(CfgSamplingCounterRate) == 2
	}, 2*time.Second, 10*time.Millisecond, "config file change was not reloaded")

	config.Close()
	config.Close()
	requireWatcherDone(t, done)

	writeConfigRate(t, path, 3)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 2, config.Int(CfgSamplingCounterRate), "closed watcher still reloaded the file")
}

// A dynamic key deleted from the file goes back to the value the file had
// overridden - the config function's here - rather than staying at the file's
// last value until restart.
func TestConfigWatcherReloadRestoresRemovedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 2)

	config, err := NewConfig(WithAppName("watcher-removed"), WithSamplingCounterRate(5), WithConfigFile(path))
	require.NoError(t, err)
	requireWatcher(t, config)
	t.Cleanup(config.Close)
	require.Equal(t, 2, config.Int(CfgSamplingCounterRate), "the file overrides the option")

	assert.NoError(t, os.WriteFile(path, []byte("Sampling:\n  PercentRate: 50\n"), 0o600))
	require.Eventually(t, func() bool {
		return config.Int(CfgSamplingCounterRate) == 5
	}, 2*time.Second, 10*time.Millisecond, "a key removed from the file kept the file's value")
	assert.Equal(t, 50.0, config.Float(CfgSamplingPercentRate), "the key still in the file is applied")
}

// A truncate+write saver leaves the file empty between its two steps. A poll
// landing there must keep the current values: an empty YAML document parses to
// no keys, and reloading it put every dynamic option back to its base value
// until the write landed.
func TestConfigWatcherSkipsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 2)

	config, err := NewConfig(WithAppName("watcher-empty"), WithSamplingCounterRate(5), WithConfigFile(path))
	require.NoError(t, err)
	requireWatcher(t, config)
	t.Cleanup(config.Close)
	require.Equal(t, 2, config.Int(CfgSamplingCounterRate), "the file overrides the option")

	var reloads sync.Mutex
	reloaded := 0
	config.AddReloadCallback([]string{CfgSamplingCounterRate}, func() {
		reloads.Lock()
		reloaded++
		reloads.Unlock()
	})

	require.NoError(t, os.WriteFile(path, nil, 0o600))
	time.Sleep(5 * configPollInterval)
	assert.Equal(t, 2, config.Int(CfgSamplingCounterRate), "an empty file reset the option to its base value")

	writeConfigRate(t, path, 3)
	require.Eventually(t, func() bool {
		return config.Int(CfgSamplingCounterRate) == 3
	}, 2*time.Second, 10*time.Millisecond, "the write after the truncate was not reloaded")
	// The reload publishes the value before it runs the callbacks, so the
	// count can trail the value just seen.
	count := func() int {
		reloads.Lock()
		defer reloads.Unlock()
		return reloaded
	}
	require.Eventually(t, func() bool { return count() >= 1 }, 2*time.Second, 10*time.Millisecond,
		"the callback did not run for the write")
	assert.Equal(t, 1, count(), "the callback ran for the empty file too")
}

func TestConfigWatcherReloadKeepsEnvValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	write := func(rate int, percent float64) {
		assert.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("Sampling:\n  CounterRate: %d\n  PercentRate: %g\n", rate, percent)), 0o600))
	}
	write(1, 10)
	t.Setenv("PINPOINT_GO_SAMPLING_COUNTERRATE", "7")

	config, err := NewConfig(WithAppName("watcher-env"), WithConfigFile(path))
	require.NoError(t, err)
	requireWatcher(t, config)
	t.Cleanup(config.Close)
	require.Equal(t, 7, config.Int(CfgSamplingCounterRate))

	write(2, 20)
	require.Eventually(t, func() bool {
		return config.Float(CfgSamplingPercentRate) == 20
	}, 2*time.Second, 10*time.Millisecond, "file-only value was not reloaded")
	require.Equal(t, 7, config.Int(CfgSamplingCounterRate), "env value was overwritten by the config file")
}

func TestConfigWatcherDoesNotAccumulateAcrossAgentLifecycles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	for i := 0; i < 10; i++ {
		config, err := NewConfig(
			WithAppName("watcher-lifecycle"),
			WithServiceName("test-service"),
			WithConfigFile(path),
		)
		require.NoError(t, err)
		config.offGrpc = true
		done := requireWatcher(t, config)

		agent, err := NewAgent(config)
		require.NoError(t, err)
		agent.Shutdown()
		requireWatcherDone(t, done)
		require.Nil(t, configWatcherDone(config))
	}
}

func TestConfigWatcherRestartsWhenConfigIsReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(
		WithAppName("watcher-reuse"),
		WithServiceName("test-service"),
		WithConfigFile(path),
	)
	require.NoError(t, err)
	config.offGrpc = true
	t.Cleanup(config.Close)

	initialDone := requireWatcher(t, config)
	config.Close()
	config.Close()
	requireWatcherDone(t, initialDone)

	first, err := NewAgent(config)
	require.NoError(t, err)
	firstDone := requireWatcher(t, config)
	first.Shutdown()
	requireWatcherDone(t, firstDone)
	writeConfigRate(t, path, 2)

	second, err := NewAgent(config)
	require.NoError(t, err)
	secondDone := requireWatcher(t, config)
	require.Equal(t, 2, config.Int(CfgSamplingCounterRate), "reused config missed a change made while stopped")

	// Reuse must not stack the logger's reload callbacks up once per agent.
	config.mu.Lock()
	callbacks := len(config.callback)
	config.mu.Unlock()
	require.Equal(t, 1, callbacks, "reload callbacks accumulated across agent lifetimes")

	// A stale idempotent Shutdown must not close the reused Config's new watcher.
	first.Shutdown()
	select {
	case <-secondDone:
		t.Fatal("stale agent shutdown stopped the replacement agent's watcher")
	default:
	}

	second.Shutdown()
	requireWatcherDone(t, secondDone)
}

func TestNewAgentFailureStopsWatcherAndConfigCanBeRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(WithConfigFile(path), WithServiceName("test-service"))
	require.NoError(t, err)
	t.Cleanup(config.Close)
	failedDone := requireWatcher(t, config)

	_, err = NewAgent(config)
	require.Error(t, err)
	requireWatcherDone(t, failedDone)

	config.Set(CfgAppName, "watcher-retry")
	config.offGrpc = true
	agent, err := NewAgent(config)
	require.NoError(t, err)
	retryDone := requireWatcher(t, config)
	agent.Shutdown()
	requireWatcherDone(t, retryDone)
}

func TestDisabledAgentStopsConfigWatcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(
		WithAppName("watcher-disabled"),
		WithServiceName("test-service"),
		WithConfigFile(path),
		WithEnable(false),
	)
	require.NoError(t, err)
	done := requireWatcher(t, config)

	agent, err := NewAgent(config)
	require.NoError(t, err)
	require.Same(t, NoopAgent(), agent)
	requireWatcherDone(t, done)
	require.Nil(t, configWatcherDone(config))
}

func TestRejectedConfigWatcherStopsWithoutAffectingRunningAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	newConfig := func() *Config {
		config, err := NewConfig(
			WithAppName("watcher-reject"),
			WithServiceName("test-service"),
			WithConfigFile(path),
		)
		require.NoError(t, err)
		config.offGrpc = true
		t.Cleanup(config.Close)
		return config
	}

	runningConfig := newConfig()
	running, err := NewAgent(runningConfig)
	require.NoError(t, err)
	t.Cleanup(running.Shutdown)
	runningDone := requireWatcher(t, runningConfig)

	rejectedConfig := newConfig()
	rejectedDone := requireWatcher(t, rejectedConfig)
	got, err := NewAgent(rejectedConfig)
	require.Error(t, err)
	require.Same(t, running, got)
	requireWatcherDone(t, rejectedDone)

	select {
	case <-runningDone:
		t.Fatal("rejecting another config stopped the running agent's watcher")
	default:
	}

	running.Shutdown()
	requireWatcherDone(t, runningDone)

	replacement, err := NewAgent(rejectedConfig)
	require.NoError(t, err)
	replacementDone := requireWatcher(t, rejectedConfig)
	replacement.Shutdown()
	requireWatcherDone(t, replacementDone)
}

// Run with -race. File rewrites reload immutable snapshots while the same
// Config repeatedly transfers between agent lifetimes and concurrent readers.
func TestConfigWatcherReloadShutdownRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(
		WithAppName("watcher-race"),
		WithServiceName("test-service"),
		WithConfigFile(path),
	)
	require.NoError(t, err)
	config.offGrpc = true
	t.Cleanup(config.Close)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	defer func() {
		close(stop)
		wg.Wait()
	}()
	wg.Go(func() {
		for i := 2; ; i++ {
			select {
			case <-stop:
				return
			default:
				writeConfigRate(t, path, i%10+1)
				time.Sleep(time.Millisecond)
			}
		}
	})

	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					_ = config.Int(CfgSamplingCounterRate)
					_ = config.Bool(CfgSQLTraceBindValue)
					_ = config.String(CfgSamplingType)
					_ = config.load().sampler
				}
			}
		})
	}

	for i := 0; i < 10; i++ {
		agent, err := NewAgent(config)
		require.NoError(t, err)
		done := requireWatcher(t, config)
		time.Sleep(5 * time.Millisecond)
		agent.Shutdown()
		requireWatcherDone(t, done)
	}

}

// A caller-supplied reload callback runs on the watcher goroutine that Close
// waits for, so it must not be able to hang Close - and through it, Shutdown.
func TestConfigWatcherCloseDoesNotHangOnBlockedReloadCallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(WithAppName("watcher-block"), WithConfigFile(path))
	require.NoError(t, err)
	t.Cleanup(config.Close)
	done := requireWatcher(t, config)

	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce sync.Once
	config.AddReloadCallback([]string{CfgSamplingCounterRate}, func() {
		enteredOnce.Do(func() { close(entered) })
		<-release
	})

	writeConfigRate(t, path, 2)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reload callback never ran")
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		config.Close()
	}()
	select {
	case <-closed:
	case <-time.After(shutdownTimeout + 2*time.Second):
		t.Fatal("Close hung on a blocked reload callback")
	}

	// The abandoned goroutine still has to exit once the callback returns.
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned watcher goroutine never exited")
	}
}

// A Close that gave up on a blocked reload callback leaves a poller that must
// not start another reload. Once the callback returns, the stop and a tick are
// both ready and a select takes either; the file is rewritten while the
// callback blocks, so a poller that took the tick published the rewrite and
// ran the callbacks again, after Close had returned. Repeated because a select
// that does not put the stop first still took it about two runs in three.
func TestConfigWatcherAbandonedPollerStartsNoReload(t *testing.T) {
	swapForTest(t, &shutdownTimeout, 50*time.Millisecond)

	for run := 0; run < 20; run++ {
		path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
		writeConfigRate(t, path, 1)

		config, err := NewConfig(WithAppName("watcher-abandon"), WithConfigFile(path))
		require.NoError(t, err)
		done := requireWatcher(t, config)

		var calls atomic.Int32
		entered, release := make(chan struct{}), make(chan struct{})
		config.AddReloadCallback([]string{CfgSamplingCounterRate}, func() {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
		})

		writeConfigRate(t, path, 2)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("reload callback never ran")
		}
		config.Close() // gives up after shutdownTimeout

		// Another size, so the stamp differs whatever the mtime granularity.
		writeConfigRate(t, path, 30)
		close(release)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("abandoned watcher goroutine never exited")
		}
		require.Equal(t, 2, config.Int(CfgSamplingCounterRate), "run %d: the abandoned poller reloaded after Close returned", run)
		require.EqualValues(t, 1, calls.Load(), "run %d: the callbacks ran again after Close returned", run)
	}
}

// panicOnceHook is a log hook of the host application's that fails: it panics
// on the first line containing substr and is harmless from then on, so the
// line that reports the recovered panic goes through.
type panicOnceHook struct {
	substr string
	fired  atomic.Bool
}

func (h *panicOnceHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *panicOnceHook) Fire(e *logrus.Entry) error {
	if strings.Contains(e.Message, h.substr) && h.fired.CompareAndSwap(false, true) {
		panic("log hook failure")
	}
	return nil
}

// panicOnLog installs a panicOnceHook for substr on the extra logger, which
// gets every agent log line, until the test ends.
func panicOnLog(t *testing.T, substr string) *panicOnceHook {
	t.Helper()
	hook := &panicOnceHook{substr: substr}
	extra := logrus.New()
	extra.SetOutput(io.Discard)
	extra.AddHook(hook)
	prev := logger.extraLogger.Load()
	SetExtraLogger(extra)
	t.Cleanup(func() { logger.extraLogger.Store(prev) })
	return hook
}

// The poller is an agent goroutine inside the host process, and its reload
// path parses an operator's file and logs through the host's hooks. A panic
// there - here the hook failing on the warning for a value of the wrong type -
// must not end the process or the poller, and must not leave config.mu held:
// the next rewrite of the file is still applied.
func TestConfigWatcherSurvivesReloadPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(WithAppName("watcher-panic"), WithConfigFile(path))
	require.NoError(t, err)
	done := requireWatcher(t, config)
	t.Cleanup(config.Close)

	hook := panicOnLog(t, "is not a valid")
	require.NoError(t, os.WriteFile(path, []byte("Sampling:\n  CounterRate: abc\n"), 0o600))
	require.Eventually(t, hook.fired.Load, 2*time.Second, 5*time.Millisecond, "the reload never reached the failing hook")

	writeConfigRate(t, path, 7)
	require.Eventually(t, func() bool {
		return config.Int(CfgSamplingCounterRate) == 7
	}, 2*time.Second, 10*time.Millisecond, "the poller stopped reloading after a panic")
	select {
	case <-done:
		t.Fatal("the poller exited on the panic")
	default:
	}
}

// NewAgent reloads the file on its caller's goroutine, where a panic is the
// caller's to recover. Whoever recovers it, the lock the reload staged under
// must be free again, or every later Set, AddReloadCallback and reload blocks.
func TestReloadConfigPanicReleasesConfigLock(t *testing.T) {
	config, err := NewConfig(WithAppName("reload-panic"))
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("Sampling:\n  CounterRate: abc\n"), 0o600))

	panicOnLog(t, "is not a valid")
	require.Panics(t, func() { config.reloadConfig(newConfigFile(path)) })
	require.True(t, config.mu.TryLock(), "the panic left config.mu held")
	config.mu.Unlock()
}
