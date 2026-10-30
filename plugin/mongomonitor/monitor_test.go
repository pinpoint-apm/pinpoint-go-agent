package ppmongomonitor

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mongo.RecordCommand turns the command annotation off and
// Mongo.CommandMaxSize bounds it; a size of 0 or less keeps the default.
func TestCommandSettings(t *testing.T) {
	cfg, err := pinpoint.NewConfig(pinpoint.WithAppName("mongo"), WithMongoRecordCommand(false), WithMongoCommandMaxSize(100))
	require.NoError(t, err)
	defer cfg.Close()
	record, maxSize := CommandSettings(cfg)
	assert.False(t, record)
	assert.Equal(t, 100, maxSize)

	cfg.Set(CfgMongoCommandMaxSize, 0)
	_, maxSize = CommandSettings(cfg)
	assert.Equal(t, DefaultCommandMaxSize, maxSize, "0 keeps the default")

	record, maxSize = CommandSettings(pinpoint.GetConfig())
	assert.True(t, record, "recorded by default")
	assert.Equal(t, DefaultCommandMaxSize, maxSize)
}

// started opens a command's span event on a fresh recording tracer and hands
// it to the monitor, as a plugin's Started does.
func started(m *Monitor, key Key) *pptest.RecordingTracer {
	tracer := pptest.NewRecordingTracer()
	tracer.NewSpanEvent("mongodb.find")
	m.Started(key, tracer)
	return tracer
}

// One command is one span event, opened by Started and closed by the matching
// Finished with whatever the driver reported.
func TestMonitor_StartedAndFinished(t *testing.T) {
	m := NewMonitor()
	key := Key{ConnectionID: "mongo1:27017[-3]", RequestID: 42}

	tracer := started(m, key)
	assert.Equal(t, 1, m.Pending())
	m.Finished(key, nil)

	require.Len(t, tracer.Events, 1)
	assert.NoError(t, tracer.Events[0].Err)
	assert.True(t, tracer.Events[0].Ended, "the span event was left open")
	assert.Zero(t, m.Pending(), "the finished command was left in the span map")

	failed := started(m, key)
	m.Finished(key, errors.New("E11000 duplicate key error"))
	assert.ErrorContains(t, failed.Events[0].Err, "duplicate key", "the driver's failure was not recorded")
	assert.True(t, failed.Events[0].Ended)
}

// A key Started never saw - an unsampled command, a driver heartbeat - must
// not end a span event that was never opened.
func TestMonitor_FinishedWithoutStarted(t *testing.T) {
	m := NewMonitor()
	assert.NotPanics(t, func() {
		m.Finished(Key{ConnectionID: "mongo1:27017[-3]", RequestID: 42}, errors.New("boom"))
	})

	other := Key{ConnectionID: "mongo1:27017[-3]", RequestID: 1}
	tracer := started(m, other)
	m.Finished(Key{ConnectionID: "mongo1:27017[-3]", RequestID: 2}, nil)
	assert.False(t, tracer.Events[0].Ended, "finishing an unknown key ended another command's event")
	assert.Equal(t, 1, m.Pending())
}

// Two commands in flight on the same connection are told apart by request id;
// finishing one must not close the other's span event.
func TestMonitor_InterleavedCommandsOnOneConnection(t *testing.T) {
	m := NewMonitor()
	first := Key{ConnectionID: "mongo1:27017[-3]", RequestID: 1}
	second := Key{ConnectionID: "mongo1:27017[-3]", RequestID: 2}

	firstTracer := started(m, first)
	secondTracer := started(m, second)
	require.Equal(t, 2, m.Pending(), "two commands in flight must hold two spans")

	m.Finished(second, nil)
	assert.Equal(t, 1, m.Pending(), "finishing one command must leave the other in flight")
	assert.True(t, secondTracer.Events[0].Ended)
	assert.False(t, firstTracer.Events[0].Ended)

	m.Finished(first, nil)
	assert.Zero(t, m.Pending())
	assert.True(t, firstTracer.Events[0].Ended)
}

