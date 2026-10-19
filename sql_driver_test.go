package pinpoint

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeDriverConn is the smallest driver.Conn: none of the optional interfaces.
type fakeDriverConn struct{}

func (*fakeDriverConn) Prepare(string) (driver.Stmt, error) { return &fakeDriverStmt{}, nil }
func (*fakeDriverConn) Close() error                        { return nil }
func (*fakeDriverConn) Begin() (driver.Tx, error)           { return nil, errors.New("not implemented") }

type fakeDriverStmt struct{}

func (*fakeDriverStmt) Close() error  { return nil }
func (*fakeDriverStmt) NumInput() int { return -1 }
func (*fakeDriverStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (*fakeDriverStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("not implemented")
}

type pingerDriverConn struct {
	fakeDriverConn
	err error
}

func (c *pingerDriverConn) Ping(context.Context) error { return c.err }

type checkerDriverConn struct {
	fakeDriverConn
	err error
}

func (c *checkerDriverConn) CheckNamedValue(*driver.NamedValue) error { return c.err }

type resetterDriverConn struct {
	fakeDriverConn
	err error
}

func (c *resetterDriverConn) ResetSession(context.Context) error { return c.err }

type validatorDriverConn struct {
	fakeDriverConn
	valid bool
}

func (c *validatorDriverConn) IsValid() bool { return c.valid }

type sessionDriverConn struct {
	resetterDriverConn
	valid bool
}

func (c *sessionDriverConn) IsValid() bool { return c.valid }

// database/sql keeps a connection after a rollback its context triggered only
// when the driver has both SessionResetter and Validator - it asks whether they
// exist, not what they answer - so the wrapper must have exactly the ones the
// wrapped connection has, and pass their answers through. A driver that marks
// a connection bad in IsValid (go-sql-driver/mysql after a busy buffer) needs
// the answer to reach database/sql, or the connection goes back to the pool.
func Test_sqlConn_SessionInterfacesMatchDriver(t *testing.T) {
	for _, tt := range []struct {
		name                string
		conn                driver.Conn
		resetter, validator bool
	}{
		{"neither", &fakeDriverConn{}, false, false},
		{"SessionResetter", &resetterDriverConn{err: driver.ErrBadConn}, true, false},
		{"Validator", &validatorDriverConn{}, false, true},
		{"both", &sessionDriverConn{resetterDriverConn: resetterDriverConn{err: driver.ErrBadConn}}, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn := newSqlConn(tt.conn, DBInfo{}).withSessionInterfaces()

			r, isResetter := conn.(driver.SessionResetter)
			assert.Equal(t, tt.resetter, isResetter)
			if isResetter {
				assert.ErrorIs(t, r.ResetSession(context.Background()), driver.ErrBadConn, "answer passed through")
			}
			v, isValidator := conn.(driver.Validator)
			assert.Equal(t, tt.validator, isValidator)
			if isValidator {
				assert.False(t, v.IsValid(), "answer passed through")
			}
			_, isQueryer := conn.(driver.QueryerContext)
			assert.True(t, isQueryer, "still the instrumented wrapper")
			_, isExecer := conn.(driver.ExecerContext)
			assert.True(t, isExecer, "still the instrumented wrapper")
		})
	}
}

// Open and Connect are the two ways database/sql obtains a connection; both
// must hand out the shape that matches the driver's connection.
func Test_sqlDriver_OpenAndConnectKeepSessionInterfaces(t *testing.T) {
	d := &sqlDriver{Driver: &fakeDriver{conn: &sessionDriverConn{valid: true}}, dbInfo: DBInfo{}}

	conn, err := d.Open("dsn")
	assert.NoError(t, err)
	_, ok := conn.(driver.Validator)
	assert.True(t, ok, "Open: Validator of the wrapped connection is visible")
	_, ok = conn.(driver.SessionResetter)
	assert.True(t, ok, "Open: SessionResetter of the wrapped connection is visible")

	c := &sqlConnector{Connector: &fakeConnector{conn: &fakeDriverConn{}}, driver: d}
	conn, err = c.Connect(context.Background())
	assert.NoError(t, err)
	_, ok = conn.(driver.Validator)
	assert.False(t, ok, "Connect: a driver without Validator does not gain one")
	_, ok = conn.(driver.SessionResetter)
	assert.False(t, ok, "Connect: a driver without SessionResetter does not gain one")
}

type fakeDriver struct{ conn driver.Conn }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return d.conn, nil }

type fakeConnector struct{ conn driver.Conn }

func (c *fakeConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c *fakeConnector) Driver() driver.Driver                        { return nil }

// The other optional interfaces can be answered the way database/sql answers
// for a driver without them, so the wrapper always has them and delegates
// when it can.
func Test_sqlConn_OptionalInterfacePassthrough(t *testing.T) {
	plain := newSqlConn(&fakeDriverConn{}, DBInfo{})
	assert.NoError(t, plain.Ping(context.Background()), "no Pinger: succeed like database/sql")
	assert.ErrorIs(t, plain.CheckNamedValue(&driver.NamedValue{}), driver.ErrSkip, "no checker: default handling")

	pingErr := errors.New("ping failed")
	assert.ErrorIs(t, newSqlConn(&pingerDriverConn{err: pingErr}, DBInfo{}).Ping(context.Background()), pingErr)

	checkErr := errors.New("unsupported argument")
	checker := newSqlConn(&checkerDriverConn{err: checkErr}, DBInfo{})
	assert.ErrorIs(t, checker.CheckNamedValue(&driver.NamedValue{}), checkErr)

	stmt, err := prepare(&fakeDriverStmt{}, nil, checker, "select 1")
	assert.NoError(t, err)
	s := stmt.(*sqlStmt)
	assert.ErrorIs(t, s.CheckNamedValue(&driver.NamedValue{}), checkErr,
		"the statement falls back to the connection's checker")
	assert.Equal(t, driver.DefaultParameterConverter, s.ColumnConverter(0),
		"no ColumnConverter: default converter")
}
