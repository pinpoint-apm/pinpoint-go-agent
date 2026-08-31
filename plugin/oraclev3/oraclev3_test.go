package pporaclev3

import (
	"database/sql"
	"database/sql/driver"
	"slices"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const driverName = "oraclev3-pinpoint"

// The endpoint recorded on every span event comes from here, so the service
// name in the URL path has to reduce to a bare database name and the authority
// to a bare host. v3 parses the DSN the same way v2 did -- url.Parse, then
// SplitHostPort over the authority and the path trimmed to the service name --
// so the expectations below carry over unchanged.
func Test_parseDSN(t *testing.T) {
	for _, tt := range []struct {
		name     string
		dsn      string
		wantHost string
		wantName string
	}{
		{
			name:     "host and port",
			dsn:      "oracle://scott:tiger@localhost:1521/xe",
			wantHost: "localhost",
			wantName: "xe",
		},
		{
			// SplitHostPort fails without a colon, so the authority is the host.
			name:     "no port",
			dsn:      "oracle://scott:tiger@dbhost/xe",
			wantHost: "dbhost",
			wantName: "xe",
		},
		{
			name:     "port only",
			dsn:      "oracle://scott:tiger@:1521/xe",
			wantHost: "localhost",
			wantName: "xe",
		},
		{
			name:     "ipv6 host",
			dsn:      "oracle://scott:tiger@[::1]:1521/xe",
			wantHost: "::1",
			wantName: "xe",
		},
		{
			name:     "no service name",
			dsn:      "oracle://scott:tiger@localhost:1521/",
			wantHost: "localhost",
			wantName: "",
		},
		{
			name:     "query parameters are ignored",
			dsn:      "oracle://scott:tiger@localhost:1521/xe?SID=ORCL&TRACE+FILE=trace.log",
			wantHost: "localhost",
			wantName: "xe",
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

// An unparsable URL must leave the driver's shared DBInfo alone rather than
// half-filling it.
func Test_parseDSN_InvalidLeavesInfoUntouched(t *testing.T) {
	info := pinpoint.DBInfo{DBHost: "keep", DBName: "keep"}
	parseDSN(&info, "oracle://scott:tiger@local\thost:1521/xe")

	assert.Equal(t, "keep", info.DBHost, "parseDSN overwrote the host")
	assert.Equal(t, "keep", info.DBName, "parseDSN overwrote the database name")
}

// parseDSN runs per connection against a copy of the shared DBInfo, and must
// only fill in the address: overwriting the service types would file that one
// connection's queries under a different node.
func Test_parseDSN_LeavesTheServiceTypesAlone(t *testing.T) {
	info := dbInfo
	parseDSN(&info, "oracle://scott:tiger@localhost:1521/xe")

	assert.Equal(t, dbInfo.DBType, info.DBType)
	assert.Equal(t, dbInfo.QueryType, info.QueryType)
	assert.Equal(t, "localhost", info.DBHost)
}

// The registered driver has to carry the oracle service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	assert.Equal(t, pinpoint.ServiceTypeOracle, dbInfo.DBType)
	assert.Equal(t, pinpoint.ServiceTypeOracleExecuteQuery, dbInfo.QueryType)
	assert.NotNil(t, dbInfo.ParseDSN, "without a ParseDSN the wrapper never learns the host or database")
}

// The documented driver name is the only thing an application refers to, so it
// has to be the name package init actually registered. go-ora v2 and v3 both register
// "oracle" themselves, so the two plugins cannot share a binary - but their own
// names still have to differ.
func TestRegisteredDriverName(t *testing.T) {
	assert.True(t, slices.Contains(sql.Drivers(), driverName),
		"%s not registered, got %v", driverName, sql.Drivers())
}

// Opening through the registered name must hand database/sql the instrumented
// driver, not the bare one - otherwise nothing is ever traced.
func TestOpenUsesTheInstrumentedDriver(t *testing.T) {
	db, err := sql.Open(driverName, "oracle://scott:tiger@localhost:1521/xe")
	require.NoError(t, err)
	defer db.Close()

	assert.Implements(t, (*driver.Driver)(nil), db.Driver())
	assert.NotEqual(t, oracleDriver, db.Driver(), "the bare oracle driver was registered")
}
