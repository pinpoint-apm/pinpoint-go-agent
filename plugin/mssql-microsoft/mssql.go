// Package ppmssqlmicrosoft instruments the microsoft/go-mssqldb package (https://github.com/microsoft/go-mssqldb).
//
// This package instruments the MS SQL Server driver calls.
// Use this package's driver in place of the SQL Server driver.
//
//	dsn := "server=localhost;user id=sa;password=TestPass123;port=1433;database=TestDB"
//	db, err := sql.Open("mssql-microsoft-pinpoint", dsn)
//
// It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	row, err := db.QueryContext(ctx, "SELECT * FROM Inventory")
package ppmssqlmicrosoft

import (
	"database/sql"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// DBInfo returns the database description the plugin instruments the driver
// with: service types and the DSN parser that fills in the host and database
// name. It is a constructor rather than a variable so that a caller that runs
// before this package initialized (the compile-time instrumentation tool's
// sql.Register hook, inside the driver's init) gets a complete value.
func DBInfo() pinpoint.DBInfo {
	return pinpoint.DBInfo{
		DBType:    pinpoint.ServiceTypeMssql,
		QueryType: pinpoint.ServiceTypeMssqlExecuteQuery,
		ParseDSN:  parseDSN,
	}
}

func init() {
	sql.Register("mssql-microsoft-pinpoint", pinpoint.WrapSQLDriver(&mssql.Driver{}, DBInfo()))
}

func parseDSN(info *pinpoint.DBInfo, dsn string) {
	cfg, err := msdsn.Parse(dsn)
	if err != nil {
		pinpoint.Log("mssql").Debugf("dsn parse error: %v", err)
		return
	}

	info.DBName = cfg.Database
	info.DBHost = cfg.Host
}
