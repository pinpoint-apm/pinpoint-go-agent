// Package ppgocql instruments the gocql package (https://github.com/gocql/gocql).
//
// This package instruments all queries created from gocql session.
// Use the NewObserver as the gocql.QueryObserver or gocql.BatchObserver:
//
//	cluster := gocql.NewCluster("127.0.0.1")
//	cluster.QueryObserver = ppgocql.NewObserver()
package ppgocql

import (
	"context"
	"strconv"
	"strings"

	"github.com/gocql/gocql"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
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

	info := cassandraInfo(query.Keyspace, query.Host.HostnameAndPort())
	span := pinpoint.NewDatabaseTracer(ctx, "cassandra.query", &info)
	defer span.EndSpanEvent()

	se := span.SpanEvent()
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

	info := cassandraInfo(batch.Keyspace, batch.Host.HostnameAndPort())
	span := pinpoint.NewDatabaseTracer(ctx, "cassandra.batch", &info)
	defer span.EndSpanEvent()

	se := span.SpanEvent()
	se.FixDuration(batch.Start, batch.End)

	se.SetSQL(batchSQL(batch.Statements), "")
	se.SetError(batch.Err, "batch error")
}

// cassandraInfo describes the keyspace and host a query ran against.
func cassandraInfo(keyspace, host string) pinpoint.DBInfo {
	return pinpoint.DBInfo{QueryType: pinpoint.ServiceTypeCassandraExecuteQuery, DBName: keyspace, DBHost: host}
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
