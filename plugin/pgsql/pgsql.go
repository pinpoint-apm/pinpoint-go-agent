package pgsql

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/lib/pq"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
)

const (
	serviceTypePgSql             = 2500
	serviceTypePgSqlExecuteQuery = 2501
)

var dbTrace = pinpoint.DatabaseTrace{
	ParseDSN: parseDSN,
}

func init() {
	dbTrace.DbType = serviceTypePgSql
	dbTrace.QueryType = serviceTypePgSqlExecuteQuery
	sql.Register("pq-pinpoint", pinpoint.MakePinpointSQLDriver(&pq.Driver{}, dbTrace))
}

var dsnSplit = regexp.MustCompile(`(\w+)\s*=\s*('[^=]*'|[^'\s]+)`)

func parseDSN(dt *pinpoint.DatabaseTrace, dsn string) {
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

	dt.DbHost = host
	dt.DbName = dbname

	//fmt.Println("host= " + host)
	//fmt.Println("dbname= " + dbname)
}
