// Package ppslog instruments the standard library's log/slog package.
//
// This package allows additional transaction id and span id of the pinpoint span
// to be printed in the log message.
// Wrap the handler of your logger with NewHandler and log with a context carrying
// the pinpoint.Tracer; the ids are added to every record written through it.
//
//	logger := slog.New(ppslog.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
//	logger.ErrorContext(pinpoint.NewContext(ctx, tracer), "handler log message")
//
// Use NewAttrs where the tracer is at hand but the context is not.
//
//	tracer := pinpoint.FromContext(ctx)
//	logger.LogAttrs(ctx, slog.LevelError, "oh, what a wonderful world", ppslog.NewAttrs(tracer)...)
package ppslog

import (
	"context"
	"log/slog"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// NewAttrs returns the transaction id and the span id of a pinpoint span as
// slog attributes. It returns nil if the span is not sampled.
func NewAttrs(tracer pinpoint.Tracer) []slog.Attr {
	if tracer == nil || !tracer.IsSampled() {
		return nil
	}

	tracer.Span().SetLogging(pinpoint.Logged)
	return []slog.Attr{
		slog.String(pinpoint.LogTransactionIdKey, tracer.TransactionId().String()),
		slog.Int64(pinpoint.LogSpanIdKey, tracer.SpanId()),
	}
}

// handler wraps a slog.Handler and adds the ids of the pinpoint span found in
// the record's context.
type handler struct{ next slog.Handler }

// NewHandler returns a new slog.Handler ready to instrument.
// It is necessary to pass the context containing the pinpoint.Tracer to the
// logger, which the *Context methods of slog.Logger do. WithGroup qualifies
// the injected ids as it does other record attributes.
func NewHandler(h slog.Handler) slog.Handler {
	return &handler{next: h}
}

func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := NewAttrs(pinpoint.FromContext(ctx))
	if len(attrs) == 0 {
		return h.next.Handle(ctx, r)
	}

	r = r.Clone()
	r.AddAttrs(attrs...)
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return &handler{next: h.next.WithAttrs(attrs)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &handler{next: h.next.WithGroup(name)}
}
