package pinpoint

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeDriverConn struct{ begun bool }

func (c *fakeDriverConn) Prepare(query string) (driver.Stmt, error) { return nil, nil }
func (c *fakeDriverConn) Close() error                              { return nil }
func (c *fakeDriverConn) Begin() (driver.Tx, error) {
	c.begun = true
	return &fakeDriverTx{}, nil
}

type fakeDriverTx struct{ rolledBack bool }

func (t *fakeDriverTx) Commit() error   { return nil }
func (t *fakeDriverTx) Rollback() error { t.rolledBack = true; return nil }

func Test_writeBindValue_TruncatesOversizedValue(t *testing.T) {
	var b bytes.Buffer
	more := writeBindValue(&b, 0, strings.Repeat("x", 5000), 0, 1024)

	assert.False(t, more)
	assert.Equal(t, strings.Repeat("x", 1024)+"...(1024)", b.String())
}

// The BeginTx fallback must mirror database/sql's own checks: the wrapper
// implements driver.ConnBeginTx, so database/sql never runs them itself.
func Test_sqlConn_BeginTxFallbackHonorsOptions(t *testing.T) {
	base := &fakeDriverConn{}
	conn := newSqlConn(base, DBInfo{})

	_, err := conn.BeginTx(context.Background(), driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSerializable)})
	assert.Error(t, err, "non-default isolation must not be silently downgraded")
	assert.False(t, base.begun, "no transaction begun")

	_, err = conn.BeginTx(context.Background(), driver.TxOptions{ReadOnly: true})
	assert.Error(t, err, "read-only must not be silently downgraded")
	assert.False(t, base.begun, "no transaction begun")

	tx, err := conn.BeginTx(context.Background(), driver.TxOptions{})
	assert.NoError(t, err)
	assert.True(t, base.begun, "default options fall back to Begin")
	assert.NotNil(t, tx)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = conn.BeginTx(canceled, driver.TxOptions{})
	assert.ErrorIs(t, err, context.Canceled)
}
