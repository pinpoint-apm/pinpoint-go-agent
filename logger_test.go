package pinpoint

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/elastic/lumberjack"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type trackingWriteCloser struct {
	closed bool
}

func (w *trackingWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *trackingWriteCloser) Close() error {
	w.closed = true
	return nil
}

func Test_SetOutputClosesPreviousFileLogger(t *testing.T) {
	l := newLogger()
	previous := &trackingWriteCloser{}
	l.defaultLogger.SetOutput(previous)
	l.fileLogger = previous

	l.setOutput("stderr", 10, 1)

	require.True(t, previous.closed, "previous file logger was not closed")
	require.Nil(t, l.fileLogger, "file logger reference was not cleared")
}

func logRotationWorkers() int {
	for size := 64 << 10; ; size *= 2 {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte(".(*Logger).millRun("))
		}
	}
}

func Test_SetOutputStopsPreviousRotationWorkers(t *testing.T) {
	before := logRotationWorkers()
	l := newLogger()
	t.Cleanup(func() { l.setOutput("stderr", 10, 1) })
	dir := t.TempDir()
	for i := 0; i < 6; i++ {
		path := filepath.Join(dir, fmt.Sprintf("agent-%d.log", i))
		for _, settings := range [][2]int{{1, 1}, {2, 1}, {2, 2}} {
			l.setOutput(path, settings[0], settings[1])
			l.defaultLogger.Info("start rotation worker")
		}
		l.setOutput("stderr", 10, 1)
	}

	require.Eventually(t, func() bool { return logRotationWorkers() == before },
		time.Second, 10*time.Millisecond, "closed outputs retained log rotation workers")
}

func Test_SetOutputWhileLogging(t *testing.T) {
	l := newLogger()
	t.Cleanup(func() { l.setOutput("stderr", 10, 1) })
	dir := t.TempDir()
	l.setOutput(filepath.Join(dir, "agent-0.log"), 1, 1)
	const lines = 500
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < lines; i++ {
			l.defaultLogger.Infof("reload-race-line %d", i)
		}
	})
	for i := 0; i < 100; i++ {
		l.setOutput(filepath.Join(dir, fmt.Sprintf("agent-%d.log", i%3)), 1+i%2, 1+i%3)
	}
	wg.Wait()
	l.setOutput("stderr", 10, 1)

	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	got := 0
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		require.NoError(t, err)
		got += bytes.Count(data, []byte("reload-race-line"))
	}
	assert.Equal(t, lines, got, "output switches must not lose or duplicate log lines")
}

func Test_FileOutputRotationAndRetentionAfterReload(t *testing.T) {
	l := newLogger()
	t.Cleanup(func() { l.setOutput("stderr", 10, 1) })
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	oldest := filepath.Join(dir, "agent-2000-01-01T00-00-00.000.log")
	latest := filepath.Join(dir, "agent-2001-01-01T00-00-00.000.log")
	unrelated := filepath.Join(dir, "keep.log")
	for _, file := range []string{oldest, latest, unrelated} {
		require.NoError(t, os.WriteFile(file, []byte("keep until rotated"), 0o600))
	}
	original := bytes.Repeat([]byte("x"), 1<<20)
	require.NoError(t, os.WriteFile(path, original, 0o600))
	l.setOutput(path, 1, 2)
	l.defaultLogger.Info("after rotation")
	require.Eventually(t, func() bool {
		_, err := os.Stat(oldest)
		return os.IsNotExist(err)
	}, time.Second, 10*time.Millisecond)
	require.FileExists(t, latest, "existing lumberjack backups must count toward retention")
	backups, err := filepath.Glob(filepath.Join(dir, "agent-*.log"))
	require.NoError(t, err)
	require.Len(t, backups, 2)
	rotated, err := os.ReadFile(backups[1])
	require.NoError(t, err)
	assert.Equal(t, original, rotated, "rotation must preserve the previous file contents")

	l.setOutput(path, 2, 1)
	l.defaultLogger.Info("after reload")
	l.setOutput("stderr", 10, 1)
	require.Eventually(t, func() bool {
		_, err := os.Stat(latest)
		return os.IsNotExist(err)
	}, time.Second, 10*time.Millisecond)
	require.FileExists(t, unrelated, "retention must leave unrelated files alone")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "after rotation")
	assert.Contains(t, string(data), "after reload")
}

