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

// Log returns a log entry for src. The entry is a value carrying only the
// source: the logrus entry behind it is built in log, after the level check,
// so a call whose level is disabled - every Debugf and Tracef site at the
// default level - allocates nothing. Built eagerly, each call cost two logrus
// entries and a Fields map (six allocations) for a line that was then dropped.
//
// Before this package's init function ran (a compile-time instrumentation
// hook reached from another package's init) the entry discards what is
// logged: logrus may not be initialized either.
func Log(src string) logEntry {
	return logEntry{src: src}
}

func IsLogLevelEnabled(level logrus.Level) bool {
	if !initDone {
		return false
	}
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
	out           string
	maxSize       int
	maxBackups    int
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
	// An unknown level is refused, not defaulted: defaulting to info turned a
	// typo in a reloaded config file into more log output on a host
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

func (l *logrusLogger) setOutput(out string, maxSize, maxBackups int) {
	l.outputMu.Lock()
	defer l.outputMu.Unlock()
	l.setOutputLocked(out, maxSize, maxBackups)
}

func (l *logrusLogger) setOutputLocked(out string, maxSize, maxBackups int) {
	// The output is applied up to three times on the way to a running agent
	// (twice while NewConfig loads, once by setup), so an unchanged one is
	// skipped rather than reopening the file.
	if out == l.out && maxSize == l.maxSize && maxBackups == l.maxBackups {
		return
	}
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
			MaxBackups: maxBackups,
			// (Log.MaxBackups is its only retention key), so a Go-only key
			// would be one more thing the ports disagree on. MaxBackups
			// already bounds the disk footprint to MaxSize x (MaxBackups+1).
			// No MaxAge: a hard-coded 30 days deleted backups before
			// MaxBackups was reached on a low-traffic process, so the
			// configured generation count was not kept.
			Compress: false,
		}
		output = fileLogger
	}

	previous := l.fileLogger
	l.defaultLogger.SetOutput(output)
	l.defaultLogger.SetFormatter(newTextFormatter())
	l.fileLogger = fileLogger
	l.out, l.maxSize, l.maxBackups = out, maxSize, maxBackups
	if previous != nil {
		_ = previous.Close()
	}
	Log("config").Infof("log output: %s", out)
}

// apply installs the logging options of a Config that is still being loaded,
// so the rest of the load reports where the operator asked rather than on
// stderr. It does not bind the logger to that Config: setup does, once the
// Config is final, and the reload callbacks act only for the bound Config. A
// Config built while an agent runs may never become an agent's, so the
// running agent keeps its logging.
func (l *logrusLogger) apply(level, out string, maxSize, maxBackups int) {
	if GetAgent() != NoopAgent() {
		return
	}
	l.outputMu.Lock()
	defer l.outputMu.Unlock()
	l.setLevel(level)
	l.setOutputLocked(out, maxSize, maxBackups)
}

func (l *logrusLogger) setup(config *Config) {
	l.outputMu.Lock()
	defer l.outputMu.Unlock()

	l.config = config
	l.setLevel(config.String(CfgLogLevel))
	l.setOutputLocked(config.String(CfgLogOutput), config.Int(CfgLogMaxSize), config.Int(CfgLogMaxBackups))
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
		l.setOutputLocked(config.String(CfgLogOutput), config.Int(CfgLogMaxSize), config.Int(CfgLogMaxBackups))
	}
}

func (l *logrusLogger) newEntry(src string) *logrus.Entry {
	return logrus.NewEntry(l.defaultLogger).WithFields(logrus.Fields{"module": "pinpoint", "src": src})
}

type logEntry struct {
	src string
}

// log writes the line to the default logger and, when one is installed, to the
// extra logger as well. Nothing is built until a logger wants the level:
// IsLogLevelEnabled covers both, and each Logf below re-checks its own. The
// extra write goes through a copy of the entry so its Logger can point at the
// extra logger while the original keeps the default one.
func (l logEntry) log(level logrus.Level, format string, args ...interface{}) {
	if !IsLogLevelEnabled(level) { // also false before init: discard
		return
	}
	entry := logger.newEntry(l.src)
	entry.Logf(level, format, args...)
	if extraLogger := logger.extra(); extraLogger != nil {
		extra := entry.Dup()
		extra.Logger = extraLogger
		extra.Logf(level, format, args...)
	}
}

func (l logEntry) Errorf(format string, args ...interface{}) {
	l.log(logrus.ErrorLevel, format, args...)
}

func (l logEntry) Warnf(format string, args ...interface{}) {
	l.log(logrus.WarnLevel, format, args...)
}

func (l logEntry) Infof(format string, args ...interface{}) {
	l.log(logrus.InfoLevel, format, args...)
}

func (l logEntry) Debugf(format string, args ...interface{}) {
	l.log(logrus.DebugLevel, format, args...)
}

func (l logEntry) Tracef(format string, args ...interface{}) {
	l.log(logrus.TraceLevel, format, args...)
}
