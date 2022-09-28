package ppgocql

import (
	"bytes"
	"context"

	"github.com/gocql/gocql"
	"github.com/pinpoint-apm/pinpoint-go-agent"
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

	se := span.SpanEvent()
	se.SetServiceType(serviceTypeCassandraExecuteQuery)
	se.SetEndPoint(query.Host.HostnameAndPort())
	se.SetDestination(query.Keyspace)
	se.SetSQL(query.Statement, "")
	se.FixDuration(query.Start, query.End)
	se.SetError(query.Err, "query error")
}

func (o *Observer) ObserveBatch(ctx context.Context, batch gocql.ObservedBatch) {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return
	}

	span := tracer.NewSpanEvent("cassandra.batch")
	defer span.EndSpanEvent()

	se := span.SpanEvent()
	se.SetServiceType(serviceTypeCassandraExecuteQuery)
	se.SetEndPoint(batch.Host.HostnameAndPort())
	se.SetDestination(batch.Keyspace)
	se.FixDuration(batch.Start, batch.End)

	var buffer bytes.Buffer
	for _, statement := range batch.Statements {
		buffer.WriteString("[")
		buffer.WriteString(statement)
		buffer.WriteString("]")
	}

	se.SetSQL(buffer.String(), "")
	se.SetError(batch.Err, "batch error")
}
