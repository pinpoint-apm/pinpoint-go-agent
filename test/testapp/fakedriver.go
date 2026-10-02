// Package testapp holds the test-only gRPC service and the fake SQL driver
// shared by the integration (test/it) and end-to-end (test/e2e) suites.
package testapp

import (
	"context"
	"database/sql/driver"
	"io"
)

// FakeDriver is a database/sql driver that connects to nothing: every
// statement succeeds and every query returns no rows.
type FakeDriver struct{}

func (FakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{}, nil }

type fakeConn struct{}

func (*fakeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*fakeConn) Close() error                        { return nil }
func (*fakeConn) Begin() (driver.Tx, error)           { return fakeTx{}, nil }

func (*fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

func (*fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &fakeRows{}, nil
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeRows struct{}

func (*fakeRows) Columns() []string         { return []string{"col"} }
func (*fakeRows) Close() error              { return nil }
func (*fakeRows) Next([]driver.Value) error { return io.EOF }
