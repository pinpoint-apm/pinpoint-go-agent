package avatica

import (
	"database/sql"
	avatica "github.com/apache/calcite-avatica-go/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

var dbInfo = pinpoint.DBInfo{
	ParseDSN: parseDSN,
}

func init() {
	dbInfo.DBType = pinpoint.ServiceTypeAvatica
	dbInfo.QueryType = pinpoint.ServiceTypeAvaticaExecuteQuery
	sql.Register("sqlserver-avatica", pinpoint.WrapSQLDriver(&avatica.Driver{}, dbInfo))
}

func parseDSN(info *pinpoint.DBInfo, dsn string) {
	cfg, err := avatica.ParseDSN(dsn)
	//cfg, _, err := msdsn.Parse(dsn)
	if nil != err {
		return
	}

	info.DBName = cfg.Database
	info.DBHost = cfg.Host
}
