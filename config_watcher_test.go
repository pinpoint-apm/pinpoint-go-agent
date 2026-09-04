package pinpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeConfigRate(t testing.TB, path string, rate int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(fmt.Sprintf("Sampling:\n  CounterRate: %d\n", rate)), 0o600); err != nil {
		t.Error(err)
	}
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

// watcherFDCount counts the inotify descriptors the process holds, one per live
// fsnotify watcher. A process-wide descriptor count cannot stand in for it:
// newAgentStats builds a gopsutil process handle once per NewAgent, and that is
// an os.FindProcess pidfd which nothing but a garbage collection closes, so ten
// agent lifecycles move the total on their own. Linux only; the caller skips the
// check where /proc is not mounted.
func watcherFDCount() (int, bool) {
	const dir = "/proc/self/fd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, false
	}
	n := 0
	for _, entry := range entries {
		// An inotify instance reads back as "anon_inode:inotify".
		if target, err := os.Readlink(filepath.Join(dir, entry.Name())); err == nil &&
			strings.Contains(target, "inotify") {
			n++
		}
	}
	return n, true
}

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

func TestConfigWatcherReloadKeepsEnvValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	write := func(rate int, percent float64) {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("Sampling:\n  CounterRate: %d\n  PercentRate: %g\n", rate, percent)), 0o600); err != nil {
			t.Error(err)
		}
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

	// Warm up logger and runtime state before taking the descriptor baseline.
	warmConfig, err := NewConfig(
		WithAppName("watcher-warmup"),
		WithAgentId("watcher-warmup"),
		WithConfigFile(path),
	)
	require.NoError(t, err)
	warmConfig.offGrpc = true
	warmAgent, err := NewAgent(warmConfig)
	require.NoError(t, err)
	warmDone := requireWatcher(t, warmConfig)
	warmAgent.Shutdown()
	requireWatcherDone(t, warmDone)

	baselineFDs, canCountFDs := watcherFDCount()
	for i := 0; i < 10; i++ {
		config, err := NewConfig(
			WithAppName("watcher-lifecycle"),
			WithAgentId(fmt.Sprintf("watcher-%d", i)),
			WithConfigFile(path),
		)
		require.NoError(t, err)
		config.offGrpc = true
		done := requireWatcher(t, config)
		if canCountFDs {
			// Asserted against every iteration, not just the last: it catches
			// accumulation as it happens, and a count that stayed at the
			// baseline with a watcher live would mean the counter had stopped
			// seeing watchers and the check below had quietly become a no-op.
			fds, _ := watcherFDCount()
			require.Equal(t, baselineFDs+1, fds, "live config watcher descriptor was not counted")
		}

		agent, err := NewAgent(config)
		require.NoError(t, err)
		agent.Shutdown()
		requireWatcherDone(t, done)
		require.Nil(t, configWatcherDone(config))
	}

	if canCountFDs {
		// Close releases the descriptors before it returns, so this needs no
		// settling window.
		fds, _ := watcherFDCount()
		require.Equal(t, baselineFDs, fds, "config watcher file descriptors accumulated")
	}
}

func TestConfigWatcherRestartsWhenConfigIsReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(
		WithAppName("watcher-reuse"),
		WithAgentId("watcher-reuse"),
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
	require.Equal(t, 2, callbacks, "reload callbacks accumulated across agent lifetimes")

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

	config, err := NewConfig(WithConfigFile(path))
	require.NoError(t, err)
	t.Cleanup(config.Close)
	failedDone := requireWatcher(t, config)

	_, err = NewAgent(config)
	require.Error(t, err)
	requireWatcherDone(t, failedDone)

	config.Set(CfgAppName, "watcher-retry")
	config.Set(CfgAgentID, "watcher-retry")
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

	newConfig := func(id string) *Config {
		config, err := NewConfig(
			WithAppName("watcher-reject"),
			WithAgentId(id),
			WithConfigFile(path),
		)
		require.NoError(t, err)
		config.offGrpc = true
		t.Cleanup(config.Close)
		return config
	}

	runningConfig := newConfig("watcher-running")
	running, err := NewAgent(runningConfig)
	require.NoError(t, err)
	t.Cleanup(running.Shutdown)
	runningDone := requireWatcher(t, runningConfig)

	rejectedConfig := newConfig("watcher-rejected")
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

// Run with -race. File events reload immutable snapshots while the same Config
// repeatedly transfers between agent lifetimes and concurrent readers.
func TestConfigWatcherReloadShutdownRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(
		WithAppName("watcher-race"),
		WithAgentId("watcher-race"),
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 2; ; i++ {
			select {
			case <-stop:
				return
			default:
				writeConfigRate(t, path, i%10+1)
				time.Sleep(time.Millisecond)
			}
		}
	}()

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
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
		}()
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
