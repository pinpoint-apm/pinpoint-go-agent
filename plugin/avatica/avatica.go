package ppavatica

import (
	"database/sql"
	avatica "github.com/apache/calcite-avatica-go/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"net"
	"net/url"
	"strings"
)

var dbInfo = pinpoint.DBInfo{
	ParseDSN: parseDSN,
}

func init() {
	dbInfo.DBType = pinpoint.ServiceTypeAvatica                // TODO: need to add
	dbInfo.QueryType = pinpoint.ServiceTypeAvaticaExecuteQuery // TODO: need to add
	sql.Register("sqlserver-avatica", pinpoint.WrapSQLDriver(&avatica.Driver{}, dbInfo))
}

func parseDSN(info *pinpoint.DBInfo, dsn string) {
	u, err := url.Parse(dsn)
	if err != nil {
		return
	}

	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
	} else if host == "" {
		host = "localhost"
	}

	s := strings.Split(u.Path, "/")
	schema := s[len(s)-1]

	info.DBHost = host
	info.DBName = schema
}
