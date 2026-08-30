package pinpoint

import (
	"io"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_dumpGoroutineProfileParsesSmallOutput(t *testing.T) {
	const id int64 = 42
	const profile = "goroutine 42 [running]:\nsmall.stack()\n\t/small.go:1 +0x1\n\n"

	agent := &agent{}
	span := &activeSpanInfo{}
	agent.realTimeActiveSpan.Store(id, span)

	dump := dumpGoroutineProfile(agent, func(w io.Writer) error {
		_, err := io.WriteString(w, profile)
		return err
	})

	require.NotNil(t, dump)
	require.Len(t, dump.goroutines, 1)
	assert.Equal(t, id, dump.goroutines[0].id)
	assert.Same(t, span, dump.goroutines[0].span)
	assert.Contains(t, dump.goroutines[0].buf.String(), "/small.go:1 +0x1")
}

func Test_dumpGoroutineParsesPprofOutput(t *testing.T) {
	agent := &agent{}
	id := goIdFromDump()
	span := &activeSpanInfo{}
	agent.realTimeActiveSpan.Store(id, span)

	dump := dumpGoroutine(agent)

	require.NotNil(t, dump)
	g := dump.search("goroutine " + strconv.FormatInt(id, 10))
	require.NotNil(t, g)
	assert.Same(t, span, g.span)
	assert.Contains(t, g.buf.String(), "Test_dumpGoroutineParsesPprofOutput")
}
