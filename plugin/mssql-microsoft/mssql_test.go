package ppmssqlmicrosoft

import (
	"database/sql"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// driverName stays "mssql-microsoft-pinpoint" rather than the
// "sqlserver-pinpoint" the retired denisenkom plugin used: a binary that still
// links that v1 plugin would panic in database/sql on the duplicate
// registration.
const driverName = "mssql-microsoft-pinpoint"

// parseDSN copies the host and database go-mssqldb's own parser found into
// the DBInfo every span event's endpoint comes from.
func Test_parseDSN(t *testing.T) {
	var info pinpoint.DBInfo
	parseDSN(&info, "server=dbhost;user id=sa;password=p123;port=1433;database=TestDB")

	assert.Equal(t, "dbhost", info.DBHost)
	assert.Equal(t, "TestDB", info.DBName)
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

// The registered driver has to carry the mssql service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	assert.Equal(t, pinpoint.ServiceTypeMssql, DBInfo().DBType)
	assert.Equal(t, pinpoint.ServiceTypeMssqlExecuteQuery, DBInfo().QueryType)
	assert.NotNil(t, DBInfo().ParseDSN, "without a ParseDSN the wrapper never learns the host or database")
}

// Opening through the registered name must hand database/sql the instrumented
// driver, not the bare one - otherwise nothing is ever traced.
func TestOpenUsesTheInstrumentedDriver(t *testing.T) {
	db, err := sql.Open(driverName, "server=dbhost;database=TestDB")
	require.NoError(t, err)
	defer db.Close()

	// A type assertion, not a comparison against a constructed driver value:
	// that only catches a bare driver registered in exactly the same form, and
	// passed for every other one.
	_, bare := db.Driver().(*mssql.Driver)
	assert.False(t, bare, "the bare mssql driver was registered, so nothing is traced")
}
