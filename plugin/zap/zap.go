// Package ppzap instruments the uber-go/zap package (https://github.com/uber-go/zap).
//
// This package allows additional transaction id and span id of the pinpoint span
// to be printed in the log message.
// Use the NewField or NewLogger and pass the zap fields back to the logger.
//
//	tracer := pinpoint.FromContext(ctx)
//	logger.Error("oh, what a wonderful world", ppzap.NewField(tracer)...)
//
// or
//
//	logger := ppzap.NewLogger(zap.L(), tracer).With(zap.String("foo", "bar"))
//	logger.Error("logger log message")
//
// Unlike the slog and logrus plugins, this one has no handler or hook that reads
// the tracer on its own: zap passes no context.Context to zapcore.Core, so the
// span has to be named where the logger is derived, once per request.
//
// For a *zap.SugaredLogger, derive it from an instrumented *zap.Logger:
//
//	sugar := ppzap.NewLogger(logger, tracer).Sugar()
package ppzap

import (
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"go.uber.org/zap"
)

// NewField returns the transaction id and the span id of a pinpoint span as zap
// fields. It returns nil if the span is not sampled.
func NewField(tracer pinpoint.Tracer) []zap.Field {
	if tracer == nil || !tracer.IsSampled() {
		return nil
	}

	tracer.Span().SetLogging(pinpoint.Logged)
	return []zap.Field{
		zap.String(pinpoint.LogTransactionIdKey, tracer.TransactionId().String()),
		zap.Int64(pinpoint.LogSpanIdKey, tracer.SpanId()),
	}
}

// NewLogger returns a new *zap.Logger derived from the provided logger.
// The logger has the transaction id and the span id of a pinpoint span, and
// keeps the fields and options the provided logger already carries.
func NewLogger(logger *zap.Logger, tracer pinpoint.Tracer) *zap.Logger {
	return logger.With(NewField(tracer)...)
}
