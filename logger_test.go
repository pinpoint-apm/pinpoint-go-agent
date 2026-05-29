package pinpoint

import (
	"testing"

	"github.com/sirupsen/logrus"
)

func Test_IsLogLevelEnabledChecksExtraLogger(t *testing.T) {
	oldDefaultLevel := logger.defaultLogger.GetLevel()
	oldExtraLogger := logger.extraLogger
	t.Cleanup(func() {
		logger.defaultLogger.SetLevel(oldDefaultLevel)
		logger.extraLogger = oldExtraLogger
	})

	logger.defaultLogger.SetLevel(logrus.InfoLevel)
	logger.extraLogger = nil
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
