package pppgsql

import (
	"database/sql"
	"database/sql/driver"
	"slices"
	"testing"

	"github.com/lib/pq"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const driverName = "pq-pinpoint"

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
			// A URL database overrides PGDATABASE for the same reason.
			name:     "url database wins over PGDATABASE",
			dsn:      "postgres://testuser@dbhost/testdb",
			pgDB:     "envdb",
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
			name:     "hostaddr wins over PGHOST too",
			dsn:      "postgres:///testdb?hostaddr=10.0.0.1",
			pgHost:   "envhost",
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
			name:     "a unix socket directory from PGHOST",
			dsn:      "postgres:///testdb",
			pgHost:   "/var/run/postgresql",
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
		{
			name:     "an ipv6 host",
			dsn:      "postgres://testuser@[::1]:5432/testdb",
			wantHost: "::1",
			wantName: "testdb",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PGHOST", tt.pgHost)
			t.Setenv("PGDATABASE", tt.pgDB)

			var info pinpoint.DBInfo
			parseDSN(&info, tt.dsn)

			assert.Equal(t, tt.wantHost, info.DBHost)
			assert.Equal(t, tt.wantName, info.DBName)
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
		"",                             // nothing at all
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
	t.Setenv("PGHOST", "")
	t.Setenv("PGDATABASE", "")

	info := dbInfo
	parseDSN(&info, "postgres://testuser@dbhost/testdb")

	assert.Equal(t, dbInfo.DBType, info.DBType)
	assert.Equal(t, dbInfo.QueryType, info.QueryType)
	assert.Equal(t, "dbhost", info.DBHost)
}

// The registered driver has to carry the postgres service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	assert.Equal(t, pinpoint.ServiceTypePgSql, dbInfo.DBType)
	assert.Equal(t, pinpoint.ServiceTypePgSqlExecuteQuery, dbInfo.QueryType)
	assert.NotNil(t, dbInfo.ParseDSN, "without a ParseDSN the wrapper never learns the host or database")
}

// The documented driver name is the only thing an application refers to, so it
// has to be the name package init actually registered.
func TestRegisteredDriverName(t *testing.T) {
	assert.True(t, slices.Contains(sql.Drivers(), driverName),
		"%s not registered, got %v", driverName, sql.Drivers())
}

// Opening through the registered name must hand database/sql the instrumented
// driver, not the bare pq one - otherwise nothing is ever traced.
func TestOpenUsesTheInstrumentedDriver(t *testing.T) {
	db, err := sql.Open(driverName, "postgres://testuser@dbhost/testdb")
	require.NoError(t, err)
	defer db.Close()

	assert.NotSame(t, &pq.Driver{}, db.Driver(), "the bare pq driver was registered")
	assert.Implements(t, (*driver.Driver)(nil), db.Driver())
}
