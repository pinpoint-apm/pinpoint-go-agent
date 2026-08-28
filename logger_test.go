package pinpoint

import (
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
)

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
