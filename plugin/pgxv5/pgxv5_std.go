package pppgxv5

import (
	"database/sql"
	"database/sql/driver"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// DBInfo returns the database description the plugin instruments the driver
// with: service types, the DSN parser that fills in the host and database
// name, and the connection reader that fills them in when the DSN was a
// stdlib.RegisterConnConfig name. It is a constructor rather than a variable so that a caller that runs
// before this package initialized (the compile-time instrumentation tool's
// sql.Register hook, inside the driver's init) gets a complete value.
func DBInfo() pinpoint.DBInfo {
	return pinpoint.DBInfo{
		DBType:    pinpoint.ServiceTypePgSql,
		QueryType: pinpoint.ServiceTypePgSqlExecuteQuery,
		ParseDSN:  parseDSN,
		ParseConn: parseConn,
	}
}

// The wrapped driver is pgx's own, not a fresh stdlib.Driver: the name
// stdlib.RegisterConnConfig returns is looked up in that driver's registry,
// and a fresh one parsed it as a DSN and failed every connect.
func init() {
	sql.Register("pgxv5-pinpoint", pinpoint.WrapSQLDriver(stdlib.GetDefaultDriver(), DBInfo()))
}

func parseDSN(info *pinpoint.DBInfo, dsn string) {
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		// Debug, like the other drivers' wrappers: the driver itself reports a
		// DSN it cannot use, and a stdlib.RegisterConnConfig name, which pgconn
		// cannot parse either, is read off each connection by parseConn.
		pinpoint.Log("pgxv5").Debugf("dsn parse error: %v", err)
		return
	}

	info.DBHost = config.Host
	info.DBName = config.Database
}

// parseConn reads the host and database from the connection itself when the
// DSN was a stdlib.RegisterConnConfig name, which pgconn cannot parse and
// pgx keeps in a registry it does not export: those connections were drawn
// to no database node.
func parseConn(info *pinpoint.DBInfo, conn driver.Conn) {
	if c, ok := conn.(*stdlib.Conn); ok {
		config := c.Conn().Config()
		info.DBHost = config.Host
		info.DBName = config.Database
	}
}
