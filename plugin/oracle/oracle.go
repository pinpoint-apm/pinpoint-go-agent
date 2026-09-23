// Package pporacle instruments the sijms/go-ora/v2 package (https://github.com/sijms/go-ora).
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
//
// This plugin cannot be used in the same binary as pporaclev3: go-ora v2 and v3
// both call sql.Register("oracle", ...) in their own package init, so linking
// both panics at startup on a duplicate driver name. That is upstream's doing
// and neither plugin can work around it - pick one go-ora major per binary.
package pporacle

import (
	"database/sql"
	"net"
	"net/url"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/sijms/go-ora/v2"
)

// DBInfo returns the database description the plugin instruments the driver
// with: service types and the DSN parser that fills in the host and database
// name. It is a constructor rather than a variable so that a caller that runs
// before this package initialized (the compile-time instrumentation tool's
// sql.Register hook, inside the driver's init) gets a complete value.
func DBInfo() pinpoint.DBInfo {
	return pinpoint.DBInfo{
		DBType:    pinpoint.ServiceTypeOracle,
		QueryType: pinpoint.ServiceTypeOracleExecuteQuery,
		ParseDSN:  parseDSN,
	}
}

func init() {
	sql.Register("oracle-pinpoint", pinpoint.WrapSQLDriver(&go_ora.OracleDriver{}, DBInfo()))
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