func Test_SetupClosesFileLoggerFromPreviousAgent(t *testing.T) {
	l := newLogger()
	previous := &trackingWriteCloser{}
	l.defaultLogger.SetOutput(previous)
	l.fileLogger = previous

	config, err := NewConfig()
	require.NoError(t, err)
	config.Set(CfgLogOutput, "stderr")
	l.setup(config)

	require.True(t, previous.closed, "file logger from previous agent was not closed")
	require.Nil(t, l.fileLogger, "file logger reference from previous agent was not cleared")
}

func Test_IsLogLevelEnabledChecksExtraLogger(t *testing.T) {
	oldDefaultLevel := logger.defaultLogger.GetLevel()
	oldExtraLogger := logger.extraLogger.Load()
	t.Cleanup(func() {
		logger.defaultLogger.SetLevel(oldDefaultLevel)
		logger.extraLogger.Store(oldExtraLogger)
	})

	logger.defaultLogger.SetLevel(logrus.InfoLevel)
	logger.extraLogger.Store(nil)
	require.False(t, isDebugLogLevelEnabled(), "debug should be disabled when default logger is info and extra logger is nil")

	extraLogger := logrus.New()
	extraLogger.SetLevel(logrus.TraceLevel)
	SetExtraLogger(extraLogger)

	require.True(t, isDebugLogLevelEnabled(), "debug should be enabled when extra logger is trace")
	require.True(t, isTraceLogLevelEnabled(), "trace should be enabled when extra logger is trace")
}

// SetExtraLogger can be called while other goroutines are logging. Run under
// -race.
func Test_SetExtraLoggerIsRaceFree(t *testing.T) {
	oldExtraLogger := logger.extraLogger.Load()
	t.Cleanup(func() { logger.extraLogger.Store(oldExtraLogger) })

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 200; i++ {
			isDebugLogLevelEnabled()
			Log("test").Debugf("line %d", i)
		}
	})

	for i := 0; i < 200; i++ {
		SetExtraLogger(logrus.New())
	}
	wg.Wait()
}

// logrus' TextFormatter decides "is the output a terminal?" once, on its
// first Format call, and latches the answer. Anything logged through the global
// logger before setOutput switches to a file (NewConfig warnings, the "log
// output" line itself) latches it against the original stderr. Simulate the
// terminal latch with ForceColors, then switch to a file.
func Test_FileOutputHasNoAnsiColors(t *testing.T) {
	oldFormatter := logger.defaultLogger.Formatter
	oldOutput := logger.defaultLogger.Out
	t.Cleanup(func() {
		logger.setOutput("stderr", 10, 1)
		logger.defaultLogger.Formatter = oldFormatter
		logger.defaultLogger.SetOutput(oldOutput)
	})

	logger.defaultLogger.Formatter = &logrus.TextFormatter{ForceColors: true}
	Log("test").Infof("latch")

	path := filepath.Join(t.TempDir(), "pinpoint.log")
	logger.setOutput(path, 10, 1)
	Log("test").Infof("hello")

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "\x1b[", "file log contains ANSI escape")
	assert.Contains(t, string(b), "hello", "file log missing message")
	assert.Contains(t, string(b), "log output: "+path, "file log missing the output-switch line")
}

// Reloading Log.Output back and forth must give each destination its own
// formatter decision: colors on a forced-color terminal, none in the file.
func Test_OutputSwitchReformatsEachTime(t *testing.T) {
	oldFormatter := logger.defaultLogger.Formatter
	oldOutput := logger.defaultLogger.Out
	t.Cleanup(func() {
		logger.setOutput("stderr", 10, 1)
		logger.defaultLogger.Formatter = oldFormatter
		logger.defaultLogger.SetOutput(oldOutput)
	})

	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		logger.defaultLogger.Formatter = &logrus.TextFormatter{ForceColors: true}
		Log("test").Infof("colored")

		path := filepath.Join(dir, "pinpoint.log")
		logger.setOutput(path, 10, 1)
		Log("test").Infof("plain")
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NotContains(t, string(b), "\x1b[", "round %d: file log contains ANSI escape", i)
		logger.setOutput("stdout", 10, 1)
		os.Remove(path)
	}
}