// The span map is shared by every connection in the pool, so commands in
// flight at the same time have to stay separate and the map has to survive
// concurrent access. Run under -race.
func TestMonitor_ConcurrentCommands(t *testing.T) {
	m := NewMonitor()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				key := Key{ConnectionID: fmt.Sprintf("mongo1:27017[-%d]", i), RequestID: int64(j)}
				tracer := started(m, key)
				m.Finished(key, nil)
				assert.True(t, tracer.Events[0].Ended, "connection %d left command %d open", i, j)
			}
		}(i)
	}
	wg.Wait()

	assert.Zero(t, m.Pending(), "spans were left in the map")
}

// The span map is bounded: a command whose finished event never arrives - a
// driver bug, a connection that vanished - must not pin its tracer forever.
// At MaxPendingSpans the next Started evicts one pending command, ending its
// event with ErrSpanEvicted so the trace says what happened, and the map stays
// at the cap.
func TestMonitor_EvictsAtTheCap(t *testing.T) {
	m := NewMonitor()
	tracers := make([]*pptest.RecordingTracer, 0, MaxPendingSpans+1)
	for i := 0; i <= MaxPendingSpans; i++ {
		tracers = append(tracers, started(m, Key{ConnectionID: "mongo1:27017[-3]", RequestID: int64(i)}))
	}

	assert.Equal(t, MaxPendingSpans, m.Pending(), "the map must stay at the cap")
	evicted := 0
	for _, tracer := range tracers {
		if e := tracer.Events[0]; e.Ended {
			evicted++
			assert.ErrorIs(t, e.Err, ErrSpanEvicted, "an evicted command must say so on its event")
		}
	}
	assert.Equal(t, 1, evicted, "exactly one pending command is evicted per Started past the cap")
}

// The driver reports the connection as an address with a pool index appended.
// The endpoint has to be the bare host, or the same server is filed under one
// node per connection on the server map.
func TestHost(t *testing.T) {
	for _, tt := range []struct{ connID, want string }{
		{"localhost:27017[-13]", "localhost"},
		{"mongo1.example:27017[-1]", "mongo1.example"},
		{"localhost:27017", "localhost"},
		{"localhost", "localhost"},
		{"[2001:db8::1]:27017[-13]", "2001:db8::1"},
		{"[2001:db8::1]:27017", "2001:db8::1"},
		{"[fe80::1%eth0]:27017[-2]", "fe80::1%eth0"},
		{"[::1]", "::1"},
		{"2001:db8::1", "2001:db8::1"},
		{"", ""},
	} {
		assert.Equal(t, tt.want, Host(tt.connID), "Host(%q)", tt.connID)
	}
}

// AbbreviateJSON is what keeps a command annotation inside the limit; the
// boundary cases are where a marker longer than the limit could push it over.
func TestAbbreviateJSON(t *testing.T) {
	assert.Equal(t, "abc", AbbreviateJSON([]byte("abc"), 10), "a short value is kept whole")
	assert.Equal(t, "abc", AbbreviateJSON([]byte("abc"), 3), "a value exactly at the limit is kept whole")

	got := AbbreviateJSON([]byte(strings.Repeat("x", 100)), 20)
	assert.Len(t, got, 20)
	assert.True(t, strings.HasSuffix(got, "...(20)"), "got %q, want the marker", got)

	// A limit shorter than the marker leaves only the marker.
	assert.Equal(t, "...(2)", AbbreviateJSON([]byte("abcdef"), 2))
	assert.True(t, utf8.ValidString(AbbreviateJSON([]byte(strings.Repeat("가", 100)), 20)),
		"the cut must land on a rune start")
}

func TestOperationName(t *testing.T) {
	assert.Equal(t, "mongodb.find", OperationName("find"))
	assert.Equal(t, "mongodb.ping", OperationName("ping"), "an uncommon command is concatenated")
}
