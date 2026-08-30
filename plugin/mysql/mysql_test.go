package ppmysql

import (
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// The endpoint recorded on every span event comes from here, so each transport
// mysql supports has to reduce to a host the collector can group by.
func Test_parseConfig(t *testing.T) {
	for _, tt := range []struct {
		name     string
		cfg      mysql.Config
		wantHost string
		wantName string
	}{
		{
			name:     "tcp host and port",
			cfg:      mysql.Config{Net: "tcp", Addr: "127.0.0.1:3306", DBName: "testdb"},
			wantHost: "127.0.0.1",
			wantName: "testdb",
		},
		{
			// SplitHostPort fails without a colon, so the address is the host.
			name:     "tcp without a port",
			cfg:      mysql.Config{Net: "tcp", Addr: "dbhost", DBName: "testdb"},
			wantHost: "dbhost",
			wantName: "testdb",
		},
		{
			name:     "tcp port only",
			cfg:      mysql.Config{Net: "tcp", Addr: ":3306", DBName: "testdb"},
			wantHost: "localhost",
			wantName: "testdb",
		},
		{
			name:     "ipv6 host",
			cfg:      mysql.Config{Net: "tcp", Addr: "[::1]:3306", DBName: "testdb"},
			wantHost: "::1",
			wantName: "testdb",
		},
		{
			// The socket path is not an address the collector can group by.
			name:     "unix socket",
			cfg:      mysql.Config{Net: "unix", Addr: "/tmp/mysql.sock", DBName: "testdb"},
			wantHost: "localhost",
			wantName: "testdb",
		},
		{
			// A Cloud SQL instance name has colons but is not host:port.
			name:     "cloudsql instance",
			cfg:      mysql.Config{Net: "cloudsql", Addr: "project:region:instance", DBName: "testdb"},
			wantHost: "project:region:instance",
			wantName: "testdb",
		},
		{
			name:     "no database selected",
			cfg:      mysql.Config{Net: "tcp", Addr: "127.0.0.1:3306"},
			wantHost: "127.0.0.1",
			wantName: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var info pinpoint.DBInfo
			cfg := tt.cfg
			parseConfig(&info, &cfg)

			if info.DBHost != tt.wantHost {
				t.Errorf("DBHost = %q, want %q", info.DBHost, tt.wantHost)
			}
			if info.DBName != tt.wantName {
				t.Errorf("DBName = %q, want %q", info.DBName, tt.wantName)
			}
		})
	}
}

func Test_parseDSN(t *testing.T) {
	var info pinpoint.DBInfo
	parseDSN(&info, "root:p123@tcp(127.0.0.1:3306)/testdb")

	if info.DBHost != "127.0.0.1" || info.DBName != "testdb" {
		t.Errorf("parseDSN() = %q/%q, want 127.0.0.1/testdb", info.DBHost, info.DBName)
	}
}

// An unparsable DSN must leave the driver's shared DBInfo alone rather than
// half-filling it: sql.Open reports the same error and the connection fails.
func Test_parseDSN_InvalidLeavesInfoUntouched(t *testing.T) {
	info := pinpoint.DBInfo{DBHost: "keep", DBName: "keep"}
	parseDSN(&info, "not a dsn")

	if info.DBHost != "keep" || info.DBName != "keep" {
		t.Errorf("parseDSN() overwrote %q/%q", info.DBHost, info.DBName)
	}
}

// The registered driver has to carry the mysql service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	if dbInfo.DBType != pinpoint.ServiceTypeMysql {
		t.Errorf("DBType = %d, want %d", dbInfo.DBType, pinpoint.ServiceTypeMysql)
	}
	if dbInfo.QueryType != pinpoint.ServiceTypeMysqlExecuteQuery {
		t.Errorf("QueryType = %d, want %d", dbInfo.QueryType, pinpoint.ServiceTypeMysqlExecuteQuery)
	}
}