// Reusing an entry must continue writing to both the default and extra loggers.
func Test_ReusedEntryWritesToBothLoggers(t *testing.T) {
	oldOutput := logger.defaultLogger.Out
	oldLevel := logger.defaultLogger.GetLevel()
	oldExtraLogger := logger.extraLogger.Load()
	t.Cleanup(func() {
		logger.defaultLogger.SetOutput(oldOutput)
		logger.defaultLogger.SetLevel(oldLevel)
		logger.extraLogger.Store(oldExtraLogger)
	})

	var defaultOut, extraOut bytes.Buffer
	logger.defaultLogger.SetOutput(&defaultOut)
	logger.defaultLogger.SetLevel(logrus.InfoLevel)

	extraLogger := logrus.New()
	extraLogger.SetOutput(&extraOut)
	extraLogger.SetLevel(logrus.InfoLevel)
	SetExtraLogger(extraLogger)

	e := Log("test")
	e.Infof("first")
	e.Warnf("second")

	for _, out := range []struct {
		name string
		buf  *bytes.Buffer
	}{{"default", &defaultOut}, {"extra", &extraOut}} {
		s := out.buf.String()
		assert.Contains(t, s, "first", "%s logger missing lines", out.name)
		assert.Contains(t, s, "second", "%s logger missing lines", out.name)
		assert.Contains(t, s, "src=test", "%s logger missing fields", out.name)
	}
}

// Log.MaxBackups is dynamic: a reload must reach the lumberjack logger, which
// only happens when the key is in the AddReloadCallback list in NewAgent.
func Test_ReloadAppliesLogMaxBackups(t *testing.T) {
	oldOutput := logger.defaultLogger.Out
	t.Cleanup(func() {
		logger.setOutput("stderr", 10, 1)
		logger.defaultLogger.SetOutput(oldOutput)
	})

	dir := t.TempDir()
	logPath := filepath.Join(dir, "pinpoint.log")
	cfgPath := filepath.Join(dir, "pinpoint-config.yaml")
	write := func(backups int) {
		body := fmt.Sprintf("Log:\n  Output: %s\n  MaxBackups: %d\n", logPath, backups)
		require.NoError(t, os.WriteFile(cfgPath, []byte(body), 0o600))
	}
	write(2)

	config, err := NewConfig(WithAppName("log-reload"), WithServiceName("test-service"), WithConfigFile(cfgPath))
	require.NoError(t, err)
	config.offGrpc = true
	a, err := NewAgent(config)
	require.NoError(t, err)
	t.Cleanup(a.Shutdown)
	requireWatcher(t, config)

	backups := func() int {
		logger.outputMu.Lock()
		defer logger.outputMu.Unlock()
		lj, ok := logger.fileLogger.(*lumberjack.Logger)
		if !ok {
			return -1
		}
		return lj.MaxBackups
	}
	require.Equal(t, 2, backups())

	write(3)
	require.Eventually(t, func() bool { return backups() == 3 },
		2*time.Second, 10*time.Millisecond, "Log.MaxBackups reload did not reach the file logger")
}

// ===========================================================================
// Locked invariants - behaviour pinned against the Java and C++ agents. The
// cross-agent rationale and references live in doc/development.md.
// ===========================================================================

// Test_UnsupportedLogLevelKeepsTheCurrentLevel locks that an
// unsupported level leaves the current level unchanged and logs the error.
// Both "warn" and "warning" are accepted.
func Test_UnsupportedLogLevelKeepsTheCurrentLevel(t *testing.T) {
	l := newLogger()

	l.setLevel("error")
	assert.Equal(t, logrus.ErrorLevel, l.defaultLogger.GetLevel())

	for _, unsupported := range []string{"warnign", "", "WARN ", "2", "verbose"} {
		l.setLevel(unsupported)
		assert.Equal(t, logrus.ErrorLevel, l.defaultLogger.GetLevel(),
			"%q must leave the level where it was", unsupported)
	}

	// Both spellings of warn are accepted, and each is a real level change.
	for _, name := range []string{"warn", "warning"} {
		l.setLevel("error")
		l.setLevel(name)
		assert.Equal(t, logrus.WarnLevel, l.defaultLogger.GetLevel(), "%q is accepted", name)
	}
}

// Tracef writes at Trace level only: it reaches a logger at Trace and is
// dropped by one at Debug.
func Test_TracefLogsAtTraceLevelOnly(t *testing.T) {
	var buf bytes.Buffer
	restore := captureLogAt(&buf, logrus.DebugLevel)
	Log("test").Tracef("dropped %d", 1)
	restore()
	assert.NotContains(t, buf.String(), "dropped")

	restore = captureLogAt(&buf, logrus.TraceLevel)
	defer restore()
	Log("test").Tracef("kept %d", 2)
	assert.Contains(t, buf.String(), "kept 2")
	assert.Contains(t, buf.String(), "level=trace")
}

// setOutput is setOutputLocked under outputMu; tests only.
func (l *logrusLogger) setOutput(out string, maxSize, maxBackups int) {
	l.outputMu.Lock()
	defer l.outputMu.Unlock()
	l.setOutputLocked(out, maxSize, maxBackups)
}
