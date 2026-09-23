// Package ppmysql instruments the go-sql-driver/mysql package (https://github.com/go-sql-driver/mysql).
//
// This package instruments the mysql driver calls.
// Use this package's driver in place of the mysql driver.
//
//	db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/testdb")
//
// It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	row := db.QueryRowContext(ctx, "SELECT count(*) from tables")
package ppmysql

import (
	"database/sql"
	"net"

	"github.com/go-sql-driver/mysql"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// DBInfo returns the database description the plugin instruments the driver
// with: service types and the DSN parser that fills in the host and database
// name. It is a constructor rather than a variable so that a caller that runs
// before this package initialized (the compile-time instrumentation tool's
// sql.Register hook, inside the driver's init) gets a complete value.
func DBInfo() pinpoint.DBInfo {
	return pinpoint.DBInfo{
		DBType:    pinpoint.ServiceTypeMysql,
		QueryType: pinpoint.ServiceTypeMysqlExecuteQuery,
		ParseDSN:  parseDSN,
	}
}

func init() {
	sql.Register("mysql-pinpoint", pinpoint.WrapSQLDriver(mysql.MySQLDriver{}, DBInfo()))
}

func parseDSN(info *pinpoint.DBInfo, dsn string) {
	cfg, err := mysql.ParseDSN(dsn)
	if nil != err {
		return
	}
	parseConfig(info, cfg)
}

func parseConfig(info *pinpoint.DBInfo, cfg *mysql.Config) {
	var host string

	switch cfg.Net {
	case "unix", "unixgram", "unixpacket":
		host = "localhost"
	case "cloudsql":
		host = cfg.Addr
	default:
		var err error
		host, _, err = net.SplitHostPort(cfg.Addr)
		if nil != err {
			host = cfg.Addr
		} else if host == "" {
			host = "localhost"
		}
	}

	info.DBName = cfg.DBName
	info.DBHost = host
}
