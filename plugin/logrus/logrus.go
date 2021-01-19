package logrus

import (
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/sirupsen/logrus"
)

func WithField(tracer pinpoint.Tracer) logrus.Fields {
	if tracer == nil {
		return nil
	}

	tracer.Span().SetLogging(pinpoint.Logged)

	tid := tracer.TransactionId()
	return logrus.Fields{
		pinpoint.LogTransactionIdKey: tid.String(),
		pinpoint.LogSpanIdKey:        tracer.SpanId(),
	}
}
