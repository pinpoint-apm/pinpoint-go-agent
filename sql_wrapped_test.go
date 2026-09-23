package pinpoint

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type plainDriver struct{}

func (plainDriver) Open(string) (driver.Conn, error) { return nil, errors.New("plain") }

type contextDriver struct{ plainDriver }

func (contextDriver) OpenConnector(string) (driver.Connector, error) {
	return nil, errors.New("connector")
}

// IsWrappedSQLDriver tells a driver WrapSQLDriver returned, in either of its
// two shapes, from a driver that has not been wrapped, so that a registration
// hook wraps once and skips a plugin's already wrapped driver.
func TestIsWrappedSQLDriver(t *testing.T) {
	assert.False(t, IsWrappedSQLDriver(plainDriver{}))
	assert.False(t, IsWrappedSQLDriver(&contextDriver{}))

	plain := WrapSQLDriver(plainDriver{}, DBInfo{})
	assert.True(t, IsWrappedSQLDriver(plain))
	_, hasConnector := plain.(driver.DriverContext)
	assert.False(t, hasConnector, "a driver without DriverContext must not gain it")

	withContext := WrapSQLDriver(&contextDriver{}, DBInfo{})
	assert.True(t, IsWrappedSQLDriver(withContext))
	_, hasConnector = withContext.(driver.DriverContext)
	assert.True(t, hasConnector, "a driver with DriverContext must keep it")

	// Wrapping twice is the mistake the check exists for; the shape still holds.
	assert.True(t, IsWrappedSQLDriver(WrapSQLDriver(plain, DBInfo{})))
	_ = context.Background()
}
