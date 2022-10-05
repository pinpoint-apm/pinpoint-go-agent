package ppmysql

import (
	"database/sql"
	"net"

	"github.com/go-sql-driver/mysql"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

var dbInfo = pinpoint.DBInfo{
	ParseDSN: parseDSN,
}

func init() {
	dbInfo.DBType = pinpoint.ServiceTypeMysql
	dbInfo.QueryType = pinpoint.ServiceTypeMysqlExecuteQuery
	sql.Register("mysql-pinpoint", pinpoint.WrapSQLDriver(mysql.MySQLDriver{}, dbInfo))
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
