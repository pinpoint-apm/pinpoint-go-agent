// Package ppgocql instruments the gocql package (https://github.com/gocql/gocql).
//
// This package instruments all queries created from gocql session.
// Use the NewObserver as the gocql.QueryObserver or gocql.BatchObserver:
//
//	cluster := gocql.NewCluster("127.0.0.1")
//	cluster.QueryObserver = ppgocql.NewObserver()
package ppgocql

import (
	"bytes"
	"context"

	"github.com/gocql/gocql"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type Observer struct{}

// NewObserver returns a query or batch observer ready to instrument.
func NewObserver() *Observer {
	return &Observer{}
}

// ObserveQuery instruments all queries created from gocql session.
// It is necessary to pass the context containing the pinpoint.Tracer to the query.
//
//	query := session.Query(query)
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	query.WithContext(ctx).Consistency(gocql.One).Scan(&id, &text)
func (o *Observer) ObserveQuery(ctx context.Context, query gocql.ObservedQuery) {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return
	}

	span := tracer.NewSpanEvent("cassandra.query")
	defer span.EndSpanEvent()

	se := span.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeCassandraExecuteQuery)
	se.SetEndPoint(query.Host.HostnameAndPort())
	se.SetDestination(query.Keyspace)
	se.SetSQL(query.Statement, "")
	se.FixDuration(query.Start, query.End)
	se.SetError(query.Err, "query error")
}

// ObserveBatch instruments all batch queries created from gocql session.
// It is necessary to pass the context containing the pinpoint.Tracer to the query.
// Refer an example of ObserveQuery.
func (o *Observer) ObserveBatch(ctx context.Context, batch gocql.ObservedBatch) {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return
	}

	span := tracer.NewSpanEvent("cassandra.batch")
	defer span.EndSpanEvent()

	se := span.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeCassandraExecuteQuery)
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
