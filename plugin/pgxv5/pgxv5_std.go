package pppgxv5

import (
	"database/sql"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
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
	sql.Register("pgxv5-pinpoint", pinpoint.WrapSQLDriver(&stdlib.Driver{}, DBInfo()))
}

func parseDSN(info *pinpoint.DBInfo, dsn string) {
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		pinpoint.Log("pgxv5").Errorf("dsn parse error: %v", err)
		return
	}

	info.DBHost = config.Host
	info.DBName = config.Database
}
