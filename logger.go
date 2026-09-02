package pinpoint

import (
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var logger *logrusLogger

func initLogger() {
	logger = newLogger()
}

func Log(src string) *logEntry {
	return logger.newEntry(src)
}

func IsLogLevelEnabled(level logrus.Level) bool {
	if logger.defaultLogger.GetLevel() >= level {
		return true
	}
	extra := logger.extra()
	return extra != nil && extra.GetLevel() >= level
}

func IsDebugLogLevelEnabled() bool {
	return IsLogLevelEnabled(logrus.DebugLevel)
}

func IsTraceLogLevelEnabled() bool {
	return IsLogLevelEnabled(logrus.TraceLevel)
}

// SetExtraLogger installs an additional logger every pinpoint log line is
// also written to. It may be called while other goroutines are logging, so the
// logger is held atomically rather than as a plain field.
func SetExtraLogger(lgr *logrus.Logger) {
	logger.extraLogger.Store(lgr)
}

type logrusLogger struct {
	defaultLogger *logrus.Logger
	extraLogger   atomic.Pointer[logrus.Logger]
	outputMu      sync.Mutex
	fileLogger    io.WriteCloser
	config        *Config
}

func (l *logrusLogger) extra() *logrus.Logger {
	return l.extraLogger.Load()
}

func newLogger() *logrusLogger {
	l := logrus.New()
	l.Formatter = &logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05.000000",
		FullTimestamp:   true,
		ForceColors:     true,
	}
	return &logrusLogger{defaultLogger: l}
}

func (l *logrusLogger) setLevel(level string) {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		Log("config").Errorf("invalid log level: %s", level)
		lvl = logrus.InfoLevel
	}

	l.defaultLogger.SetLevel(lvl)
	reportCaller := false
	if lvl > logrus.InfoLevel {
		reportCaller = true
	}
	l.defaultLogger.SetReportCaller(reportCaller)
}

func (l *logrusLogger) setOutput(out string, maxSize int) {
	l.outputMu.Lock()
	defer l.outputMu.Unlock()
	l.setOutputLocked(out, maxSize)
}

func (l *logrusLogger) setOutputLocked(out string, maxSize int) {
	Log("config").Infof("log output: %s", out)

	var output io.Writer
	var fileLogger io.WriteCloser
	if strings.EqualFold(out, "stdout") {
		output = os.Stdout
	} else if strings.EqualFold(out, "stderr") {
		output = os.Stderr
	} else {
		fileLogger = &lumberjack.Logger{
			Filename:   out,
			MaxSize:    maxSize,
			MaxBackups: 1,
			MaxAge:     30,
			Compress:   false,
		}
		output = fileLogger
	}

	previous := l.fileLogger
	l.defaultLogger.SetOutput(output)
	l.fileLogger = fileLogger
	if previous != nil {
		_ = previous.Close()
	}
}

func (l *logrusLogger) setup(config *Config) {
	l.outputMu.Lock()
	defer l.outputMu.Unlock()

	l.config = config
	l.setLevel(config.String(CfgLogLevel))
	l.setOutputLocked(config.String(CfgLogOutput), config.Int(CfgLogMaxSize))
}

func (l *logrusLogger) reloadLevel(config *Config) {
	l.outputMu.Lock()
	defer l.outputMu.Unlock()
	if l.config == config {
		l.setLevel(config.String(CfgLogLevel))
	}
}

func (l *logrusLogger) reloadOutput(config *Config) {
	l.outputMu.Lock()
	defer l.outputMu.Unlock()
	if l.config == config {
		l.setOutputLocked(config.String(CfgLogOutput), config.Int(CfgLogMaxSize))
	}
}

func (l *logrusLogger) newEntry(src string) *logEntry {
	return &logEntry{
		entry:       logrus.NewEntry(l.defaultLogger).WithFields(logrus.Fields{"module": "pinpoint", "src": src}),
		extraLogger: l.extra(),
	}
}

type logEntry struct {
	entry       *logrus.Entry
	extraLogger *logrus.Logger
}

func (l *logEntry) log(logFunc func(string, ...interface{}), format string, args ...interface{}) {
	logFunc(format, args...)
	if l.extraLogger != nil {
		l.entry.Logger = l.extraLogger
		logFunc(format, args...)
	}
}

func (l *logEntry) Errorf(format string, args ...interface{}) {
	l.log(l.entry.Errorf, format, args...)
}

func (l *logEntry) Warnf(format string, args ...interface{}) {
	l.log(l.entry.Warnf, format, args...)
}

func (l *logEntry) Infof(format string, args ...interface{}) {
	l.log(l.entry.Infof, format, args...)
}

func (l *logEntry) Debugf(format string, args ...interface{}) {
	l.log(l.entry.Debugf, format, args...)
}

func (l *logEntry) Tracef(format string, args ...interface{}) {
	l.log(l.entry.Tracef, format, args...)
}
