// Package pporaclev3 instruments the sijms/go-ora/v3 package (https://github.com/sijms/go-ora).
//
// This package instruments the Oracle driver calls.
// Use this package's driver in place of the Oracle driver.
//
//	db, err := sql.Open("oraclev3-pinpoint", "oracle://scott:tiger@localhost:1521/xe")
//
// It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	row := db.QueryRowContext(ctx, "SELECT * FROM BONUS")
//
// This plugin cannot be used in the same binary as pporacle: go-ora v2 and v3
// both call sql.Register("oracle", ...) in their own package init, so linking
// both panics at startup on a duplicate driver name. That is upstream's doing
// and neither plugin can work around it - pick one go-ora major per binary.
package pporaclev3

import (
	"database/sql"
	"net"
	"net/url"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/sijms/go-ora/v3"
)

var dbInfo = pinpoint.DBInfo{
	ParseDSN: parseDSN,
}

// Unlike v2, a v3 OracleDriver is not usable as a zero value: NewDriver fills
// in the parameter coder maps that every connection copies, and a literal
// &OracleDriver{} leaves them nil.
var oracleDriver = go_ora.NewDriver()

func init() {
	dbInfo.DBType = pinpoint.ServiceTypeOracle
	dbInfo.QueryType = pinpoint.ServiceTypeOracleExecuteQuery
	sql.Register("oraclev3-pinpoint", pinpoint.WrapSQLDriver(oracleDriver, dbInfo))
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
