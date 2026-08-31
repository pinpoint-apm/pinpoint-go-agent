package ppmssqldb

import (
	"database/sql"
	"database/sql/driver"
	"slices"
	"testing"

	"github.com/denisenkom/go-mssqldb"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const driverName = "sqlserver-pinpoint"

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

			assert.Equal(t, tt.wantHost, info.DBHost)
			assert.Equal(t, tt.wantName, info.DBName)
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

		assert.Equal(t, "keep", info.DBHost, "parseDSN(%q) overwrote the host", dsn)
		assert.Equal(t, "keep", info.DBName, "parseDSN(%q) overwrote the database name", dsn)
	}
}

// parseDSN runs per connection against a copy of the shared DBInfo, and must
// only fill in the address: overwriting the service types would file that one
// connection's queries under a different node.
func Test_parseDSN_LeavesTheServiceTypesAlone(t *testing.T) {
	info := dbInfo
	parseDSN(&info, "server=dbhost;database=TestDB")

	assert.Equal(t, dbInfo.DBType, info.DBType)
	assert.Equal(t, dbInfo.QueryType, info.QueryType)
	assert.Equal(t, "dbhost", info.DBHost)
}

// The registered driver has to carry the mssql service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	assert.Equal(t, pinpoint.ServiceTypeMssql, dbInfo.DBType)
	assert.Equal(t, pinpoint.ServiceTypeMssqlExecuteQuery, dbInfo.QueryType)
	assert.NotNil(t, dbInfo.ParseDSN, "without a ParseDSN the wrapper never learns the host or database")
}

// The documented driver name is the only thing an application refers to, so it
// has to be the name package init actually registered. plugin/mssql-microsoft registers the other
// fork under its own name, so a binary importing both does not panic on a
// duplicate registration.
func TestRegisteredDriverName(t *testing.T) {
	assert.True(t, slices.Contains(sql.Drivers(), driverName),
		"%s not registered, got %v", driverName, sql.Drivers())
}

// Opening through the registered name must hand database/sql the instrumented
// driver, not the bare one - otherwise nothing is ever traced.
func TestOpenUsesTheInstrumentedDriver(t *testing.T) {
	db, err := sql.Open(driverName, "server=dbhost;database=TestDB")
	require.NoError(t, err)
	defer db.Close()

	assert.Implements(t, (*driver.Driver)(nil), db.Driver())
	assert.NotEqual(t, &mssql.Driver{}, db.Driver(), "the bare mssql driver was registered")
}
