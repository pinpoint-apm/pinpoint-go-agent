// Package ppgoora instruments the sijms/go-ora/v2 package (https://github.com/sijms/go-ora).
//
// This package instruments the Oracle driver calls.
// Use this package's driver in place of the Oracle driver.
//
//	db, err := sql.Open("oracle-pinpoint", "oracle://scott:tiger@localhost:1521/xe")
//
// It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	row := db.QueryRowContext(ctx, "SELECT * FROM BONUS")
package ppgoora

import (
	"database/sql"
	"net"
	"net/url"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/sijms/go-ora/v2"
)

var dbInfo = pinpoint.DBInfo{
	ParseDSN: parseDSN,
}

func init() {
	dbInfo.DBType = pinpoint.ServiceTypeOracle
	dbInfo.QueryType = pinpoint.ServiceTypeOracleExecuteQuery
	sql.Register("oracle-pinpoint", pinpoint.WrapSQLDriver(&go_ora.OracleDriver{}, dbInfo))
}

func parseDSN(info *pinpoint.DBInfo, dbUrl string) {
	u, err := url.Parse(dbUrl)
	if err != nil {
		return
	}

	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
	} else if host == "" {
		host = "localhost"
	}

	info.DBHost = host
	info.DBName = strings.Trim(u.Path, "/")
}
