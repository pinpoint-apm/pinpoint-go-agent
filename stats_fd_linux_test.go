//go:build linux

package pinpoint

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The getdents walk has to agree with the directory listing, follow the
// descriptors as they open and close, and allocate nothing on the way.
func TestCountOpenFDs(t *testing.T) {
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	want := len(entries) - 1 // the listing counts the handle it was read through

	n, ok := countOpenFDs()
	require.True(t, ok)
	assert.Equal(t, int32(want), n)

	files := make([]*os.File, 3)
	for i := range files {
		files[i], err = os.Open("/dev/null")
		require.NoError(t, err)
	}
	n, ok = countOpenFDs()
	require.True(t, ok)
	assert.Equal(t, int32(want+len(files)), n, "every opened descriptor is counted")
	for _, f := range files {
		f.Close()
	}
	n, _ = countOpenFDs()
	assert.Equal(t, int32(want), n, "a closed descriptor is no longer counted")

	// The one allocation is syscall.Open's copy of the path; the walk itself
	// allocates nothing however many descriptors are open.
	assert.LessOrEqual(t, testing.AllocsPerRun(10, func() { countOpenFDs() }), 1.0)
}
