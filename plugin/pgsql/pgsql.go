// Package pppgsql instruments the lib/pq package (https://github.com/lib/pq).
//
// This package instruments the postgres driver calls.
// Use this package's driver in place of the postgres driver.
//
//	db, err := sql.Open("pq-pinpoint", "postgresql://testuser:p123@localhost/testdb?sslmode=disable")
//
// It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
package pppgsql

import (
	"database/sql"
	"strings"

	"github.com/lib/pq"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// DBInfo returns the database description the plugin instruments the driver
// with: service types and the DSN parser that fills in the host and database
// name. It is a constructor rather than a variable so that a caller that runs
// before this package initialized (the compile-time instrumentation tool's
// sql.Register hook, inside the driver's init) gets a complete value.
func DBInfo() pinpoint.DBInfo {
	return pinpoint.DBInfo{
		DBType:    pinpoint.ServiceTypePgSql,
		QueryType: pinpoint.ServiceTypePgSqlExecuteQuery,
		ParseDSN:  parseDSN,
	}
}

func init() {
	sql.Register("pq-pinpoint", pinpoint.WrapSQLDriver(&pq.Driver{}, DBInfo()))
}

// parseDSN reads the DSN as lib/pq does, through its own pq.NewConfig: the URL
// and the keyword/value form, the PG* environment variables, the localhost
// default, and hostaddr over host, since hostaddr is the server contacted.
func parseDSN(info *pinpoint.DBInfo, dsn string) {
	cfg, err := pq.NewConfig(dsn)
	if err != nil {
		// Debug, like the other drivers' wrappers: the driver itself reports a
		// DSN it cannot use, and this runs once per pooled connection.
		pinpoint.Log("pgsql").Debugf("dsn parse error: %v", err)
		return
	}

	host := cfg.Host
	if cfg.Hostaddr.IsValid() {
		host = cfg.Hostaddr.String()
	}
	if host == "" || strings.HasPrefix(host, "/") {
		// No host, or a unix socket directory, which is no address the
		// collector can group by.
		host = "localhost"
	}

	info.DBHost = host
	info.DBName = cfg.Database
}
