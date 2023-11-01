package pppgxv5

import (
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

var dbInfo = pinpoint.DBInfo{
	ParseDSN: parseDSN,
}

func init() {
	dbInfo.DBType = pinpoint.ServiceTypePgSql
	dbInfo.QueryType = pinpoint.ServiceTypePgSqlExecuteQuery
	sql.Register("pgxv5-pinpoint", pinpoint.WrapSQLDriver(&stdlib.Driver{}, dbInfo))
}

func parseDSN(info *pinpoint.DBInfo, dsn string) {
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		fmt.Println("error= " + err.Error())
		return
	}

	info.DBHost = config.Host
	info.DBName = config.Database
}
