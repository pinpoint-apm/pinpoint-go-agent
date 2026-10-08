package pinpoint

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testGoroutine(id int64) *goroutine {
	header := fmt.Sprintf("goroutine %d", id)
	return &goroutine{
		id:     id,
		header: header,
		state:  "running",
		buf:    bytes.NewBufferString(header + " [running]:\nmain.active()\n"),
		span:   &activeSpanInfo{startTime: time.Unix(1, 0)},
	}
}

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
	header := "goroutine " + strconv.FormatInt(id, 10)
	g := dump.indexByHeader([]string{header})[header]
	require.NotNil(t, g)
	assert.Same(t, span, g.span)
	assert.Contains(t, g.buf.String(), "Test_dumpGoroutineParsesPprofOutput")
}

func Test_goroutineDumpIndexesParsedActiveGoroutines(t *testing.T) {
	agent := &agent{}
	agent.realTimeActiveSpan.Store(int64(7), &activeSpanInfo{startTime: time.Unix(1, 0)})
	profile := "goroutine 7 [running]:\nmain.active()\n\t/tmp/main.go:1 +0x1\n\n" +
		"goroutine 8 [runnable]:\nmain.idle()\n\t/tmp/main.go:2 +0x1\n\n"

	dump := parseProfile(strings.NewReader(profile), agent)
	require.NotNil(t, dump)
	require.Len(t, dump.goroutines, 1)
	byHeader := dump.indexByHeader([]string{"goroutine 7", "goroutine 8"})
	assert.Same(t, dump.goroutines[0], byHeader["goroutine 7"])
	assert.Nil(t, byHeader["goroutine 8"])
}

// The parser skips an untracked goroutine's block without collecting it, and
// a tracked block that follows gets only its own lines. The header is read by
// hand (parseGoroutineHeader), so the shapes runtime.Stack writes - a wait
// reason with a duration, a header without a reason - and a malformed line
// are checked here rather than left to a regexp.
func Test_parseProfileSkipsUntrackedBlocksAndReadsHeaders(t *testing.T) {
	agent := &agent{}
	agent.realTimeActiveSpan.Store(int64(9), &activeSpanInfo{startTime: time.Unix(1, 0)})
	profile := "goroutine 8 [runnable]:\nmain.idle()\n\t/tmp/main.go:2 +0x1\n\n" +
		"goroutine x [running]:\nmain.bad()\n\n" +
		"goroutine 9 [select, 3 minutes]:\nmain.active()\n\t/tmp/main.go:1 +0x1\n\n"

	dump := parseProfile(strings.NewReader(profile), agent)
	require.NotNil(t, dump)
	require.Len(t, dump.goroutines, 1)
	g := dump.goroutines[0]
	assert.Equal(t, int64(9), g.id)
	assert.Equal(t, "goroutine 9", g.header)
	assert.Equal(t, "select", g.state)
	assert.Equal(t, "goroutine 9 [select, 3 minutes]:\nmain.active()\n\t/tmp/main.go:1 +0x1\n", g.buf.String())

	for _, tt := range []struct {
		line  string
		id    int64
		state string
		ok    bool
	}{
		{"goroutine 1 [running]:", 1, "running", true},
		{"goroutine 123 [IO wait, 5 minutes]:", 123, "IO wait, 5 minutes", true},
		{"goroutine 1 [running]", 0, "", false},                     // no trailing colon
		{"goroutine  1 [running]:", 0, "", false},                   // empty id
		{"goroutine 99999999999999999999 [running]:", 0, "", false}, // past int64
		{"main.active()", 0, "", false},
	} {
		id, state, ok := parseGoroutineHeader([]byte(tt.line))
		assert.Equal(t, tt.ok, ok, tt.line)
		assert.Equal(t, tt.id, id, tt.line)
		assert.Equal(t, tt.state, string(state), tt.line)
	}
}

func Test_makePActiveThreadDumpListPreservesRequestedOrderAndLimit(t *testing.T) {
	dump := newGoroutineDump()
	for id := int64(1); id <= 3; id++ {
		dump.add(testGoroutine(id))
	}

	got := makePActiveThreadDumpList(dump, 2, []string{"goroutine 3", "missing", "goroutine 1", "goroutine 2"})
	require.Len(t, got, 2)
	assert.Equal(t, "goroutine 3", got[0].GetThreadDump().GetThreadName())
	assert.Equal(t, "goroutine 1", got[1].GetThreadDump().GetThreadName())
}

// BenchmarkActiveThreadDumpSelection covers building one request-local header
// index and running every lookup through it. Goroutine profile parsing is
// common to any selection strategy and is left out.
func BenchmarkActiveThreadDumpSelection(b *testing.B) {
	const (
		active    = 10000
		requested = 100
	)
	goroutines := make([]*goroutine, active)
	for id := 0; id < active; id++ {
		goroutines[id] = &goroutine{header: fmt.Sprintf("goroutine %d", id)}
	}
	names := make([]string, requested)
	for i := range names {
		names[i] = fmt.Sprintf("goroutine %d", active-1-i)
	}

	b.ReportAllocs()
	for b.Loop() {
		dump := &goroutineDump{goroutines: goroutines}
		byHeader := dump.indexByHeader(names)
		selected := make([]*goroutine, 0, requested)
		for _, name := range names {
			if g := byHeader[name]; g != nil {
				selected = append(selected, g)
			}
		}
		if len(selected) != requested {
			b.Fatalf("selected %d of %d requested goroutines", len(selected), requested)
		}
	}
}
