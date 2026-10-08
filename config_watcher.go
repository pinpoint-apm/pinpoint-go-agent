package pinpoint

import (
	"os"
	"time"
)

// configPollInterval is how often the config file is checked for a change. A
// variable so tests can shorten it.
var configPollInterval = time.Second

// Close stops the config file poller and waits for its goroutine to exit. It
// is safe to call more than once; a later NewAgent with the same Config starts
// the poller again.
//
// Close must not be called from a reload callback: callbacks run on the very
// goroutine Close waits for, so a callback that closes its own Config
// deadlocks and leaves the watcher lock held.
func (config *Config) Close() {
	config.watchMu.Lock()
	defer config.watchMu.Unlock()

	if config.watcherStop == nil {
		return
	}

	// The wait is for the goroutine, which may still be running a reload
	// callback. Callbacks are caller-supplied (see AddReloadCallback) and Close
	// is on the Shutdown path, so bound the wait the same way the worker drain
	// is bounded - a slow callback must not keep the process alive. The
	// abandoned goroutine only has to finish the callback in flight before it
	// sees the stop; it publishes nothing after the timeout, having published
	// before the callbacks ran.
	close(config.watcherStop)
	timer := time.NewTimer(shutdownTimeout)
	select {
	case <-config.watcherDone:
	case <-timer.C:
		Log("config").Warnf("config file watcher shutdown timeout(%v) exceeded, abandon reload in progress", shutdownTimeout)
	}
	timer.Stop()

	config.watcherStop = nil
	config.watcherDone = nil
}

func (config *Config) startConfigWatcher() bool {
	config.watchMu.Lock()
	defer config.watchMu.Unlock()

	// The poller returns only once Close stops it, and Close clears both
	// channels, so a set watcherStop is a poller still running.
	if config.configFileCfg == nil || config.watcherStop != nil {
		return false
	}

	stop, done := make(chan struct{}), make(chan struct{})
	config.watcherStop = stop
	config.watcherDone = done
	// The baseline is taken here, not in the poller: a rewrite between this
	// return and the goroutine's first stat would otherwise become the
	// baseline and never be reloaded.
	last, _ := configFileStamp(config.configFile)
	go config.pollConfigFile(stop, done, config.configFile, config.configFileCfg, last)
	return true
}

// pollConfigFile reloads the config file whenever its modification time or
// size changes, checked every configPollInterval. A poll rather than a file
// system watch: it needs no platform code and no dependency, a rename or an
// unlink+rewrite save (editors, deploy tools, a Kubernetes ConfigMap symlink
// swap) is just a file whose stamp changed, and a config change taking up to
// one interval to apply costs nothing.
//
// ponytail: mtime+size, so a same-size rewrite inside the file system's
// timestamp granularity is missed; hash the contents if that ever bites.
func (config *Config) pollConfigFile(stop, done chan struct{}, configFile string, f *configFile, last fileStamp) {
	defer close(done)

	ticker := time.NewTicker(configPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		// A file that is missing right now - an unlink+rewrite saver between
		// its two steps - keeps the old stamp, so the rewrite is seen as a
		// change on a later tick.
		stamp, ok := configFileStamp(configFile)
		if !ok || stamp.same(last) {
			continue
		}
		last = stamp
		config.reloadConfig(f)
	}
}

type fileStamp struct {
	modTime time.Time
	size    int64
}

func (s fileStamp) same(o fileStamp) bool {
	return s.size == o.size && s.modTime.Equal(o.modTime)
}

// configFileStamp identifies the file's current contents. os.Stat follows a
// symlink, so a link that now points at another file reports that file.
func configFileStamp(path string) (fileStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{fi.ModTime(), fi.Size()}, true
}
