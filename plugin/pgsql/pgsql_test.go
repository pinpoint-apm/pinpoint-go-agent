package pppgsql

import (
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// The endpoint recorded on every span event comes from here. lib/pq resolves a
// URL against libpq's environment defaults at connect time, so the parsed host
// and database have to follow the same precedence or the span points at a
// different server than the connection.
func Test_parseDSN(t *testing.T) {
	for _, tt := range []struct {
		name     string
		dsn      string
		pgHost   string
		pgDB     string
		wantHost string
		wantName string
	}{
		{
			name:     "host and database in the url",
			dsn:      "postgresql://testuser:p123@dbhost/testdb?sslmode=disable",
			wantHost: "dbhost",
			wantName: "testdb",
		},
		{
			name:     "explicit port is not part of the host",
			dsn:      "postgres://testuser:p123@dbhost:5432/testdb",
			wantHost: "dbhost",
			wantName: "testdb",
		},
		{
			name:     "no host falls back to localhost",
			dsn:      "postgres://testuser@/testdb",
			wantHost: "localhost",
			wantName: "testdb",
		},
		{
			name:     "no host falls back to PGHOST",
			dsn:      "postgres://testuser@/testdb",
			pgHost:   "envhost",
			wantHost: "envhost",
			wantName: "testdb",
		},
		{
			name:     "no database falls back to PGDATABASE",
			dsn:      "postgres://testuser@dbhost/",
			pgDB:     "envdb",
			wantHost: "dbhost",
			wantName: "envdb",
		},
		{
			// A URL host overrides PGHOST, the same way libpq resolves it.
			name:     "url host wins over PGHOST",
			dsn:      "postgres://testuser@dbhost/testdb",
			pgHost:   "envhost",
			wantHost: "dbhost",
			wantName: "testdb",
		},
		{
			// libpq skips name resolution when hostaddr is set, so that is the
			// server actually contacted.
			name:     "hostaddr wins over host",
			dsn:      "postgres://dbhost:5432/testdb?hostaddr=10.0.0.1",
			wantHost: "10.0.0.1",
			wantName: "testdb",
		},
		{
			// A socket directory is not an address the collector can group by.
			name:     "unix socket directory",
			dsn:      "postgres:///testdb?host=/var/run/postgresql",
			wantHost: "localhost",
			wantName: "testdb",
		},
		{
			// pq quotes values, so a database name with a space survives the
			// key=value split.
			name:     "quoted value with a space",
			dsn:      "postgres://dbhost/db%20name",
			wantHost: "dbhost",
			wantName: "db name",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PGHOST", tt.pgHost)
			t.Setenv("PGDATABASE", tt.pgDB)

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

// pq.ParseURL only accepts URLs. A keyword/value DSN - which lib/pq itself
// connects with - and a malformed URL must leave the driver's shared DBInfo
// alone rather than half-filling it.
func Test_parseDSN_UnparsableLeavesInfoUntouched(t *testing.T) {
	for _, dsn := range []string{
		"host=localhost dbname=testdb", // keyword/value, not a URL
		"://bad",                       // missing scheme
		"mysql://dbhost/testdb",        // wrong protocol
	} {
		info := pinpoint.DBInfo{DBHost: "keep", DBName: "keep"}
		parseDSN(&info, dsn)

		if info.DBHost != "keep" || info.DBName != "keep" {
			t.Errorf("parseDSN(%q) overwrote %q/%q", dsn, info.DBHost, info.DBName)
		}
	}
}

// The registered driver has to carry the postgres service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	if dbInfo.DBType != pinpoint.ServiceTypePgSql {
		t.Errorf("DBType = %d, want %d", dbInfo.DBType, pinpoint.ServiceTypePgSql)
	}
	if dbInfo.QueryType != pinpoint.ServiceTypePgSqlExecuteQuery {
		t.Errorf("QueryType = %d, want %d", dbInfo.QueryType, pinpoint.ServiceTypePgSqlExecuteQuery)
	}
}
