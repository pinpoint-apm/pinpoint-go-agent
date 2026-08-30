package pinpoint

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

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

type checkerDriverConn struct {
	fakeDriverConn
	checked bool
}

func (c *checkerDriverConn) CheckNamedValue(nv *driver.NamedValue) error {
	c.checked = true
	return nil
}

type fakeDriverStmt struct{}

func (s *fakeDriverStmt) Close() error                                    { return nil }
func (s *fakeDriverStmt) NumInput() int                                   { return -1 }
func (s *fakeDriverStmt) Exec(args []driver.Value) (driver.Result, error) { return nil, nil }
func (s *fakeDriverStmt) Query(args []driver.Value) (driver.Rows, error)  { return nil, nil }

type bindStringer string

func (s bindStringer) String() string { return "stringer:" + string(s) }

func Test_writeBindValue_PreservesFormatting(t *testing.T) {
	values := []interface{}{
		nil,
		"text",
		[]byte{0, 1, 127, 255},
		int64(-42),
		float64(1.25),
		true,
		time.Date(2026, time.August, 28, 1, 2, 3, 4, time.UTC),
		bindStringer("value"),
	}

	var b bytes.Buffer
	for i, value := range values {
		assert.True(t, writeBindValue(&b, i, value, len(values)-1, 4096))
	}

	want := make([]string, len(values))
	for i, value := range values {
		want[i] = fmt.Sprint(value)
	}
	assert.Equal(t, strings.Join(want, ", "), b.String())
}

func Test_writeBindValue_TruncatesOversizedValue(t *testing.T) {
	var b bytes.Buffer
	more := writeBindValue(&b, 0, strings.Repeat("x", 5000), 0, 1024)

	assert.False(t, more)
	assert.Equal(t, strings.Repeat("x", 1024)+"...(1024)", b.String())
}

func Test_writeBindValue_TruncatesOversizedBytes(t *testing.T) {
	value := bytes.Repeat([]byte{255}, 5000)
	want := fmt.Sprint(value)

	var b bytes.Buffer
	more := writeBindValue(&b, 0, value, 0, 1024)

	assert.False(t, more)
	assert.Equal(t, want[:1024]+"...(1024)", b.String())
}

// The separator between two values is written under the same limit as the
// values themselves, so a value landing on the boundary keeps only part of
// ", " - and a zero limit keeps nothing at all.
func Test_writeBindValue_TruncatesAtBoundary(t *testing.T) {
	tests := []struct {
		name     string
		values   []interface{}
		maxSize  int
		want     string
		wantMore bool
	}{
		{
			name:    "separator split by the limit",
			values:  []interface{}{strings.Repeat("p", 1023), "z"},
			maxSize: 1024,
			want:    strings.Repeat("p", 1023) + ",...(1024)",
		},
		{
			name:     "everything fits",
			values:   []interface{}{strings.Repeat("p", 1020), "z"},
			maxSize:  1024,
			want:     strings.Repeat("p", 1020) + ", z",
			wantMore: true,
		},
		{
			name:    "zero limit",
			values:  []interface{}{"abc"},
			maxSize: 0,
			want:    "...(0)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			more := true
			for i, v := range tt.values {
				if more = writeBindValue(&b, i, v, len(tt.values)-1, tt.maxSize); !more {
					break
				}
			}
			assert.Equal(t, tt.wantMore, more)
			assert.Equal(t, tt.want, b.String())
		})
	}
}

var benchmarkBindValueSink string

func Benchmark_writeBindValue_Large(b *testing.B) {
	for _, benchmark := range []struct {
		name  string
		value interface{}
	}{
		{"string", strings.Repeat("x", 1<<20)},
		{"bytes", bytes.Repeat([]byte{255}, 1<<20)},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.SetBytes(1 << 20)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var out bytes.Buffer
				writeBindValue(&out, 0, benchmark.value, 0, 1024)
				benchmarkBindValueSink = out.String()
			}
		})
	}
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

// The wrapper must keep the underlying connection's optional interfaces
// working, and answer as database/sql would when they are absent.
func Test_sqlConn_OptionalInterfacePassthrough(t *testing.T) {
	plain := newSqlConn(&fakeDriverConn{}, DBInfo{})
	assert.NoError(t, plain.Ping(context.Background()), "no Pinger: succeed like database/sql")
	assert.NoError(t, plain.ResetSession(context.Background()), "no SessionResetter: succeed")
	assert.True(t, plain.IsValid(), "no Validator: assume valid")
	assert.ErrorIs(t, plain.CheckNamedValue(&driver.NamedValue{}), driver.ErrSkip, "no checker: default handling")

	checker := &checkerDriverConn{}
	conn := newSqlConn(checker, DBInfo{})
	assert.NoError(t, conn.CheckNamedValue(&driver.NamedValue{}))
	assert.True(t, checker.checked, "conn checker delegated")

	// database/sql consults only the outermost statement's checker, so the
	// statement must fall back to the connection's checker itself.
	checker.checked = false
	stmt := &sqlStmt{Stmt: &fakeDriverStmt{}, conn: conn}
	assert.NoError(t, stmt.CheckNamedValue(&driver.NamedValue{}))
	assert.True(t, checker.checked, "stmt falls back to the conn checker")

	assert.Equal(t, driver.DefaultParameterConverter,
		stmt.ColumnConverter(0), "no ColumnConverter: default converter")
}

// A connection must read the live config, not the one captured when it was
// opened: connections opened before NewAgent otherwise kept the noop agent's
// config forever, so no setting and no reload ever reached them.
func Test_sqlConn_UsesLiveConfig(t *testing.T) {
	conn := newSqlConn(&fakeDriverConn{}, DBInfo{})
	assert.Equal(t, GetConfig().load(), conn.cfg(), "config resolved per operation")

	c, err := NewConfig(WithAppName("TestApp"), WithSQLMaxBindValueSize(7))
	assert.NoError(t, err)
	c.offGrpc = true
	a, err := NewAgent(c)
	assert.NoError(t, err)
	defer a.Shutdown()

	assert.Equal(t, 7, conn.cfg().sqlMaxBindValueSize, "connection sees the new agent's config")

	c.Set(CfgSQLMaxBindValueSize, 9)
	assert.Equal(t, 9, conn.cfg().sqlMaxBindValueSize, "connection sees the reload")
}
