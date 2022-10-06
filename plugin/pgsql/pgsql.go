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
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/lib/pq"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

var dbInfo = pinpoint.DBInfo{
	ParseDSN: parseDSN,
}

func init() {
	dbInfo.DBType = pinpoint.ServiceTypePgSql
	dbInfo.QueryType = pinpoint.ServiceTypePgSqlExecuteQuery
	sql.Register("pq-pinpoint", pinpoint.WrapSQLDriver(&pq.Driver{}, dbInfo))
}

var dsnSplit = regexp.MustCompile(`(\w+)\s*=\s*('[^=]*'|[^'\s]+)`)

func parseDSN(info *pinpoint.DBInfo, dsn string) {
	convDsn, err := pq.ParseURL(dsn)
	if err != nil {
		fmt.Println("error= " + err.Error())
		return
	}

	host := os.Getenv("PGHOST")
	hostaddr := ""
	dbname := os.Getenv("PGDATABASE")

	for _, split := range dsnSplit.FindAllStringSubmatch(convDsn, -1) {
		if len(split) != 3 {
			continue
		}
		key := split[1]
		value := strings.Trim(split[2], `'`)

		switch key {
		case "dbname":
			dbname = value
		case "host":
			host = value
		case "hostaddr":
			hostaddr = value
		}
	}

	if "" != hostaddr {
		host = hostaddr
	} else if "" == host {
		host = "localhost"
	}

	if strings.HasPrefix(host, "/") {
		// this is a unix socket
		host = "localhost"
	}

	info.DBHost = host
	info.DBName = dbname

	//fmt.Println("host= " + host)
	//fmt.Println("dbname= " + dbname)
}
