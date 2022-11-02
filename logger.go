package pinpoint

import (
	"github.com/sirupsen/logrus"
	prefixed "github.com/x-cray/logrus-prefixed-formatter"
	"strings"
)

var logger Logger

func initLogger() {
	l := logrus.New()
	formatter := new(prefixed.TextFormatter)
	formatter.TimestampFormat = "2006-01-02 15:04:05.000000"
	formatter.FullTimestamp = true
	formatter.ForceFormatting = true
	formatter.ForceColors = true
	l.Formatter = formatter
	SetLogger(&logrusLogger{l})
}

func Log(src string) LogEntry {
	return logger.NewEntry(src)
}

const (
	ErrorLevel uint32 = iota
	WarnLevel
	InfoLevel
	DebugLevel
	TraceLevel
)

func parseLogLevel(level string) uint32 {
	switch strings.ToLower(level) {
	case "error":
		return ErrorLevel
	case "warn", "warning":
		return WarnLevel
	case "info":
		return InfoLevel
	case "debug":
		return DebugLevel
	case "trace":
		return TraceLevel
	default:
		Log("config").Errorf("invalid Log level: %s", level)
		return InfoLevel
	}
}

type Logger interface {
	SetLevel(level string)
	NewEntry(src string) LogEntry
}

type LogEntry interface {
	Errorf(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Debugf(format string, args ...interface{})
	Tracef(format string, args ...interface{})
}

func SetLogger(l Logger) {
	logger = l
}

type logrusLogger struct {
	logger *logrus.Logger
}

func (l *logrusLogger) SetLevel(level string) {
	lvl := parseLogLevel(level)
	l.logger.SetLevel(logrus.Level(lvl + 2))
	if lvl > InfoLevel {
		l.logger.SetReportCaller(true)
	}
}

func (l *logrusLogger) NewEntry(src string) LogEntry {
	entry := l.logger.WithFields(logrus.Fields{"module": "pinpoint", "src": src})
	return &logrusEntry{entry: entry}
}

type logrusEntry struct {
	entry *logrus.Entry
}

func (l *logrusEntry) Errorf(format string, args ...interface{}) {
	l.entry.Errorf(format, args...)
}

func (l *logrusEntry) Warnf(format string, args ...interface{}) {
	l.entry.Warnf(format, args...)
}

func (l *logrusEntry) Infof(format string, args ...interface{}) {
	l.entry.Infof(format, args...)
}

func (l *logrusEntry) Debugf(format string, args ...interface{}) {
	l.entry.Debugf(format, args...)
}

func (l *logrusEntry) Tracef(format string, args ...interface{}) {
	l.entry.Tracef(format, args...)
}
