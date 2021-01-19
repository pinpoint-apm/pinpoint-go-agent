package gocql

import (
	"bytes"
	"context"

	"github.com/gocql/gocql"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
)

const serviceTypeCassandraExecuteQuery = 2601

type Observer struct{}

func NewObserver() *Observer {
	return &Observer{}
}

func (o *Observer) ObserveQuery(ctx context.Context, query gocql.ObservedQuery) {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return
	}

	span := tracer.NewSpanEvent("cassandra.query")
	defer span.EndSpanEvent()

	span.SpanEvent().SetServiceType(serviceTypeCassandraExecuteQuery)
	span.SpanEvent().SetEndPoint(query.Host.HostnameAndPort())
	span.SpanEvent().SetDestination(query.Keyspace)
	span.SpanEvent().SetSQL(query.Statement)
	span.SpanEvent().FixDuration(query.Start, query.End)
	span.SpanEvent().SetError(query.Err)
}

func (o *Observer) ObserveBatch(ctx context.Context, batch gocql.ObservedBatch) {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return
	}

	span := tracer.NewSpanEvent("cassandra.batch")
	defer span.EndSpanEvent()

	span.SpanEvent().SetServiceType(serviceTypeCassandraExecuteQuery)
	span.SpanEvent().SetEndPoint(batch.Host.HostnameAndPort())
	span.SpanEvent().SetDestination(batch.Keyspace)
	span.SpanEvent().FixDuration(batch.Start, batch.End)

	var buffer bytes.Buffer
	for _, statement := range batch.Statements {
		buffer.WriteString("[")
		buffer.WriteString(statement)
		buffer.WriteString("]")
	}
	span.SpanEvent().SetSQL(buffer.String())
	span.SpanEvent().SetError(batch.Err)
}
