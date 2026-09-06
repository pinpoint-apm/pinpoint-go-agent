package pinpoint

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
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

	l.setOutput("stderr", 10)

	if !previous.closed {
		t.Fatal("previous file logger was not closed")
	}
	if l.fileLogger != nil {
		t.Fatal("file logger reference was not cleared")
	}
}

func Test_SetupClosesFileLoggerFromPreviousAgent(t *testing.T) {
	l := newLogger()
	previous := &trackingWriteCloser{}
	l.defaultLogger.SetOutput(previous)
	l.fileLogger = previous

	config, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	config.Set(CfgLogOutput, "stderr")
	l.setup(config)

	if !previous.closed {
		t.Fatal("file logger from previous agent was not closed")
	}
	if l.fileLogger != nil {
		t.Fatal("file logger reference from previous agent was not cleared")
	}
}

func Test_IsLogLevelEnabledChecksExtraLogger(t *testing.T) {
	oldDefaultLevel := logger.defaultLogger.GetLevel()
	oldExtraLogger := logger.extra()
	t.Cleanup(func() {
		logger.defaultLogger.SetLevel(oldDefaultLevel)
		logger.extraLogger.Store(oldExtraLogger)
	})

	logger.defaultLogger.SetLevel(logrus.InfoLevel)
	logger.extraLogger.Store(nil)
	if IsDebugLogLevelEnabled() {
		t.Fatal("debug should be disabled when default logger is info and extra logger is nil")
	}

	extraLogger := logrus.New()
	extraLogger.SetLevel(logrus.TraceLevel)
	SetExtraLogger(extraLogger)

	if !IsDebugLogLevelEnabled() {
		t.Fatal("debug should be enabled when extra logger is trace")
	}
	if !IsTraceLogLevelEnabled() {
		t.Fatal("trace should be enabled when extra logger is trace")
	}
}

// SetExtraLogger can be called while other goroutines are logging. Run under
// -race.
func Test_SetExtraLoggerIsRaceFree(t *testing.T) {
	oldExtraLogger := logger.extra()
	t.Cleanup(func() { logger.extraLogger.Store(oldExtraLogger) })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			IsDebugLogLevelEnabled()
			Log("test").Debugf("line %d", i)
		}
	}()

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
		logger.setOutput("stderr", 10)
		logger.defaultLogger.Formatter = oldFormatter
		logger.defaultLogger.SetOutput(oldOutput)
	})

	logger.defaultLogger.Formatter = &logrus.TextFormatter{ForceColors: true}
	Log("test").Infof("latch")

	path := filepath.Join(t.TempDir(), "pinpoint.log")
	logger.setOutput(path, 10)
	Log("test").Infof("hello")

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "\x1b[") {
		t.Errorf("file log contains ANSI escape: %q", b)
	}
	if !strings.Contains(string(b), "hello") {
		t.Errorf("file log missing message: %q", b)
	}
	if !strings.Contains(string(b), "log output: "+path) {
		t.Errorf("file log missing the output-switch line: %q", b)
	}
}

// Reloading Log.Output back and forth must give each destination its own
// formatter decision: colors on a forced-color terminal, none in the file.
func Test_OutputSwitchReformatsEachTime(t *testing.T) {
	oldFormatter := logger.defaultLogger.Formatter
	oldOutput := logger.defaultLogger.Out
	t.Cleanup(func() {
		logger.setOutput("stderr", 10)
		logger.defaultLogger.Formatter = oldFormatter
		logger.defaultLogger.SetOutput(oldOutput)
	})

	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		logger.defaultLogger.Formatter = &logrus.TextFormatter{ForceColors: true}
		Log("test").Infof("colored")

		path := filepath.Join(dir, "pinpoint.log")
		logger.setOutput(path, 10)
		Log("test").Infof("plain")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "\x1b[") {
			t.Fatalf("round %d: file log contains ANSI escape: %q", i, b)
		}
		logger.setOutput("stdout", 10)
		os.Remove(path)
	}
}
