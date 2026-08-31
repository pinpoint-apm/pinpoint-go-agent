package ppmysql

import (
	"database/sql"
	"database/sql/driver"
	"slices"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const driverName = "mysql-pinpoint"

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
			name:     "unixgram socket",
			cfg:      mysql.Config{Net: "unixgram", Addr: "/tmp/mysql.sock", DBName: "testdb"},
			wantHost: "localhost",
			wantName: "testdb",
		},
		{
			name:     "unixpacket socket",
			cfg:      mysql.Config{Net: "unixpacket", Addr: "/tmp/mysql.sock", DBName: "testdb"},
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
		{
			name:     "no address at all",
			cfg:      mysql.Config{Net: "tcp", DBName: "testdb"},
			wantHost: "",
			wantName: "testdb",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var info pinpoint.DBInfo
			cfg := tt.cfg
			parseConfig(&info, &cfg)

			assert.Equal(t, tt.wantHost, info.DBHost)
			assert.Equal(t, tt.wantName, info.DBName)
		})
	}
}

func Test_parseDSN(t *testing.T) {
	for _, tt := range []struct {
		name     string
		dsn      string
		wantHost string
		wantName string
	}{
		{
			name:     "the documented dsn",
			dsn:      "root:p123@tcp(127.0.0.1:3306)/testdb",
			wantHost: "127.0.0.1",
			wantName: "testdb",
		},
		{
			name:     "a named host",
			dsn:      "root:p123@tcp(dbhost:3306)/testdb?parseTime=true",
			wantHost: "dbhost",
			wantName: "testdb",
		},
		{
			name:     "a unix socket dsn",
			dsn:      "root:p123@unix(/tmp/mysql.sock)/testdb",
			wantHost: "localhost",
			wantName: "testdb",
		},
		{
			// mysql defaults to 127.0.0.1:3306 when the dsn names no address.
			name:     "no address in the dsn",
			dsn:      "root:p123@/testdb",
			wantHost: "127.0.0.1",
			wantName: "testdb",
		},
		{
			// An empty dsn is not an error to mysql: it is every default.
			name:     "an empty dsn is mysql's defaults",
			dsn:      "",
			wantHost: "127.0.0.1",
			wantName: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var info pinpoint.DBInfo
			parseDSN(&info, tt.dsn)

			assert.Equal(t, tt.wantHost, info.DBHost)
			assert.Equal(t, tt.wantName, info.DBName)
		})
	}
}

// An unparsable DSN must leave the driver's shared DBInfo alone rather than
// half-filling it: sql.Open reports the same error and the connection fails.
func Test_parseDSN_InvalidLeavesInfoUntouched(t *testing.T) {
	for _, dsn := range []string{
		"not a dsn",
		"root:p123@tcp(127.0.0.1:3306)", // no database part at all
		"root:p123@tcp(127.0.0.1:3306)/testdb?parseTime=nope",
	} {
		info := pinpoint.DBInfo{DBHost: "keep", DBName: "keep"}
		parseDSN(&info, dsn)

		assert.Equal(t, "keep", info.DBHost, "parseDSN(%q) overwrote the host", dsn)
		assert.Equal(t, "keep", info.DBName, "parseDSN(%q) overwrote the database name", dsn)
	}
}

// parseDSN runs per connection against a copy of the shared DBInfo, and must
// only fill in the address: overwriting the service types would file that one
// connection's queries under a different node.
func Test_parseDSN_LeavesTheServiceTypesAlone(t *testing.T) {
	info := dbInfo
	parseDSN(&info, "root:p123@tcp(127.0.0.1:3306)/testdb")

	assert.Equal(t, dbInfo.DBType, info.DBType)
	assert.Equal(t, dbInfo.QueryType, info.QueryType)
	assert.Equal(t, "127.0.0.1", info.DBHost)
}

// The registered driver has to carry the mysql service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	assert.Equal(t, pinpoint.ServiceTypeMysql, dbInfo.DBType)
	assert.Equal(t, pinpoint.ServiceTypeMysqlExecuteQuery, dbInfo.QueryType)
	assert.NotNil(t, dbInfo.ParseDSN, "without a ParseDSN the wrapper never learns the host or database")
}

// The documented driver name is the only thing an application refers to, so it
// has to be the name package init actually registered.
func TestRegisteredDriverName(t *testing.T) {
	assert.True(t, slices.Contains(sql.Drivers(), driverName),
		"%s not registered, got %v", driverName, sql.Drivers())
}

// Opening through the registered name must hand database/sql the instrumented
// driver, not the bare mysql one - otherwise nothing is ever traced.
func TestOpenUsesTheInstrumentedDriver(t *testing.T) {
	db, err := sql.Open(driverName, "root:p123@tcp(127.0.0.1:3306)/testdb")
	require.NoError(t, err)
	defer db.Close()

	assert.NotEqual(t, mysql.MySQLDriver{}, db.Driver(), "the bare mysql driver was registered")
	assert.Implements(t, (*driver.DriverContext)(nil), db.Driver(),
		"the wrapper must keep the driver's OpenConnector reachable, or database/sql re-parses the dsn per connection")
}
