package logrus

import (
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/sirupsen/logrus"
)

// WithField is deprecated
func WithField(tracer pinpoint.Tracer) logrus.Fields {
	return NewField(tracer)
}

func NewField(tracer pinpoint.Tracer) logrus.Fields {
	if tracer == nil || !tracer.IsSampled() {
		return logrus.Fields{}
	}

	tracer.Span().SetLogging(pinpoint.Logged)
	return logrus.Fields{
		pinpoint.LogTransactionIdKey: tracer.TransactionId().String(),
		pinpoint.LogSpanIdKey:        tracer.SpanId(),
	}
}

func NewEntry(tracer pinpoint.Tracer) *logrus.Entry {
	return logrus.WithFields(NewField(tracer))
}

func NewLoggerEntry(logger *logrus.Logger, tracer pinpoint.Tracer) *logrus.Entry {
	return logger.WithFields(NewField(tracer))
}

type hook struct {
}

func NewHook() *hook {
	return &hook{}
}

func (h *hook) Fire(entry *logrus.Entry) error {
	tracer := pinpoint.FromContext(entry.Context)
	if !tracer.IsSampled() {
		return nil
	}

	tracer.Span().SetLogging(pinpoint.Logged)
	entry.Data[pinpoint.LogTransactionIdKey] = tracer.TransactionId().String()
	entry.Data[pinpoint.LogSpanIdKey] = tracer.SpanId()

	return nil
}

func (h *hook) Levels() []logrus.Level {
	return logrus.AllLevels
}
