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
	return logger.newEntry(src)
}

func IsLogLevelEnabled(level logrus.Level) bool {
	return logger.defaultLogger.GetLevel() >= level
}

func SetExtraLogger(lgr *logrus.Logger) {
	logger.extraLogger = lgr
}

type logrusLogger struct {
	defaultLogger *logrus.Logger
	extraLogger   *logrus.Logger
	fileLogger    *lumberjack.Logger
	config        *Config
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
	Log("config").Infof("log output: %s", out)

	if strings.EqualFold(out, "stdout") {
		l.defaultLogger.SetOutput(os.Stdout)
	} else if strings.EqualFold(out, "stderr") {
		l.defaultLogger.SetOutput(os.Stderr)
	} else {
		l.fileLogger = &lumberjack.Logger{
			Filename:   out,
			MaxSize:    maxSize,
			MaxBackups: 1,
			MaxAge:     30,
			Compress:   false,
		}
		l.defaultLogger.SetOutput(l.fileLogger)
	}
}

func (l *logrusLogger) setup(config *Config) {
	l.setLevel(config.String(CfgLogLevel))
	l.setOutput(config.String(CfgLogOutput), config.Int(CfgLogMaxSize))
	l.config = config
}

func (l *logrusLogger) reloadLevel() {
	l.setLevel(l.config.String(CfgLogLevel))
}

func (l *logrusLogger) reloadOutput() {
	if l.fileLogger != nil {
		defer func(f *lumberjack.Logger) {
			f.Close()
		}(l.fileLogger)
	}
	l.setOutput(l.config.String(CfgLogOutput), l.config.Int(CfgLogMaxSize))
}

func (l *logrusLogger) newEntry(src string) *logEntry {
	return &logEntry{
		entry:       logrus.NewEntry(l.defaultLogger).WithFields(logrus.Fields{"module": "pinpoint", "src": src}),
		extraLogger: l.extraLogger,
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
