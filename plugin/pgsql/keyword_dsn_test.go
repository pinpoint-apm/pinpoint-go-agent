package pppgsql

import (
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
)

// The keyword/value form lib/pq connects with is read as it is: handing it to
// pq.ParseURL rejected it, once per pooled connection with an ERROR line, and
// left every span of the pool without an endpoint.
func Test_parseDSN_KeywordForm(t *testing.T) {
	t.Setenv("PGHOST", "")
	t.Setenv("PGDATABASE", "")
	for _, tt := range []struct {
		dsn      string
		wantHost string
		wantName string
	}{
		{"host=db1 dbname=app user=x sslmode=disable", "db1", "app"},
		{"hostaddr=10.0.0.1 host=db1 dbname=app", "10.0.0.1", "app"},
		{"dbname='app db' host=/var/run/postgresql", "localhost", "app db"},
	} {
		var info pinpoint.DBInfo
		parseDSN(&info, tt.dsn)
		assert.Equal(t, tt.wantHost, info.DBHost, "parseDSN(%q) host", tt.dsn)
		assert.Equal(t, tt.wantName, info.DBName, "parseDSN(%q) database", tt.dsn)
	}
}
