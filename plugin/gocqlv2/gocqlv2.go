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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"weak"

	"github.com/apache/cassandra-gocql-driver/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// hostNames caches HostnameAndPort per host. gocql formats it on every call
// under the HostInfo's write lock - the lock the driver read-locks on its own
// path for every query, so each sampled query contended with host selection -
// and allocates twice, for a string that never changes for a host. Weak keys:
// a host that left the ring is dropped with its HostInfo.
var hostNames sync.Map // weak.Pointer[gocql.HostInfo] -> string

func hostnameAndPort(h *gocql.HostInfo) string {
	k := weak.Make(h)
	if v, ok := hostNames.Load(k); ok {
		return v.(string)
	}
	name := h.HostnameAndPort()
	if _, loaded := hostNames.LoadOrStore(k, name); !loaded {
		runtime.AddCleanup(h, func(k weak.Pointer[gocql.HostInfo]) { hostNames.Delete(k) }, k)
	}
	return name
}

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

	info := cassandraInfo(query.Keyspace, hostnameAndPort(query.Host))
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

	info := cassandraInfo(batch.Keyspace, hostnameAndPort(batch.Host))
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
