package pporaclev3

import (
	"database/sql"
	"reflect"
	"slices"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/sijms/go-ora/v3"
)

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

			if info.DBHost != tt.wantHost {
				t.Errorf("DBHost = %q, want %q", info.DBHost, tt.wantHost)
			}
			if info.DBName != tt.wantName {
				t.Errorf("DBName = %q, want %q", info.DBName, tt.wantName)
			}
		})
	}
}

// An unparsable URL must leave the driver's shared DBInfo alone rather than
// half-filling it.
func Test_parseDSN_InvalidLeavesInfoUntouched(t *testing.T) {
	info := pinpoint.DBInfo{DBHost: "keep", DBName: "keep"}
	parseDSN(&info, "oracle://scott:tiger@local\thost:1521/xe")

	if info.DBHost != "keep" || info.DBName != "keep" {
		t.Errorf("parseDSN() overwrote %q/%q", info.DBHost, info.DBName)
	}
}

// The registered driver has to carry the oracle service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	if dbInfo.DBType != pinpoint.ServiceTypeOracle {
		t.Errorf("DBType = %d, want %d", dbInfo.DBType, pinpoint.ServiceTypeOracle)
	}
	if dbInfo.QueryType != pinpoint.ServiceTypeOracleExecuteQuery {
		t.Errorf("QueryType = %d, want %d", dbInfo.QueryType, pinpoint.ServiceTypeOracleExecuteQuery)
	}
}

// v3 moved the parameter coder maps into the driver and builds them in
// NewDriver, and every connection copies them at Open. A zero-value
// &go_ora.OracleDriver{} -- which is what v2 used and what still compiles --
// hands every connection nil maps, and that only shows up against a live
// database. Nothing else in this package would catch the regression.
func TestDriverIsInitialized(t *testing.T) {
	if reflect.DeepEqual(oracleDriver, &go_ora.OracleDriver{}) {
		t.Error("driver is a zero-value OracleDriver; v3 requires go_ora.NewDriver()")
	}
}

// The registration name is the whole public API of this package.
func TestDriverRegistered(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "oraclev3-pinpoint") {
		t.Errorf("oraclev3-pinpoint not registered, have %v", sql.Drivers())
	}
}
