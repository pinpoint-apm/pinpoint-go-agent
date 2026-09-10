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
	l.Formatter = newTextFormatter()
	return &logrusLogger{defaultLogger: l}
}

// newTextFormatter returns a fresh formatter. logrus' TextFormatter decides
// whether its output is a terminal once, on the first Format call, and latches
// the answer in terminalInitOnce. Every output switch therefore installs a new
// formatter so the decision is re-made against the new writer: colors on a
// terminal, none in a lumberjack file.
func newTextFormatter() logrus.Formatter {
	return &logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05.000000",
		FullTimestamp:   true,
	}
}

func (l *logrusLogger) setLevel(level string) {
	// An unknown level keeps the current one, as the C++ agent does
	// (Logger::setLogLevel in src/logging.cpp). Resetting to info instead
	// turned a typo in a reloaded config file into more log output on a host
	// that had just lowered the level to get less. publish already rejects
	// such a value, so this is the guard for a caller that bypasses Config.
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		Log("config").Errorf("invalid log level: %s, keeping the current level", level)
		return
	}

	// No SetReportCaller: every line goes through logEntry.log, so logrus would
	// always report logger.go as the caller. The src field names the source.
	l.defaultLogger.SetLevel(lvl)
}

func (l *logrusLogger) setOutput(out string, maxSize int) {
	l.outputMu.Lock()
	defer l.outputMu.Unlock()
	l.setOutputLocked(out, maxSize)
}

func (l *logrusLogger) setOutputLocked(out string, maxSize int) {
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
	l.defaultLogger.SetFormatter(newTextFormatter())
	l.fileLogger = fileLogger
	if previous != nil {
		_ = previous.Close()
	}
	l.newEntry("config").Infof("log output: %s", out)
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

// log writes the line to the default logger and, when one is installed, to the
// extra logger as well. The extra write goes through a copy of the entry: the
// entry may be reused for later calls, so its Logger must keep pointing at the
// default logger.
func (l *logEntry) log(level logrus.Level, format string, args ...interface{}) {
	l.entry.Logf(level, format, args...)
	if l.extraLogger != nil {
		extra := l.entry.Dup()
		extra.Logger = l.extraLogger
		extra.Logf(level, format, args...)
	}
}

func (l *logEntry) Errorf(format string, args ...interface{}) {
	l.log(logrus.ErrorLevel, format, args...)
}

func (l *logEntry) Warnf(format string, args ...interface{}) {
	l.log(logrus.WarnLevel, format, args...)
}

func (l *logEntry) Infof(format string, args ...interface{}) {
	l.log(logrus.InfoLevel, format, args...)
}

func (l *logEntry) Debugf(format string, args ...interface{}) {
	l.log(logrus.DebugLevel, format, args...)
}

func (l *logEntry) Tracef(format string, args ...interface{}) {
	l.log(logrus.TraceLevel, format, args...)
}
