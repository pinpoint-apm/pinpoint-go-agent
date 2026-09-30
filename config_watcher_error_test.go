package pinpoint

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

// The errors fsnotify reports are transient - an inotify queue overflow while
// the watched directory is busy - so the watcher logs one and keeps reloading.
// Returning on it ended dynamic reload for the rest of the process.
func TestConfigWatcherSurvivesAWatcherError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeConfigRate(t, path, 1)

	config, err := NewConfig(WithAppName("watcher-error"), WithConfigFile(path))
	require.NoError(t, err)
	done := requireWatcher(t, config)
	t.Cleanup(config.Close)

	config.watchMu.Lock()
	watcher := config.watcher
	config.watchMu.Unlock()
	watcher.Errors <- fsnotify.ErrEventOverflow

	writeConfigRate(t, path, 2)
	require.Eventually(t, func() bool {
		return config.Int(CfgSamplingCounterRate) == 2
	}, 2*time.Second, 10*time.Millisecond, "the file change after a watcher error was not reloaded")

	select {
	case <-done:
		t.Fatal("the watcher goroutine exited on a transient error")
	default:
	}
}
