// Package ppmssqldb instruments the denisenkom/go-mssqldb package (https://github.com/denisenkom/go-mssqldb).
//
// This package instruments the MS SQL Server driver calls.
// Use this package's driver in place of the SQL Server driver.
//
//  dsn := "server=localhost;user id=sa;password=TestPass123;port=1433;database=TestDB"
//	db, err := sql.Open("sqlserver-pinpoint", dsn)
//
// It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	row, err := db.QueryContext(ctx, "SELECT * FROM Inventory")
package ppmssqldb

import (
	"database/sql"

	"github.com/denisenkom/go-mssqldb"
	"github.com/denisenkom/go-mssqldb/msdsn"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

var dbInfo = pinpoint.DBInfo{
	ParseDSN: parseDSN,
}

func init() {
	dbInfo.DBType = pinpoint.ServiceTypeMssql
	dbInfo.QueryType = pinpoint.ServiceTypeMssqlExecuteQuery
	sql.Register("sqlserver-pinpoint", pinpoint.WrapSQLDriver(&mssql.Driver{}, dbInfo))
}

func parseDSN(info *pinpoint.DBInfo, dsn string) {
	cfg, _, err := msdsn.Parse(dsn)
	if nil != err {
		return
	}

	info.DBName = cfg.Database
	info.DBHost = cfg.Host
}
