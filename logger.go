package pinpoint

import (
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	prefixed "github.com/x-cray/logrus-prefixed-formatter"
	"gopkg.in/natefinch/lumberjack.v2"
)

var logger *logrusLogger

func initLogger() {
	logger = newLogger()
}

func Log(src string) *logEntry {
	return logger.NewEntry(src)
}

func AddLogger(l *logrus.Logger) {
	logger.secondLogger = l
}

type logrusLogger struct {
	defaultLogger *logrus.Logger
	secondLogger  *logrus.Logger
}

func newLogger() *logrusLogger {
	l := logrus.New()
	formatter := new(prefixed.TextFormatter)
	formatter.TimestampFormat = "2006-01-02 15:04:05.000000"
	formatter.FullTimestamp = true
	formatter.ForceFormatting = true
	formatter.ForceColors = true
	l.Formatter = formatter
	return &logrusLogger{defaultLogger: l}
}

func (l *logrusLogger) SetLevel(level string) {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		Log("config").Errorf("invalid log level: %s", level)
		lvl = logrus.InfoLevel
	}

	l.defaultLogger.SetLevel(lvl)
	if lvl > logrus.InfoLevel {
		l.defaultLogger.SetReportCaller(true)
	}
}

func (l *logrusLogger) SetOutput(out string, maxSize int) {
	Log("config").Infof("log output: %s", out)

	if strings.EqualFold(out, "stdout") {
		l.defaultLogger.SetOutput(os.Stdout)
	} else if strings.EqualFold(out, "stderr") {
		l.defaultLogger.SetOutput(os.Stderr)
	} else {
		l.defaultLogger.SetOutput(&lumberjack.Logger{
			Filename:   out,
			MaxSize:    maxSize,
			MaxBackups: 1,
			MaxAge:     30,
			Compress:   false,
		})
	}
}

func (l *logrusLogger) NewEntry(src string) *logEntry {
	var e1, e2 *logrus.Entry
	f := logrus.Fields{"module": "pinpoint", "src": src}
	e1 = l.defaultLogger.WithFields(f)
	if l.secondLogger != nil {
		e2 = l.secondLogger.WithFields(f)
	}
	return &logEntry{defaultEntry: e1, secondEntry: e2}
}

type logEntry struct {
	defaultEntry *logrus.Entry
	secondEntry  *logrus.Entry
}

func (l *logEntry) Errorf(format string, args ...interface{}) {
	l.defaultEntry.Errorf(format, args...)
	if l.secondEntry != nil {
		l.secondEntry.Errorf(format, args...)
	}
}

func (l *logEntry) Warnf(format string, args ...interface{}) {
	l.defaultEntry.Warnf(format, args...)
	if l.secondEntry != nil {
		l.secondEntry.Warnf(format, args...)
	}
}

func (l *logEntry) Infof(format string, args ...interface{}) {
	l.defaultEntry.Infof(format, args...)
	if l.secondEntry != nil {
		l.secondEntry.Infof(format, args...)
	}
}

func (l *logEntry) Debugf(format string, args ...interface{}) {
	l.defaultEntry.Debugf(format, args...)
	if l.secondEntry != nil {
		l.secondEntry.Debugf(format, args...)
	}
}

func (l *logEntry) Tracef(format string, args ...interface{}) {
	l.defaultEntry.Tracef(format, args...)
	if l.secondEntry != nil {
		l.secondEntry.Tracef(format, args...)
	}
}
