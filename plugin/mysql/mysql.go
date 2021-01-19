package mysql

import (
	"database/sql"
	"net"

	"github.com/go-sql-driver/mysql"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
)

const (
	serviceTypeMysql             = 2100
	serviceTypeMysqlExecuteQuery = 2101
)

var dbTrace = pinpoint.DatabaseTrace{
	ParseDSN: parseDSN,
}

func init() {
	dbTrace.DbType = serviceTypeMysql
	dbTrace.QueryType = serviceTypeMysqlExecuteQuery
	sql.Register("mysql-pinpoint", pinpoint.MakePinpointSQLDriver(mysql.MySQLDriver{}, dbTrace))
}

func parseDSN(dt *pinpoint.DatabaseTrace, dsn string) {
	cfg, err := mysql.ParseDSN(dsn)
	if nil != err {
		return
	}
	parseConfig(dt, cfg)
}

func parseConfig(dt *pinpoint.DatabaseTrace, cfg *mysql.Config) {
	dt.DbName = cfg.DBName

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

	dt.DbHost = host
}
