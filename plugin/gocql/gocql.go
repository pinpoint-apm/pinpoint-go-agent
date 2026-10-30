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
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"
	"weak"

	"github.com/gocql/gocql"
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
	ctx, fork := onOwnGoroutine(ctx, tracer)
	if fork != nil {
		defer fork.EndSpan()
	}

	info := cassandraInfo(query.Keyspace, hostnameAndPort(query.Host))
	span := pinpoint.NewDatabaseTracer(ctx, "cassandra.query", &info)
	defer span.EndSpanEvent()

	se := span.SpanEvent()
	se.SetSQL(query.Statement, "")
	se.FixDuration(query.Start, query.End)
	// Guarded: SetError is an interface method, so its variadic name escapes
	// and allocated on every call, nil error and all.
	if query.Err != nil {
		se.SetError(query.Err, "query error")
	}
}

// ObserveBatch instruments all batch queries created from gocql session.
// It is necessary to pass the context containing the pinpoint.Tracer to the query.
// Refer an example of ObserveQuery.
func (o *Observer) ObserveBatch(ctx context.Context, batch gocql.ObservedBatch) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return
	}
	ctx, fork := onOwnGoroutine(ctx, tracer)
	if fork != nil {
		defer fork.EndSpan()
	}

	info := cassandraInfo(batch.Keyspace, hostnameAndPort(batch.Host))
	span := pinpoint.NewDatabaseTracer(ctx, "cassandra.batch", &info)
	defer span.EndSpanEvent()

	se := span.SpanEvent()
	se.FixDuration(batch.Start, batch.End)

	se.SetSQL(batchSQL(batch.Statements), "")
	if batch.Err != nil {
		se.SetError(batch.Err, "batch error")
	}
}

// onOwnGoroutine hands the observer a context whose tracer it may record on
// here, and the goroutine tracer it forked for that, to end, if it had to:
// gocql runs a page prefetch and a speculative execution on goroutines of its
// own, with the query's context, and recorded on the caller's tracer from
// there the event interleaved with the ones the caller was opening meanwhile.
// One made while the caller has no event open is not recorded: a goroutine
// tracer has nothing to link to.
func onOwnGoroutine(ctx context.Context, tracer pinpoint.Tracer) (context.Context, pinpoint.Tracer) {
	if pinpoint.OnTracerGoroutine(tracer) {
		return ctx, nil
	}
	fork := tracer.NewGoroutineTracer()
	return pinpoint.NewContext(ctx, fork), fork
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
	size := 0
	for _, statement := range statements {
		size += min(len(statement), maxBatchSQL) + 2
		if size >= maxBatchSQL {
			size = maxBatchSQL
			break
		}
	}
	b.Grow(size)
	for _, statement := range statements {
		room := maxBatchSQL - b.Len() - 2
		if room < 0 || len(statement) > room {
			if room > 0 {
				b.WriteByte('[')
				b.WriteString(statement[:room])
			}
			const marker = "...(truncated)"
			prefix := b.String()
			cut := min(len(prefix), maxBatchSQL-len(marker))
			for cut > 0 && cut < len(prefix) && !utf8.RuneStart(prefix[cut]) {
				cut--
			}
			return prefix[:cut] + marker
		}
		b.WriteByte('[')
		b.WriteString(statement)
		b.WriteByte(']')
	}
	return b.String()
}
