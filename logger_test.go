package pinpoint

import (
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
