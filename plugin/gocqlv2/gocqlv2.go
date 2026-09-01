// Package ppgocqlv2 instruments the gocql/v2 package (https://github.com/apache/cassandra-gocql-driver).
//
// This package instruments all queries created from gocql session.
// Use the NewObserver as the gocql.QueryObserver or gocql.BatchObserver:
//
//	cluster := gocql.NewCluster("127.0.0.1")
//	cluster.QueryObserver = ppgocqlv2.NewObserver()
package ppgocqlv2

import (
	"context"
	"strconv"
	"strings"

	"github.com/apache/cassandra-gocql-driver/v2"
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
	if !tracer.IsSampled() {
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
	if !tracer.IsSampled() {
		return
	}

	span := tracer.NewSpanEvent("cassandra.batch")
	defer span.EndSpanEvent()

	se := span.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeCassandraExecuteQuery)
	se.SetEndPoint(batch.Host.HostnameAndPort())
	se.SetDestination(batch.Keyspace)
	se.FixDuration(batch.Start, batch.End)

	se.SetSQL(batchSQL(batch.Statements), "")
	se.SetError(batch.Err, "batch error")
}

// maxBatchSQL bounds the batch annotation: gocql puts no limit on the batch
// size, and SetSQL keeps only a 64KB prefix anyway, so building the whole
// batch as one string paid O(batch) for a capped result.
const maxBatchSQL = 64 * 1024

func batchSQL(statements []string) string {
	var b strings.Builder
	for i, statement := range statements {
		if b.Len() > maxBatchSQL {
			b.WriteString("...(")
			b.WriteString(strconv.Itoa(len(statements) - i))
			b.WriteString(" more statements)")
			break
		}
		b.WriteString("[")
		b.WriteString(statement)
		b.WriteString("]")
	}
	return b.String()
}
