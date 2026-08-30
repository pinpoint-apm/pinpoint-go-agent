package ppmssqldb

import (
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// The endpoint recorded on every span event comes from here, so both DSN
// dialects go-mssqldb accepts - ADO keyword pairs and a sqlserver:// URL -
// have to reduce to the same host and database.
func Test_parseDSN(t *testing.T) {
	for _, tt := range []struct {
		name     string
		dsn      string
		wantHost string
		wantName string
	}{
		{
			name:     "ado keyword dsn",
			dsn:      "server=dbhost;user id=sa;password=p123;port=1433;database=TestDB",
			wantHost: "dbhost",
			wantName: "TestDB",
		},
		{
			name:     "url dsn",
			dsn:      "sqlserver://sa:p123@dbhost:1433?database=TestDB",
			wantHost: "dbhost",
			wantName: "TestDB",
		},
		{
			name:     "odbc dsn",
			dsn:      "odbc:server=dbhost;database=TestDB",
			wantHost: "dbhost",
			wantName: "TestDB",
		},
		{
			// The named instance is not part of the host the collector groups by.
			name:     "named instance",
			dsn:      `server=dbhost\SQLEXPRESS;database=TestDB`,
			wantHost: "dbhost",
			wantName: "TestDB",
		},
		{
			// go-mssqldb resolves both "." and an omitted server to localhost.
			name:     "local server shorthand",
			dsn:      "server=.;database=TestDB",
			wantHost: "localhost",
			wantName: "TestDB",
		},
		{
			name:     "no database selected",
			dsn:      "server=dbhost;user id=sa",
			wantHost: "dbhost",
			wantName: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var info pinpoint.DBInfo
			parseDSN(&info, tt.dsn)

			if info.DBHost != tt.wantHost {
				t.Errorf("DBHost = %q, want %q", info.DBHost, tt.wantHost)
			}
			if info.DBName != tt.wantName {
				t.Errorf("DBName = %q, want %q", info.DBName, tt.wantName)
			}
		})
	}
}

// An unparsable DSN must leave the driver's shared DBInfo alone rather than
// half-filling it: sql.Open reports the same error and the connection fails.
func Test_parseDSN_InvalidLeavesInfoUntouched(t *testing.T) {
	for _, dsn := range []string{
		"sqlserver://sa:p123@%zz/", // invalid URL escape
		"server=dbhost;port=nope",  // invalid port
	} {
		info := pinpoint.DBInfo{DBHost: "keep", DBName: "keep"}
		parseDSN(&info, dsn)

		if info.DBHost != "keep" || info.DBName != "keep" {
			t.Errorf("parseDSN(%q) overwrote %q/%q", dsn, info.DBHost, info.DBName)
		}
	}
}

// The registered driver has to carry the mssql service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	if dbInfo.DBType != pinpoint.ServiceTypeMssql {
		t.Errorf("DBType = %d, want %d", dbInfo.DBType, pinpoint.ServiceTypeMssql)
	}
	if dbInfo.QueryType != pinpoint.ServiceTypeMssqlExecuteQuery {
		t.Errorf("QueryType = %d, want %d", dbInfo.QueryType, pinpoint.ServiceTypeMssqlExecuteQuery)
	}
}
