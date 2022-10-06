// Package pplogrus instruments the sirupsen/logrus package (https://github.com/sirupsen/logrus).
//
// This package allows additional transaction id and span id of the pinpoint span to be printed in the log message.
// Use the NewField or NewEntry and pass the logrus field back to the logger.
//
//	tracer := pinpoint.FromContext(ctx)
//	logger.WithFields(pplogrus.NewField(tracer)).Fatal("oh, what a wonderful world")
//
// or
//
//	entry := pplogrus.NewEntry(tracer).WithField("foo", "bar")
//	entry.Error("entry log message")
//
// You can use NewHook as the logrus.Hook.
// It is necessary to pass the context containing the pinpoint.Tracer to logrus.Logger.
//
//	logger.AddHook(pplogrus.NewHook())
//	entry := logger.WithContext(pinpoint.NewContext(context.Background(), tracer)).WithField("foo", "bar")
//	entry.Error("hook log message")
package pplogrus

import (
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/sirupsen/logrus"
)

// WithField is deprecated. Use NewField.
func WithField(tracer pinpoint.Tracer) logrus.Fields {
	return NewField(tracer)
}

// NewField returns a new logrus.Fields added the transaction id and the span id of a pinpoint span.
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

// NewEntry returns a new *logrus.Entry from standard logger.
// The entry has the transaction id and the span id of a pinpoint span.
func NewEntry(tracer pinpoint.Tracer) *logrus.Entry {
	return logrus.WithFields(NewField(tracer))
}

// NewLoggerEntry returns a new *logrus.Entry from the provided logger.
// The entry has the transaction id and the span id of a pinpoint span.
func NewLoggerEntry(logger *logrus.Logger, tracer pinpoint.Tracer) *logrus.Entry {
	return logger.WithFields(NewField(tracer))
}

type hook struct {
}

// NewHook returns a new logrus.Hook ready to instrument.
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
